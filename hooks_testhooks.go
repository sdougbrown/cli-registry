//go:build testhooks

package cliregistry

// Test-only lifecycle seams, active only under -tags testhooks. They insert
// scheduling pauses and observers around operations the engine performs
// anyway; they never replace a wait, a signal, a pipe or an error path.
//
// The seam set is stored atomically because a callback that has already
// published its cause may still be inside a seam after Call returned: joining
// publication (not the hook body) is what lets a test observe that a cause is
// known before the callback finishes.

import (
	"os/exec"
	"sync/atomic"
)

// hooks is one test's set of seams; a nil field is a no-op.
type hooks struct {
	// beforeGroupSignal runs before every process-group signal.
	beforeGroupSignal func(*exec.Cmd)
	// afterTimerSignal runs after the timeout callback published its cause and
	// signalled the group.
	afterTimerSignal func()
	// afterCancelSignal runs after the cancel watcher published its cause and
	// signalled the group.
	afterCancelSignal func()
	// afterWait runs after the leader has been reaped.
	afterWait func(*exec.Cmd)
}

var testHooks atomic.Pointer[hooks]

// setTestHooks installs h (or clears the seams when h is nil). Tests restore
// nil when they are done.
func setTestHooks(h *hooks) { testHooks.Store(h) }

func hookBeforeGroupSignal(cmd *exec.Cmd) {
	if h := testHooks.Load(); h != nil && h.beforeGroupSignal != nil {
		h.beforeGroupSignal(cmd)
	}
}

func hookAfterTimerSignal() {
	if h := testHooks.Load(); h != nil && h.afterTimerSignal != nil {
		h.afterTimerSignal()
	}
}

func hookAfterCancelSignal() {
	if h := testHooks.Load(); h != nil && h.afterCancelSignal != nil {
		h.afterCancelSignal()
	}
}

func hookAfterWait(cmd *exec.Cmd) {
	if h := testHooks.Load(); h != nil && h.afterWait != nil {
		h.afterWait(cmd)
	}
}
