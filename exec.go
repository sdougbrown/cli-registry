// Package cliregistry turns pre-registered CLI commands into typed tool calls.
//
// This file is the library's own process engine: Plan.run executes argv with
// exec.Command (no shell), a resolved environment, a bounded capture of both
// streams, prompt cancellation, and a process-group kill on cancellation,
// timeout and after every exit.
package cliregistry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxCapture = 1 << 20 // 1 MiB per stream

	// drainGrace bounds how long a finished call waits for the capture pipes to
	// close. A same-group descendant is killed, which closes its inherited
	// pipes; only a descendant that left the process group (setsid) can still
	// hold them, and its output is cut rather than blocking the call.
	drainGrace = 500 * time.Millisecond
)

func (p *Plan) run(ctx context.Context) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, &CanceledError{Tool: p.Tool, Args: p.Argv, Err: err}
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

	// killGroup serializes the timeout and cancellation kills with the
	// post-exit reap, so a late callback cannot signal a recycled group.
	var killMu sync.Mutex
	waited := false
	killGroup := func() bool {
		killMu.Lock()
		defer killMu.Unlock()
		if waited {
			return false
		}
		killProcessGroup(cmd)
		return true
	}
	var timedOut, cancelled atomic.Bool
	timer := time.AfterFunc(p.Timeout, func() { timedOut.Store(killGroup()) })
	ctxDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			cancelled.Store(killGroup())
		case <-ctxDone:
		}
	}()

	waitErr := cmd.Wait()
	timer.Stop()
	killMu.Lock()
	waited = true
	killMu.Unlock()
	close(ctxDone)

	// Any exit ends the group: same-group descendants must not survive a
	// normal exit either.
	killProcessGroup(cmd)

	drained := make(chan struct{})
	go func() { copies.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(drainGrace):
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
	if timedOut.Load() {
		return nil, &TimeoutError{Tool: p.Tool, Args: p.Argv, Timout: p.Timeout}
	}
	if cancelled.Load() {
		return nil, &CanceledError{Tool: p.Tool, Args: p.Argv, Err: ctx.Err()}
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
