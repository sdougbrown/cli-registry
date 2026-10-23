//go:build linux

package cliregistry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitForFile(t *testing.T, path string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

// pidRunning reports whether pid is a live (non-zombie) process.
func pidRunning(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	// State follows the ")" that closes the (possibly space-containing) comm.
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return true
	}
	fields := strings.Fields(string(b[i+1:]))
	if len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X") {
		return false
	}
	return true
}

func waitGone(t *testing.T, pid int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !pidRunning(pid) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pid %d is still running", pid)
}

func TestCancelledContextStopsProcess(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	r := fixtureRegistry(t, map[string]fixtureDef{
		"sleeper": {
			env: map[string]string{
				"GO_WANT_HELPER_PROCESS": "1",
				"FAKE_STDOUT":            "before-cancel\n",
				"FAKE_SLEEP":             "60s",
			},
			timeout: "60s",
		},
	})
	tool := mustTool(t, r, "sleeper")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type call struct {
		res *Result
		err error
	}
	done := make(chan call, 1)
	go func() {
		res, err := r.Call(ctx, tool, map[string]any{}, WithEnv("FAKE_RECORD_PID", pidFile))
		done <- call{res, err}
	}()
	waitForFile(t, pidFile, 15*time.Second)
	pid := readPID(t, pidFile)
	t.Cleanup(func() {
		if pidRunning(pid) {
			killPID(pid)
		}
	})

	start := time.Now()
	cancel()
	select {
	case got := <-done:
		if got.res != nil {
			t.Fatalf("cancelled Call returned a result: %+v", got.res)
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("err = %v, want one wrapping context.Canceled", got.err)
		}
		var te *TimeoutError
		if !errors.As(got.err, &te) {
			t.Fatalf("err = %T, want *TimeoutError (v0.1.0's family)", got.err)
		}
		if !IsCanceled(got.err) {
			t.Fatal("IsCanceled = false for a cancelled call")
		}
		if te.Cause == nil {
			t.Fatal("TimeoutError.Cause = nil for a cancelled call")
		}
		if string(te.Stdout) != "before-cancel\n" {
			t.Fatalf("TimeoutError.Stdout = %q, want the partial output", te.Stdout)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("cancel took %s to stop the process, want < 1s", elapsed)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Call did not return after the context was cancelled")
	}
	waitGone(t, pid, 2*time.Second)
}

func TestCancelledContextBeforeStartDoesNotSpawn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran.marker")
	r := fixtureRegistry(t, map[string]fixtureDef{
		"marker": {
			env:        map[string]string{"GO_WANT_HELPER_PROCESS": "1"},
			properties: map[string]any{"name": map[string]any{"type": "string"}},
			required:   []string{"name"},
			bindings:   []map[string]any{{"property": "name", "style": "positional"}},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err := r.Call(ctx, mustTool(t, r, "marker"), map[string]any{"name": "x"}, WithEnv("FAKE_MARKER", marker))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("pre-cancelled Call took %s", elapsed)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("a pre-cancelled Call started the process")
	}
}

func TestSameGroupDescendantsAreReaped(t *testing.T) {
	leaderPID := filepath.Join(t.TempDir(), "leader.pid")
	childPID := filepath.Join(t.TempDir(), "child.pid")
	r := fixtureRegistry(t, map[string]fixtureDef{
		"bg": {
			env: map[string]string{
				"GO_WANT_HELPER_PROCESS": "1",
				"FAKE_DESCENDANT":        "bg",
				"FAKE_STDOUT":            "done\n",
				"FAKE_OUTPUT":            "quiet",
			},
			timeout: "10s",
		},
	})
	res, err := r.Call(context.Background(), mustTool(t, r, "bg"), map[string]any{},
		WithEnv("FAKE_RECORD_PID", leaderPID), WithEnv("FAKE_DESCENDANT_PID", childPID))
	if err != nil {
		t.Fatal(err)
	}
	if exitCode := res.ExitCode; exitCode != 0 {
		t.Fatalf("exit code %d", exitCode)
	}
	pid := readPID(t, childPID)
	t.Cleanup(func() {
		if pidRunning(pid) {
			killPID(pid)
		}
	})
	waitGone(t, pid, 3*time.Second)
}

func TestPipeHoldingDescendantIsNotATimeout(t *testing.T) {
	childPID := filepath.Join(t.TempDir(), "child.pid")
	r := fixtureRegistry(t, map[string]fixtureDef{
		"holder": {
			env: map[string]string{
				"GO_WANT_HELPER_PROCESS": "1",
				"FAKE_DESCENDANT":        "pipe",
				"FAKE_STDOUT":            "done\n",
				"FAKE_OUTPUT":            "quiet",
			},
			timeout: "5s",
		},
	})
	start := time.Now()
	res, err := r.Call(context.Background(), mustTool(t, r, "holder"), map[string]any{},
		WithEnv("FAKE_DESCENDANT_PID", childPID))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("a normal exit became %v after %s, want success", err, elapsed)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Call took %s; a descendant holding the capture pipes stalled it", elapsed)
	}
	if string(res.Stdout) != "done\n" {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "done\n")
	}
	pid := readPID(t, childPID)
	t.Cleanup(func() {
		if pidRunning(pid) {
			killPID(pid)
		}
	})
	waitGone(t, pid, 3*time.Second)
}
