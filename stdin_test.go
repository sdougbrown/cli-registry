package cliregistry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestStdinBinding(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"stdin": {
			env:        map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "stdin"},
			properties: map[string]any{"message": map[string]any{"type": "string"}},
			bindings:   []map[string]any{{"property": "message", "style": "stdin", "flag": "--message-stdin"}},
		},
	})
	tool := mustTool(t, r, "stdin")
	for _, tc := range []struct {
		name    string
		args    map[string]any
		want    string
		present bool
	}{
		{"absent", map[string]any{}, "", false},
		{"empty", map[string]any{"message": ""}, "", true},
		{"text", map[string]any{"message": "secret\n雪\n"}, "secret\n雪\n", true},
		{"large", map[string]any{"message": strings.Repeat("x", 128<<10)}, strings.Repeat("x", 128<<10), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := r.Plan(tool, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if (p.Stdin != nil) != tc.present || (p.Stdin != nil && *p.Stdin != tc.want) {
				t.Fatalf("unexpected planned stdin: %v", p.Stdin)
			}
			if slices.Contains(p.Argv, "--message-stdin") != tc.present {
				t.Fatalf("argv: %q", p.Argv)
			}
			if tc.want != "" && slices.Contains(p.Argv, tc.want) {
				t.Fatal("input appeared in argv")
			}
			res, err := r.Call(context.Background(), tool, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Input string   `json:"input"`
				Args  []string `json:"args"`
			}
			if err := json.Unmarshal(res.Stdout, &got); err != nil {
				t.Fatal(err)
			}
			if got.Input != tc.want || slices.Contains(got.Args, "--message-stdin") != tc.present {
				t.Fatalf("child received %+v", got)
			}
		})
	}
}

func TestStdinIncompleteWrite(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"early": {
			env:        map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "quiet"},
			properties: map[string]any{"data": map[string]any{"type": "string"}},
			bindings:   []map[string]any{{"property": "data", "style": "stdin"}},
		},
	})
	_, err := r.Call(context.Background(), mustTool(t, r, "early"), map[string]any{"data": strings.Repeat("secret", 1<<18)})
	if err == nil {
		t.Fatal("successful exit with incomplete stdin was accepted")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("error leaked input")
	}
}

func readFixtureStdin() {
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"input": string(b), "args": os.Args})
}

func TestStdinBindingDefinitions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		properties map[string]any
		bindings   []map[string]any
	}{
		{"non-string", map[string]any{"data": map[string]any{"type": "integer"}}, []map[string]any{{"property": "data", "style": "stdin"}}},
		{"multiple", map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "string"}}, []map[string]any{{"property": "a", "style": "stdin"}, {"property": "b", "style": "stdin"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"description": "test", "command": []string{helperBin(t)}, "inputSchema": map[string]any{"type": "object", "properties": tc.properties, "additionalProperties": false}, "bindings": tc.bindings}
			data, err := json.Marshal(map[string]any{"version": 1, "tools": map[string]any{"test": body}})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "registry.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}

func TestStdinCancellation(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"blocked": {
			env:        map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_SLEEP": "30s"},
			properties: map[string]any{"data": map[string]any{"type": "string"}},
			bindings:   []map[string]any{{"property": "data", "style": "stdin"}},
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := r.Call(ctx, mustTool(t, r, "blocked"), map[string]any{"data": strings.Repeat("secret", 1<<18)})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want deadline exceeded", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("cancellation blocked on stdin")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("error leaked input")
	}
}
