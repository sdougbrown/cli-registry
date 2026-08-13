package schema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// loadConformance reads the vendored umpire-spec json-schema profile fixture.
func loadConformance(t *testing.T) map[string]any {
	t.Helper()
	p := filepath.Join("..", "conformance", "fixtures", "avenor-workflow.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func valueSchema(t *testing.T, doc map[string]any) any {
	t.Helper()
	profile, ok := doc["profile"].(map[string]any)
	if !ok {
		t.Fatal("missing profile")
	}
	vs, ok := profile["valueSchema"]
	if !ok {
		t.Fatal("missing valueSchema")
	}
	return vs
}

func TestConformanceStructuralCases(t *testing.T) {
	doc := loadConformance(t)
	vs, err := Compile(valueSchema(t, doc))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, c := range doc["cases"].([]any) {
		c := c.(map[string]any)
		id := c["id"].(string)
		values := c["values"]
		exp, _ := c["expectedStructure"].(map[string]any)
		wantValid := exp["valid"] == true
		got := vs.Validate(values)
		if wantValid {
			if len(got) != 0 {
				t.Errorf("[%s] expected valid, got issues %v", id, got)
			}
			continue
		}
		if len(got) == 0 {
			t.Errorf("[%s] expected structural issues, got none", id)
			continue
		}
		want := map[string]bool{}
		for _, w := range exp["issues"].([]any) {
			m := w.(map[string]any)
			want[m["code"].(string)+" "+m["path"].(string)] = true
		}
		for _, g := range got {
			if !want[g.Code+" "+g.Path] {
				t.Errorf("[%s] unexpected issue %s @ %s", id, g.Code, g.Path)
			}
		}
	}
}

func TestExplicitNullFailsType(t *testing.T) {
	doc := loadConformance(t)
	vs, err := Compile(valueSchema(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	// workflowType declared as string; explicit null must fail with type
	values := map[string]any{
		"nodes": []any{"x"},
		"title": "t",
		"workflowType": nil,
	}
	issues := vs.Validate(values)
	found := false
	for _, i := range issues {
		if i.Code == "type" && i.Path == "/workflowType" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected type issue @ /workflowType for explicit null, got %v", issues)
	}
}
