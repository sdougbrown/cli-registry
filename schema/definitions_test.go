package schema

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// implementedRejectionCodes are definition-issue codes the cli-registry
// structural compiler produces. Codes owned by the umpire availability
// authority (incompatibleIsEmpty, invalidDefault, fieldMismatch, invalidProfile
// in the umpire sense) are out of scope and skipped.
var implementedRejectionCodes = map[string]bool{
	"unsupportedKeyword":   true,
	"invalidReference":     true,
	"referenceCycle":       true,
	"invalidDiscriminator": true,
}

type expIssue struct {
	Code string `json:"code"`
}

// TestConformanceDefinitionFailures runs the pinned umpire-spec
// profile-definition-failures fixture and asserts that every failure whose
// expected issues are all in the implemented structural subset is rejected by
// Compile with (at least one of) the expected codes. This is the structural
// definition-rejection parity half of cross-runtime conformance.
func TestConformanceDefinitionFailures(t *testing.T) {
	p := filepath.Join("..", "conformance", "failures", "profile-definition-failures.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Failures []struct {
			ID       string         `json:"id"`
			Profile  map[string]any `json:"profile"`
			Expected []expIssue     `json:"expectedDefinitionIssues"`
		} `json:"failures"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, f := range doc.Failures {
		if !allStructural(f.Expected) {
			continue
		}
		ran++
		vs, _ := f.Profile["valueSchema"].(map[string]any)
		_, err := Compile(vs)
		if err == nil {
			t.Errorf("[%s] expected compile rejection, got none", f.ID)
			continue
		}
		if !hasAnyCode(t, err, f.Expected) {
			t.Errorf("[%s] rejected but missing expected code(s): %v (err=%v)", f.ID, codesOf(f.Expected), err)
		}
	}
	if ran == 0 {
		t.Fatal("no structural failure cases exercised")
	}
	t.Logf("exercised %d structural definition-failure cases", ran)
}

func allStructural(exp []expIssue) bool {
	if len(exp) == 0 {
		return false
	}
	for _, e := range exp {
		if !implementedRejectionCodes[e.Code] {
			return false
		}
	}
	return true
}

func hasAnyCode(t *testing.T, err error, expected []expIssue) bool {
	t.Helper()
	var ce *CompileError
	if !errors.As(err, &ce) {
		return false
	}
	for _, e := range expected {
		for _, i := range ce.Issues {
			if i.Code == e.Code {
				return true
			}
		}
	}
	return false
}

func codesOf(exp []expIssue) []string {
	out := []string{}
	for _, e := range exp {
		out = append(out, e.Code)
	}
	return out
}
