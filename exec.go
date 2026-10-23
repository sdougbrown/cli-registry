// Package cliregistry turns pre-registered CLI commands into typed tool calls.
//
// This file is the library's own process engine: Plan.run executes argv with
// exec.Command (no shell), a resolved environment, a bounded capture of both
// streams and a process-group kill on timeout.
package cliregistry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

const maxCapture = 1 << 20 // 1 MiB per stream

func (p *Plan) run(ctx context.Context) (*Result, error) {
	cmd := exec.Command(p.Argv[0], p.Argv[1:]...)

	env, err := p.ResolveEnv(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	cmd.Env = env

	// cwd: "" leaves the choice to the process engine (inherit)
	cmd.Dir = p.Dir

	// stdin closed; bounded capture via io.Writer
	cmd.Stdin = nil
	outW := newBoundedWriter(maxCapture)
	errW := newBoundedWriter(maxCapture)
	cmd.Stdout = outW
	cmd.Stderr = errW

	// process group so we can kill descendants
	setProcAttr(cmd)
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	var timedOut atomic.Bool
	timer := time.AfterFunc(p.Timeout, func() {
		// mark timed-out first, then kill the whole process group (descendants)
		timedOut.Store(true)
		killProcessGroup(cmd)
	})
	waitErr := cmd.Wait()
	timer.Stop()

	// honor an already-cancelled caller context as a timeout as well
	if ctx.Err() != nil {
		timedOut.Store(true)
		killProcessGroup(cmd)
	}

	if timedOut.Load() {
		return nil, &TimeoutError{Tool: p.Tool, Args: p.Argv, Timout: p.Timeout}
	}
	code, err := exitCodeOf(waitErr)
	if err != nil {
		return nil, err
	}
	return p.Finish(Output{
		Stdout:    outW.Bytes(),
		Stderr:    errW.Bytes(),
		Truncated: outW.Truncated() || errW.Truncated(),
		ExitCode:  code,
		Duration:  time.Since(start),
	})
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
