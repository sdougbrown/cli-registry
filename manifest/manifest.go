// Package manifest loads cli-registry manifests.
//
// JSON, YAML (.yaml/.yml), and TOML (.toml) are supported, detected by file
// extension (unknown extensions default to JSON). Non-JSON formats normalize
// to a JSON intermediate before a strict decode, so DisallowUnknownFields and
// null normalization apply uniformly across all formats — one JSON-land diff
// choke point.
//
// Stage 1 scope: format detection, normalization, decoding with unknown-field
// rejection, version validation, registry discovery (file | dir | repeated
// paths, deterministic order), duplicate tool-name detection (within a file
// and across files), all-or-nothing load semantics, and null normalization.
// Schema/argv compilation happens in later stages against Document.Tools.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	gotoml "github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// SupportedManifestVersion is the accepted top-level manifest version.
const SupportedManifestVersion = 1

// Errors surfaced by the loader. Callers may wrap them for path context.
var (
	ErrUnsupportedVersion = errors.New("unsupported manifest version")
	ErrDuplicateTool      = errors.New("duplicate tool name")
)

// Format identifies the encoding of a manifest file.
type Format int

const (
	// FormatJSON is the default and requires no external dependencies.
	FormatJSON Format = iota
	// FormatYAML covers .yaml and .yml extensions.
	FormatYAML
	// FormatTOML covers .toml extensions.
	FormatTOML
)

// DetectFormat infers the manifest format from the file extension. Unknown
// extensions default to JSON so explicitly-passed paths without an extension
// continue to work.
func DetectFormat(path string) Format {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return FormatYAML
	case ".toml":
		return FormatTOML
	default:
		return FormatJSON
	}
}

// manifestExtensions are the recognized manifest extensions used when scanning
// a directory for manifests.
var manifestExtensions = map[string]bool{
	".json": true, ".yaml": true, ".yml": true, ".toml": true,
}

// Document is a decoded, null-normalized manifest ready for schema/argv
// compilation in later stages. Tool bodies are preserved as raw JSON so later
// stages control their own decoding.
type Document struct {
	// Path is the file the document was parsed from.
	Path string
	// Version is the validated top-level manifest version.
	Version int
	// Tools maps tool name to its raw (null-normalized) body.
	Tools map[string]json.RawMessage
	// ToolSource records the file path each tool came from, for dedup errors
	// and later error context.
	ToolSource map[string]string
}

// envelope is the typed top-level manifest shape used for unknown-field
// rejection at the document level.
type envelope struct {
	Version int                        `json:"version"`
	Tools   map[string]json.RawMessage `json:"tools"`
}

// Parse reads and validates a single manifest file into a Document.
func Parse(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	return parse(path, data)
}

// Decode normalizes raw manifest bytes by path's extension and decodes them
// into dst (any Go struct) with unknown fields rejected. Useful for callers
// that have already read the bytes or preprocessed them.
func Decode(path string, data []byte, dst any) error {
	jsonBytes, err := normalizeToJSON(path, data)
	if err != nil {
		return err
	}
	// Duplicate-key detection must run on the raw normalized bytes BEFORE
	// stripNull: stripping round-trips through a Go map, which would already
	// have collapsed duplicate keys by the time it ran.
	if err := detectDuplicateKeys(jsonBytes); err != nil {
		return fmt.Errorf("normalize %s: %w", path, err)
	}
	jsonBytes, err = stripNull(jsonBytes)
	if err != nil {
		return fmt.Errorf("normalize %s: %w", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(jsonBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode %s: multiple top-level values", path)
		}
		return fmt.Errorf("decode %s: trailing data: %w", path, err)
	}
	return nil
}

func parse(path string, data []byte) (*Document, error) {
	var env envelope
	if err := Decode(path, data, &env); err != nil {
		return nil, err
	}
	if env.Version != SupportedManifestVersion {
		return nil, fmt.Errorf("manifest %s: version %d: %w", path, env.Version, ErrUnsupportedVersion)
	}
	if env.Tools == nil {
		env.Tools = map[string]json.RawMessage{}
	}
	doc := &Document{
		Path:       path,
		Version:    env.Version,
		Tools:      env.Tools,
		ToolSource: map[string]string{},
	}
	for name := range env.Tools {
		doc.ToolSource[name] = path
	}
	return doc, nil
}

// Load reads every manifest reachable from paths and merges them into one
// Document. A path may be a file, a directory (all manifests within, in
// deterministic order), or repeated paths. Duplicate tool names — within a
// file or across files — fail the load. Load is all-or-nothing: any error
// discards the whole result.
func Load(paths ...string) (*Document, error) {
	files, err := Discover(paths...)
	if err != nil {
		return nil, err
	}
	merged := &Document{
		Path:       "merged",
		Tools:      map[string]json.RawMessage{},
		ToolSource: map[string]string{},
	}
	var sawVersion int
	for _, f := range files {
		doc, err := Parse(f)
		if err != nil {
			return nil, err
		}
		if sawVersion == 0 {
			sawVersion = doc.Version
		} else if doc.Version != sawVersion {
			return nil, fmt.Errorf("manifest %s: version %d conflicts with %d: %w", f, doc.Version, sawVersion, ErrUnsupportedVersion)
		}
		merged.Version = sawVersion
		for name, body := range doc.Tools {
			if _, dup := merged.Tools[name]; dup {
				return nil, fmt.Errorf("tool %q defined in both %s and %s: %w", name, merged.ToolSource[name], f, ErrDuplicateTool)
			}
			merged.Tools[name] = body
			merged.ToolSource[name] = f
		}
	}
	return merged, nil
}

// Discover returns the deterministic, sorted, de-duplicated list of manifest
// files across the given paths. Directory paths are scanned for recognized
// manifest extensions (.json/.yaml/.yml/.toml).
func Discover(paths ...string) ([]string, error) {
	seen := map[string]bool{}
	var files []string
	var add func(f string) error
	add = func(f string) error {
		abs, err := filepath.Abs(f)
		if err != nil {
			return err
		}
		if seen[abs] {
			return nil
		}
		seen[abs] = true
		files = append(files, abs)
		return nil
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("discover %s: %w", p, err)
		}
		if !info.IsDir() {
			if err := add(p); err != nil {
				return nil, err
			}
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, fmt.Errorf("discover dir %s: %w", p, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if !manifestExtensions[strings.ToLower(filepath.Ext(e.Name()))] {
				continue
			}
			if err := add(filepath.Join(p, e.Name())); err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// stripNull removes null-valued object members recursively. It operates on the
// whole document: for CLI registration, null and absent are equivalent, so a
// null member is discarded everywhere. null array elements are preserved (and
// remain invalid at validation time).
func stripNull(b []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	stripped := stripNullValue(v)
	out, err := json.Marshal(stripped)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func stripNullValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if val == nil {
				continue
			}
			out[k] = stripNullValue(val)
		}
		return out
	case []any:
		for i := range t {
			t[i] = stripNullValue(t[i])
		}
		return t
	default:
		return v
	}
}

// detectDuplicateKeys rejects any JSON object with a repeated key. Go's JSON
// decoder collapses duplicate object keys (last wins), so this check runs on
// the normalized bytes before typed decoding — required to catch duplicate
// tool names within a single file.
func detectDuplicateKeys(b []byte) error {
	type cont struct {
		isObj   bool
		keys    map[string]bool
		needKey bool
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	stack := []cont{{isObj: true, keys: map[string]bool{}, needKey: true}} // implicit root
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				// the container fills the parent's value slot
				if n := len(stack); n > 0 {
					stack[n-1].needKey = true
				}
				stack = append(stack, cont{isObj: true, keys: map[string]bool{}, needKey: true})
			case '[':
				if n := len(stack); n > 0 {
					stack[n-1].needKey = true
				}
				stack = append(stack, cont{isObj: false})
			case '}', ']':
				stack = stack[:len(stack)-1]
			}
		case string:
			top := &stack[len(stack)-1]
			if top.isObj && top.needKey {
				if top.keys[v] {
					return fmt.Errorf("duplicate object key %q", v)
				}
				top.keys[v] = true
				top.needKey = false
			} else if top.isObj {
				top.needKey = true
			}
		default:
			if top := &stack[len(stack)-1]; top.isObj {
				top.needKey = true
			}
		}
	}
	return nil
}

// normalizeToJSON converts non-JSON manifest data into a JSON byte slice
// suitable for the strict json.Decoder path. JSON data is returned as-is so
// error offsets stay accurate.
func normalizeToJSON(path string, data []byte) ([]byte, error) {
	switch DetectFormat(path) {
	case FormatYAML:
		return yamlToJSON(path, data)
	case FormatTOML:
		return tomlToJSON(path, data)
	default:
		return data, nil
	}
}

func yamlToJSON(path string, data []byte) ([]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var node any
	if err := dec.Decode(&node); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("manifest %s: empty YAML document", path)
		}
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("manifest %s: %w", path, err)
		}
		return nil, fmt.Errorf("manifest %s: multiple YAML documents", path)
	}
	out, err := json.Marshal(node)
	if err != nil {
		return nil, fmt.Errorf("normalize YAML %s: %w", path, err)
	}
	return out, nil
}

func tomlToJSON(path string, data []byte) ([]byte, error) {
	var node any
	if err := gotoml.Unmarshal(data, &node); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	out, err := json.Marshal(node)
	if err != nil {
		return nil, fmt.Errorf("normalize TOML %s: %w", path, err)
	}
	return out, nil
}
