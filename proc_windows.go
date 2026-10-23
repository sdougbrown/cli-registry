//go:build windows

package cliregistry

import (
	"os/exec"
)

// setProcAttr is a no-op on Windows (no process groups via this mechanism).
func setProcAttr(cmd *exec.Cmd) {}

// killProcessGroup kills the direct child on Windows. There is no portable
// process-group kill; descendants are best-effort reaped by the OS.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// canSignalAfterReap reports whether signalling after the leader was reaped is
// still identity-safe. Windows kills through the process handle the runtime
// holds (TerminateProcess), not through a reusable number, so it is.
func canSignalAfterReap() bool { return true }

// observeExitBeforeReap reports whether the leader's exit could be observed
// without reaping it. Windows has no such observation here, so the leader is
// reaped first; the post-exit kill stays safe because it uses the handle.
func observeExitBeforeReap(*exec.Cmd) bool { return false }
