package schema

import (
	"errors"
	"strings"
	"testing"
)

func mustCompileOK(t *testing.T, raw any) *ValueSchema {
	t.Helper()
	vs, err := Compile(raw)
	if err != nil {
		t.Fatalf("unexpected compile error: %v", err)
	}
	return vs
}

func findCode(t *testing.T, err error, code string) bool {
	t.Helper()
	var ce *CompileError
	if !errors.As(err, &ce) {
		return false
	}
	for _, i := range ce.Issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

func TestRejectUnsupportedKeyword(t *testing.T) {
	raw := map[string]any{
		"type": "object", "properties": map[string]any{
			"x": map[string]any{"allOf": []any{map[string]any{"type": "string"}}},
		},
		"additionalProperties": false,
	}
	_, err := Compile(raw)
	if err == nil {
		t.Fatal("expected rejection for allOf")
	}
	if !findCode(t, err, "unsupportedKeyword") {
		t.Fatalf("expected unsupportedKeyword, got %v", err)
	}
}

func TestRejectCyclicRef(t *testing.T) {
	raw := map[string]any{
		"type": "object", "properties": map[string]any{"x": map[string]any{"$ref": "#/$defs/a"}},
		"additionalProperties": false,
		"$defs": map[string]any{
			"a": map[string]any{"type": "object", "properties": map[string]any{"loop": map[string]any{"$ref": "#/$defs/a"}}, "additionalProperties": false},
		},
	}
	_, err := Compile(raw)
	if err == nil {
		t.Fatal("expected rejection for cyclic ref")
	}
	if !findCode(t, err, "referenceCycle") {
		t.Fatalf("expected referenceCycle, got %v", err)
	}
}

func TestRejectUnknownDef(t *testing.T) {
	raw := map[string]any{
		"type": "object", "properties": map[string]any{"x": map[string]any{"$ref": "#/$defs/nope"}},
		"additionalProperties": false,
	}
	_, err := Compile(raw)
	if !findCode(t, err, "invalidReference") {
		t.Fatalf("expected invalidReference, got %v", err)
	}
}

func TestRejectAnyOfPatternFormat(t *testing.T) {
	for _, kw := range []string{"anyOf", "pattern", "format", "multipleOf", "uniqueItems", "not"} {
		raw := map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"x": map[string]any{"type": "string", kw: "zzz"}},
			"additionalProperties": false,
		}
		if _, err := Compile(raw); err == nil {
			t.Errorf("expected rejection for %s", kw)
		}
	}
}

func TestObjectRequiresAdditionalPropertiesFalse(t *testing.T) {
	raw := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}}
	_, err := Compile(raw)
	if err == nil {
		t.Fatal("expected rejection for object without additionalProperties:false")
	}
	var ce *CompileError
	if !errors.As(err, &ce) || len(ce.Issues) == 0 {
		t.Fatalf("expected definition issues, got %v", err)
	}
}

func TestBasicTypesAndConstraints(t *testing.T) {
	vs := mustCompileOK(t, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"count": map[string]any{"type": "integer", "minimum": 1.0, "maximum": 5.0},
			"name":  map[string]any{"type": "string", "minLength": float64(2)},
			"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 3.0},
		},
		"required":             []any{"count"},
		"additionalProperties": false,
	})
	assertIssue(t, vs, map[string]any{"count": 9}, "maximum", "/count")
	assertIssue(t, vs, map[string]any{"count": 0}, "minimum", "/count")
	assertIssue(t, vs, map[string]any{"name": "x"}, "minLength", "/name")
	assertIssue(t, vs, map[string]any{"count": 1, "tags": []any{"a", "b", "c", "d"}}, "maxItems", "/tags")
	assertIssue(t, vs, map[string]any{"count": 1, "extra": 2}, "additionalProperties", "/extra")
	assertIssue(t, vs, map[string]any{}, "required", "/count")
	if got := vs.Validate(map[string]any{"count": 3, "name": "ok", "tags": []any{"a"}}); len(got) != 0 {
		t.Fatalf("expected valid, got %v", got)
	}
}

func assertIssue(t *testing.T, vs *ValueSchema, value any, code, path string) bool {
	t.Helper()
	for _, i := range vs.Validate(value) {
		if i.Code == code && i.Path == path {
			return true
		}
	}
	tests := []string{}
	for _, i := range vs.Validate(value) {
		tests = append(tests, i.Code+"@"+i.Path)
	}
	t.Fatalf("expected %s @ %s, got %v", code, path, tests)
	return false
}

func TestEnumConst(t *testing.T) {
	vs := mustCompileOK(t, map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode": map[string]any{"type": "string", "enum": []any{"a", "b"}},
			"ver":  map[string]any{"const": 1.0},
		},
		"additionalProperties": false,
	})
	if got := vs.Validate(map[string]any{"mode": "z"}); !strings.Contains(got[0].Code, "enum") {
		t.Fatalf("expected enum issue, got %v", got)
	}
	if got := vs.Validate(map[string]any{"ver": 2}); got[0].Code != "const" {
		t.Fatalf("expected const issue, got %v", got)
	}
}
