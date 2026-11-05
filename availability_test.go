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

	t.Run("everything on: all tools available", func(t *testing.T) {
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
	})

	t.Run("read-only profile: edit drops out with its reason", func(t *testing.T) {
		availability, err := reg.Availability(map[string]any{
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
	})

	t.Run("messaging off: the reply tool drops out", func(t *testing.T) {
		availability, err := reg.Availability(map[string]any{
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
	})
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
			wantErr: `Unknown condition "b"`,
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
			wantErr: `Unknown condition "undeclared"`,
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

func TestAvailabilityMergeWithToolOnlyFile(t *testing.T) {
	// A sibling file without an availability block must not erase the block
	// another file in the same directory declared; losing it would gate
	// nothing and report every tool as available.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.json"), []byte(availabilityFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	toolOnly := `{"version": 1, "tools": {"echo-tool": {"description": "d", "command": ["echo"], "timeout": "1s",
		"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
		"bindings": [], "output": {"mode": "raw"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "b.json"), []byte(toolOnly), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	availability, err := reg.Availability(map[string]any{"profile_side_effecting": false, "messaging_enabled": true})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if !availability["echo-tool"].Available {
		t.Errorf("echo-tool should stay available: %+v", availability["echo-tool"])
	}
	if availability["gh-pr-edit"].Available {
		t.Error("gh-pr-edit should be gated off with profile_side_effecting false")
	}
	if availability["gh-pr-edit"].Reason != "the profile is read-only" {
		t.Errorf("gh-pr-edit reason = %q", availability["gh-pr-edit"].Reason)
	}
}

func TestAvailabilityCompositeFieldMismatchFailsLoad(t *testing.T) {
	// The Umpire runtime panics when composite operands gate different
	// tools; the load pre-validation must reject the manifest first.
	tool := `"t": {"description": "d", "command": ["echo"], "timeout": "1s",
		"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
		"bindings": [], "output": {"mode": "raw"}}`
	manifest := `{"version": 1, "tools": {` + tool + `,
		"echo": {"description": "e", "command": ["echo"], "timeout": "1s",
			"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
			"bindings": [], "output": {"mode": "raw"}}},
		"availability": {
			"conditions": {"a": {"type": "boolean"}},
			"rules": [{"type": "eitherOf", "group": "g", "branches": {
				"x": [{"type": "enabledWhen", "field": "t", "when": {"op": "cond", "condition": "a"}}],
				"y": [{"type": "requires", "field": "echo", "when": {"op": "cond", "condition": "a"}}]
			}}]
		}}`
	_, err := Load(writeManifest(t, manifest))
	if err == nil {
		t.Fatal("expected load failure for composite operands targeting different tools")
	}
	if !strings.Contains(err.Error(), "must target the same fields") {
		t.Errorf("error %q does not include the same-fields constraint", err.Error())
	}
}

func TestAvailabilityConditionsOnlyBlockLoads(t *testing.T) {
	// The spec declares rules optional; a conditions-only block must load
	// and leave every tool available.
	tool := `"t": {"description": "d", "command": ["echo"], "timeout": "1s",
		"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
		"bindings": [], "output": {"mode": "raw"}}`
	manifest := `{"version": 1, "tools": {` + tool + `},
		"availability": {"conditions": {"on": {"type": "boolean"}}}}`
	reg, err := Load(writeManifest(t, manifest))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	availability, err := reg.Availability(map[string]any{"on": true})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if !availability["t"].Available {
		t.Errorf("conditions-only block should gate nothing: %+v", availability["t"])
	}
	if len(reg.AvailabilityInputs()) != 1 {
		t.Errorf("declared inputs = %v", reg.AvailabilityInputs())
	}
}

func TestAvailabilityDisablesGatesTargets(t *testing.T) {
	// A disables rule with a condition expression turns its targets off
	// while the condition holds; targets stay enabled when it passes.
	tool := func(name string) string {
		return `"` + name + `": {"description": "d", "command": ["echo"], "timeout": "1s",
			"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
			"bindings": [], "output": {"mode": "raw"}}`
	}
	manifest := `{"version": 1, "tools": {` + tool("a") + `, ` + tool("b") + `},
		"availability": {
			"conditions": {"maintenance": {"type": "boolean"}},
			"rules": [{"type": "disables", "targets": ["b"],
				"when": {"op": "cond", "condition": "maintenance"},
				"reason": "maintenance window"}]
		}}`
	reg, err := Load(writeManifest(t, manifest))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	availability, err := reg.Availability(map[string]any{"maintenance": true})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if availability["b"].Available || availability["b"].Reason != "maintenance window" {
		t.Errorf("b during maintenance = %+v", availability["b"])
	}
	if !availability["a"].Available {
		t.Errorf("a should be untouched: %+v", availability["a"])
	}
	availability, err = reg.Availability(map[string]any{"maintenance": false})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if !availability["b"].Available {
		t.Errorf("b after maintenance = %+v", availability["b"])
	}
}

func TestAvailabilityWrongTypedInputCoerces(t *testing.T) {
	// Umpire evaluates condition values with JS-style truthiness and does
	// not enforce declared types at runtime: a non-boolean input to a
	// boolean condition coerces. This pins that behavior — if the contract
	// ever enforces declared types, this test should flip to expecting an
	// error.
	tool := `"t": {"description": "d", "command": ["echo"], "timeout": "1s",
		"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
		"bindings": [], "output": {"mode": "raw"}}`
	manifest := `{"version": 1, "tools": {` + tool + `},
		"availability": {
			"conditions": {"on": {"type": "boolean"}},
			"rules": [{"type": "enabledWhen", "field": "t",
				"when": {"op": "cond", "condition": "on"}}]
		}}`
	reg, err := Load(writeManifest(t, manifest))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// A non-empty string is truthy → the gate passes, matching Boolean().
	availability, err := reg.Availability(map[string]any{"on": "yes"})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if !availability["t"].Available {
		t.Errorf("truthy string should pass a cond gate: %+v", availability["t"])
	}
	// An empty string is falsy → the gate fails.
	availability, err = reg.Availability(map[string]any{"on": ""})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if availability["t"].Available {
		t.Errorf("empty string should fail a cond gate: %+v", availability["t"])
	}
}

func TestAvailabilityAndExpressionRuntime(t *testing.T) {
	tool := `"t": {"description": "d", "command": ["echo"], "timeout": "1s",
		"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
		"bindings": [], "output": {"mode": "raw"}}`
	manifest := `{"version": 1, "tools": {` + tool + `},
		"availability": {
			"conditions": {"window_open": {"type": "boolean"}, "staffed": {"type": "boolean"}},
			"rules": [{"type": "enabledWhen", "field": "t",
				"when": {"op": "and", "exprs": [
					{"op": "cond", "condition": "window_open"},
					{"op": "cond", "condition": "staffed"}
				]},
				"reason": "needs window and staffing"}]
		}}`
	reg, err := Load(writeManifest(t, manifest))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	availability, err := reg.Availability(map[string]any{"window_open": true, "staffed": true})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if !availability["t"].Available {
		t.Errorf("both conditions true should enable: %+v", availability["t"])
	}
	availability, err = reg.Availability(map[string]any{"window_open": true, "staffed": false})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if availability["t"].Available || availability["t"].Reason != "needs window and staffing" {
		t.Errorf("one condition false should disable with the rule reason: %+v", availability["t"])
	}
}

func TestAvailabilityRejectsSiblingKeys(t *testing.T) {
	// Umpire's schema accepts validators and excluded; the availability
	// block must not — their effects never surface through Availability.
	tool := `"t": {"description": "d", "command": ["echo"], "timeout": "1s",
		"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
		"bindings": [], "output": {"mode": "raw"}}`
	manifest := `{"version": 1, "tools": {` + tool + `},
		"availability": {
			"conditions": {"on": {"type": "boolean"}},
			"validators": {"t": {"op": "email", "error": "bad"}},
			"rules": []
		}}`
	_, err := Load(writeManifest(t, manifest))
	if err == nil || !strings.Contains(err.Error(), `unknown member "validators"`) {
		t.Errorf("error = %v, want a sibling-member rejection", err)
	}
}

func TestAvailabilityMergesAcrossFiles(t *testing.T) {
	// One manifest carries the availability block, a sibling carries only
	// tools; Load must keep the gating.
	dir := t.TempDir()
	gated := `"t": {"description": "d", "command": ["echo"], "timeout": "1s",
		"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
		"bindings": [], "output": {"mode": "raw"}}`
	first := `{"version": 1, "tools": {` + gated + `},
		"availability": {
			"conditions": {"on": {"type": "boolean"}},
			"rules": [{"type": "enabledWhen", "field": "t",
				"when": {"op": "cond", "condition": "on"}, "reason": "not on"}]
		}}`
	second := `{"version": 1, "tools": {"u": {"description": "d", "command": ["echo"], "timeout": "1s",
		"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
		"bindings": [], "output": {"mode": "raw"}}}}`
	for name, body := range map[string]string{"a.json": first, "b.json": second} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	availability, err := reg.Availability(map[string]any{"on": false})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	if availability["t"].Available || availability["t"].Reason != "not on" {
		t.Errorf("gating lost across files: %+v", availability["t"])
	}
	if !availability["u"].Available {
		t.Errorf("ungated tool should stay available: %+v", availability["u"])
	}
}

func TestAvailabilityReasonsAccumulate(t *testing.T) {
	tool := `"t": {"description": "d", "command": ["echo"], "timeout": "1s",
		"inputSchema": {"type": "object", "properties": {}, "additionalProperties": false},
		"bindings": [], "output": {"mode": "raw"}}`
	manifest := `{"version": 1, "tools": {` + tool + `},
		"availability": {
			"conditions": {"a": {"type": "boolean"}, "b": {"type": "boolean"}},
			"rules": [
				{"type": "enabledWhen", "field": "t",
				 "when": {"op": "cond", "condition": "a"}, "reason": "first failure"},
				{"type": "enabledWhen", "field": "t",
				 "when": {"op": "cond", "condition": "b"}, "reason": "second failure"}
			]
		}}`
	reg, err := Load(writeManifest(t, manifest))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	availability, err := reg.Availability(map[string]any{"a": false, "b": false})
	if err != nil {
		t.Fatalf("availability: %v", err)
	}
	got := availability["t"]
	if got.Available {
		t.Fatalf("both rules failing should disable the tool")
	}
	if len(got.Reasons) != 2 || got.Reasons[0] != "first failure" || got.Reasons[1] != "second failure" {
		t.Errorf("reasons = %v, want both in declaration order", got.Reasons)
	}
	if got.Reason != "first failure" {
		t.Errorf("reason = %q, want the first failure", got.Reason)
	}
}
