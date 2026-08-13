package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tools.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func goodRegistry(t *testing.T) string {
	t.Helper()
	return writeManifest(t, `{
		"version": 1,
		"tools": {
			"say": {
				"description": "echo a message",
				"command": ["echo"],
				"timeout": "10s",
				"inputSchema": {"type":"object","properties":{"message":{"type":"string"}},"additionalProperties":false,"required":["message"]},
				"bindings": [{"property":"message","style":"positional"}],
				"output": {"mode":"raw"}
			}
		}
	}`)
}

func TestCmdValidate(t *testing.T) {
	good := goodRegistry(t)
	if rc := run([]string{"validate", good}); rc != 0 {
		t.Fatalf("validate good: rc=%d", rc)
	}
	bad := writeManifest(t, `{"version":1,"tools":{"t":{"inputSchema":{"type":"object","properties":{},"additionalProperties":false}}}}`)
	if rc := run([]string{"validate", bad}); rc == 0 {
		t.Fatal("validate bad: expected nonzero")
	}
	if rc := run([]string{"validate"}); rc == 0 {
		t.Fatal("validate no paths: expected nonzero")
	}
}

func TestCmdRunTool(t *testing.T) {
	good := goodRegistry(t)
	if rc := run([]string{"run", "-registry", good, "say", `{"message":"hi"}`}); rc != 0 {
		t.Fatalf("run say: rc=%d", rc)
	}
	if rc := run([]string{"run", "-registry", good, "nope", "{}"}); rc == 0 {
		t.Fatal("run unknown tool: expected nonzero")
	}
	// missing required field => validation failure
	if rc := run([]string{"run", "-registry", good, "say", `{}`}); rc != 2 {
		t.Fatalf("run validation fail: rc=%d, want 2", rc)
	}
}

func TestRunCapturesStdout(t *testing.T) {
	good := goodRegistry(t)
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	rc := run([]string{"run", "-registry", good, "say", `{"message":"captured"}`})
	w.Close()
	os.Stdout = old
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	if !strings.Contains(string(buf[:n]), "captured") {
		t.Fatalf("stdout missing output: %q", buf[:n])
	}
}

func TestCLIRegistryEnvFallback(t *testing.T) {
	good := goodRegistry(t)
	t.Setenv("CLI_REGISTRY", good)
	if rc := run([]string{"validate"}); rc != 0 {
		t.Fatalf("validate via env: rc=%d", rc)
	}
}
