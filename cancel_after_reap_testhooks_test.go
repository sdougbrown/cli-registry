//go:build testhooks

package cliregistry

import (
	"context"
	"os/exec"
	"sync/atomic"
	"testing"
)

// A cancellation that lands after the leader was reaped must not reclassify the
// call: the exit already won, and stop() refuses to latch a cause for a run
// whose leader is gone.
func TestCancelAfterReapKeepsTheExitOutcome(t *testing.T) {
	var fired atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := fixtureRegistry(t, map[string]fixtureDef{
		"quick": {env: map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "quiet"}},
	})
	tool := mustTool(t, r, "quick")
	setTestHooks(&hooks{afterWait: func(*exec.Cmd) { fired.Store(true); cancel() }})
	t.Cleanup(func() { setTestHooks(nil) })

	res, err := r.Call(ctx, tool, map[string]any{})
	if err != nil {
		t.Fatalf("a cancel that arrived after the reap reclassified the call: %v", err)
	}
	// Without this the test would also pass if the seam stopped being called
	// and the cancellation simply never reached the engine.
	if !fired.Load() {
		t.Fatal("the afterWait seam never ran; the post-reap window was not exercised")
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code %d, want 0", res.ExitCode)
	}
}
