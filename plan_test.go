package cliregistry

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// probeTool is a fixture tool that reports its argv, cwd and environment.
func probeTool(extra map[string]string, def fixtureDef) fixtureDef {
	env := map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "probe"}
	for k, v := range extra {
		env[k] = v
	}
	def.env = env
	def.output = map[string]any{"mode": "json"}
	return def
}

func TestPlanDoesNotSpawn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran.marker")
	r := fixtureRegistry(t, map[string]fixtureDef{
		"marker": {
			env:        map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
			properties: map[string]any{"name": map[string]any{"type": "string"}},
			required:   []string{"name"},
			bindings:   []map[string]any{{"property": "name", "style": "positional"}},
		},
	})
	tool := mustTool(t, r, "marker")
	p, err := r.Plan(tool, map[string]any{"name": "x"}, WithEnv("FAKE_MARKER", marker))
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || len(p.Argv) == 0 {
		t.Fatalf("plan = %+v", p)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("Plan spawned the process: marker exists")
	}

	// Control: Call does run, so the marker proves the fixture works.
	if _, err := r.Call(context.Background(), tool, map[string]any{"name": "x"}, WithEnv("FAKE_MARKER", marker)); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("Call did not spawn: %v", statErr)
	}
}

func TestPlanMatchesCallArgv(t *testing.T) {
	arr := func() map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	}
	r := fixtureRegistry(t, map[string]fixtureDef{
		"equals-scalar": probeTool(nil, fixtureDef{
			properties: map[string]any{"state": map[string]any{"type": "string"}},
			bindings:   []map[string]any{{"property": "state", "style": "equals", "flag": "--state"}},
		}),
		"equals-array": probeTool(nil, fixtureDef{
			properties: map[string]any{"labels": arr()},
			bindings:   []map[string]any{{"property": "labels", "style": "equals", "flag": "--label"}},
		}),
		"equals-bool": probeTool(nil, fixtureDef{
			properties: map[string]any{"verbose": map[string]any{"type": "boolean"}},
			bindings:   []map[string]any{{"property": "verbose", "style": "equals", "flag": "--verbose"}},
		}),
		"space-scalar": probeTool(nil, fixtureDef{
			properties: map[string]any{"count": map[string]any{"type": "integer"}},
			bindings:   []map[string]any{{"property": "count", "style": "space", "flag": "--count"}},
		}),
		"space-array": probeTool(nil, fixtureDef{
			properties: map[string]any{"tags": arr()},
			bindings:   []map[string]any{{"property": "tags", "style": "space", "flag": "--tag"}},
		}),
		"space-bool": probeTool(nil, fixtureDef{
			properties: map[string]any{"dry": map[string]any{"type": "boolean"}},
			bindings:   []map[string]any{{"property": "dry", "style": "space", "flag": "--dry-run"}},
		}),
		"short-scalar": probeTool(nil, fixtureDef{
			properties: map[string]any{"level": map[string]any{"type": "integer"}},
			bindings:   []map[string]any{{"property": "level", "style": "short", "flag": "-n"}},
		}),
		"short-array": probeTool(nil, fixtureDef{
			properties: map[string]any{"labels": arr()},
			bindings:   []map[string]any{{"property": "labels", "style": "short", "flag": "-l"}},
		}),
		"positional-scalar": probeTool(nil, fixtureDef{
			properties: map[string]any{"path": map[string]any{"type": "string"}},
			bindings:   []map[string]any{{"property": "path", "style": "positional"}},
		}),
		"positional-array": probeTool(nil, fixtureDef{
			properties: map[string]any{"paths": arr()},
			bindings:   []map[string]any{{"property": "paths", "style": "positional"}},
		}),
	})

	cases := []struct {
		name string
		tool string
		args map[string]any
		want []string
	}{
		{"equals scalar", "equals-scalar", map[string]any{"state": "merged"}, []string{"--state=merged"}},
		{"equals array", "equals-array", map[string]any{"labels": []any{"a", "b"}}, []string{"--label=a", "--label=b"}},
		{"equals bool true", "equals-bool", map[string]any{"verbose": true}, []string{"--verbose"}},
		{"equals bool false", "equals-bool", map[string]any{"verbose": false}, nil},
		{"space scalar", "space-scalar", map[string]any{"count": 3}, []string{"--count", "3"}},
		{"space array", "space-array", map[string]any{"tags": []any{"one", "two"}}, []string{"--tag", "one", "--tag", "two"}},
		{"space bool true", "space-bool", map[string]any{"dry": true}, []string{"--dry-run"}},
		{"space bool false", "space-bool", map[string]any{"dry": false}, nil},
		{"short scalar", "short-scalar", map[string]any{"level": 5}, []string{"-n5"}},
		{"short array", "short-array", map[string]any{"labels": []any{"a", "b"}}, []string{"-la", "-lb"}},
		{"positional scalar", "positional-scalar", map[string]any{"path": "x/y"}, []string{"x/y"}},
		{"positional dash escape", "positional-scalar", map[string]any{"path": "-x"}, []string{"--", "-x"}},
		{"positional array", "positional-array", map[string]any{"paths": []any{"a", "-b"}}, []string{"a", "--", "-b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := mustTool(t, r, tc.tool)
			p, err := r.Plan(tool, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			want := append(fixturePrefix(), tc.want...)
			if !slices.Equal(p.Argv[1:], want) {
				t.Fatalf("Plan.Argv = %q, want %q (after the command path)", p.Argv, want)
			}
			res, err := r.Call(context.Background(), tool, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			got := decodeProbe(t, res).Args
			if !slices.Equal(got, want) {
				t.Fatalf("Call ran %q, want %q", got, want)
			}
		})
	}
}

func TestPlanMatchesCallDirEnvAndTimeout(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLAN_HOST", "from-host")
	r := fixtureRegistryIn(t, dir, map[string]fixtureDef{
		"env": probeTool(map[string]string{"LITERAL": "lit", "FROM_HOST": "env:PLAN_HOST"}, fixtureDef{timeout: "7s"}),
		"allow": probeTool(nil, fixtureDef{
			cwd: map[string]any{"mode": "allow", "roots": []any{"."}},
		}),
		"inherit": probeTool(nil, fixtureDef{
			cwd: map[string]any{"mode": "inherit"},
		}),
	})
	canon := func(p string) string {
		t.Helper()
		got, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	t.Run("manifest env, unresolved in the plan", func(t *testing.T) {
		tool := mustTool(t, r, "env")
		p, err := r.Plan(tool, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		want := []EnvBinding{
			{Name: "FAKE_OUTPUT", Value: "probe"},
			{Name: "FROM_HOST", From: "PLAN_HOST", Reference: true},
			{Name: "GO_WANT_HELPER_PROCESS", Value: "1"},
			{Name: "LITERAL", Value: "lit"},
		}
		if !reflect.DeepEqual(p.Env, want) {
			t.Fatalf("Plan.Env = %+v, want %+v", p.Env, want)
		}
		res, err := r.Call(context.Background(), tool, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		got := decodeProbe(t, res).Env
		if got["LITERAL"] != "lit" || got["FROM_HOST"] != "from-host" {
			t.Fatalf("Call env LITERAL=%q FROM_HOST=%q", got["LITERAL"], got["FROM_HOST"])
		}
		if p.Timeout != 7*time.Second {
			t.Fatalf("Plan.Timeout = %s, want 7s", p.Timeout)
		}
		p, err = r.Plan(tool, map[string]any{}, WithTimeout(250*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		if p.Timeout != 250*time.Millisecond {
			t.Fatalf("Plan.Timeout with WithTimeout = %s, want 250ms", p.Timeout)
		}
	})

	t.Run("manifest allow root", func(t *testing.T) {
		tool := mustTool(t, r, "allow")
		p, err := r.Plan(tool, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if p.Dir != canon(dir) {
			t.Fatalf("Plan.Dir = %q, want %q", p.Dir, canon(dir))
		}
		res, err := r.Call(context.Background(), tool, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if got := decodeProbe(t, res).Dir; got != canon(p.Dir) {
			t.Fatalf("Call ran in %q, Plan.Dir is %q", got, p.Dir)
		}
	})

	t.Run("WithCWD inside the allow root", func(t *testing.T) {
		tool := mustTool(t, r, "allow")
		p, err := r.Plan(tool, map[string]any{}, WithCWD(sub))
		if err != nil {
			t.Fatal(err)
		}
		if p.Dir != canon(sub) {
			t.Fatalf("Plan.Dir = %q, want the resolved %q", p.Dir, canon(sub))
		}
		res, err := r.Call(context.Background(), tool, map[string]any{}, WithCWD(sub))
		if err != nil {
			t.Fatal(err)
		}
		if got := decodeProbe(t, res).Dir; got != canon(sub) {
			t.Fatalf("Call ran in %q, want %q", got, canon(sub))
		}
	})

	t.Run("WithCWD outside the allow root fails before spawn", func(t *testing.T) {
		tool := mustTool(t, r, "allow")
		if _, err := r.Plan(tool, map[string]any{}, WithCWD(t.TempDir())); err == nil || !strings.Contains(err.Error(), "outside allowed roots") {
			t.Fatalf("Plan error = %v, want cwd containment error", err)
		}
	})

	t.Run("inherit leaves the directory to the host", func(t *testing.T) {
		tool := mustTool(t, r, "inherit")
		p, err := r.Plan(tool, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if p.Dir != "" {
			t.Fatalf("Plan.Dir = %q, want empty for cwd.mode inherit", p.Dir)
		}
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		res, err := r.Call(context.Background(), tool, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if got := decodeProbe(t, res).Dir; got != canon(wd) {
			t.Fatalf("Call ran in %q, want the host cwd %q", got, canon(wd))
		}
	})
}

func TestPlanResolvesEnvOnlyOnHostDemand(t *testing.T) {
	t.Setenv("PLAN_HOST", "host-value")
	r := fixtureRegistry(t, map[string]fixtureDef{
		"env": probeTool(map[string]string{"FROM_HOST": "env:PLAN_HOST", "LITERAL": "lit"}, fixtureDef{}),
	})
	p, err := r.Plan(mustTool(t, r, "env"), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range p.Env {
		if b.Reference && b.Value != "" {
			t.Fatalf("Plan resolved the host env: %+v", b)
		}
	}

	// A host that scrubs PLAN_HOST resolves the binding from its own view, and
	// a host that does not have the variable at all gets an error.
	scrubbed := func(name string) (string, bool) {
		if name == "PLAN_HOST" {
			return "scrubbed-value", true
		}
		return "", false
	}
	env, err := p.ResolveEnv(scrubbed)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(env, "FROM_HOST=scrubbed-value") {
		t.Fatalf("resolved env = %q, want FROM_HOST=scrubbed-value", env)
	}
	for _, kv := range env {
		if strings.Contains(kv, "host-value") {
			t.Fatalf("resolved env leaked the host value: %q", kv)
		}
	}

	_, err = p.ResolveEnv(func(string) (string, bool) { return "", false })
	if err == nil || !strings.Contains(err.Error(), "PLAN_HOST") {
		t.Fatalf("missing referenced env: err = %v, want a PLAN_HOST error", err)
	}
}

func TestFinishAgreesWithCall(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"raw-ok": probeTool(nil, fixtureDef{}),
		"json-ok": {
			env:    map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "json"},
			output: map[string]any{"mode": "json"},
		},
		"ptr-ok": {
			env:    map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "pointer"},
			output: map[string]any{"mode": "json-pointer", "json_pointer": "/items/1/id"},
		},
		"exit-3": {
			env:    map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_EXIT": "3"},
			output: map[string]any{"mode": "raw"},
		},
		"exit-3-ok": {
			env:    map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_EXIT": "3"},
			output: map[string]any{"mode": "raw", "success_exit_codes": []any{0, 3}},
		},
	})
	for _, name := range []string{"raw-ok", "json-ok", "ptr-ok", "exit-3", "exit-3-ok"} {
		t.Run(name, func(t *testing.T) {
			tool := mustTool(t, r, name)
			res, callErr := r.Call(context.Background(), tool, map[string]any{})
			p, err := r.Plan(tool, map[string]any{})
			if err != nil {
				t.Fatal(err)
			}

			out := Output{ExitCode: 0}
			if callErr == nil {
				out = Output{Stdout: res.Stdout, Stderr: res.Stderr, Truncated: res.Truncated, ExitCode: res.ExitCode, Duration: res.Duration}
			} else {
				var ee *ExecError
				if !errors.As(callErr, &ee) {
					t.Fatalf("Call error = %v, want ExecError", callErr)
				}
				out = Output{Stdout: ee.Stdout, Stderr: ee.Stderr, Truncated: ee.Truncated, ExitCode: ee.ExitCode}
			}

			finRes, finErr := p.Finish(out)
			switch {
			case callErr == nil && finErr != nil:
				t.Fatalf("Call succeeded, Finish failed: %v", finErr)
			case callErr != nil && finErr == nil:
				t.Fatalf("Call failed (%v), Finish succeeded", callErr)
			case callErr == nil:
				if !reflect.DeepEqual(finRes.Decoded, res.Decoded) {
					t.Fatalf("Finish decoded %#v, Call decoded %#v", finRes.Decoded, res.Decoded)
				}
				if finRes.ExitCode != res.ExitCode || finRes.Truncated != res.Truncated || string(finRes.Stdout) != string(res.Stdout) {
					t.Fatalf("Finish result %+v disagrees with Call result %+v", finRes, res)
				}
			default:
				var want, got *ExecError
				errors.As(callErr, &want)
				errors.As(finErr, &got)
				if got.ExitCode != want.ExitCode || got.Truncated != want.Truncated || string(got.Stdout) != string(want.Stdout) || string(got.Stderr) != string(want.Stderr) {
					t.Fatalf("Finish ExecError %+v disagrees with Call ExecError %+v", got, want)
				}
			}
		})
	}
}

func TestFinishReportsDecodeFailure(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"json-bad": {
			env:    map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_STDOUT": "not json", "FAKE_OUTPUT": "quiet"},
			output: map[string]any{"mode": "json"},
		},
	})
	tool := mustTool(t, r, "json-bad")
	_, callErr := r.Call(context.Background(), tool, map[string]any{})
	if callErr == nil || !strings.Contains(callErr.Error(), "decode output") {
		t.Fatalf("Call error = %v, want a decode error", callErr)
	}
	p, err := r.Plan(tool, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	_, finErr := p.Finish(Output{Stdout: []byte("not json")})
	if finErr == nil || finErr.Error() != callErr.Error() {
		t.Fatalf("Finish error = %v, want %v", finErr, callErr)
	}
}

// A bare `env:` names no host variable, so it is a missing reference — as it
// was in v0.1.0 — not an empty literal.
func TestEmptyReferenceIsMissing(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{"empty-ref": {env: map[string]string{"TARGET": "env:"}}})
	p, err := r.Plan(mustTool(t, r, "empty-ref"), map[string]any{})
	if err != nil {
		return
	}
	env, err := p.ResolveEnv(func(string) (string, bool) { return "", false })
	if err == nil {
		t.Fatalf("missing env reference silently became literal: bindings=%+v env=%q", p.Env, env)
	}
	if !strings.Contains(err.Error(), "referenced env") {
		t.Fatalf("error = %v, want a missing-reference error", err)
	}
	if _, err := r.Call(context.Background(), mustTool(t, r, "empty-ref"), map[string]any{}); err == nil {
		t.Fatal("Call accepted a bare env: reference")
	}
}

// ResolveEnv order: BenignEnv, then the manifest's bindings name-sorted, then
// WithEnv overrides; a scrubbed lookup is the only source of host values.
func TestResolveEnvOrderOverridesAndScrub(t *testing.T) {
	t.Setenv("REVIEW_SYNTHETIC_SECRET", "dummy-host-value")
	r := fixtureRegistry(t, map[string]fixtureDef{"env": {env: map[string]string{
		"PATH": "manifest-path", "A": "env:SOURCE", "EMPTY": "", "Z": "literal-z",
	}}})
	p, err := r.Plan(mustTool(t, r, "env"), map[string]any{}, WithEnv("A", "override-a"), WithEnv("EXTRA", "extra"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := p.ResolveEnv(func(k string) (string, bool) {
		values := map[string]string{"PATH": "host-path", "HOME": "scrubbed-home", "SOURCE": "scrubbed-source"}
		v, ok := values[k]
		return v, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"PATH=manifest-path", "HOME=scrubbed-home", "A=override-a", "EMPTY=", "Z=literal-z", "EXTRA=extra"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("env = %q, want %q", env, want)
	}
	if _, err := p.ResolveEnv(func(string) (string, bool) { return "", false }); err == nil || !strings.Contains(err.Error(), "SOURCE") {
		t.Fatalf("missing reference error = %v, want one naming SOURCE", err)
	}
	// The secret in the process environment is never consulted.
	for _, kv := range env {
		if strings.Contains(kv, "dummy-host-value") {
			t.Fatalf("resolved env leaked the process environment: %q", kv)
		}
	}
	p, err = r.Plan(mustTool(t, r, "env"), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	p.Env = nil
	env, err = p.ResolveEnv(func(string) (string, bool) { return "", false })
	if err != nil || env == nil || len(env) != 0 {
		t.Fatalf("empty env must be non-nil: %#v, %v", env, err)
	}
}

// WithCWD is for a host that decided the directory itself: a tool whose
// manifest leaves the cwd to the host accepts it, rather than failing as if it
// were an unauthorised allow-scope request.
func TestWithCWDForHostDecidedTool(t *testing.T) {
	dir := t.TempDir()
	r := fixtureRegistry(t, map[string]fixtureDef{
		"inherit": probeTool(nil, fixtureDef{cwd: map[string]any{"mode": "inherit"}}),
		"plain":   probeTool(nil, fixtureDef{}),
	})
	for _, name := range []string{"inherit", "plain"} {
		p, err := r.Plan(mustTool(t, r, name), map[string]any{}, WithCWD(dir))
		if err != nil {
			t.Fatalf("%s: Plan(WithCWD(%q)) = %v", name, dir, err)
		}
		res, err := r.Call(context.Background(), mustTool(t, r, name), map[string]any{}, WithCWD(dir))
		if err != nil {
			t.Fatalf("%s: Call = %v", name, err)
		}
		if got := decodeProbe(t, res).Dir; got != p.Dir {
			t.Fatalf("%s: Call ran in %q, Plan.Dir is %q", name, got, p.Dir)
		}
	}
}

// A host executor that runs the plan itself gets the same classification Call
// does for a completed run.
func TestHostExecutorParity(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{"host": probeTool(nil, fixtureDef{})})
	tool := mustTool(t, r, "host")
	p, err := r.Plan(tool, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	env, err := p.ResolveEnv(os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(p.Argv[0], p.Argv[1:]...)
	cmd.Env, cmd.Dir = env, p.Dir
	stdout, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	host, err := p.Finish(Output{Stdout: stdout})
	if err != nil {
		t.Fatal(err)
	}
	own, err := r.Call(context.Background(), tool, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(host.Decoded, own.Decoded) {
		t.Fatalf("host and Call differ: %#v / %#v", host.Decoded, own.Decoded)
	}
}

// Finish does not classify a lifetime error: the same killed-process output is
// a *TimeoutError through Call and an *ExecError through Finish.
func TestFinishHasNoLifetimeClassification(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{"sleep": {env: map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_SLEEP": "60s"}}})
	tool := mustTool(t, r, "sleep")
	opts := []Option{WithTimeout(20 * time.Millisecond)}
	p, err := r.Plan(tool, map[string]any{}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	_, callErr := r.Call(context.Background(), tool, map[string]any{}, opts...)
	var te *TimeoutError
	if !errors.As(callErr, &te) {
		t.Fatalf("Call error = %T (%v), want *TimeoutError", callErr, callErr)
	}
	_, finishErr := p.Finish(Output{Stdout: te.Stdout, Stderr: te.Stderr, Truncated: te.Truncated, ExitCode: -1})
	var ee *ExecError
	if !errors.As(finishErr, &ee) || IsTimeout(finishErr) {
		t.Fatalf("Finish error = %T (%v), want *ExecError", finishErr, finishErr)
	}
}
