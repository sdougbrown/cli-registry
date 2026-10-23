package cliregistry

// This file is the library's own process engine: Plan.run executes argv with
// exec.Command (no shell), a resolved environment, a bounded capture of both
// streams, prompt cancellation, and a process-group teardown on cancellation,
// timeout and after every exit.
//
// # Process identity ownership
//
// A process group is addressed by the leader's pid, so a group signal is only
// a signal to the run's own group while that pid still belongs to the leader.
// Reaping the leader releases the pid, and the kernel may reuse it for an
// unrelated process group. The engine therefore never signals after reaping:
// it observes the exit without reaping it (waitid with WNOWAIT where the
// platform offers it), arbitrates the teardown under one lock, signals the
// group, and reaps last.
//
// Every group signal follows that protocol:
//
//   - The timeout and cancellation paths latch their cause under the lock and
//     signal only while the cause is still unlatched. On a platform where the
//     exit is observed without reaping, an unlatched cause means the leader
//     has not exited yet, so the leader is alive and owns the group id.
//   - The post-exit teardown signals the group while the leader is an
//     unreaped zombie, which still owns the group id.
//
// Platforms without a non-reaping exit observation (everything except Linux
// today, see observeExitBeforeReap) fall back to reaping first and signal the
// group afterwards only where the platform pins the process by handle rather
// than by pid (Windows); see awaitExit.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

const (
	maxCapture = 1 << 20 // 1 MiB per stream

	// drainGrace bounds how long a finished call waits for the capture pipes to
	// close after an ordinary exit or a timeout. A same-group descendant is
	// killed, which closes its inherited pipes; only a descendant that left the
	// process group (setsid) can still hold them, and its output is cut rather
	// than blocking the call.
	drainGrace = 500 * time.Millisecond
)

// terminationCause is the single reason a run's process was stopped. Exit,
// cancellation and timeout compete for it under executor.mu, and the winner is
// recorded before any group signal, so the result cannot be classified before
// its cause is known.
type terminationCause int

const (
	causeNone terminationCause = iota
	causeExit
	causeCancel
	causeTimeout
)

// executor owns one started process: its group signals and its reaping.
type executor struct {
	cmd *exec.Cmd

	mu     sync.Mutex
	cause  terminationCause
	ctxErr error // ctx.Err(), when cause is causeCancel
	reaped bool  // the leader has been reaped; its pid may be reused
}

// stop latches cause as the run's termination cause and signals the group. It
// reports whether it won the race. The cause is stored before the signal, so
// no observer of a dead process can miss it.
func (e *executor) stop(cause terminationCause, ctxErr error) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cause != causeNone || e.reaped {
		return false
	}
	e.cause = cause
	e.ctxErr = ctxErr
	killProcessGroup(e.cmd)
	return true
}

// awaitExit blocks until the leader has exited, arbitrates the teardown and
// reaps the leader. It returns the error cmd.Wait reported.
func (e *executor) awaitExit() error {
	if observeExitBeforeReap(e.cmd) {
		return e.reapPeeked()
	}
	// The exit was not observed without reaping (or cannot be on this
	// platform): reap directly rather than signal a group whose identity can
	// no longer be proven.
	return e.reapDirect()
}

// reapPeeked finishes a run whose exit was observed without reaping. The
// leader is a zombie, so its pid still names this run's process group: the
// post-exit kill and the reap both happen under the lifecycle lock, which is
// what keeps a concurrent stop from signalling an identity that has lapsed.
func (e *executor) reapPeeked() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cause == causeNone {
		e.cause = causeExit
	}
	// Any exit ends the group: same-group descendants must not survive a
	// normal exit either.
	killProcessGroup(e.cmd)
	err := e.cmd.Wait()
	e.reaped = true
	return err
}

// reapDirect finishes a run on a platform without a non-reaping exit
// observation: the leader is reaped first, and the group is signalled
// afterwards only where that is still identity-safe.
func (e *executor) reapDirect() error {
	err := e.cmd.Wait()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cause == causeNone {
		e.cause = causeExit
	}
	e.reaped = true
	if canSignalAfterReap() {
		killProcessGroup(e.cmd)
	}
	return err
}

// termination returns the latched cause and the context error that ended the
// run, if any.
func (e *executor) termination() (terminationCause, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cause, e.ctxErr
}

func (p *Plan) run(ctx context.Context) (*Result, error) {
	if err := ctx.Err(); err != nil {
		// No process is spawned, but the error keeps v0.1.0's family: a caller
		// with a timeout branch still catches it, and Cause names the context.
		return nil, &TimeoutError{
			Tool:    p.Tool,
			Args:    p.Argv,
			Timeout: p.Timeout,
			Timout:  p.Timeout,
			Cause:   err,
		}
	}
	env, err := p.ResolveEnv(os.LookupEnv)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(p.Argv[0], p.Argv[1:]...)
	cmd.Env = env
	cmd.Dir = p.Dir // "" leaves the choice to the process engine (inherit)
	cmd.Stdin = nil

	// Own the capture pipes so that a descendant holding one cannot keep
	// cmd.Wait blocked: the parent closes its write ends right after Start, and
	// the group kill closes the descendants' copies.
	stdout := newBoundedWriter(maxCapture)
	stderr := newBoundedWriter(maxCapture)
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("capture: %w", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return nil, fmt.Errorf("capture: %w", err)
	}
	cmd.Stdout = outW
	cmd.Stderr = errW

	setProcAttr(cmd)
	start := time.Now()
	if err := cmd.Start(); err != nil {
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		return nil, fmt.Errorf("start: %w", err)
	}
	outW.Close()
	errW.Close()

	var copies sync.WaitGroup
	copies.Add(2)
	go func() { defer copies.Done(); io.Copy(stdout, outR) }()
	go func() { defer copies.Done(); io.Copy(stderr, errR) }()

	ex := &executor{cmd: cmd}

	// The timer callback publishes its cause before the signal and before the
	// test seam below, so a callback that is still running cannot change the
	// outcome: timerDone closes once publication is complete.
	timerDone := make(chan struct{})
	var timer *time.Timer
	if p.Timeout > 0 {
		timer = time.AfterFunc(p.Timeout, func() {
			ex.stop(causeTimeout, nil)
			close(timerDone)
			hookAfterTimerSignal()
		})
	} else {
		close(timerDone)
	}

	// The cancel watcher publishes under the same lock; ctxDone stops it once
	// the leader has been reaped, so a cancellation that arrives after the run
	// cannot change its classification.
	ctxDone := make(chan struct{})
	cancelDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			ex.stop(causeCancel, ctx.Err())
			close(cancelDone)
			hookAfterCancelSignal()
		case <-ctxDone:
			close(cancelDone)
		}
	}()

	waitErr := ex.awaitExit()
	hookAfterWait(cmd)
	close(ctxDone)
	<-cancelDone
	if timer != nil && !timer.Stop() {
		<-timerDone
	}

	cause, ctxErr := ex.termination()

	grace := drainGrace
	drained := make(chan struct{})
	go func() { copies.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(grace):
		// A descendant that escaped the group still holds a pipe: cut the
		// capture instead of stalling the call, then let the copies finish.
		outR.Close()
		errR.Close()
		<-drained
	}
	outR.Close()
	errR.Close()

	out := Output{
		Stdout:    stdout.Bytes(),
		Stderr:    stderr.Bytes(),
		Truncated: stdout.Truncated() || stderr.Truncated(),
		Duration:  time.Since(start),
	}
	switch cause {
	case causeTimeout:
		return nil, &TimeoutError{
			Tool:      p.Tool,
			Args:      p.Argv,
			Timeout:   p.Timeout,
			Timout:    p.Timeout,
			Stdout:    out.Stdout,
			Stderr:    out.Stderr,
			Truncated: out.Truncated,
		}
	case causeCancel:
		return nil, &TimeoutError{
			Tool:      p.Tool,
			Args:      p.Argv,
			Timeout:   p.Timeout,
			Timout:    p.Timeout,
			Cause:     ctxErr,
			Stdout:    out.Stdout,
			Stderr:    out.Stderr,
			Truncated: out.Truncated,
		}
	}
	code, err := exitCodeOf(waitErr)
	if err != nil {
		return nil, err
	}
	out.ExitCode = code
	return p.Finish(out)
}

func exitCodeOf(waitErr error) (int, error) {
	if waitErr == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, fmt.Errorf("wait: %w", waitErr)
}

type boundedWriter struct {
	mu        sync.Mutex
	buf       []byte
	limit     int
	truncated bool
}

func newBoundedWriter(limit int) *boundedWriter { return &boundedWriter{limit: limit} }

func (b *boundedWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(b.buf) < b.limit {
		room := b.limit - len(b.buf)
		if n > room {
			b.buf = append(b.buf, p[:room]...)
			b.truncated = true
		} else {
			b.buf = append(b.buf, p...)
		}
	} else {
		b.truncated = true
	}
	return n, nil // report full write; we silently drop excess
}

func (b *boundedWriter) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...)
}

func (b *boundedWriter) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}
