package cliregistry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// dashValues are the values a separate-value binding must refuse: strings that
// begin with a dash, a bare "-" and a bare "--" included, and negative numbers.
var dashValues = []struct {
	name  string
	value any
	safe  any
	typ   string
	want  string
}{
	{"string", "--recursive", "safe", "string", "--recursive"},
	{"single dash", "-", "safe", "string", "-"},
	{"flag delimiter", "--", "safe", "string", "--"},
	{"negative int", -1, 1, "integer", "-1"},
	{"negative float", -1.5, 1.5, "number", "-1.5"},
}

// TestSeparateValueDashRefused covers a scalar bound with style: space: Plan and
// Call refuse it as a *ValidationError before anything is built, and no process
// runs.
func TestSeparateValueDashRefused(t *testing.T) {
	for _, tc := range dashValues {
		t.Run(tc.name, func(t *testing.T) {
			r := fixtureRegistry(t, map[string]fixtureDef{
				"space": probeTool(nil, fixtureDef{
					properties: map[string]any{"color": map[string]any{"type": tc.typ}},
					bindings:   []map[string]any{{"property": "color", "style": "space", "flag": "--color"}},
				}),
			})
			tool := mustTool(t, r, "space")
			marker := filepath.Join(t.TempDir(), "ran.marker")
			args := map[string]any{"color": tc.value}

			p, err := r.Plan(tool, args, WithEnv("FAKE_MARKER", marker))
			planIssues := assertDashRefusal(t, err, "/color", "color", tc.want).Issues
			if p != nil {
				t.Fatalf("Plan built a command for a refused value: %+v", p)
			}

			_, err = r.Call(context.Background(), tool, args, WithEnv("FAKE_MARKER", marker))
			callIssues := assertDashRefusal(t, err, "/color", "color", tc.want).Issues
			if !reflect.DeepEqual(planIssues, callIssues) {
				t.Fatalf("Plan issues %v, Call issues %v", planIssues, callIssues)
			}
			assertNotRun(t, marker)

			// Control: the same fixture does spawn and write the marker for a
			// value it can read as data, so the absent marker means "refused",
			// not "broken fixture".
			if _, err := r.Call(context.Background(), tool, map[string]any{"color": tc.safe}, WithEnv("FAKE_MARKER", marker)); err != nil {
				t.Fatalf("control call: %v", err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("control call did not run: %v", err)
			}
		})
	}
}

// TestFlagDelimiterConsumedAsValueIsRefused is the poo review's `--` case: with
// two space bindings, "--ignore --" makes the command consume the delimiter as
// a value, so the next element is parsed as an option.
func TestFlagDelimiterConsumedAsValueIsRefused(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"space": probeTool(nil, fixtureDef{
			properties: map[string]any{
				"a_ignore": map[string]any{"type": "string"},
				"z_color":  map[string]any{"type": "string"},
			},
			bindings: []map[string]any{
				{"property": "a_ignore", "style": "space", "flag": "--ignore"},
				{"property": "z_color", "style": "space", "flag": "--color"},
			},
		}),
	})
	tool := mustTool(t, r, "space")
	marker := filepath.Join(t.TempDir(), "ran.marker")
	args := map[string]any{"a_ignore": "--", "z_color": "--recursive"}

	ve := assertDashRefusal(t, planErr(r, tool, args, marker), "/a_ignore", "a_ignore", "--")
	if !hasIssue(ve.Issues, "/z_color") {
		t.Fatalf("issues = %v, want one at /z_color too", ve.Issues)
	}
	assertDashRefusal(t, callErr(r, tool, args, marker), "/z_color", "z_color", "--recursive")
	assertNotRun(t, marker)

	// Control: ordinary values plan and run.
	res, err := r.Call(context.Background(), tool, map[string]any{"a_ignore": "x", "z_color": "y"})
	if err != nil {
		t.Fatalf("control call: %v", err)
	}
	want := append(fixturePrefix(), "--ignore", "x", "--color", "y")
	if got := decodeProbe(t, res).Args; !slices.Equal(got, want) {
		t.Fatalf("control argv = %q, want %q", got, want)
	}
}

// TestSeparateValueArrayElementRefused covers every element of an array bound
// with style: space, and the safe elements around a refused one.
func TestSeparateValueArrayElementRefused(t *testing.T) {
	arrSchema := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	r := fixtureRegistry(t, map[string]fixtureDef{
		"space": probeTool(nil, fixtureDef{
			properties: map[string]any{"tags": arrSchema},
			bindings:   []map[string]any{{"property": "tags", "style": "space", "flag": "--tag"}},
		}),
	})
	tool := mustTool(t, r, "space")
	marker := filepath.Join(t.TempDir(), "ran.marker")

	ve := assertDashRefusal(t, planErr(r, tool, map[string]any{"tags": []any{"one", "--recursive"}}, marker), "/tags/1", "tags", "--recursive")
	if len(ve.Issues) != 1 {
		t.Fatalf("issues = %v, want only the refused element", ve.Issues)
	}
	assertDashRefusal(t, callErr(r, tool, map[string]any{"tags": []any{"one", "--recursive"}}, marker), "/tags/1", "tags", "--recursive")
	assertNotRun(t, marker)

	// An empty array and a dash-free array emit only safe elements.
	if _, err := r.Plan(tool, map[string]any{"tags": []any{}}, WithEnv("FAKE_MARKER", marker)); err != nil {
		t.Fatalf("empty array refused: %v", err)
	}
	res, err := r.Call(context.Background(), tool, map[string]any{"tags": []any{"one", "two"}}, WithEnv("FAKE_MARKER", marker))
	if err != nil {
		t.Fatalf("safe array refused: %v", err)
	}
	want := append(fixturePrefix(), "--tag", "one", "--tag", "two")
	if got := decodeProbe(t, res).Args; !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// TestEqualsAcceptsDashLeadingValues keeps the documented workaround working:
// the value stays inside one argv element, where no parser reads it as an
// option, for a scalar and for every array element.
func TestEqualsAcceptsDashLeadingValues(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"equals": probeTool(nil, fixtureDef{
			properties: map[string]any{"color": map[string]any{"type": "string"}},
			bindings:   []map[string]any{{"property": "color", "style": "equals", "flag": "--color"}},
		}),
		"equals-array": probeTool(nil, fixtureDef{
			properties: map[string]any{"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
			bindings:   []map[string]any{{"property": "tags", "style": "equals", "flag": "--tag"}},
		}),
	})
	for _, tc := range []struct {
		name string
		tool string
		args map[string]any
		want []string
	}{
		{"scalar option", "equals", map[string]any{"color": "--recursive"}, []string{"--color=--recursive"}},
		{"scalar dash", "equals", map[string]any{"color": "-"}, []string{"--color=-"}},
		{"scalar delimiter", "equals", map[string]any{"color": "--"}, []string{"--color=--"}},
		{"array", "equals-array", map[string]any{"tags": []any{"-a", "--b"}}, []string{"--tag=-a", "--tag=--b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := mustTool(t, r, tc.tool)
			p, err := r.Plan(tool, tc.args)
			if err != nil {
				t.Fatalf("Plan refused an equals value: %v", err)
			}
			if got := p.Argv[len(p.Argv)-len(tc.want):]; !slices.Equal(got, tc.want) {
				t.Fatalf("tail argv = %q, want %q", got, tc.want)
			}
			res, err := r.Call(context.Background(), tool, tc.args)
			if err != nil {
				t.Fatalf("Call refused an equals value: %v", err)
			}
			want := append(fixturePrefix(), tc.want...)
			if got := decodeProbe(t, res).Args; !slices.Equal(got, want) {
				t.Fatalf("argv = %q, want %q", got, want)
			}
		})
	}
}

// TestAttachedShortValueIsNotASeparateElement pins the short style's attached
// form: the value is part of the flag's own argv element, so a dash-leading
// value is unaffected. (This library has no separate-value short form.)
func TestAttachedShortValueIsNotASeparateElement(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"short-scalar": probeTool(nil, fixtureDef{
			properties: map[string]any{"level": map[string]any{"type": "integer"}},
			bindings:   []map[string]any{{"property": "level", "style": "short", "flag": "-n"}},
		}),
		"short-string": probeTool(nil, fixtureDef{
			properties: map[string]any{"name": map[string]any{"type": "string"}},
			bindings:   []map[string]any{{"property": "name", "style": "short", "flag": "-l"}},
		}),
		"short-array": probeTool(nil, fixtureDef{
			properties: map[string]any{"names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
			bindings:   []map[string]any{{"property": "names", "style": "short", "flag": "-l"}},
		}),
	})
	for _, tc := range []struct {
		name string
		tool string
		args map[string]any
		want []string
	}{
		{"negative int", "short-scalar", map[string]any{"level": -1}, []string{"-n-1"}},
		{"option string", "short-string", map[string]any{"name": "--recursive"}, []string{"-l--recursive"}},
		{"delimiter", "short-string", map[string]any{"name": "--"}, []string{"-l--"}},
		{"array", "short-array", map[string]any{"names": []any{"-a", "--"}}, []string{"-l-a", "-l--"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := mustTool(t, r, tc.tool)
			p, err := r.Plan(tool, tc.args)
			if err != nil {
				t.Fatalf("Plan refused an attached short value: %v", err)
			}
			if got := p.Argv[len(p.Argv)-len(tc.want):]; !slices.Equal(got, tc.want) {
				t.Fatalf("tail argv = %q, want %q", got, tc.want)
			}
			res, err := r.Call(context.Background(), tool, tc.args)
			if err != nil {
				t.Fatalf("Call refused an attached short value: %v", err)
			}
			want := append(fixturePrefix(), tc.want...)
			if got := decodeProbe(t, res).Args; !slices.Equal(got, want) {
				t.Fatalf("argv = %q, want %q", got, want)
			}
		})
	}
}

// TestPositionalDashValuesKeepEscape keeps the positional "--" escape and its
// exemption for a lone "-", which is a value, not an option.
func TestPositionalDashValuesKeepEscape(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"positional": probeTool(nil, fixtureDef{
			properties: map[string]any{"path": map[string]any{"type": "string"}},
			bindings:   []map[string]any{{"property": "path", "style": "positional"}},
		}),
		"positional-int": probeTool(nil, fixtureDef{
			properties: map[string]any{"n": map[string]any{"type": "integer"}},
			bindings:   []map[string]any{{"property": "n", "style": "positional"}},
		}),
	})
	for _, tc := range []struct {
		name string
		tool string
		args map[string]any
		want []string
	}{
		{"option string", "positional", map[string]any{"path": "--recursive"}, []string{"--", "--recursive"}},
		{"delimiter", "positional", map[string]any{"path": "--"}, []string{"--", "--"}},
		{"lone dash", "positional", map[string]any{"path": "-"}, []string{"-"}},
		{"negative int", "positional-int", map[string]any{"n": -1}, []string{"--", "-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := mustTool(t, r, tc.tool)
			res, err := r.Call(context.Background(), tool, tc.args)
			if err != nil {
				t.Fatalf("Call refused a positional value: %v", err)
			}
			want := append(fixturePrefix(), tc.want...)
			if got := decodeProbe(t, res).Args; !slices.Equal(got, want) {
				t.Fatalf("argv = %q, want %q", got, want)
			}
		})
	}
}

// TestSafeEqualsValueMatchingFixedOptionIsAccepted is the poo review's
// provenance case: the refusal is about the element a binding emitted, not
// about a string that also appears in the command's fixed argv.
func TestSafeEqualsValueMatchingFixedOptionIsAccepted(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran.marker")
	bin := helperBin(t)
	m := fmt.Sprintf(`{"version":1,"tools":{"probe":{
		"description":"fixed option that equals an input value",
		"command":[%s,"-test.run=TestHelperProcess","--","--recursive"],
		"env":{"GO_WANT_HELPER_PROCESS":"1","FAKE_OUTPUT":"probe"},
		"timeout":"5s",
		"inputSchema":{"type":"object","properties":{"ignore":{"type":"string"}},"additionalProperties":false},
		"bindings":[{"property":"ignore","style":"equals","flag":"--ignore"}],
		"output":{"mode":"json"}
	}}}`, js(bin))
	path := filepath.Join(dir, "tools.json")
	if err := os.WriteFile(path, []byte(m), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	tool := mustTool(t, r, "probe")
	args := map[string]any{"ignore": "--recursive"}

	p, err := r.Plan(tool, args, WithEnv("FAKE_MARKER", marker))
	if err != nil {
		t.Fatalf("safe equals value refused because the manifest fixes the same string: %v", err)
	}
	want := append(fixturePrefix(), "--recursive", "--ignore=--recursive")
	if got := p.Argv[len(p.Argv)-len(want):]; !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	res, err := r.Call(context.Background(), tool, args, WithEnv("FAKE_MARKER", marker))
	if err != nil {
		t.Fatalf("Call refused a safe equals value: %v", err)
	}
	if got := decodeProbe(t, res).Args; !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("Call did not run: %v", err)
	}
}

func planErr(r *Registry, tool Tool, args map[string]any, marker string) error {
	_, err := r.Plan(tool, args, WithEnv("FAKE_MARKER", marker))
	return err
}

func callErr(r *Registry, tool Tool, args map[string]any, marker string) error {
	_, err := r.Call(context.Background(), tool, args, WithEnv("FAKE_MARKER", marker))
	return err
}

// assertDashRefusal requires a *ValidationError whose issues include a
// separate-value dash issue at path, naming property and the refused value.
func assertDashRefusal(t *testing.T, err error, path, property, value string) *ValidationError {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %v", err)
	}
	found := false
	for _, issue := range ve.Issues {
		if issue.Path != path {
			continue
		}
		found = true
		if issue.Source != "argv-binding" || issue.Code != "separateValueDash" {
			t.Fatalf("issue at %s = %+v, want Source argv-binding Code separateValueDash", path, issue)
		}
		if !strings.Contains(issue.Message, property) || !strings.Contains(issue.Message, value) ||
			!strings.Contains(issue.Message, "equals") {
			t.Fatalf("issue message does not name the property, the value and the way out: %q", issue.Message)
		}
	}
	if !found {
		t.Fatalf("no issue at %s: %v", path, ve.Issues)
	}
	return ve
}

func hasIssue(issues []StructuralIssue, path string) bool {
	for _, issue := range issues {
		if issue.Path == path && issue.Code == "separateValueDash" {
			return true
		}
	}
	return false
}

func assertNotRun(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a process ran for a refused call: marker exists")
	}
}
