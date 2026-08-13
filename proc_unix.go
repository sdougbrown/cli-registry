//go:build !windows

package cliregistry

import (
	"os/exec"
	"syscall"
)

// setProcAttr places the child in its own process group on Unix so the whole
// group (including descendants) can be killed on timeout.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the child's process group (the child and its
// descendants) with SIGKILL.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
