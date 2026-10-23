package cliregistry

import (
	"context"
	"slices"
	"testing"
)

func TestSpaceArrayBinding(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"tags": {
			env: map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "probe"},
			properties: map[string]any{
				"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			bindings: []map[string]any{{"property": "tags", "style": "space", "flag": "--tag"}},
			output:   map[string]any{"mode": "json"},
		},
	})
	tool := mustTool(t, r, "tags")
	for _, tc := range []struct {
		name string
		tags []any
		want []string
	}{
		{"empty", []any{}, nil},
		{"one", []any{"one"}, []string{"--tag", "one"}},
		{"two", []any{"one", "two"}, []string{"--tag", "one", "--tag", "two"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := r.Call(context.Background(), tool, map[string]any{"tags": tc.tags})
			if err != nil {
				t.Fatal(err)
			}
			got := decodeProbe(t, res).Args
			want := append(fixturePrefix(), tc.want...)
			if !slices.Equal(got, want) {
				t.Fatalf("argv = %q, want %q", got, want)
			}
		})
	}
}
