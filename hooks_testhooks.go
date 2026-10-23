//go:build testhooks

package cliregistry

// Test-only lifecycle seams, active only under -tags testhooks. They insert
// scheduling pauses and observers around operations the engine performs
// anyway; they never replace a wait, a signal, a pipe or an error path.

import "os/exec"

// hookBeforeGroupSignal runs before every process-group signal.
var hookBeforeGroupSignal = func(*exec.Cmd) {}

// hookAfterTimerSignal runs after the timeout callback has published its cause
// and signalled the group.
var hookAfterTimerSignal = func() {}

// hookAfterCancelSignal runs after the cancel watcher has published its cause
// and signalled the group.
var hookAfterCancelSignal = func() {}

// hookAfterWait runs after the leader has been reaped.
var hookAfterWait = func(*exec.Cmd) {}
