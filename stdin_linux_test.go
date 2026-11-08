//go:build linux

package cliregistry

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStdinEscaperProcess(t *testing.T) {
	mode := os.Getenv("STDIN_ESCAPER")
	if mode == "" {
		return
	}
	ready := os.Getenv("STDIN_ESCAPER_READY")
	if mode == "child" {
		if err := os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(2)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestStdinEscaperProcess$")
	child.Env = []string{"STDIN_ESCAPER=child", "STDIN_ESCAPER_READY=" + ready}
	child.Stdin = os.Stdin
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		os.Exit(3)
	}
	for i := 0; i < 1000; i++ {
		if _, err := os.Stat(ready); err == nil {
			os.Exit(0)
		}
		time.Sleep(time.Millisecond)
	}
	os.Exit(4)
}

func TestStdinEscapedReaderDoesNotBlock(t *testing.T) {
	becomeSubreaper(t)
	ready := filepath.Join(t.TempDir(), "escaped.pid")
	t.Cleanup(func() {
		b, err := os.ReadFile(ready)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(string(b))
		if err != nil {
			t.Error(err)
			return
		}
		syscall.Kill(pid, syscall.SIGKILL)
		var status syscall.WaitStatus
		if _, err := syscall.Wait4(pid, &status, 0, nil); err != nil {
			t.Error(err)
		}
	})
	r := fixtureRegistry(t, map[string]fixtureDef{"escape": {
		env:        map[string]string{"STDIN_ESCAPER": "leader", "STDIN_ESCAPER_READY": ready, "GORACE": "atexit_sleep_ms=0"},
		properties: map[string]any{"data": map[string]any{"type": "string"}},
		bindings:   []map[string]any{{"property": "data", "style": "stdin"}},
	}})
	r.tools["escape"].command[1] = "-test.run=^TestStdinEscaperProcess$"
	tool := mustTool(t, r, "escape")
	done := make(chan error, 1)
	go func() {
		_, err := r.Call(context.Background(), tool, map[string]any{"data": strings.Repeat("x", 1<<20)})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.HasPrefix(err.Error(), "stdin:") {
			t.Fatalf("expected incomplete input error, got %v", err)
		}
		if _, err := os.Stat(ready); err != nil {
			t.Fatalf("escaped reader did not start: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("escaped reader blocked the call")
	}
}
