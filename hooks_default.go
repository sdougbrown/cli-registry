//go:build !testhooks

package cliregistry

import "os/exec"

// Test-only lifecycle seams. The default build carries no hooks: every
// function below has an empty body and inlines away, so observing and
// scheduling probes cost the engine nothing. Build with -tags testhooks to
// activate the variables in hooks_testhooks.go.

func hookBeforeGroupSignal(*exec.Cmd) {}
func hookAfterTimerSignal()           {}
func hookAfterCancelSignal()          {}
func hookAfterWait(*exec.Cmd)         {}
