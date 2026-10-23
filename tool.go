package cliregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sdougbrown/cli-registry/manifest"
	"github.com/sdougbrown/cli-registry/schema"
)

type Tool struct {
	Name        string
	Description string
	// InputSchema is the tool's argument JSON Schema (an object schema), used
	// for descriptors (MCP tools/list) and downstream function-calling.
	InputSchema map[string]any
}

// tool is the internal compiled representation.
type tool struct {
	name           string
	description    string
	command        []string // absolute resolved argv prefix
	env            map[string]string
	cwd            *cwdScope
	timeout        time.Duration
	output         outputConfig
	schema         *schema.ValueSchema
	inputSchemaRaw map[string]any
	required       []string
	bindings       []binding
}

type binding struct {
	property string
	style    string // equals | space | short | positional
	flag     string // flag text (equals/space) or short flag (short)
}

type outputConfig struct {
	mode             string // raw | json | json-pointer
	jsonPointer      string
	successExitCodes map[int]bool
}

type cwdScope struct {
	mode  string   // allow | inherit
	roots []string // absolute canonical allowed roots
	base  string   // manifest file dir (for relative-root resolution at first call is not needed; resolved at load)
}

// toolDef is the typed tool body used for DisallowUnknownFields decoding.
type toolDef struct {
	Description string            `json:"description"`
	Command     []string          `json:"command"`
	Env         map[string]string `json:"env"`
	Cwd         *struct {
		Mode  string   `json:"mode"`
		Roots []string `json:"roots"`
	} `json:"cwd"`
	Timeout     string         `json:"timeout"`
	InputSchema map[string]any `json:"inputSchema"`
	Bindings    []*struct {
		Property string `json:"property"`
		Style    string `json:"style"`
		Flag     string `json:"flag"`
	} `json:"bindings"`
	Output *struct {
		Mode             string `json:"mode"`
		JSONPointer      string `json:"json_pointer"`
		SuccessExitCodes []int  `json:"success_exit_codes"`
	} `json:"output"`
}

func (r *Registry) compileTool(doc *manifest.Document, name string) error {
	raw := doc.Tools[name]
	var def toolDef
	if err := json.Unmarshal(raw, &def); err != nil {
		return fmt.Errorf("decode tool body: %w", err)
	}

	t := &tool{
		name:        name,
		description: def.Description,
		env:         def.Env,
		output:      outputConfig{mode: "raw", successExitCodes: map[int]bool{0: true}},
	}

	if t.description == "" {
		return errors.New("tool requires a non-empty description")
	}
	if len(def.Command) == 0 {
		return errors.New("tool requires a command")
	}
	resolved, err := resolveCommand(def.Command[0])
	if err != nil {
		return fmt.Errorf("resolve command %q: %w", def.Command[0], err)
	}
	t.command = append([]string{resolved}, def.Command[1:]...)

	if def.Timeout != "" {
		d, err := time.ParseDuration(def.Timeout)
		if err != nil {
			return fmt.Errorf("invalid timeout %q: %w", def.Timeout, err)
		}
		t.timeout = d
	} else {
		t.timeout = 30 * time.Second // mandatory-safe default
	}

	if def.Output != nil {
		if def.Output.Mode != "" {
			t.output.mode = def.Output.Mode
		}
		t.output.jsonPointer = def.Output.JSONPointer
		if len(def.Output.SuccessExitCodes) > 0 {
			t.output.successExitCodes = map[int]bool{}
			for _, c := range def.Output.SuccessExitCodes {
				t.output.successExitCodes[c] = true
			}
		}
	}
	switch t.output.mode {
	case "raw", "json", "json-pointer":
	default:
		return fmt.Errorf("unsupported output.mode %q", t.output.mode)
	}
	if t.output.mode == "json-pointer" && t.output.jsonPointer == "" {
		return errors.New("output.mode json-pointer requires output.json_pointer")
	}

	if def.InputSchema == nil {
		return errors.New("tool requires inputSchema")
	}
	vs, err := schema.Compile(def.InputSchema)
	if err != nil {
		return fmt.Errorf("compile inputSchema: %w", err)
	}
	t.schema = vs
	t.inputSchemaRaw = def.InputSchema

	// top-level property set from inputSchema
	props, _ := def.InputSchema["properties"].(map[string]any)
	propSet := map[string]bool{}
	for k := range props {
		propSet[k] = true
	}
	// argv bindings
	if len(def.Bindings) == 0 {
		if len(propSet) > 0 {
			return errors.New("inputSchema has properties but no bindings")
		}
	} else {
		bound := map[string]bool{}
		for _, b := range def.Bindings {
			if b == nil || b.Property == "" || b.Style == "" {
				return errors.New("binding requires property and style")
			}
			if !propSet[b.Property] {
				return fmt.Errorf("binding references unknown property %q", b.Property)
			}
			if bound[b.Property] {
				return fmt.Errorf("duplicate binding for property %q", b.Property)
			}
			bound[b.Property] = true
			switch b.Style {
			case "equals", "space":
				if b.Flag == "" {
					return fmt.Errorf("binding %q (%s) requires a flag", b.Property, b.Style)
				}
			case "short":
				if b.Flag == "" {
					return fmt.Errorf("binding %q (short) requires a flag", b.Property)
				}
			case "positional":
			default:
				return fmt.Errorf("binding %q: unsupported style %q", b.Property, b.Style)
			}
			t.bindings = append(t.bindings, binding{property: b.Property, style: b.Style, flag: b.Flag})
		}
		// exact correspondence: every top-level property must be bound
		for k := range propSet {
			if !bound[k] {
				return fmt.Errorf("inputSchema property %q has no binding", k)
			}
		}
	}

	// cwd scope
	if def.Cwd != nil {
		t.cwd = &cwdScope{mode: def.Cwd.Mode, base: filepath.Dir(doc.ToolSource[name])}
		switch def.Cwd.Mode {
		case "allow":
			for _, root := range def.Cwd.Roots {
				abs := root
				if !filepath.IsAbs(abs) {
					abs = filepath.Join(t.cwd.base, root)
				}
				abs, err := filepath.Abs(abs)
				if err != nil {
					return err
				}
				abs, err = filepath.EvalSymlinks(abs)
				if err != nil {
					return fmt.Errorf("cwd root %q: %w", root, err)
				}
				t.cwd.roots = append(t.cwd.roots, abs)
			}
		case "inherit":
		default:
			return fmt.Errorf("unsupported cwd.mode %q", def.Cwd.Mode)
		}
	}
	r.tools[name] = t
	return nil
}

func resolveCommand(bin string) (string, error) {
	var path string
	if filepath.IsAbs(bin) {
		path = bin
	} else {
		// no PATH globbing at call time; resolve once now
		p, err := exec.LookPath(bin)
		if err != nil {
			return "", err
		}
		path = p
	}
	// canonicalize symlinks
	real, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = real
	}
	return filepath.Abs(path)
}

// buildArgv converts validated args into an argv sequence per specs/argv.md.
func (t *tool) buildArgv(args map[string]any, opts *callOptions) ([]string, error) {
	if len(t.bindings) == 0 {
		return t.command, nil
	}
	argv := append([]string{}, t.command...)
	// deterministic order: props sorted, positionals last
	type item struct {
		b       binding
		v       any
		present bool
	}
	var items []item
	for _, b := range t.bindings {
		v, present := args[b.property]
		items = append(items, item{b, v, present})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].b.style == "positional" && items[j].b.style != "positional" {
			return false
		}
		if items[i].b.style != "positional" && items[j].b.style == "positional" {
			return true
		}
		return items[i].b.property < items[j].b.property
	})
	first := true
	for _, it := range items {
		if !it.present {
			continue
		}
		arg2, nextFirst, err := t.appendBinding(argv, it.b, it.v, first)
		if err != nil {
			return nil, err
		}
		argv = arg2
		first = nextFirst
	}
	return argv, nil
}

func (t *tool) appendBinding(argv []string, b binding, v any, first bool) ([]string, bool, error) {
	scalar := isScalar(v)
	switch b.style {
	case "equals":
		key := b.flag
		if bv, ok := v.(bool); ok {
			if bv {
				argv = append(argv, key)
			}
			return argv, first, nil
		}
		if scalar {
			argv = append(argv, key+"="+encodeScalar(v))
			return argv, first, nil
		}
		if arr, ok := v.([]any); ok {
			for _, el := range arr {
				argv = append(argv, key+"="+encodeScalar(el))
			}
			return argv, first, nil
		}
		return argv, first, fmt.Errorf("binding %q: cannot flatten %T", b.property, v)
	case "space":
		if bv, ok := v.(bool); ok {
			if bv {
				argv = append(argv, b.flag)
			}
			return argv, first, nil
		}
		if scalar {
			argv = append(argv, b.flag, encodeScalar(v))
			return argv, first, nil
		}
		if arr, ok := v.([]any); ok {
			for _, el := range arr {
				argv = append(argv, b.flag, encodeScalar(el))
			}
			return argv, first, nil
		}
		return argv, first, fmt.Errorf("binding %q: cannot flatten %T", b.property, v)
	case "short":
		if bv, ok := v.(bool); ok {
			if bv {
				argv = append(argv, b.flag)
			}
			return argv, first, nil
		}
		if scalar {
			argv = append(argv, b.flag+encodeScalar(v))
			return argv, first, nil
		}
		if arr, ok := v.([]any); ok {
			for _, el := range arr {
				argv = append(argv, b.flag+encodeScalar(el))
			}
			return argv, first, nil
		}
		return argv, first, fmt.Errorf("binding %q: cannot flatten %T", b.property, v)
	case "positional":
		if _, ok := v.(bool); ok {
			return argv, first, fmt.Errorf("binding %q: boolean not allowed positional", b.property)
		}
		if scalar {
			if needsDashEscape(encodeScalar(v)) && first {
				argv = append(argv, "--")
				first = false
			}
			argv = append(argv, encodeScalar(v))
			return argv, first, nil
		}
		if arr, ok := v.([]any); ok {
			for _, el := range arr {
				s := encodeScalar(el)
				if needsDashEscape(s) && first {
					argv = append(argv, "--")
					first = false
				}
				argv = append(argv, s)
			}
			return argv, first, nil
		}
		return argv, first, fmt.Errorf("binding %q: cannot flatten %T", b.property, v)
	}
	return argv, first, fmt.Errorf("binding %q: unsupported style %q", b.property, b.style)
}

func isScalar(v any) bool {
	switch v.(type) {
	case string, float64, int, int64, bool:
		return true
	}
	return false
}

func encodeScalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return fmt.Sprint(t)
	case float64:
		// shortest reliable decimal; integral floats print without a decimal
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	case int:
		return fmt.Sprintf("%d", t)
	case int64:
		return fmt.Sprintf("%d", t)
	default:
		return fmt.Sprint(v)
	}
}

func needsDashEscape(s string) bool { return strings.HasPrefix(s, "-") && s != "-" }

// resolveDir returns the absolute, symlink-resolved working directory the
// manifest or WithCWD fixes, or "" when the host decides (cwd.mode inherit, no
// manifest scope).
//
// A fixed directory is resolved here, once, against the planning process's
// working directory: a host executor that runs the argv from its own session
// base must land in the directory Plan validated, not in whatever its base
// happens to make of the same relative string.
func (t *tool) resolveDir(opts *callOptions) (string, error) {
	if opts.cwd != "" {
		if t.cwd == nil || t.cwd.mode != "allow" {
			return "", fmt.Errorf("cwd requested but tool has no allowed cwd roots")
		}
		resolved, err := resolvePath(opts.cwd)
		if err != nil {
			return "", fmt.Errorf("requested cwd %q: %w", opts.cwd, err)
		}
		if !containedInRoots(resolved, t.cwd.roots) {
			return "", fmt.Errorf("requested cwd %q outside allowed roots", opts.cwd)
		}
		return resolved, nil
	}
	if t.cwd != nil && t.cwd.mode == "allow" && len(t.cwd.roots) > 0 {
		return t.cwd.roots[0], nil
	}
	return "", nil
}

// resolvePath makes dir absolute and resolves symlinks, including in the part
// of the path that does not exist yet, so containment is checked on the
// directory the process would really start in rather than on its spelling.
func resolvePath(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	rest := ""
	for {
		resolved, err := filepath.EvalSymlinks(abs)
		if err == nil {
			if rest == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, rest), nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", err
		}
		rest = filepath.Join(filepath.Base(abs), rest)
		abs = parent
	}
}

// evalPointer evaluates an RFC 6901 JSON Pointer against a decoded value.
func evalPointer(v any, ptr string) (any, error) {
	if ptr == "" || ptr == "#" {
		return v, nil
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, fmt.Errorf("invalid json pointer %q", ptr)
	}
	cur := v
	for _, tok := range strings.Split(ptr[1:], "/") {
		tok = strings.ReplaceAll(tok, "~1", "/")
		tok = strings.ReplaceAll(tok, "~0", "~")
		switch c := cur.(type) {
		case map[string]any:
			next, ok := c[tok]
			if !ok {
				return nil, fmt.Errorf("pointer %q: key %q not found", ptr, tok)
			}
			cur = next
		case []any:
			var idx int
			if _, err := fmt.Sscanf(tok, "%d", &idx); err != nil || idx < 0 || idx >= len(c) {
				return nil, fmt.Errorf("pointer %q: bad array index %q", ptr, tok)
			}
			cur = c[idx]
		default:
			return nil, fmt.Errorf("pointer %q: cannot descend into %T", ptr, cur)
		}
	}
	return cur, nil
}

// decodeOutput decodes stdout per the tool's output mode.
func (t *tool) decodeOutput(res *Result) error {
	switch t.output.mode {
	case "raw":
		res.Decoded = nil
		return nil
	case "json":
		var v any
		if err := json.Unmarshal(res.Stdout, &v); err != nil {
			return err
		}
		res.Decoded = v
		return nil
	case "json-pointer":
		var v any
		if err := json.Unmarshal(res.Stdout, &v); err != nil {
			return err
		}
		got, err := evalPointer(v, t.output.jsonPointer)
		if err != nil {
			return err
		}
		res.Decoded = got
		return nil
	}
	return nil
}

// containedInRoots reports whether dir is one of roots or inside one of them.
// Both sides are absolute with symlinks resolved (see resolvePath and the cwd
// roots compiled at load), so the check compares identities, not spellings.
func containedInRoots(dir string, roots []string) bool {
	for _, root := range roots {
		if dir == root || strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
