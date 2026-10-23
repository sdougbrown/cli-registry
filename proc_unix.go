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
// leader is either still running or an unreaped zombie (see exec.go). Only the
// paths that observe the exit without reaping may use it; the fallback in
// reapDirect must use killProcessLeader instead.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		hookBeforeGroupSignal(cmd)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// killProcessLeader stops the leader through the runtime's process operation.
// Unlike a numeric group signal, the runtime refuses to signal a process it
// has already waited for, so this is safe on a path that reaps before it can
// prove the group id (see reapDirect). It does not signal descendants.
func killProcessLeader(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
