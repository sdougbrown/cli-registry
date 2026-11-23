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
		"flagless": {
			env:        map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "stdin"},
			properties: map[string]any{"message": map[string]any{"type": "string"}},
			bindings:   []map[string]any{{"property": "message", "style": "stdin"}},
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
	res, err := r.Call(context.Background(), mustTool(t, r, "flagless"), map[string]any{"message": "flagless\n"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Input string   `json:"input"`
		Args  []string `json:"args"`
	}
	if err := json.Unmarshal(res.Stdout, &got); err != nil || got.Input != "flagless\n" || slices.Contains(got.Args, "--message-stdin") {
		t.Fatalf("flagless result: %+v, %v", got, err)
	}
	_, err = r.Call(context.Background(), tool, map[string]any{"message": 42})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("invalid input: %v", err)
	}
}

func TestJSONBinding(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"json": {
			env:        map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "stdin"},
			properties: map[string]any{"data": map[string]any{"type": "object", "additionalProperties": true}},
			bindings:   []map[string]any{{"property": "data", "style": "json", "flag": "--data-stdin"}},
		},
		"json-array": {
			env:        map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "stdin"},
			properties: map[string]any{"data": map[string]any{"type": "array"}},
			bindings:   []map[string]any{{"property": "data", "style": "json", "flag": "--data-stdin"}},
		},
		"json-flagless": {
			env:        map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "stdin"},
			properties: map[string]any{"data": map[string]any{"type": "object", "additionalProperties": true}},
			bindings:   []map[string]any{{"property": "data", "style": "json"}},
		},
	})
	for _, tc := range []struct {
		name    string
		tool    string
		args    map[string]any
		want    string
		present bool
	}{
		{"absent", "json", map[string]any{}, "", false},
		{"object", "json", map[string]any{"data": map[string]any{"b": 1, "a": "two"}}, `{"a":"two","b":1}`, true},
		{"array", "json-array", map[string]any{"data": []any{"x", 2, true}}, `["x",2,true]`, true},
	} {
		tool := mustTool(t, r, tc.tool)
		t.Run(tc.name, func(t *testing.T) {
			p, err := r.Plan(tool, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if (p.Stdin != nil) != tc.present || (p.Stdin != nil && *p.Stdin != tc.want) {
				t.Fatalf("unexpected planned stdin: %v", p.Stdin)
			}
			// Pin Go's sorted-key determinism for the object case: the planned
			// bytes must be exactly what json.Marshal produces, and the two-key
			// sorted order is part of the contract.
			if tc.name == "object" {
				mb, merr := json.Marshal(map[string]any{"b": 1, "a": "two"})
				if merr != nil {
					t.Fatal(merr)
				}
				if p.Stdin == nil || *p.Stdin != string(mb) {
					t.Fatalf("planned stdin %v, want Go marshal %q", p.Stdin, mb)
				}
			}
			if slices.Contains(p.Argv, "--data-stdin") != tc.present {
				t.Fatalf("argv: %q", p.Argv)
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
			if got.Input != tc.want || slices.Contains(got.Args, "--data-stdin") != tc.present {
				t.Fatalf("child received %+v", got)
			}
		})
	}
	res, err := r.Call(context.Background(), mustTool(t, r, "json-flagless"), map[string]any{"data": map[string]any{"b": 1, "a": "two"}})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Input string   `json:"input"`
		Args  []string `json:"args"`
	}
	if err := json.Unmarshal(res.Stdout, &got); err != nil || got.Input != `{"a":"two","b":1}` || slices.Contains(got.Args, "--data-stdin") {
		t.Fatalf("flagless result: %+v, %v", got, err)
	}
	tool := mustTool(t, r, "json")
	_, err = r.Call(context.Background(), tool, map[string]any{"data": nil})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("null input accepted: %v", err)
	}
}

func TestStdinIncompleteWrite(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"early": {
			env:        map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "quiet"},
			properties: map[string]any{"data": map[string]any{"type": "string"}},
			bindings:   []map[string]any{{"property": "data", "style": "stdin"}},
		},
		"failure": {
			env:        map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_EXIT": "3"},
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
	_, err = r.Call(context.Background(), mustTool(t, r, "failure"), map[string]any{"data": strings.Repeat("secret", 1<<18)})
	var exit *ExecError
	if !errors.As(err, &exit) || exit.ExitCode != 3 {
		t.Fatalf("child failure: %v", err)
	}
}

func TestStdinCompleteWriteBeforeExit(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"fixed": {
			env:        map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "stdin-fixed", "GORACE": "atexit_sleep_ms=0"},
			properties: map[string]any{"data": map[string]any{"type": "string"}},
			bindings:   []map[string]any{{"property": "data", "style": "stdin"}},
		},
	})
	tool := mustTool(t, r, "fixed")
	for i := 0; i < 30; i++ {
		if _, err := r.Call(context.Background(), tool, map[string]any{"data": strings.Repeat("x", 128<<10)}); err != nil {
			t.Fatalf("fully delivered input: %v", err)
		}
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

func TestJSONBindingDefinitions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		properties map[string]any
		bindings   []map[string]any
	}{
		{"json-on-string", map[string]any{"data": map[string]any{"type": "string"}}, []map[string]any{{"property": "data", "style": "json"}}},
		// A schema-valid tagged oneOf (the schema rejects untagged branches), so
		// the rejection here comes from the binding guard, not the schema.
		{"json-on-oneof", map[string]any{"data": map[string]any{"oneOf": []any{
			map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "obj"}}, "required": []any{"kind"}, "additionalProperties": false},
			map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"const": "arr"}}, "required": []any{"kind"}, "additionalProperties": false},
		}}}, []map[string]any{{"property": "data", "style": "json"}}},
		{"stdin-and-json", map[string]any{"a": map[string]any{"type": "string"}, "b": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}, []map[string]any{{"property": "a", "style": "stdin"}, {"property": "b", "style": "json"}}},
		{"json-twice", map[string]any{"a": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}, "b": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}, []map[string]any{{"property": "a", "style": "json"}, {"property": "b", "style": "json"}}},
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

func TestStdinManifestTimeout(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"blocked": {
			env:        map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_SLEEP": "30s"},
			timeout:    "100ms",
			properties: map[string]any{"data": map[string]any{"type": "string"}},
			bindings:   []map[string]any{{"property": "data", "style": "stdin"}},
		},
	})
	start := time.Now()
	_, err := r.Call(context.Background(), mustTool(t, r, "blocked"), map[string]any{"data": strings.Repeat("secret", 1<<18)})
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("got %v, want *TimeoutError", err)
	}
	if timeoutErr.Cause != nil {
		t.Fatalf("manifest timeout carried Cause %v, want nil", timeoutErr.Cause)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("call blocked on unread stdin for %s", time.Since(start))
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("error leaked input")
	}
}
