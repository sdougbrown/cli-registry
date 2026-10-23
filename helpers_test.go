package cliregistry

import (
	"os"
	"testing"
	"time"
)

// waitForFile polls for path to appear. Fixtures use it to synchronise on
// something the child has already done — it writes its output before its pid
// file — instead of sleeping and hoping. Portable on purpose: tests without a
// build tag need it too.
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
