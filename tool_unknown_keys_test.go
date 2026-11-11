package cliregistry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A tool body is decoded strictly: a misspelled key must fail the load and
// name the key, instead of silently discarding the configuration it carried
// (an "inputRule" typo would otherwise drop every input rule). The spec
// mandates DisallowUnknownFields at ingest; the tool body is part of the
// manifest. Every supported format runs through the same strict decode, so
// one typo is checked per format.
func TestUnknownToolKeyIsRejectedAtLoad(t *testing.T) {
	const jsonBody = `{"version":1,"tools":{"t":{
		"description":"d","command":["echo"],"timeout":"1s",
		"inputSchema":{"type":"object","properties":{},"additionalProperties":false},
		"bindings":[],
		"inputRule":[{"type":"requires","field":"t","when":{"op":"cond","condition":"on"}}]
	}}}`
	const yamlBody = `version: 1
tools:
  t:
    description: d
    command: ["echo"]
    timeout: 1s
    inputSchema:
      type: object
      properties: {}
      additionalProperties: false
    bindings: []
    inputRule:
      - type: requires
        field: t
        when:
          op: cond
          condition: on
`
	const tomlBody = `version = 1

[tools.t]
description = "d"
command = ["echo"]
timeout = "1s"
bindings = []

[tools.t.inputRule]
type = "requires"
field = "t"

[tools.t.inputRule.when]
op = "cond"
condition = "on"

[tools.t.inputSchema]
type = "object"
properties = {}
additionalProperties = false
`
	for _, tt := range []struct{ name, ext, body string }{
		{"json", ".json", jsonBody},
		{"yaml", ".yaml", yamlBody},
		{"toml", ".toml", tomlBody},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "tools"+tt.ext)
			if err := os.WriteFile(p, []byte(tt.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			var de *DefinitionError
			if !errors.As(err, &de) {
				t.Fatalf("Load = %T (%v), want *DefinitionError", err, err)
			}
			if msg := err.Error(); !strings.Contains(msg, `"inputRule"`) {
				t.Fatalf("error %q does not name the unknown key", msg)
			}
		})
	}
}

// Unknown keys are rejected at every level of the tool body: a misspelled
// member inside output, bindings, or cwd would silently discard just that
// member's configuration.
func TestUnknownNestedToolKeyIsRejectedAtLoad(t *testing.T) {
	for _, tt := range []struct{ name, extra, unknownKey string }{
		{"output", `"output":{"mode":"raw","jsonPointer":"/x"}`, `"jsonPointer"`},
		{"bindings", `"bindings":[{"property":"a","style":"equals","flag":"--a","flagSpelling":"--a="}]`, `"flagSpelling"`},
		{"cwd", `"cwd":{"mode":"inherit","rootz":["/tmp"]}`, `"rootz"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := `{"version":1,"tools":{"t":{
				"description":"d","command":["echo"],"timeout":"1s",
				"inputSchema":{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":false},
				` + tt.extra + `
			}}}`
			p := filepath.Join(t.TempDir(), "tools.json")
			if err := os.WriteFile(p, []byte(m), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			var de *DefinitionError
			if !errors.As(err, &de) {
				t.Fatalf("Load = %T (%v), want *DefinitionError", err, err)
			}
			if msg := err.Error(); !strings.Contains(msg, tt.unknownKey) {
				t.Fatalf("error %q does not name the unknown key", msg)
			}
		})
	}
}

// The rejection names the manifest file the tool came from, so a multi-file
// load points at the file to fix.
func TestUnknownToolKeyErrorNamesTheManifestFile(t *testing.T) {
	dir := t.TempDir()
	bodies := map[string]string{
		"a.json": `{"version":1,"tools":{"t":{"description":"d","command":["echo"],"timeout":"1s","inputSchema":{"type":"object","properties":{},"additionalProperties":false},"bindings":[]}}}`,
		"b.json": `{"version":1,"tools":{"u":{"description":"d","command":["echo"],"timeout":"1s","bindings":[],"inpoutSchema":{"type":"object","properties":{},"additionalProperties":false}}}}`,
	}
	for name, body := range bodies {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("Load = nil, want unknown-key error")
	}
	var de *DefinitionError
	if !errors.As(err, &de) || de.Tool != "u" {
		t.Fatalf("error = %v (%T), want a DefinitionError naming tool %q", err, err, "u")
	}
	if msg := err.Error(); !strings.Contains(msg, "b.json") {
		t.Fatalf("error %q does not name the manifest file", msg)
	}
}

// The spec derives a tool's name from its key but lists "name" as a field, so
// a body may carry it redundantly. A matching name loads; a name that points
// at another tool fails the load naming both spellings.
func TestExplicitToolNameMustMatchItsKey(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		wantErr    string
	}{
		{"matching", `"name":"t","description":"d","command":["echo"],"timeout":"1s","bindings":[]`, ``},
		{"mismatched", `"name":"other","description":"d","command":["echo"],"timeout":"1s","bindings":[]`, `"other"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := `{"version":1,"tools":{"t":{` + tt.body + `,
				"inputSchema":{"type":"object","properties":{},"additionalProperties":false}
			}}}`
			p := filepath.Join(t.TempDir(), "tools.json")
			if err := os.WriteFile(p, []byte(m), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Load = %v, want success", err)
				}
				return
			}
			var de *DefinitionError
			if !errors.As(err, &de) {
				t.Fatalf("Load = %T (%v), want *DefinitionError", err, err)
			}
			if msg := err.Error(); !strings.Contains(msg, tt.wantErr) || !strings.Contains(msg, `"t"`) {
				t.Fatalf("error %q does not name the declared name and the key", msg)
			}
		})
	}
}

// A well-formed tool body still loads with every supported key present, so
// the strict decode adds no false rejections.
func TestKnownToolKeysStillLoad(t *testing.T) {
	m := `{"version":1,"tools":{"t":{
		"description":"d","command":["echo","-n"],"env":{"K":"V"},"cwd":{"mode":"inherit"},
		"timeout":"2s",
		"inputSchema":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"additionalProperties":false},
		"bindings":[{"property":"a","style":"equals","flag":"--a"},{"property":"b","style":"space","flag":"--b"}],
		"inputRules":[{"type":"requires","field":"a","when":{"op":"present","field":"b"},"reason":"b requires a"}],
		"output":{"mode":"raw","success_exit_codes":[0,1]}
	}}}`
	p := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(p, []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("Load = %v, want success", err)
	}
}
