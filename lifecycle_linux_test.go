//go:build linux

package cliregistry

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// TestEscaperProcess is the child used by the process-lifecycle tests. It
// starts a real descendant that leaves the process group with setsid and keeps
// the capture pipes, so no group kill can reach it.
func TestEscaperProcess(t *testing.T) {
	mode := os.Getenv("ESCAPER_MODE")
	if mode == "" {
		return
	}
	ready := os.Getenv("ESCAPER_READY")
	if mode == "escaped" {
		os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0o600)
		time.Sleep(2 * time.Second)
		fmt.Print("late-tail\n")
		os.Exit(0)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestEscaperProcess$")
	child.Env = []string{"ESCAPER_MODE=escaped", "ESCAPER_READY=" + ready}
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		os.Exit(20)
	}
	for i := 0; i < 500; i++ {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	fmt.Print("prefix\n")
	if mode == "leader-sleep" {
		time.Sleep(60 * time.Second)
	}
	os.Exit(0)
}

// becomeSubreaper makes the test process the reaper of orphaned descendants so
// that an escaped child can be waited for during cleanup.
func becomeSubreaper(t *testing.T) {
	t.Helper()
	var prior uint32
	if _, _, err := syscall.Syscall6(syscall.SYS_PRCTL, 37, uintptr(unsafe.Pointer(&prior)), 0, 0, 0, 0); err != 0 {
		t.Fatal(err)
	}
	if _, _, err := syscall.Syscall6(syscall.SYS_PRCTL, 36, 1, 0, 0, 0, 0); err != 0 {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Syscall6(syscall.SYS_PRCTL, 36, uintptr(prior), 0, 0, 0, 0) })
}

// escaperRegistry builds a tool whose process forks a setsid descendant that
// inherits the capture pipes. The descendant is adopted, killed and reaped
// during cleanup.
func escaperRegistry(t *testing.T, mode string) (*Registry, Tool, string) {
	t.Helper()
	becomeSubreaper(t)
	ready := filepath.Join(t.TempDir(), "escaped.pid")
	r := fixtureRegistry(t, map[string]fixtureDef{"escape": {env: map[string]string{"ESCAPER_MODE": mode, "ESCAPER_READY": ready}, timeout: "10s"}})
	r.tools["escape"].command[1] = "-test.run=^TestEscaperProcess$"
	t.Cleanup(func() {
		data, err := os.ReadFile(ready)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil {
			t.Error(err)
			return
		}
		syscall.Kill(pid, syscall.SIGKILL)
		var status syscall.WaitStatus
		if _, err := syscall.Wait4(pid, &status, 0, nil); err != nil {
			t.Errorf("reap escaper %d: %v", pid, err)
		}
	})
	return r, mustTool(t, r, "escape"), ready
}

func TestRepeatedCallsDoNotLeakFDsOrGoroutines(t *testing.T) {
	beforeGoroutines := runtime.NumGoroutine()
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	r := fixtureRegistry(t, map[string]fixtureDef{"quick": {env: map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "quiet", "GORACE": "atexit_sleep_ms=0"}}})
	tool := mustTool(t, r, "quick")
	for i := 0; i < 100; i++ {
		opts := []Option{WithEnv("GORACE", "atexit_sleep_ms=0")}
		switch i % 4 {
		case 0:
			if _, err := r.Call(context.Background(), tool, map[string]any{}, opts...); err != nil {
				t.Fatal(err)
			}
		case 1:
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := r.Call(ctx, tool, map[string]any{}, opts...); err == nil {
				t.Fatal("pre-cancelled Call succeeded")
			}
		case 2:
			opts = append(opts, WithTimeout(time.Nanosecond))
			if _, err := r.Call(context.Background(), tool, map[string]any{}, opts...); err == nil {
				t.Fatal("nanosecond timeout succeeded")
			}
		case 3:
			p, err := r.Plan(tool, map[string]any{}, opts...)
			if err != nil {
				t.Fatal(err)
			}
			p.Argv[0] = filepath.Join(t.TempDir(), "does-not-exist")
			if _, err := p.run(context.Background()); err == nil {
				t.Fatal("missing executable succeeded")
			}
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before)+2 {
		t.Fatalf("fd count grew from %d to %d", len(before), len(after))
	}
	time.Sleep(20 * time.Millisecond)
	if afterGoroutines := runtime.NumGoroutine(); afterGoroutines > beforeGoroutines+1 {
		t.Fatalf("goroutines grew from %d to %d", beforeGoroutines, afterGoroutines)
	}
}
