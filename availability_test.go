package cliregistry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sdougbrown/cli-registry/manifest"
)

const availabilityFixture = `{
	"version": 1,
	"tools": {
		"gh-pr-view": {
			"description": "List pull requests",
			"command": ["gh", "pr", "list"],
			"timeout": "30s",
			"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
			"bindings": [],
			"output": {"mode": "raw"}
		},
		"gh-pr-edit": {
			"description": "Edit a pull request",
			"command": ["gh", "pr", "edit"],
			"timeout": "30s",
			"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
			"bindings": [],
			"output": {"mode": "raw"}
		},
		"avenor-reply": {
			"description": "Reply through messaging",
			"command": ["echo"],
			"timeout": "30s",
			"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
			"bindings": [],
			"output": {"mode": "raw"}
		}
	},
	"availability": {
		"conditions": {
			"profile_side_effecting": {"type": "boolean", "description": "the profile offers a side-effecting tool"},
			"messaging_enabled": {"type": "boolean"}
		},
		"rules": [
			{"type": "enabledWhen", "field": "gh-pr-edit",
			 "when": {"op": "cond", "condition": "profile_side_effecting"},
			 "reason": "the profile is read-only"},
			{"type": "requires", "field": "avenor-reply",
			 "when": {"op": "cond", "condition": "messaging_enabled"},
			 "reason": "messaging is not enabled"}
		]
	}
}`

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "r.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAvailabilityGatesTools(t *testing.T) {
	reg, err := Load(writeManifest(t, availabilityFixture))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// Everything on: all tools available.
	availability, err := reg.Availability(map[string]any{
		"profile_side_effecting": true, "messaging_enabled": true,
	})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	for _, name := range []string{"gh-pr-view", "gh-pr-edit", "avenor-reply"} {
		if !availability[name].Available {
			t.Errorf("%s should be available: %+v", name, availability[name])
		}
	}

	// Read-only profile: the edit tool drops out with its reason.
	availability, err = reg.Availability(map[string]any{
		"profile_side_effecting": false, "messaging_enabled": true,
	})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if availability["gh-pr-edit"].Available {
		t.Errorf("gh-pr-edit should be unavailable on a read-only profile")
	}
	if availability["gh-pr-edit"].Reason != "the profile is read-only" {
		t.Errorf("reason = %q", availability["gh-pr-edit"].Reason)
	}
	if !availability["gh-pr-view"].Available {
		t.Errorf("gh-pr-view should stay available: %+v", availability["gh-pr-view"])
	}
	if !availability["avenor-reply"].Available {
		t.Errorf("avenor-reply should stay available: %+v", availability["avenor-reply"])
	}

	// Messaging off: the reply tool drops out.
	availability, err = reg.Availability(map[string]any{
		"profile_side_effecting": true, "messaging_enabled": false,
	})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if availability["avenor-reply"].Available {
		t.Errorf("avenor-reply should be unavailable without messaging")
	}
	if availability["avenor-reply"].Reason != "messaging is not enabled" {
		t.Errorf("reason = %q", availability["avenor-reply"].Reason)
	}
}

func TestAvailabilityInputs(t *testing.T) {
	reg, err := Load(writeManifest(t, availabilityFixture))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	inputs := reg.AvailabilityInputs()
	if inputs["profile_side_effecting"] != "boolean" || inputs["messaging_enabled"] != "boolean" {
		t.Errorf("inputs = %v", inputs)
	}

	plain, err := Load(writeManifest(t, `{
		"version": 1,
		"tools": {"t": {"description": "d", "command": ["echo"], "timeout": "1s",
			"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
			"bindings": [], "output": {"mode": "raw"}}}
	}`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := plain.AvailabilityInputs(); len(got) != 0 {
		t.Errorf("plain registry should declare no inputs: %v", got)
	}
	availability, err := plain.Availability(nil)
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if !availability["t"].Available {
		t.Errorf("tool should be available with no availability block")
	}
}

func TestAvailabilityMissingConditionInputFails(t *testing.T) {
	reg, err := Load(writeManifest(t, availabilityFixture))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := reg.Availability(map[string]any{"profile_side_effecting": true}); err == nil {
		t.Fatal("expected an error when a declared condition input is missing")
	}
}

func TestAvailabilityLoadFailures(t *testing.T) {
	validTool := `"gh-pr": {"description": "d", "command": ["gh", "pr", "list"], "timeout": "30s",
		"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
		"bindings": [], "output": {"mode": "raw"}}`

	cases := []struct {
		name     string
		manifest string
		wantErr  string
	}{
		{
			name: "undeclared condition",
			manifest: `{"version": 1, "tools": {` + validTool + `},
				"availability": {
					"conditions": {"a": {"type": "boolean"}},
					"rules": [{"type": "enabledWhen", "field": "gh-pr",
						"when": {"op": "cond", "condition": "b"}}]
				}}`,
			wantErr: `undeclared condition "b"`,
		},
		{
			name: "field-bound expression op",
			manifest: `{"version": 1, "tools": {` + validTool + `},
				"availability": {
					"conditions": {"a": {"type": "boolean"}},
					"rules": [{"type": "enabledWhen", "field": "gh-pr",
						"when": {"op": "eq", "field": "gh-pr", "value": 1}}]
				}}`,
			wantErr: "not a condition expression",
		},
		{
			name: "requires with a tool dependency",
			manifest: `{"version": 1, "tools": {` + validTool + `},
				"availability": {
					"conditions": {"a": {"type": "boolean"}},
					"rules": [{"type": "requires", "field": "gh-pr", "dependency": "gh-pr"}]
				}}`,
			wantErr: "do not read tool fields",
		},
		{
			name: "oneOf is not an availability rule",
			manifest: `{"version": 1, "tools": {` + validTool + `},
				"availability": {
					"conditions": {"a": {"type": "boolean"}},
					"rules": [{"type": "oneOf", "group": "g",
						"branches": {"b": ["gh-pr"]}}]
				}}`,
			wantErr: "not an availability rule",
		},
		{
			name: "invalid umpire schema",
			manifest: `{"version": 1, "tools": {` + validTool + `},
				"availability": {
					"conditions": {"a": {"type": "nonsense"}},
					"rules": []
				}}`,
			wantErr: "supported type",
		},
		{
			name: "disables with a tool source",
			manifest: `{"version": 1, "tools": {` + validTool + `},
				"availability": {
					"conditions": {"a": {"type": "boolean"}},
					"rules": [{"type": "disables", "targets": ["gh-pr"], "source": "gh-pr"}]
				}}`,
			wantErr: `"disables" must use "when" with a condition expression`,
		},
		{
			name: "requires with plural dependencies",
			manifest: `{"version": 1, "tools": {` + validTool + `},
				"availability": {
					"conditions": {"a": {"type": "boolean"}},
					"rules": [{"type": "requires", "field": "gh-pr", "dependencies": ["gh-pr"]}]
				}}`,
			wantErr: `"requires" must use "when" with a condition expression`,
		},
		{
			name: "anyOf wrapping a field-bound expression",
			manifest: `{"version": 1, "tools": {` + validTool + `},
				"availability": {
					"conditions": {"a": {"type": "boolean"}},
					"rules": [{"type": "anyOf", "rules": [
						{"type": "enabledWhen", "field": "gh-pr",
						 "when": {"op": "eq", "field": "gh-pr", "value": 1}}
					]}]
				}}`,
			wantErr: "not a condition expression",
		},
		{
			name: "not wrapping an undeclared condition",
			manifest: `{"version": 1, "tools": {` + validTool + `},
				"availability": {
					"conditions": {"a": {"type": "boolean"}},
					"rules": [{"type": "enabledWhen", "field": "gh-pr",
						"when": {"op": "not", "expr": {"op": "condEq", "condition": "undeclared", "value": true}}}]
				}}`,
			wantErr: `undeclared condition "undeclared"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeManifest(t, tc.manifest))
			if err == nil {
				t.Fatalf("expected load failure")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not include %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestAvailabilityCompositeRules(t *testing.T) {
	manifestBody := `{
		"version": 1,
		"tools": {
			"a": {"description": "a", "command": ["echo"], "timeout": "1s",
				"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
				"bindings": [], "output": {"mode": "raw"}},
			"b": {"description": "b", "command": ["echo"], "timeout": "1s",
				"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
				"bindings": [], "output": {"mode": "raw"}}
		},
		"availability": {
			"conditions": {
				"mode": {"type": "string"}
			},
			"rules": [
				{"type": "enabledWhen", "field": "a",
				 "when": {"op": "condIn", "condition": "mode", "values": ["full", "extended"]},
				 "reason": "mode does not allow a"},
				{"type": "enabledWhen", "field": "b",
				 "when": {"op": "or", "exprs": [
					{"op": "condEq", "condition": "mode", "value": "full"},
					{"op": "condEq", "condition": "mode", "value": "extended"}
				 ]},
				 "reason": "mode does not allow b"}
			]
		}
	}`
	reg, err := Load(writeManifest(t, manifestBody))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	availability, err := reg.Availability(map[string]any{
		"mode": "full",
	})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if !availability["a"].Available || !availability["b"].Available {
		t.Errorf("full mode should enable both: %+v", availability)
	}

	availability, err = reg.Availability(map[string]any{
		"mode": "basic",
	})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if availability["a"].Available || availability["b"].Available {
		t.Errorf("basic mode should disable both: %+v", availability)
	}
	if len(availability["a"].Reasons) != 1 || availability["a"].Reasons[0] != "mode does not allow a" {
		t.Errorf("a reasons = %v", availability["a"].Reasons)
	}
}

func TestAvailabilityYAMLAndTOML(t *testing.T) {
	toolBlock := `tools:
  t:
    description: d
    command: [echo]
    timeout: 1s
    inputSchema:
      type: object
      properties: {}
      additionalProperties: false
    bindings: []
    output:
      mode: raw
availability:
  conditions:
    on:
      type: boolean
  rules:
    - type: enabledWhen
      field: t
      when:
        op: cond
        condition: on
      reason: not on
`
	yamlBody := "version: 1\n" + toolBlock
	dir := t.TempDir()
	path := filepath.Join(dir, "r.yaml")
	if err := os.WriteFile(path, []byte(yamlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := Load(path)
	if err != nil {
		t.Fatalf("load yaml: %v", err)
	}
	availability, err := reg.Availability(map[string]any{"on": false})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if availability["t"].Available || availability["t"].Reason != "not on" {
		t.Errorf("yaml availability = %+v", availability["t"])
	}

	tomlBody := `version = 1

[tools.t]
description = "d"
command = ["echo"]
timeout = "1s"
bindings = []

[tools.t.inputSchema]
type = "object"
properties = {}
additionalProperties = false

[tools.t.output]
mode = "raw"

[availability.conditions.on]
type = "boolean"

[[availability.rules]]
type = "enabledWhen"
field = "t"
reason = "not on"

[availability.rules.when]
op = "cond"
condition = "on"
`
	path = filepath.Join(dir, "r.toml")
	if err := os.WriteFile(path, []byte(tomlBody), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err = Load(path)
	if err != nil {
		t.Fatalf("load toml: %v", err)
	}
	availability, err = reg.Availability(map[string]any{"on": false})
	if err != nil {
		t.Fatalf("availability toml: %v", err)
	}
	if availability["t"].Available || availability["t"].Reason != "not on" {
		t.Errorf("toml availability = %+v", availability["t"])
	}
}

func TestAvailabilityDuplicateAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	tool := `"t": {"description": "d", "command": ["echo"], "timeout": "1s",
		"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
		"bindings": [], "output": {"mode": "raw"}}`
	block := `"availability": {"conditions": {"on": {"type": "boolean"}}, "rules": []}`
	for _, name := range []string{"a.json", "b.json"} {
		body := `{"version": 1, "tools": {` + tool + `}, ` + block + `}`
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected duplicate availability load failure")
	}
	if !strings.Contains(err.Error(), manifest.ErrDuplicateAvailability.Error()) {
		t.Errorf("error %q does not include %q", err.Error(), manifest.ErrDuplicateAvailability.Error())
	}
}
