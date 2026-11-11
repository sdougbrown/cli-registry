package cliregistry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// inputRulesManifest writes a one-tool registry whose command is the test
// helper process, and returns the manifest path with the marker path the child
// creates when it runs. props is the tool's top-level property schema; every
// property is bound with style space.
func inputRulesManifest(t *testing.T, props map[string]any, rules []any) (path, marker string) {
	t.Helper()
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	bindings := make([]any, 0, len(names))
	for _, name := range names {
		bindings = append(bindings, map[string]any{
			"property": name, "style": "space", "flag": "--" + name,
		})
	}
	marker = filepath.Join(t.TempDir(), "spawned")
	body := map[string]any{
		"description": "input-rules fixture",
		"command":     []any{helperBin(t), "-test.run=TestHelperProcess", "--"},
		"env":         map[string]any{"GO_WANT_HELPER_PROCESS": "1", "FAKE_MARKER": marker},
		"timeout":     "5s",
		"inputSchema": map[string]any{"type": "object", "properties": props, "additionalProperties": false},
		"bindings":    bindings,
		"output":      map[string]any{"mode": "raw"},
	}
	if rules != nil {
		body["inputRules"] = rules
	}
	raw, err := json.Marshal(map[string]any{"version": 1, "tools": map[string]any{"tool": body}})
	if err != nil {
		t.Fatal(err)
	}
	return writeManifest(t, string(raw)), marker
}

func loadInputRules(t *testing.T, props map[string]any, rules []any) *Registry {
	t.Helper()
	path, _ := inputRulesManifest(t, props, rules)
	reg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return reg
}

func stringProps(names ...string) map[string]any {
	props := make(map[string]any, len(names))
	for _, name := range names {
		props[name] = map[string]any{"type": "string"}
	}
	return props
}

// inputRuleIssue is the normalized issue a rule violation reports: the rule's
// own reason travels in Message, and Path names the subject property (empty
// for a rule that constrains the whole invocation).
func wantInputRuleIssue(path, reason string) StructuralIssue {
	return StructuralIssue{Source: "input-rules", Code: "inputRule", Path: path, Message: reason}
}

func wantValidationIssues(t *testing.T, err error, want []StructuralIssue) {
	t.Helper()
	if len(want) == 0 {
		if err != nil {
			t.Fatalf("Plan returned %v, want no error", err)
		}
		return
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Plan error = %v (%T), want *ValidationError", err, err)
	}
	if !reflect.DeepEqual(ve.Issues, want) {
		t.Errorf("issues = %#v, want %#v", ve.Issues, want)
	}
}

func TestInputRulesGateSuppliedProperties(t *testing.T) {
	// A field-named rule gates a property the caller supplied: it is checked
	// against the values actually present, never against an omitted flag.
	reg := loadInputRules(t, stringProps("file", "entry", "prompt", "prompt-file"), []any{
		map[string]any{
			"type": "requires", "field": "file",
			"when":   map[string]any{"op": "present", "field": "entry"},
			"reason": "a roster file needs an entry",
		},
		map[string]any{
			"type": "requires", "field": "prompt-file",
			"when":   map[string]any{"op": "absent", "field": "prompt"},
			"reason": "cannot combine with --prompt",
		},
	})
	tool, ok := reg.Lookup("tool")
	if !ok {
		t.Fatal("lookup: tool not found")
	}

	cases := []struct {
		name string
		args map[string]any
		want []StructuralIssue
	}{
		{
			name: "gated property supplied without its dependency",
			args: map[string]any{"file": "r.json"},
			want: []StructuralIssue{wantInputRuleIssue("/file", "a roster file needs an entry")},
		},
		{
			name: "dependency supplied",
			args: map[string]any{"file": "r.json", "entry": "main"},
		},
		{
			name: "gated property omitted is not a violation",
			args: map[string]any{"entry": "main"},
		},
		{
			name: "excluded property supplied with its sibling",
			args: map[string]any{"prompt": "hi", "prompt-file": "p.txt"},
			want: []StructuralIssue{wantInputRuleIssue("/prompt-file", "cannot combine with --prompt")},
		},
		{
			name: "excluded property supplied alone",
			args: map[string]any{"prompt-file": "p.txt"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := reg.Plan(tool, tc.args)
			wantValidationIssues(t, err, tc.want)
		})
	}
}

func TestInputRulesInvocationWideRequirement(t *testing.T) {
	// A fieldless requires rule constrains every invocation: it must run even
	// when the caller supplies nothing, and it reads the properties named by
	// its expression.
	reg := loadInputRules(t, stringProps("loop-file", "team-file", "roster-file"), []any{
		map[string]any{
			"type":   "requires",
			"reason": "one of --loop-file, --team-file, or --roster-file is required",
			"when": map[string]any{"op": "or", "exprs": []any{
				map[string]any{"op": "truthy", "field": "loop-file"},
				map[string]any{"op": "truthy", "field": "team-file"},
				map[string]any{"op": "truthy", "field": "roster-file"},
			}},
		},
	})
	tool, _ := reg.Lookup("tool")
	reason := "one of --loop-file, --team-file, or --roster-file is required"

	cases := []struct {
		name string
		args map[string]any
		want []StructuralIssue
	}{
		{
			name: "nothing supplied",
			args: map[string]any{},
			want: []StructuralIssue{wantInputRuleIssue("", reason)},
		},
		{
			name: "explicitly empty value",
			args: map[string]any{"loop-file": ""},
			want: []StructuralIssue{wantInputRuleIssue("", reason)},
		},
		{
			name: "one non-empty value",
			args: map[string]any{"team-file": "foo"},
		},
		{
			name: "all empty values",
			args: map[string]any{"loop-file": "", "team-file": ""},
			want: []StructuralIssue{wantInputRuleIssue("", reason)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := reg.Plan(tool, tc.args)
			wantValidationIssues(t, err, tc.want)
		})
	}
}

func TestInputRulesInvocationWideEnabledWhen(t *testing.T) {
	// A fieldless enabledWhen is the same invocation-wide gate. Its expression
	// distinguishes an omitted property from one supplied empty: an empty
	// string is a supplied value.
	reg := loadInputRules(t, stringProps("allow"), []any{
		map[string]any{
			"type":   "enabledWhen",
			"reason": "--allow is required",
			"when":   map[string]any{"op": "present", "field": "allow"},
		},
	})
	tool, _ := reg.Lookup("tool")

	cases := []struct {
		name string
		args map[string]any
		want []StructuralIssue
	}{
		{
			name: "property omitted",
			args: map[string]any{},
			want: []StructuralIssue{wantInputRuleIssue("", "--allow is required")},
		},
		{
			name: "property supplied empty",
			args: map[string]any{"allow": ""},
		},
		{
			name: "property supplied",
			args: map[string]any{"allow": "yes"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := reg.Plan(tool, tc.args)
			wantValidationIssues(t, err, tc.want)
		})
	}
}

func TestInputRulesFieldlessSubjectForms(t *testing.T) {
	// Only an absent `field` and an empty string are fieldless: both gate the
	// whole invocation. A null member is stripped by manifest normalization
	// before the tool body is decoded, so it reaches the compiler as absent and
	// behaves the same way.
	const reason = "one source is required"
	cases := []struct {
		name    string
		rule    map[string]any
		wantErr []StructuralIssue
	}{
		{
			name: "omitted field",
			rule: map[string]any{
				"type": "requires", "reason": reason,
				"when": map[string]any{"op": "truthy", "field": "a"},
			},
			wantErr: []StructuralIssue{wantInputRuleIssue("", reason)},
		},
		{
			name: "empty string field",
			rule: map[string]any{
				"type": "requires", "field": "", "reason": reason,
				"when": map[string]any{"op": "truthy", "field": "a"},
			},
			wantErr: []StructuralIssue{wantInputRuleIssue("", reason)},
		},
		{
			name: "null field",
			rule: map[string]any{
				"type": "requires", "field": nil, "reason": reason,
				"when": map[string]any{"op": "truthy", "field": "a"},
			},
			wantErr: []StructuralIssue{wantInputRuleIssue("", reason)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := loadInputRules(t, stringProps("a"), []any{tc.rule})
			tool, _ := reg.Lookup("tool")
			_, err := reg.Plan(tool, map[string]any{})
			wantValidationIssues(t, err, tc.wantErr)
			if _, err := reg.Plan(tool, map[string]any{"a": "x"}); err != nil {
				t.Errorf("Plan with a satisfied fieldless rule: %v", err)
			}
		})
	}
}

func TestInputRulesRejectNonStringSubjectAtLoad(t *testing.T) {
	// A requires/enabledWhen subject must be a string. A malformed subject is
	// not an absent one: overwriting it with the invocation field would silently
	// widen a property gate to every invocation, so the load fails instead.
	when := map[string]any{"op": "present", "field": "a"}
	cases := []struct {
		name  string
		props map[string]any
		rules []any
	}{
		{
			name:  "requires with a numeric field",
			props: stringProps("a"),
			rules: []any{map[string]any{"type": "requires", "field": 42, "when": when}},
		},
		{
			name:  "requires with a boolean field",
			props: stringProps("a"),
			rules: []any{map[string]any{"type": "requires", "field": false, "when": when}},
		},
		{
			name:  "requires with an object field",
			props: stringProps("a"),
			rules: []any{map[string]any{
				"type": "requires", "field": map[string]any{"property": "a"}, "when": when,
			}},
		},
		{
			name:  "requires with an array field",
			props: stringProps("a"),
			rules: []any{map[string]any{"type": "requires", "field": []any{"a"}, "when": when}},
		},
		{
			name:  "enabledWhen with a numeric field",
			props: stringProps("a"),
			rules: []any{map[string]any{"type": "enabledWhen", "field": 3.5, "when": when}},
		},
		{
			name:  "enabledWhen with a boolean field",
			props: stringProps("a"),
			rules: []any{map[string]any{"type": "enabledWhen", "field": true, "when": when}},
		},
		{
			name:  "anyOf operand with a numeric field",
			props: stringProps("a"),
			rules: []any{map[string]any{"type": "anyOf", "rules": []any{
				map[string]any{"type": "requires", "field": 7, "when": when},
			}}},
		},
		{
			name:  "eitherOf operand with a boolean field",
			props: stringProps("a"),
			rules: []any{map[string]any{
				"type": "eitherOf", "group": "g",
				"branches": map[string]any{"only": []any{
					map[string]any{"type": "enabledWhen", "field": true, "when": when},
				}},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := inputRulesManifest(t, tc.props, tc.rules)
			_, err := Load(path)
			if err == nil {
				t.Fatal("expected a load failure for a non-string rule subject")
			}
			if !strings.Contains(err.Error(), "must be a string") {
				t.Errorf("error %q does not report the malformed subject", err)
			}
			var de *DefinitionError
			if !errors.As(err, &de) || de.Tool != "tool" {
				t.Errorf("error = %v (%T), want a DefinitionError naming the tool", err, err)
			}
		})
	}
}

func TestInputRulesBlockBeforeAnythingIsBuilt(t *testing.T) {
	// A rule violation is a caller error raised after the input schema and
	// before argv: Call never spawns, and a host that plans the call itself
	// gets the same *ValidationError from Plan.
	path, marker := inputRulesManifest(t, stringProps("file", "entry"), []any{
		map[string]any{
			"type": "requires", "field": "file",
			"when":   map[string]any{"op": "present", "field": "entry"},
			"reason": "a roster file needs an entry",
		},
	})
	reg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	tool, _ := reg.Lookup("tool")

	invalid := map[string]any{"file": "r.json"}
	want := []StructuralIssue{wantInputRuleIssue("/file", "a roster file needs an entry")}

	if _, err := reg.Call(context.Background(), tool, invalid); err == nil {
		t.Fatal("Call: expected a validation error")
	} else {
		wantValidationIssues(t, err, want)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Call spawned a process for a rule violation: %v", err)
	}

	if _, err := reg.Plan(tool, invalid); err == nil {
		t.Fatal("Plan: expected a validation error")
	} else {
		wantValidationIssues(t, err, want)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Plan spawned a process for a rule violation: %v", err)
	}

	// The same tool runs when the rules hold: the fixture records its start.
	if _, err := reg.Call(context.Background(), tool, map[string]any{"file": "r.json", "entry": "main"}); err != nil {
		t.Fatalf("Call with a satisfied rule: %v", err)
	}
	waitForFile(t, marker, time.Second)
}

func TestInputRulesDisablesTargets(t *testing.T) {
	// disables turns its named targets off while the condition holds, and a
	// target the caller did not supply cannot be violated.
	reg := loadInputRules(t, map[string]any{
		"json": map[string]any{"type": "boolean"},
		"yaml": map[string]any{"type": "boolean"},
	}, []any{
		map[string]any{
			"type":    "disables",
			"targets": []any{"json"},
			"when":    map[string]any{"op": "present", "field": "yaml"},
			"reason":  "--yaml overrides --json",
		},
	})
	tool, _ := reg.Lookup("tool")

	cases := []struct {
		name string
		args map[string]any
		want []StructuralIssue
	}{
		{
			name: "source and target supplied",
			args: map[string]any{"yaml": true, "json": true},
			want: []StructuralIssue{wantInputRuleIssue("/json", "--yaml overrides --json")},
		},
		{
			name: "a false source is still supplied",
			args: map[string]any{"yaml": false, "json": true},
			want: []StructuralIssue{wantInputRuleIssue("/json", "--yaml overrides --json")},
		},
		{
			name: "target not supplied",
			args: map[string]any{"yaml": true},
		},
		{
			name: "source not supplied",
			args: map[string]any{"json": true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := reg.Plan(tool, tc.args)
			wantValidationIssues(t, err, tc.want)
		})
	}
}

func TestInputRulesComposites(t *testing.T) {
	t.Run("anyOf passes when any operand passes", func(t *testing.T) {
		reg := loadInputRules(t, stringProps("a", "b"), []any{
			map[string]any{"type": "anyOf", "rules": []any{
				map[string]any{
					"type": "requires", "field": "b",
					"when":   map[string]any{"op": "present", "field": "a"},
					"reason": "b needs a",
				},
				map[string]any{
					"type": "enabledWhen", "field": "b",
					"when":   map[string]any{"op": "truthy", "field": "a"},
					"reason": "a must not be empty",
				},
			}},
		})
		tool, _ := reg.Lookup("tool")

		cases := []struct {
			name string
			args map[string]any
			want []StructuralIssue
		}{
			{
				name: "every operand fails",
				args: map[string]any{"b": "x"},
				want: []StructuralIssue{wantInputRuleIssue("/b", "b needs a")},
			},
			{
				name: "one operand fails and one passes",
				args: map[string]any{"a": "", "b": "x"},
			},
			{
				name: "every operand passes",
				args: map[string]any{"a": "x", "b": "x"},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := reg.Plan(tool, tc.args)
				wantValidationIssues(t, err, tc.want)
			})
		}
	})

	t.Run("eitherOf branches constrain the invocation", func(t *testing.T) {
		reg := loadInputRules(t, stringProps("file", "text"), []any{
			map[string]any{
				"type":  "eitherOf",
				"group": "source",
				"branches": map[string]any{
					"file": []any{map[string]any{
						"type":   "requires",
						"reason": "one of --file or --text is required",
						"when":   map[string]any{"op": "truthy", "field": "file"},
					}},
					"text": []any{map[string]any{
						"type":   "requires",
						"reason": "one of --file or --text is required",
						"when":   map[string]any{"op": "truthy", "field": "text"},
					}},
				},
			},
		})
		tool, _ := reg.Lookup("tool")
		reason := "one of --file or --text is required"

		cases := []struct {
			name string
			args map[string]any
			want []StructuralIssue
		}{
			{
				name: "no branch holds",
				args: map[string]any{},
				want: []StructuralIssue{wantInputRuleIssue("", reason)},
			},
			{
				name: "file branch holds",
				args: map[string]any{"file": "f"},
			},
			{
				name: "text branch holds",
				args: map[string]any{"text": "t"},
			},
			{
				name: "an empty value does not hold",
				args: map[string]any{"file": "", "text": "t"},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := reg.Plan(tool, tc.args)
				wantValidationIssues(t, err, tc.want)
			})
		}
	})

	t.Run("anyOf over fieldless operands gates the invocation", func(t *testing.T) {
		// A composite wrapper carries no field of its own; its fieldless
		// operands are pinned to the same invocation subject, so anyOf still
		// reports one invocation-wide verdict.
		reg := loadInputRules(t, stringProps("x", "y"), []any{
			map[string]any{"type": "anyOf", "rules": []any{
				map[string]any{
					"type":   "requires",
					"reason": "x is required",
					"when":   map[string]any{"op": "present", "field": "x"},
				},
				map[string]any{
					"type":   "requires",
					"reason": "y is required",
					"when":   map[string]any{"op": "present", "field": "y"},
				},
			}},
		})
		tool, _ := reg.Lookup("tool")

		cases := []struct {
			name string
			args map[string]any
			want []StructuralIssue
		}{
			{
				name: "every operand fails",
				args: map[string]any{},
				want: []StructuralIssue{wantInputRuleIssue("", "x is required")},
			},
			{
				name: "one operand passes",
				args: map[string]any{"y": "v"},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := reg.Plan(tool, tc.args)
				wantValidationIssues(t, err, tc.want)
			})
		}
	})
}

func TestInputRulesReportEveryViolationInOrder(t *testing.T) {
	// Every failing subject is reported, the invocation-wide gate first and
	// the supplied properties in name order.
	reg := loadInputRules(t, stringProps("a", "b", "entry", "z"), []any{
		map[string]any{
			"type":   "requires",
			"reason": "z is required",
			"when":   map[string]any{"op": "truthy", "field": "z"},
		},
		map[string]any{
			"type": "requires", "field": "a",
			"when":   map[string]any{"op": "present", "field": "entry"},
			"reason": "a needs entry",
		},
		map[string]any{
			"type": "enabledWhen", "field": "b",
			"when":   map[string]any{"op": "present", "field": "entry"},
			"reason": "b needs entry",
		},
	})
	tool, _ := reg.Lookup("tool")

	_, err := reg.Plan(tool, map[string]any{"b": "y", "a": "x"})
	wantValidationIssues(t, err, []StructuralIssue{
		wantInputRuleIssue("", "z is required"),
		wantInputRuleIssue("/a", "a needs entry"),
		wantInputRuleIssue("/b", "b needs entry"),
	})
}

func TestInputRulesRejectInvalidRulesAtLoad(t *testing.T) {
	cases := []struct {
		name    string
		props   map[string]any
		rules   []any
		wantErr string
	}{
		{
			name:  "unknown rule subject",
			props: stringProps("a"),
			rules: []any{map[string]any{
				"type": "requires", "field": "nope",
				"when": map[string]any{"op": "present", "field": "a"},
			}},
			wantErr: `references unknown field "nope"`,
		},
		{
			name:  "unknown property in an expression",
			props: stringProps("a"),
			rules: []any{map[string]any{
				"type": "requires", "field": "a",
				"when": map[string]any{"op": "present", "field": "nope"},
			}},
			wantErr: `Unknown field "nope"`,
		},
		{
			name:  "fairness rule",
			props: stringProps("a"),
			rules: []any{map[string]any{
				"type": "fairWhen", "field": "a",
				"when": map[string]any{"op": "present", "field": "a"},
			}},
			wantErr: "not an input rule",
		},
		{
			name:  "oneOf",
			props: stringProps("a"),
			rules: []any{map[string]any{
				"type": "oneOf", "group": "g",
				"branches": map[string]any{"x": []any{"a"}},
			}},
			wantErr: "not an input rule",
		},
		{
			name:  "requires with a field dependency",
			props: stringProps("a", "b"),
			rules: []any{map[string]any{
				"type": "requires", "field": "a", "dependency": "b",
			}},
			wantErr: `must use "when"`,
		},
		{
			// Umpire's plural form (a non-empty list of field names or
			// expressions) is a different spelling of the same field-dependency
			// contract, so the dialect refuses it too.
			name:  "requires with field dependencies",
			props: stringProps("a", "b"),
			rules: []any{map[string]any{
				"type": "requires", "field": "a", "dependencies": []any{"b"},
			}},
			wantErr: `must use "when"`,
		},
		{
			name:  "disables with a field source",
			props: stringProps("a", "b"),
			rules: []any{map[string]any{
				"type": "disables", "targets": []any{"b"}, "source": "a",
			}},
			wantErr: `must use "when"`,
		},
		{
			name:  "composite operands targeting different subjects",
			props: stringProps("a", "b"),
			rules: []any{map[string]any{
				"type": "anyOf",
				"rules": []any{
					map[string]any{
						"type":   "requires",
						"reason": "the invocation needs a",
						"when":   map[string]any{"op": "truthy", "field": "a"},
					},
					map[string]any{
						"type": "requires", "field": "b",
						"when": map[string]any{"op": "present", "field": "a"},
					},
				},
			}},
			wantErr: "must target the same fields",
		},
		{
			name:    "rule is not an object",
			props:   stringProps("a"),
			rules:   []any{"nope"},
			wantErr: "must be an object",
		},
		{
			name:  "consumer condition in the input dialect",
			props: stringProps("a"),
			rules: []any{map[string]any{
				"type": "requires", "field": "a",
				"when": map[string]any{"op": "cond", "condition": "undeclared"},
			}},
			wantErr: `Unknown condition "undeclared"`,
		},
		{
			name:  "unknown rule member",
			props: stringProps("a"),
			rules: []any{map[string]any{
				"type": "requires", "field": "a", "bogus": true,
				"when": map[string]any{"op": "present", "field": "a"},
			}},
			wantErr: `unknown member "bogus"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := inputRulesManifest(t, tc.props, tc.rules)
			_, err := Load(path)
			if err == nil {
				t.Fatal("expected a load failure")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not include %q", err, tc.wantErr)
			}
			var de *DefinitionError
			if !errors.As(err, &de) || de.Tool != "tool" {
				t.Errorf("error = %v (%T), want a DefinitionError naming the tool", err, err)
			}
		})
	}
}

func TestInputRulesRejectNestedDialectForms(t *testing.T) {
	// The dialect refusal must reach rules nested in composites. Umpire accepts
	// these shapes, so a broken recursion would load them instead of refusing
	// the producer-only subset.
	cases := []struct {
		name    string
		rule    map[string]any
		wantErr string
	}{
		{
			name: "anyOf containing disables with a source",
			rule: map[string]any{"type": "anyOf", "rules": []any{
				map[string]any{
					"type": "disables", "targets": []any{"b"}, "source": "a",
					"reason": "a overrides b",
				},
				map[string]any{
					"type": "enabledWhen", "field": "b",
					"when": map[string]any{"op": "present", "field": "a"},
				},
			}},
			wantErr: `"disables" must use "when"`,
		},
		{
			name: "eitherOf containing requires with a dependency",
			rule: map[string]any{
				"type": "eitherOf", "group": "g",
				"branches": map[string]any{
					"x": []any{map[string]any{
						"type": "requires", "field": "a", "dependency": "b",
						"reason": "a needs b",
					}},
					"y": []any{map[string]any{
						"type": "enabledWhen", "field": "a",
						"when": map[string]any{"op": "present", "field": "b"},
					}},
				},
			},
			wantErr: `"requires" must use "when"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := inputRulesManifest(t, stringProps("a", "b"), []any{tc.rule})
			_, err := Load(path)
			if err == nil {
				t.Fatal("expected a load failure for the nested dialect form")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not include %q", err, tc.wantErr)
			}
		})
	}
}

func TestInputRulesReservedPropertyNameFailsLoad(t *testing.T) {
	// The invocation-wide gate needs an internal field no property may claim.
	// A manifest that declares it is refused rather than silently sharing it.
	path, _ := inputRulesManifest(t, stringProps("a", inputRuleInvocationField), []any{
		map[string]any{
			"type":   "requires",
			"reason": "the invocation needs a",
			"when":   map[string]any{"op": "truthy", "field": "a"},
		},
	})
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected a load failure for the reserved property name")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error %q does not name the reserved property", err)
	}
}

func TestInputRulesAbsentOrEmptyDeclaration(t *testing.T) {
	// The key is optional, and an empty list declares nothing: both load and
	// enforce nothing.
	for _, tc := range []struct {
		name  string
		rules []any
	}{
		{name: "absent", rules: nil},
		{name: "empty", rules: []any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := loadInputRules(t, stringProps("a"), tc.rules)
			tool, _ := reg.Lookup("tool")
			if _, err := reg.Plan(tool, map[string]any{"a": "x"}); err != nil {
				t.Errorf("Plan: %v", err)
			}
		})
	}
}

// producerInputRulesManifest is the shape github.com/sdougbrown/cli-contract's
// registry bridge emits for two commands with input rules: a field-named gate
// per flag and a fieldless requirement over the invocation.
const producerInputRulesManifest = `{
  "version": 1,
  "tools": {
    "testcli-run": {
      "description": "run a thing",
      "command": ["echo"],
      "timeout": "120s",
      "inputSchema": {
        "type": "object",
        "properties": {
          "entry": {"type": "string"},
          "file": {"type": "string"},
          "prompt": {"type": "string"},
          "prompt-file": {"type": "string"}
        },
        "additionalProperties": false
      },
      "inputRules": [
        {"type": "requires", "field": "file", "reason": "a roster file needs an entry",
         "when": {"op": "present", "field": "entry"}},
        {"type": "requires", "field": "prompt-file", "reason": "cannot combine with --prompt",
         "when": {"op": "absent", "field": "prompt"}}
      ],
      "bindings": [
        {"property": "entry", "style": "space", "flag": "--entry"},
        {"property": "file", "style": "space", "flag": "--file"},
        {"property": "prompt", "style": "space", "flag": "--prompt"},
        {"property": "prompt-file", "style": "space", "flag": "--prompt-file"}
      ],
      "output": {"mode": "raw"}
    },
    "testcli-verify": {
      "description": "verify a roster",
      "command": ["echo"],
      "timeout": "120s",
      "inputSchema": {
        "type": "object",
        "properties": {
          "loop-file": {"type": "string"},
          "team-file": {"type": "string"}
        },
        "additionalProperties": false
      },
      "inputRules": [
        {"type": "requires", "reason": "one of --loop-file or --team-file is required",
         "when": {"op": "or", "exprs": [
           {"op": "truthy", "field": "loop-file"},
           {"op": "truthy", "field": "team-file"}
         ]}}
      ],
      "bindings": [
        {"property": "loop-file", "style": "space", "flag": "--loop-file"},
        {"property": "team-file", "style": "space", "flag": "--team-file"}
      ],
      "output": {"mode": "raw"}
    }
  }
}`

func TestInputRulesAcceptTheProducerWire(t *testing.T) {
	// The manifest a producer emits loads as-is and enforces the same
	// verdicts the producer's own parser reports.
	reg, err := Load(writeManifest(t, producerInputRulesManifest))
	if err != nil {
		t.Fatalf("load producer manifest: %v", err)
	}

	run, _ := reg.Lookup("testcli-run")
	runCases := []struct {
		name string
		args map[string]any
		want []StructuralIssue
	}{
		{
			name: "gated flag without its dependency",
			args: map[string]any{"file": "r.json"},
			want: []StructuralIssue{wantInputRuleIssue("/file", "a roster file needs an entry")},
		},
		{
			name: "gated flag with its dependency",
			args: map[string]any{"file": "r.json", "entry": "main"},
		},
		{
			name: "excluded pair",
			args: map[string]any{"prompt": "hi", "prompt-file": "p.txt"},
			want: []StructuralIssue{wantInputRuleIssue("/prompt-file", "cannot combine with --prompt")},
		},
	}
	for _, tc := range runCases {
		t.Run("run/"+tc.name, func(t *testing.T) {
			_, err := reg.Plan(run, tc.args)
			wantValidationIssues(t, err, tc.want)
		})
	}

	verify, _ := reg.Lookup("testcli-verify")
	verifyCases := []struct {
		name string
		args map[string]any
		want []StructuralIssue
	}{
		{
			name: "no source supplied",
			args: map[string]any{},
			want: []StructuralIssue{wantInputRuleIssue("", "one of --loop-file or --team-file is required")},
		},
		{
			name: "one source supplied",
			args: map[string]any{"team-file": "team.json"},
		},
	}
	for _, tc := range verifyCases {
		t.Run("verify/"+tc.name, func(t *testing.T) {
			_, err := reg.Plan(verify, tc.args)
			wantValidationIssues(t, err, tc.want)
		})
	}
}

func TestInputRulesEmptyReasonStillExplainsRejection(t *testing.T) {
	// Umpire accepts an explicitly empty reason and reports it as an empty
	// status reason, while an absent reason gets Umpire's default. The issue
	// must still explain the rejection, not carry the empty string through to
	// the host.
	reg := loadInputRules(t, stringProps("file", "entry"), []any{
		map[string]any{
			"type": "requires", "field": "file",
			"when":   map[string]any{"op": "present", "field": "entry"},
			"reason": "",
		},
	})
	tool, ok := reg.Lookup("tool")
	if !ok {
		t.Fatal("lookup: tool not found")
	}

	_, err := reg.Plan(tool, map[string]any{"file": "r.json"})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Plan error = %v (%T), want *ValidationError", err, err)
	}
	if len(ve.Issues) != 1 {
		t.Fatalf("issues = %#v, want one input-rule issue", ve.Issues)
	}
	issue := ve.Issues[0]
	if issue.Source != "input-rules" || issue.Code != "inputRule" || issue.Path != "/file" {
		t.Fatalf("issue = %#v, want an input-rule issue on /file", issue)
	}
	message := strings.TrimSpace(issue.Message)
	if message == "" || message == issue.Code || message == issue.Path {
		t.Errorf("issue message = %q, want a nonempty explanation", issue.Message)
	}
}

func TestInputRulesFollowSchemaValidation(t *testing.T) {
	// A value that fails the input schema is reported as a schema issue only:
	// the rules run after the schema, never instead of it.
	reg := loadInputRules(t, map[string]any{"a": map[string]any{"type": "string"}}, []any{
		map[string]any{
			"type":   "requires",
			"reason": "a is required",
			"when":   map[string]any{"op": "truthy", "field": "a"},
		},
	})
	tool, _ := reg.Lookup("tool")

	_, err := reg.Plan(tool, map[string]any{"a": 42})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v (%T), want *ValidationError", err, err)
	}
	for _, issue := range ve.Issues {
		if issue.Source != "json-schema" {
			t.Errorf("issue %+v: rules must not run before the schema", issue)
		}
	}
}
