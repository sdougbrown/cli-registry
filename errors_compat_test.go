package cliregistry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A v0.1.0 client that handles timeouts with `IsTimeout` keeps catching a
// cancelled call and an expired deadline: context termination is reported as
// *TimeoutError, with Cause and Unwrap carrying ctx.Err().
func TestOldClientContextClassification(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if deadline {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}
		cancel()
		r := fixtureRegistry(t, map[string]fixtureDef{"quick": {env: map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_OUTPUT": "quiet"}}})
		_, err := r.Call(ctx, mustTool(t, r, "quick"), map[string]any{})
		var te *TimeoutError
		if !IsTimeout(err) || !errors.As(err, &te) {
			t.Fatalf("deadline=%v: old client no longer catches cancellation: %T %v", deadline, err, err)
		}
		if !IsCanceled(err) {
			t.Fatalf("deadline=%v: IsCanceled = false for a context-terminated call", deadline)
		}
		if te.Cause == nil || !errors.Is(err, te.Cause) {
			t.Fatalf("deadline=%v: TimeoutError.Cause = %v, want ctx.Err() and a working Unwrap", deadline, te.Cause)
		}
		want := context.Canceled
		other := context.DeadlineExceeded
		if deadline {
			want, other = context.DeadlineExceeded, context.Canceled
		}
		if !errors.Is(err, want) {
			t.Fatalf("deadline=%v: errors.Is(err, %v) = false for %v", deadline, want, err)
		}
		if errors.Is(err, other) {
			t.Fatalf("deadline=%v: errors.Is(err, %v) = true", deadline, other)
		}
	}
}

// A cancelled process keeps the partial output and is still distinguishable
// from the plan's own timeout.
func TestContextTerminationCarriesCauseAndOutput(t *testing.T) {
	r := fixtureRegistry(t, map[string]fixtureDef{
		"slow": {
			env:     map[string]string{"GO_WANT_HELPER_PROCESS": "1", "FAKE_STDOUT": "before\n", "FAKE_SLEEP": "60s"},
			timeout: "60s",
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := r.Call(ctx, mustTool(t, r, "slow"), map[string]any{})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	err := <-done
	var te *TimeoutError
	if !errors.As(err, &te) {
		t.Fatalf("err = %T (%v), want *TimeoutError", err, err)
	}
	if !errors.Is(err, context.Canceled) || !IsCanceled(err) {
		t.Fatalf("err = %v, want it to carry context.Canceled", err)
	}
	if string(te.Stdout) != "before\n" {
		t.Fatalf("TimeoutError.Stdout = %q, want the partial output", te.Stdout)
	}
	if te.Cause == nil {
		t.Fatal("TimeoutError.Cause = nil for a cancelled call")
	}

	// A killed process whose cause is the plan's own timeout carries no cause.
	_, err = r.Call(context.Background(), mustTool(t, r, "slow"), map[string]any{}, WithTimeout(100*time.Millisecond))
	if !IsTimeout(err) || IsCanceled(err) {
		t.Fatalf("plan timeout: IsTimeout=%v IsCanceled=%v (%v)", IsTimeout(err), IsCanceled(err), err)
	}
}

func TestDeprecatedTimoutStillFormats(t *testing.T) {
	err := &TimeoutError{Tool: "old-caller", Timout: 3 * time.Second}
	if !IsTimeout(err) {
		t.Fatalf("IsTimeout = false for %v", err)
	}
	if IsCanceled(err) {
		t.Fatal("IsCanceled = true for the plan's own timeout")
	}
	if got := err.Error(); !strings.Contains(got, "3s") {
		t.Fatalf("Error() = %q, want it to name 3s", got)
	}
	if IsTimeout(context.DeadlineExceeded) || IsTimeout(errors.New("x")) {
		t.Fatal("IsTimeout claimed a non-timeout error")
	}
}
