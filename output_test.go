package cliregistry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecErrorCarriesStdout(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"half": {
			env: map[string]string{
				"GO_WANT_HELPER_PROCESS": "1",
				"FAKE_STDOUT":            "partial-out\n",
				"FAKE_STDERR":            "boom\n",
				"FAKE_EXIT":              "3",
			},
		},
	})
	res, err := r.Call(context.Background(), mustTool(t, r, "half"), map[string]any{})
	if res != nil {
		t.Fatalf("failed Call returned a result: %+v", res)
	}
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v, want *ExecError", err)
	}
	if ee.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", ee.ExitCode)
	}
	if string(ee.Stdout) != "partial-out\n" {
		t.Fatalf("ExecError.Stdout = %q, want %q", ee.Stdout, "partial-out\n")
	}
	if !strings.Contains(string(ee.Stderr), "boom") {
		t.Fatalf("ExecError.Stderr = %q, want it to contain %q", ee.Stderr, "boom")
	}
	if ee.Truncated {
		t.Fatal("ExecError.Truncated = true for a short stream")
	}
}

func TestExecErrorReportsTruncation(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"big-fail": {
			env:    map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "big", "FAKE_EXIT": "3"},
			output: map[string]any{"mode": "raw"},
		},
	})
	_, err := r.Call(context.Background(), mustTool(t, r, "big-fail"), map[string]any{})
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v, want *ExecError", err)
	}
	if !ee.Truncated {
		t.Fatal("ExecError.Truncated = false for output over the capture limit")
	}
	if len(ee.Stdout) != maxCapture {
		t.Fatalf("ExecError.Stdout is %d bytes, want the %d-byte capture", len(ee.Stdout), maxCapture)
	}
}

func TestTimeoutErrorCarriesPartialOutput(t *testing.T) {
	// 1s, not 200ms: the helper must reach its writes before the timeout can
	// classify the run, which it does within ~7.4ms measured, so 1s leaves
	// ~135x headroom for a loaded runner. The leader sleeps 60s, so the
	// timeout is still what stops it.
	r := fixtureRegistry(t, map[string]fixtureDef{
		"slow": {
			env: map[string]string{
				"GO_WANT_HELPER_PROCESS": "1",
				"FAKE_STDOUT":            "before\n",
				"FAKE_STDERR":            "stderr-before\n",
				"FAKE_SLEEP":             "60s",
			},
			timeout: "1s",
		},
	})
	start := time.Now()
	res, err := r.Call(context.Background(), mustTool(t, r, "slow"), map[string]any{})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timed-out Call took %s", elapsed)
	}
	if res != nil {
		t.Fatalf("timed-out Call returned a result: %+v", res)
	}
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("err = %v, want *TimeoutError", err)
	}
	if string(te.Stdout) != "before\n" {
		t.Fatalf("TimeoutError.Stdout = %q, want %q", te.Stdout, "before\n")
	}
	if !strings.Contains(string(te.Stderr), "stderr-before") {
		t.Fatalf("TimeoutError.Stderr = %q, want it to contain %q", te.Stderr, "stderr-before")
	}
	if te.Truncated {
		t.Fatal("TimeoutError.Truncated = true for short streams")
	}
	if te.Timeout != time.Second {
		t.Fatalf("TimeoutError.Timeout = %s, want 1s", te.Timeout)
	}
	if te.Timout != te.Timeout {
		t.Fatalf("deprecated Timout = %s, Timeout = %s; both must be set", te.Timout, te.Timeout)
	}
	if !strings.Contains(te.Error(), "1s") {
		t.Fatalf("TimeoutError.Error() = %q, want it to name the timeout", te.Error())
	}
}
