package cliregistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fixtureDef describes one tool in a test registry. Every tool runs the test
// binary's TestHelperProcess entry point, configured through env.
type fixtureDef struct {
	env        map[string]string
	timeout    string
	properties map[string]any
	required   []string
	bindings   []map[string]any
	output     map[string]any
	cwd        map[string]any
}

func fixtureRegistry(t *testing.T, defs map[string]fixtureDef) *Registry {
	t.Helper()
	tools := map[string]any{}
	for name, d := range defs {
		props := d.properties
		if props == nil {
			props = map[string]any{}
		}
		schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(d.required) > 0 {
			schema["required"] = d.required
		}
		body := map[string]any{
			"description": "fixture " + name,
			"command":     []string{helperBin(t), "-test.run=TestHelperProcess", "--"},
			"timeout":     d.timeout,
			"inputSchema": schema,
			"output":      d.output,
		}
		if body["timeout"] == "" {
			body["timeout"] = "5s"
		}
		if body["output"] == nil {
			body["output"] = map[string]any{"mode": "raw"}
		}
		if d.env != nil {
			body["env"] = d.env
		}
		if len(d.bindings) > 0 {
			body["bindings"] = d.bindings
		}
		if d.cwd != nil {
			body["cwd"] = d.cwd
		}
		tools[name] = body
	}
	raw, err := json.Marshal(map[string]any{"version": 1, "tools": tools})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "fixtures.json")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// probe is what FAKE_OUTPUT=probe prints: the child's full argv, working
// directory and environment.
type probe struct {
	Args []string
	Dir  string
	Env  map[string]string
}

func decodeProbe(t *testing.T, res *Result) probe {
	t.Helper()
	m, ok := res.Decoded.(map[string]any)
	if !ok {
		t.Fatalf("probe payload is %T, want object (%s)", res.Decoded, res.Stdout)
	}
	p := probe{Env: map[string]string{}}
	for _, a := range m["args"].([]any) {
		p.Args = append(p.Args, a.(string))
	}
	p.Dir, _ = m["dir"].(string)
	for k, v := range m["env"].(map[string]any) {
		p.Env[k] = v.(string)
	}
	return p
}

// fixturePrefix is the argv prefix every fixture tool shares.
func fixturePrefix() []string {
	return []string{"-test.run=TestHelperProcess", "--"}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatalf("parse pid %q: %v", b, err)
	}
	return pid
}

func killPID(pid int) {
	if pid <= 0 {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
