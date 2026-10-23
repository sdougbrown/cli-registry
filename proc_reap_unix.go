//go:build unix && !linux

package cliregistry

import "os/exec"

// canSignalAfterReap reports whether signalling a group after the leader was
// reaped is still identity-safe. A Unix group is addressed by a numeric pid,
// which the kernel may reuse once the leader is gone, so it is not.
func canSignalAfterReap() bool { return false }

// observeExitBeforeReap blocks until the leader exits without reaping it and
// reports whether it managed to. Nothing is signalled from a group whose
// identity could not be observed this way; the fallback in reapDirect reaps
// first and then stops only the leader through the runtime's guarded process
// operation (see exec.go).
//
// Linux is the only Unix platform with a non-reaping exit observation wired up
// (waitid with WNOWAIT, see proc_reap_linux.go). The alternative on the BSDs is
// kqueue EVFILT_PROC, whose behaviour for a child that exits before the watch
// is registered cannot be verified in this repository, and a missed exit event
// would hang every call; reaping first is the safe fallback. Because that
// fallback cannot prove the group id after the reap, it never sends a numeric
// group signal: timeout and cancellation stop the leader only, and same-group
// descendants may survive the call.
func observeExitBeforeReap(*exec.Cmd) bool { return false }
