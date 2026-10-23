package cliregistry

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// waitForFile polls for path to appear. Fixtures use it to synchronise on
// something the child has already done, instead of sleeping and hoping.
// Portable on purpose: tests without a build tag need it too.
func waitForFile(t *testing.T, path string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

// waitForPID polls path until its contents parse as a process id and returns
// it. Fixtures write pid files with os.WriteFile, which truncates before it
// writes. Waiting only for the file to exist can observe it empty, which the
// caller would take for a parse error on a healthy run.
func waitForPID(t *testing.T, path string, d time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(b))); convErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for a pid in %s", path)
	return 0
}
