package cliregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess is re-invoked by cli-registry as a fake CLI. When
// GO_WANT_HELPER_PROCESS=1 it behaves like a configurable command-line tool.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	// args after the "--" marker
	rest := []string{}
	if i := indexAfter(os.Args, "--"); i >= 0 {
		rest = os.Args[i:]
	}

	if m := os.Getenv("FAKE_MARKER"); m != "" {
		os.WriteFile(m, []byte("marker"), 0o644)
	}
	if s := os.Getenv("FAKE_STDOUT"); s != "" {
		fmt.Print(s)
	}
	if s := os.Getenv("FAKE_STDERR"); s != "" {
		fmt.Fprint(os.Stderr, s)
	}
	if os.Getenv("FAKE_OUTPUT") == "big" {
		// more than the 1 MiB capture limit
		fmt.Print(strings.Repeat("x", maxCapture+512))
	}
	// Recorded after the output so a test that waits for the pid file also
	// knows what the fixture has already written.
	if p := os.Getenv("FAKE_RECORD_PID"); p != "" {
		os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0o644)
	}
	if mode := os.Getenv("FAKE_DESCENDANT"); mode != "" {
		spawnFixtureDescendant(mode)
	}
	if s := os.Getenv("FAKE_SLEEP"); s != "" {
		d, _ := time.ParseDuration(s)
		time.Sleep(d)
	}
	if e := os.Getenv("FAKE_EXIT"); e != "" {
		ec, _ := strconv.Atoi(e)
		fmt.Fprintln(os.Stderr, "fake exit requested")
		os.Exit(ec)
	}
	switch os.Getenv("FAKE_OUTPUT") {
	case "json":
		fmt.Println(`{"ok":true,"value":42}`)
	case "pointer":
		fmt.Println(`{"items":[{"id":1},{"id":2}],"total":2}`)
	case "args":
		b, _ := json.Marshal(rest)
		fmt.Println(string(b))
	case "probe":
		probe := map[string]any{"args": os.Args[1:], "env": environMap()}
		probe["dir"], _ = os.Getwd()
		json.NewEncoder(os.Stdout).Encode(probe)
	case "env":
		fmt.Println(os.Getenv("FAKE_ECHO_OUT"))
	case "big":
		// printed above, before FAKE_EXIT
	case "quiet":
		// print nothing; FAKE_STDOUT carries the output
	default:
		fmt.Println("hello")
	}
	os.Exit(0)
}

// spawnFixtureDescendant starts a long-lived child in the fixture's process
// group. "bg" gives it the null device (it holds no capture pipe); "pipe"
// hands it the inherited stdout/stderr so it keeps the capture pipes open.
func spawnFixtureDescendant(mode string) {
	c := exec.Command("sleep", "60")
	if mode == "pipe" {
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
	}
	if err := c.Start(); err != nil {
		return
	}
	if p := os.Getenv("FAKE_DESCENDANT_PID"); p != "" {
		os.WriteFile(p, []byte(strconv.Itoa(c.Process.Pid)), 0o644)
	}
}

func environMap() map[string]string {
	m := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

func indexAfter(args []string, tok string) int {
	for i, a := range args {
		if a == tok {
			return i + 1
		}
	}
	return -1
}

func helperBin(t *testing.T) string { return os.Args[0] }

func js(s string) string { b, _ := json.Marshal(s); return string(b) }

func writeToolRegistry(t *testing.T) *Registry {
	t.Helper()
	bin := helperBin(t)
	manifestJSON := fmt.Sprintf(`{
  "version": 1,
  "tools": {
    "echo-args": {
      "description": "print argv as json",
      "command": [%s, "-test.run=TestHelperProcess", "--"],
      "env": {"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "args"},
      "timeout": "5s",
      "inputSchema": {"type":"object","properties":{
        "state":{"type":"string","enum":["open","closed","merged"]},
        "count":{"type":"integer"},
        "path":{"type":"string"}
      },"additionalProperties":false,"required":["path"]},
      "bindings": [
        {"property":"state","style":"equals","flag":"--state"},
        {"property":"count","style":"space","flag":"--count"},
        {"property":"path","style":"positional"}
      ],
      "output": {"mode":"raw"}
    },
    "json-out": {
      "description": "output json",
      "command": [%s, "-test.run=TestHelperProcess", "--"],
      "env": {"GO_WANT_HELPER_PROCESS":"1","FAKE_OUTPUT":"json"},
      "timeout": "5s",
      "inputSchema": {"type":"object","properties":{},"additionalProperties":false},
      "output": {"mode":"json"}
    },
    "ptr-out": {
      "description": "output json pointer",
      "command": [%s, "-test.run=TestHelperProcess", "--"],
      "env": {"GO_WANT_HELPER_PROCESS":"1","FAKE_OUTPUT":"pointer"},
      "timeout": "5s",
      "inputSchema": {"type":"object","properties":{},"additionalProperties":false},
      "output": {"mode":"json-pointer","json_pointer":"/items/1/id"}
    },
    "env-echo": {
      "description": "print env",
      "command": [%s, "-test.run=TestHelperProcess", "--"],
      "env": {"GO_WANT_HELPER_PROCESS":"1","FAKE_OUTPUT":"env","FAKE_ECHO_OUT":"env:SECRET"},
      "timeout": "5s",
      "inputSchema": {"type":"object","properties":{},"additionalProperties":false}
    },
    "fail": {
      "description": "always exit 3",
      "command": [%s, "-test.run=TestHelperProcess", "--"],
      "env": {"GO_WANT_HELPER_PROCESS":"1","FAKE_EXIT":"3"},
      "timeout": "5s",
      "inputSchema": {"type":"object","properties":{},"additionalProperties":false}
    },
    "slow": {
      "description": "sleeps 1s",
      "command": [%s, "-test.run=TestHelperProcess", "--"],
      "env": {"GO_WANT_HELPER_PROCESS":"1","FAKE_SLEEP":"1s"},
      "timeout": "200ms",
      "inputSchema": {"type":"object","properties":{},"additionalProperties":false}
    },
    "marker": {
      "description": "writes marker on exec",
      "command": [%s, "-test.run=TestHelperProcess", "--"],
      "env": {"GO_WANT_HELPER_PROCESS":"1"},
      "timeout": "5s",
      "inputSchema": {"type":"object","properties":{"name":{"type":"string"}},"additionalProperties":false,"required":["name"]},
      "bindings": [{"property":"name","style":"positional"}]
    }
  }
}`, js(bin), js(bin), js(bin), js(bin), js(bin), js(bin), js(bin))

	dir := t.TempDir()
	p := filepath.Join(dir, "tools.json")
	if err := os.WriteFile(p, []byte(manifestJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mustTool(t *testing.T, r *Registry, name string) Tool {
	t.Helper()
	tool, ok := r.Lookup(name)
	if !ok {
		t.Fatalf("tool %q not found", name)
	}
	return tool
}

func call(t *testing.T, r *Registry, name string, args map[string]any) (*Result, error) {
	t.Helper()
	return r.Call(context.Background(), mustTool(t, r, name), args)
}

func TestArgsBinding(t *testing.T) {
	r := writeToolRegistry(t)
	ctx := context.Background()
	tool := mustTool(t, r, "echo-args")
	res, err := r.Call(ctx, tool, map[string]any{"state": "merged", "count": 3, "path": "/tmp/x"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit %d", res.ExitCode)
	}
	var got []string
	if err := json.Unmarshal(res.Stdout, &got); err != nil {
		t.Fatalf("stdout not json: %v (%s)", err, res.Stdout)
	}
	joined := strings.Join(got, " ")
	for _, want := range []string{"--state=merged", "--count", "3", "/tmp/x"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv %v missing %q", got, want)
		}
	}
}

func TestJSONOutputMode(t *testing.T) {
	r := writeToolRegistry(t)
	res, err := call(t, r, "json-out", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := res.Decoded.(map[string]any)
	if !ok || m["ok"] != true || m["value"] != float64(42) {
		t.Fatalf("decoded = %v", res.Decoded)
	}
}

func TestJSONPointerOutputMode(t *testing.T) {
	r := writeToolRegistry(t)
	res, err := call(t, r, "ptr-out", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(res.Decoded), "2"; got != want {
		t.Fatalf("pointer decoded = %v, want %v", res.Decoded, want)
	}
}

func TestEnvPassthrough(t *testing.T) {
	t.Setenv("SECRET", "topsecret")
	r := writeToolRegistry(t)
	res, err := call(t, r, "env-echo", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(res.Stdout)) != "topsecret" {
		t.Fatalf("env not passed through: %q", res.Stdout)
	}
}

func TestExecErrorOnExit(t *testing.T) {
	r := writeToolRegistry(t)
	_, err := call(t, r, "fail", map[string]any{})
	var ee *ExecError
	if !errors.As(err, &ee) || ee.ExitCode != 3 {
		t.Fatalf("want ExecError exit 3, got %v", err)
	}
}

func TestTimeoutError(t *testing.T) {
	r := writeToolRegistry(t)
	_, err := call(t, r, "slow", map[string]any{})
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("want TimeoutError, got %v", err)
	}
}

// A non-positive manifest timeout would arm no watchdog, so the tool could
// never time out; it is refused when the manifest is compiled. v0.1.0 armed a
// zero timer instead, which fired at once.
func TestNonPositiveTimeoutIsRejectedAtLoad(t *testing.T) {
	for _, timeout := range []string{"0s", "-1s"} {
		t.Run(timeout, func(t *testing.T) {
			m := fmt.Sprintf(`{"version":1,"tools":{"t":{
				"description":"non-positive timeout","command":[%s,"-test.run=TestHelperProcess","--"],
				"timeout":%s,
				"inputSchema":{"type":"object","properties":{},"additionalProperties":false}
			}}}`, js(helperBin(t)), js(timeout))
			p := filepath.Join(t.TempDir(), "tools.json")
			if err := os.WriteFile(p, []byte(m), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			var de *DefinitionError
			if !errors.As(err, &de) {
				t.Fatalf("Load with timeout %q = %T (%v), want *DefinitionError", timeout, err, err)
			}
			if msg := err.Error(); !strings.Contains(msg, timeout) || !strings.Contains(msg, "must be positive") {
				t.Fatalf("error %q does not name the timeout %q and the rule it broke", msg, timeout)
			}
		})
	}
}

func TestInvalidArgsDoNotSpawn(t *testing.T) {
	r := writeToolRegistry(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran.marker")
	tool := mustTool(t, r, "marker")
	// WithEnv sets the variable regardless of the manifest's env allow-list, and
	// the helper writes the marker whenever FAKE_MARKER is set. So the marker
	// would exist if the process ran, and its absence proves validation refused
	// the call before any spawn.
	_, err := r.Call(context.Background(), tool, map[string]any{}, WithEnv("FAKE_MARKER", marker))
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want ValidationError for missing required name, got %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("marker exists — process spawned despite validation failure")
	}
	if !veIssues(ve, "required", "/name") {
		t.Fatalf("expected required @ /name issue, got %v", ve.Issues)
	}
}

func veIssues(ve *ValidationError, code, path string) bool {
	for _, i := range ve.Issues {
		if i.Code == code && i.Path == path {
			return true
		}
	}
	return false
}

func TestCWDRestricted(t *testing.T) {
	dir := t.TempDir()
	bin := helperBin(t)
	m := fmt.Sprintf(`{"version":1,"tools":{"t":{
		"description":"cwd test","command":[%s,"-test.run=TestHelperProcess","--"],
		"env":{"GO_WANT_HELPER_PROCESS":"1"},
		"timeout":"5s",
		"inputSchema":{"type":"object","properties":{},"additionalProperties":false},
		"cwd":{"mode":"allow","roots":["."]}
	}}}`, js(bin))
	p := filepath.Join(dir, "tools.json")
	os.WriteFile(p, []byte(m), 0o644)
	r, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "..", "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = r.Call(context.Background(), mustTool(t, r, "t"), map[string]any{}, WithCWD(outside))
	if err == nil || !strings.Contains(err.Error(), "outside allowed roots") {
		t.Fatalf("expected cwd containment error, got %v", err)
	}
}
