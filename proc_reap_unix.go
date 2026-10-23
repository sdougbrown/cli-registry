//go:build unix && !linux

package cliregistry

import "os/exec"

// canSignalAfterReap reports whether signalling a group after the leader was
// reaped is still identity-safe. A Unix group is addressed by a numeric pid,
// which the kernel may reuse once the leader is gone, so it is not.
func canSignalAfterReap() bool { return false }

// observeExitBeforeReap blocks until the leader exits without reaping it and
// reports whether it managed to. Nothing is signalled from a group whose
// identity could not be observed this way; see awaitExit.
//
// Linux is the only Unix platform with a non-reaping exit observation wired up
// (waitid with WNOWAIT, see proc_reap_linux.go). The alternative on the BSDs is
// kqueue EVFILT_PROC, whose behaviour for a child that exits before the watch
// is registered cannot be verified in this repository, and a missed exit event
// would hang every call; reaping first is the safe fallback.
func observeExitBeforeReap(*exec.Cmd) bool { return false }
