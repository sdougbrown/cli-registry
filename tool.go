package cliregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

// benignDefaultEnv is the minimal set of standard variables passed to a child
// in addition to declared env, so real CLIs (and their subprocesses, via PATH,
// HOME, LANG) can run without blanket-inheriting arbitrary host env.
var benignDefaultEnv = []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR"}

const maxCapture = 1 << 20 // 1 MiB per stream

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
			argv = append(argv, b.flag)
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

// exec spawns argv under the tool's resolved command semantics.
func (t *tool) exec(ctx context.Context, argv []string, opts *callOptions) (*Result, error) {
	cmd := exec.Command(argv[0], argv[1:]...)

	// env allow-list
	env := map[string]string{}
	for _, k := range benignDefaultEnv {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	for k, v := range t.env {
		if strings.HasPrefix(v, "env:") {
			name := strings.TrimPrefix(v, "env:")
			host, ok := os.LookupEnv(name)
			if !ok {
				return nil, fmt.Errorf("referenced env %q not present", name)
			}
			env[k] = host
		} else {
			env[k] = v
		}
	}
	for k, v := range opts.env {
		env[k] = v
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	// cwd
	if opts.cwd != "" {
		if t.cwd == nil || t.cwd.mode != "allow" {
			return nil, fmt.Errorf("cwd requested but tool has no allowed cwd roots")
		}
		if !containedInRoots(opts.cwd, t.cwd.roots) {
			return nil, fmt.Errorf("requested cwd %q outside allowed roots", opts.cwd)
		}
		cmd.Dir = opts.cwd
	} else if t.cwd != nil && t.cwd.mode == "allow" && len(t.cwd.roots) > 0 {
		cmd.Dir = t.cwd.roots[0]
	}

	// stdin closed; bounded capture via io.Writer
	cmd.Stdin = nil
	outW := newBoundedWriter(maxCapture)
	errW := newBoundedWriter(maxCapture)
	cmd.Stdout = outW
	cmd.Stderr = errW

	// process group so we can kill descendants
	setProcAttr(cmd)
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	timeout := t.timeout
	if opts.timeout > 0 {
		timeout = opts.timeout
	}
	var timedOut atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		// mark timed-out first, then kill the whole process group (descendants)
		timedOut.Store(true)
		killProcessGroup(cmd)
	})
	waite := cmd.Wait()
	timer.Stop()

	// honor an already-cancelled caller context as a timeout as well
	if ctx.Err() != nil {
		timedOut.Store(true)
		killProcessGroup(cmd)
	}

	if timedOut.Load() {
		return nil, &TimeoutError{Tool: t.name, Args: argv, Timout: timeout}
	}

	exitCode := 0
	if waite != nil {
		var ee *exec.ExitError
		if errors.As(waite, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return nil, fmt.Errorf("wait: %w", waite)
		}
	}

	res := &Result{
		Stdout:    outW.Bytes(),
		Stderr:    errW.Bytes(),
		Truncated: outW.Truncated() || errW.Truncated(),
		ExitCode:  exitCode,
		Duration:  time.Since(start),
	}
	if !t.output.successExitCodes[exitCode] {
		return nil, &ExecError{Tool: t.name, Args: argv, ExitCode: exitCode, Stderr: res.Stderr}
	}
	if err := t.decodeOutput(res); err != nil {
		return nil, fmt.Errorf("decode output: %w", err)
	}
	return res, nil
}

type boundedWriter struct {
	mu        sync.Mutex
	buf       []byte
	limit     int
	truncated bool
}

func newBoundedWriter(limit int) *boundedWriter { return &boundedWriter{limit: limit} }

func (b *boundedWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(b.buf) < b.limit {
		room := b.limit - len(b.buf)
		if n > room {
			b.buf = append(b.buf, p[:room]...)
			b.truncated = true
		} else {
			b.buf = append(b.buf, p...)
		}
	} else {
		b.truncated = true
	}
	return n, nil // report full write; we silently drop excess
}

func (b *boundedWriter) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...)
}

func (b *boundedWriter) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
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

func containedInRoots(dir string, roots []string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for _, root := range roots {
		if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
