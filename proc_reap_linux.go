//go:build linux

package cliregistry

import (
	"os/exec"

	"golang.org/x/sys/unix"
)

// canSignalAfterReap reports whether signalling a group after the leader was
// reaped is still identity-safe. A Unix group is addressed by a numeric pid,
// which the kernel may reuse once the leader is gone, so it is not; the
// post-exit teardown therefore happens before the reap instead.
func canSignalAfterReap() bool { return false }

// observeExitBeforeReap blocks until the leader exits without reaping it,
// using waitid with WNOWAIT. The leader is left as a waitable zombie, so its
// pid — and therefore its process-group id — cannot be reused until cmd.Wait
// reaps it. It reports whether the exit was observed; when it was not, the
// caller falls back to reaping directly.
func observeExitBeforeReap(cmd *exec.Cmd) bool {
	if cmd.Process == nil {
		return false
	}
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err == unix.EINTR {
			continue
		}
		return err == nil
	}
}
