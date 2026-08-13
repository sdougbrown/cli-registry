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
