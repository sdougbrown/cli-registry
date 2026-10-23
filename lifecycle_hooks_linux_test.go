//go:build linux && testhooks

package cliregistry

// These tests pin the two publication rules the engine's teardown relies on:
// a termination cause is latched before its group signal, and no group signal
// is sent once the leader's identity has been released by the reap. The hooks
// only pause or observe; the engine still performs every real wait, signal and
// reap itself.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestTimeoutCausePublishedBeforeReturn(t *testing.T) {
	killed, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	setTestHooks(&hooks{afterTimerSignal: func() { close(killed); <-release; close(finished) }})
	defer setTestHooks(nil)
	r := fixtureRegistry(t, map[string]fixtureDef{"slow": {env: map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_SLEEP": "60s"}, timeout: "50ms"}})
	done := make(chan error, 1)
	go func() { _, err := r.Call(context.Background(), mustTool(t, r, "slow"), map[string]any{}); done <- err }()
	select {
	case <-killed:
	case <-time.After(5 * time.Second):
		t.Fatal("timer never killed")
	}
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		close(release)
		<-finished
		t.Fatal("Call did not return while the timer callback was still in flight")
	}
	close(release)
	<-finished
	if !IsTimeout(err) {
		t.Fatalf("timer killed the process but Call returned %T (%v), IsTimeout=false", err, err)
	}
}

func TestCancelCausePublishedBeforeReturn(t *testing.T) {
	killed, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	setTestHooks(&hooks{afterCancelSignal: func() { close(killed); <-release; close(finished) }})
	defer setTestHooks(nil)
	r := fixtureRegistry(t, map[string]fixtureDef{"slow": {env: map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_SLEEP": "60s"}, timeout: "60s"}})
	pidFile := filepath.Join(t.TempDir(), "leader.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := r.Call(ctx, mustTool(t, r, "slow"), map[string]any{}, WithEnv("FAKE_RECORD_PID", pidFile))
		done <- err
	}()
	waitForFile(t, pidFile, 5*time.Second)
	cancel()
	select {
	case <-killed:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel never killed")
	}
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		close(release)
		<-finished
		t.Fatal("Call did not return while the cancel callback was still in flight")
	}
	close(release)
	<-finished
	if !IsCanceled(err) {
		t.Fatalf("cancel killed the process but Call returned %T (%v), IsCanceled=false", err, err)
	}
}

// The identity invariant covers every real group signal, including the ones
// awaitExit sends while it observes and reaps the leader. Checking it only
// after Wait has returned cannot see a kill that was moved behind the reap, so
// this observes every hook and never gates on a later stage.
func TestEverySignalOwnsLeader(t *testing.T) {
	var staleSignals atomic.Int32
	setTestHooks(&hooks{beforeGroupSignal: func(cmd *exec.Cmd) {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", cmd.Process.Pid)); os.IsNotExist(err) {
			staleSignals.Add(1)
		}
	}})
	defer setTestHooks(nil)
	r := fixtureRegistry(t, map[string]fixtureDef{"quick": {env: map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "quiet", "GORACE": "atexit_sleep_ms=0"}, timeout: "60s"}})
	if _, err := r.Call(context.Background(), mustTool(t, r, "quick"), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if n := staleSignals.Load(); n != 0 {
		t.Fatalf("%d real group signals after /proc/leader disappeared", n)
	}
}

// The fallback path reaps before it can prove the group id, so a timeout or
// cancel callback that runs in the window after the real Wait must not send a
// numeric group signal. It stops the leader through the runtime's guarded
// operation instead, which is a no-op once the leader was waited for.
func TestFallbackStopsOnlyTheReleasedLeader(t *testing.T) {
	for _, cause := range []terminationCause{causeCancel, causeTimeout} {
		t.Run(fmt.Sprint(cause), func(t *testing.T) {
			cmd := exec.Command("/bin/true")
			setProcAttr(cmd)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			e := &executor{cmd: cmd}
			var staleSignals atomic.Int32
			var won atomic.Bool
			var guardedErr error
			setTestHooks(&hooks{
				beforeGroupSignal: func(c *exec.Cmd) {
					if _, err := os.Stat(fmt.Sprintf("/proc/%d", c.Process.Pid)); os.IsNotExist(err) {
						staleSignals.Add(1)
					}
				},
				afterDirectWait: func(c *exec.Cmd) {
					if _, err := os.Stat(fmt.Sprintf("/proc/%d", c.Process.Pid)); !os.IsNotExist(err) {
						t.Errorf("leader identity not released by the fallback Wait: %v", err)
					}
					// Run the production callback in the Wait-to-latch window.
					done := make(chan struct{})
					go func() {
						var ctxErr error
						if cause == causeCancel {
							ctxErr = context.Canceled
						}
						won.Store(e.stop(cause, ctxErr))
						close(done)
					}()
					<-done
					// The guarded operation is what the fallback path uses.
					guardedErr = c.Process.Kill()
				},
			})
			defer setTestHooks(nil)
			if err := e.reapDirect(); err != nil {
				t.Fatal(err)
			}
			latched, _ := e.termination()
			if !won.Load() {
				t.Fatal("callback lost; the post-Wait window was not exercised")
			}
			if latched != cause {
				t.Fatalf("latched cause = %v, want %v", latched, cause)
			}
			if n := staleSignals.Load(); n != 0 {
				t.Fatalf("fallback sent %d group signals after Wait released the leader", n)
			}
			if !errors.Is(guardedErr, os.ErrProcessDone) {
				t.Fatalf("guarded process kill after the reap = %v, want %v", guardedErr, os.ErrProcessDone)
			}
		})
	}
}
