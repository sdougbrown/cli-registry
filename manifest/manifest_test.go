package manifest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadJSON(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "r.json", `{"version":1,"tools":{"gh-pr":{"command":["gh","pr","list"]}}}`)
	doc, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Version != 1 {
		t.Fatalf("version = %d, want 1", doc.Version)
	}
	if _, ok := doc.Tools["gh-pr"]; !ok {
		t.Fatalf("tool gh-pr missing: %v", keys(doc.Tools))
	}
	if doc.ToolSource["gh-pr"] == "" {
		t.Fatal("ToolSource not recorded")
	}
}

func TestLoadYAMLAndTOML(t *testing.T) {
	dir := t.TempDir()
	y := write(t, dir, "r.yaml", "version: 1\ntools:\n  a:\n    command: [x]\n")
	to := write(t, dir, "r.toml", "version = 1\n[tools.a]\ncommand = [\"x\"]\n")
	for _, p := range []string{y, to} {
		doc, err := Load(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if doc.Version != 1 {
			t.Fatalf("%s: version %d", p, doc.Version)
		}
		if _, ok := doc.Tools["a"]; !ok {
			t.Fatalf("%s: tool a missing", p)
		}
	}
}

func TestDiscoverDirDeterministic(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "b.json", `{"version":1,"tools":{"b":{}}}`)
	write(t, dir, "a.yaml", "version: 1\ntools:\n  a:\n    command: [x]\n")
	write(t, dir, "a.json", `{"version":1,"tools":{"a2":{}}}`)
	write(t, dir, ".gitignore", "ignored") // not a manifest extension
	files, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.json", "a.yaml", "b.json"}
	if len(files) != len(want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	for i, f := range files {
		if filepath.Base(f) != want[i] {
			t.Fatalf("files[%d] = %s, want %s (not sorted?)", i, filepath.Base(f), want[i])
		}
	}
	for _, f := range files {
		if filepath.Base(f) == ".gitignore" {
			t.Fatal("non-manifest extension was discovered")
		}
	}
}

func TestLoadDirectoryMergesAndRejectsDup(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.json", `{"version":1,"tools":{"one":{}}}`)
	write(t, dir, "b.json", `{"version":1,"tools":{"two":{}}}`)
	doc, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Tools) != 2 {
		t.Fatalf("tools = %v", keys(doc.Tools))
	}

	// duplicate across files must fail
	write(t, dir, "c.json", `{"version":1,"tools":{"one":{}}}`)
	if _, err := Load(dir); !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("want ErrDuplicateTool, got %v", err)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "r.json", `{"version":1,"tools":{},"typo_field":"x"}`)
	if _, err := Load(p); err == nil {
		t.Fatal("expected unknown-field rejection")
	}
}

func TestUnsupportedVersion(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "r.json", `{"version":99,"tools":{}}`)
	if _, err := Load(p); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("want ErrUnsupportedVersion, got %v", err)
	}
}

func TestEmptyAndTrailingRejected(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(write(t, dir, "empty.yaml", "")); err == nil {
		t.Fatal("expected error for empty YAML")
	}
	p := write(t, dir, "multi.json", `{"version":1,"tools":{}} {"version":1,"tools":{}}`)
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for multiple top-level values")
	}
}

func TestDuplicateToolNameWithinFile(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "r.json", `{"version":1,"tools":{"a":{},"a":{}}}`)
	// Duplicate keys within a file are rejected at the raw-JSON ingress layer
	// (generic duplicate-key error), while duplicate names across files surface
	// as ErrDuplicateTool. Both must fail the load.
	if _, err := Load(p); err == nil {
		t.Fatal("expected duplicate-key rejection")
	}
}

func TestNullStripRemovesObjectNulls(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "r.json", `{"version":1,"tools":{"t":{
		"command":["x"],
		"timeout": null,
		"env": {"A": null},
		"inputSchema":{"type":"object","properties":{},"additionalProperties":false}
	}}}`)
	doc, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	body := string(doc.Tools["t"])
	if strings.Contains(body, "null") {
		t.Fatalf("null members not stripped: %s", body)
	}
	// tool key survives, only its null members are dropped
	var m map[string]any
	if err := json.Unmarshal(doc.Tools["t"], &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["command"]; !ok {
		t.Fatalf("command lost: %v", m)
	}
	if _, ok := m["timeout"]; ok {
		t.Fatalf("timeout should be absent: %v", m)
	}
}

func TestDecodeYAMLUnknownField(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "r.yaml", "version: 1\nbogus: true\ntools: {}\n")
	if _, err := Load(p); err == nil {
		t.Fatal("expected unknown-field rejection in YAML")
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
