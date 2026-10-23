//go:build unix

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
// descendants) with SIGKILL. Callers must hold the process identity: the
// leader is either still running or an unreaped zombie (see exec.go).
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		hookBeforeGroupSignal(cmd)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
