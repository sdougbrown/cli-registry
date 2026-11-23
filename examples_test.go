package cliregistry

import (
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// exampleManifest resolves examples/fn.json relative to this test file so the
// tests run from any working directory.
func exampleManifest(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "examples", "fn.json")
}

func TestExampleFnRegistryLoads(t *testing.T) {
	r, err := Load(exampleManifest(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := r.Lookup("notes"); !ok {
		t.Fatal(`tool "notes" not found in examples/fn.json`)
	}
}

func TestExampleFnPlan(t *testing.T) {
	r, err := Load(exampleManifest(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tool, _ := r.Lookup("notes")
	p, err := r.Plan(tool, map[string]any{"data": map[string]any{"op": "add", "title": "x", "body": "y"}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if p.Stdin == nil {
		t.Fatal("Stdin is nil, want the json binding's marshaled input")
	}
	want := `{"body":"y","op":"add","title":"x"}`
	if *p.Stdin != want {
		t.Fatalf("Stdin = %q, want %q", *p.Stdin, want)
	}
	if !slices.Contains(p.Argv, "--data-stdin") {
		t.Fatalf("Argv = %v, want it to contain --data-stdin", p.Argv)
	}
}

func TestExampleFnPlanRejectsMissingOp(t *testing.T) {
	r, err := Load(exampleManifest(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tool, _ := r.Lookup("notes")
	_, err = r.Plan(tool, map[string]any{"data": map[string]any{"title": "x"}})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Plan error = %v (%T), want *ValidationError", err, err)
	}
}
