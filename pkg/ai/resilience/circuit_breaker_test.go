package resilience

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCircuitBreakerStartsClosedAndKeepsSuccessfulCallsClosed(t *testing.T) {
	t.Parallel()

	breaker := NewCircuitBreaker(Config{
		FailureThreshold: 2,
		RecoveryTimeout:  time.Minute,
	})

	if breaker.State() != StateClosed {
		t.Fatalf("initial State() = %q, want %q", breaker.State(), StateClosed)
	}

	called := false
	err := breaker.Call(context.Background(), func(ctx context.Context) error {
		called = true
		return nil
	})

	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if !called {
		t.Fatal("Call() did not invoke protected function")
	}
	if breaker.State() != StateClosed {
		t.Fatalf("State() = %q, want %q", breaker.State(), StateClosed)
	}
}

func TestCircuitBreakerOpensAfterFailureThresholdAndFastFails(t *testing.T) {
	t.Parallel()

	cause := errors.New("upstream timeout")
	breaker := NewCircuitBreaker(Config{
		FailureThreshold: 2,
		RecoveryTimeout:  time.Minute,
	})

	if err := breaker.Call(context.Background(), func(ctx context.Context) error { return cause }); !errors.Is(err, cause) {
		t.Fatalf("first Call() error = %v, want preserve cause", err)
	}
	if breaker.State() != StateClosed {
		t.Fatalf("State() after first failure = %q, want %q", breaker.State(), StateClosed)
	}
	if err := breaker.Call(context.Background(), func(ctx context.Context) error { return cause }); !errors.Is(err, cause) {
		t.Fatalf("second Call() error = %v, want preserve cause", err)
	}
	if breaker.State() != StateOpen {
		t.Fatalf("State() after threshold = %q, want %q", breaker.State(), StateOpen)
	}

	called := false
	err := breaker.Call(context.Background(), func(ctx context.Context) error {
		called = true
		return nil
	})

	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("open Call() error = %v, want ErrCircuitOpen", err)
	}
	if called {
		t.Fatal("open Call() invoked protected function, want fast fail")
	}
	if breaker.State() != StateOpen {
		t.Fatalf("State() after fast fail = %q, want %q", breaker.State(), StateOpen)
	}
}

func TestCircuitBreakerTransitionsHalfOpenAfterRecoveryTimeout(t *testing.T) {
	t.Parallel()

	clock := newManualClock(time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC))
	breaker := NewCircuitBreaker(Config{
		FailureThreshold: 1,
		RecoveryTimeout:  time.Minute,
		HalfOpenMaxProbe: 2,
		Now:              clock.Now,
	})

	if err := breaker.Call(context.Background(), func(ctx context.Context) error { return errors.New("upstream 500") }); err == nil {
		t.Fatal("Call() error = nil, want first failure")
	}
	if breaker.State() != StateOpen {
		t.Fatalf("State() = %q, want %q", breaker.State(), StateOpen)
	}

	clock.Advance(time.Minute)
	called := false
	err := breaker.Call(context.Background(), func(ctx context.Context) error {
		called = true
		return nil
	})

	if err != nil {
		t.Fatalf("half-open probe Call() error = %v", err)
	}
	if !called {
		t.Fatal("half-open probe did not invoke protected function")
	}
	if breaker.State() != StateHalfOpen {
		t.Fatalf("State() after one successful probe = %q, want %q", breaker.State(), StateHalfOpen)
	}
}

func TestCircuitBreakerClosesAfterSuccessfulHalfOpenProbes(t *testing.T) {
	t.Parallel()

	clock := newManualClock(time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC))
	breaker := NewCircuitBreaker(Config{
		FailureThreshold: 1,
		RecoveryTimeout:  time.Minute,
		HalfOpenMaxProbe: 2,
		Now:              clock.Now,
	})
	openBreaker(t, breaker)

	clock.Advance(time.Minute)
	for i := 0; i < 2; i++ {
		if err := breaker.Call(context.Background(), func(ctx context.Context) error { return nil }); err != nil {
			t.Fatalf("half-open success probe %d error = %v", i+1, err)
		}
	}

	if breaker.State() != StateClosed {
		t.Fatalf("State() after successful probes = %q, want %q", breaker.State(), StateClosed)
	}
}

func TestCircuitBreakerReopensWhenHalfOpenProbeFails(t *testing.T) {
	t.Parallel()

	clock := newManualClock(time.Date(2026, 6, 29, 10, 0, 0, 0, time.UTC))
	cause := errors.New("probe failed")
	breaker := NewCircuitBreaker(Config{
		FailureThreshold: 1,
		RecoveryTimeout:  time.Minute,
		HalfOpenMaxProbe: 2,
		Now:              clock.Now,
	})
	openBreaker(t, breaker)

	clock.Advance(time.Minute)
	err := breaker.Call(context.Background(), func(ctx context.Context) error { return cause })

	if !errors.Is(err, cause) {
		t.Fatalf("half-open failed probe error = %v, want preserve cause", err)
	}
	if breaker.State() != StateOpen {
		t.Fatalf("State() after failed probe = %q, want %q", breaker.State(), StateOpen)
	}
}

func openBreaker(t *testing.T, breaker CircuitBreaker) {
	t.Helper()

	if err := breaker.Call(context.Background(), func(ctx context.Context) error { return errors.New("open breaker") }); err == nil {
		t.Fatal("Call() error = nil, want opening failure")
	}
	if breaker.State() != StateOpen {
		t.Fatalf("State() = %q, want %q", breaker.State(), StateOpen)
	}
}

type manualClock struct {
	now time.Time
}

func newManualClock(now time.Time) *manualClock {
	return &manualClock{now: now}
}

func (c *manualClock) Now() time.Time {
	return c.now
}

func (c *manualClock) Advance(duration time.Duration) {
	c.now = c.now.Add(duration)
}
