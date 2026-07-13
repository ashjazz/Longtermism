package smoke

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestBoundedMarkerPollerReturnsImmediatelyWhenCurrentRunMarkerIsPresent(t *testing.T) {
	clock := newPollerTestClock(time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC))
	poller := NewBoundedMarkerPoller(clock, time.Second)
	target := newMarkerPollTarget(clock.Now())
	queryCalls := 0

	observation, err := poller.WaitForMarker(context.Background(), target, func(_ context.Context, got PollMarkerTarget) ([]MarkerObservation, error) {
		queryCalls++
		assertPollTarget(t, got, target)
		return []MarkerObservation{{Marker: target.Marker, ObservedAt: target.StartedAt.Add(time.Second)}}, nil
	})
	if err != nil {
		t.Fatal("WaitForMarker() returned an unexpected error")
	}
	if observation.Marker != target.Marker || !observation.ObservedAt.Equal(target.StartedAt.Add(time.Second)) || queryCalls != 1 || clock.WaitCount() != 0 {
		t.Fatal("WaitForMarker() did not return the immediate current-run observation")
	}
}

func TestBoundedMarkerPollerWaitsForDelayedCurrentRunMarker(t *testing.T) {
	clock := newPollerTestClock(time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC))
	poller := NewBoundedMarkerPoller(clock, time.Second)
	target := newMarkerPollTarget(clock.Now())
	queryCalls := 0

	observation, err := poller.WaitForMarker(context.Background(), target, func(_ context.Context, got PollMarkerTarget) ([]MarkerObservation, error) {
		queryCalls++
		assertPollTarget(t, got, target)
		if queryCalls == 1 {
			return nil, nil
		}
		return []MarkerObservation{{Marker: target.Marker, ObservedAt: clock.Now()}}, nil
	})
	if err != nil {
		t.Fatal("WaitForMarker() returned an unexpected error")
	}
	if observation.Marker != target.Marker || queryCalls != 2 || clock.WaitCount() != 1 {
		t.Fatal("WaitForMarker() did not wait exactly once for the delayed marker")
	}
}

func TestBoundedMarkerPollerAcceptsObservationsAtBothWindowBounds(t *testing.T) {
	tests := []struct {
		name       string
		observedAt func(PollMarkerTarget) time.Time
	}{
		{name: "started at bound", observedAt: func(target PollMarkerTarget) time.Time { return target.StartedAt }},
		{name: "deadline bound", observedAt: func(target PollMarkerTarget) time.Time { return target.Deadline }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := newPollerTestClock(time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC))
			poller := NewBoundedMarkerPoller(clock, time.Second)
			target := newMarkerPollTarget(clock.Now())
			observation, err := poller.WaitForMarker(context.Background(), target, func(_ context.Context, got PollMarkerTarget) ([]MarkerObservation, error) {
				assertPollTarget(t, got, target)
				return []MarkerObservation{{Marker: target.Marker, ObservedAt: tt.observedAt(target)}}, nil
			})
			if err != nil || !observation.ObservedAt.Equal(tt.observedAt(target)) {
				t.Fatal("WaitForMarker() did not accept a marker at the inclusive window bound")
			}
		})
	}
}

func TestBoundedMarkerPollerRejectsOtherAndOutOfWindowMarkers(t *testing.T) {
	clock := newPollerTestClock(time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC))
	poller := NewBoundedMarkerPoller(clock, time.Second)
	target := newMarkerPollTarget(clock.Now())
	target.Deadline = target.StartedAt.Add(time.Second)
	queryCalls := 0

	_, err := poller.WaitForMarker(context.Background(), target, func(_ context.Context, got PollMarkerTarget) ([]MarkerObservation, error) {
		queryCalls++
		assertPollTarget(t, got, target)
		return []MarkerObservation{
			{Marker: "marker-t021-other-run", ObservedAt: target.StartedAt.Add(time.Nanosecond)},
			{Marker: target.Marker, ObservedAt: target.StartedAt.Add(-time.Nanosecond)},
			{Marker: target.Marker, ObservedAt: target.Deadline.Add(time.Nanosecond)},
		}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || queryCalls == 0 {
		t.Fatal("WaitForMarker() accepted another-run, old, or late marker")
	}
}

func TestBoundedMarkerPollerTimesOutWhenNoCurrentRunMarkerArrives(t *testing.T) {
	clock := newPollerTestClock(time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC))
	poller := NewBoundedMarkerPoller(clock, 5*time.Second)
	target := newMarkerPollTarget(clock.Now())
	target.Deadline = target.StartedAt.Add(2 * time.Second)
	queryCalls := 0

	_, err := poller.WaitForMarker(context.Background(), target, func(_ context.Context, got PollMarkerTarget) ([]MarkerObservation, error) {
		queryCalls++
		assertPollTarget(t, got, target)
		return nil, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || !clock.Now().Equal(target.Deadline) || clock.LastWaitDuration() != 2*time.Second || queryCalls != 1 {
		t.Fatal("WaitForMarker() did not stop at the configured deadline")
	}
}

func TestBoundedMarkerPollerStopsBeforeQueryWhenContextIsCanceled(t *testing.T) {
	clock := newPollerTestClock(time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC))
	poller := NewBoundedMarkerPoller(clock, time.Second)
	target := newMarkerPollTarget(clock.Now())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	queryCalls := 0

	_, err := poller.WaitForMarker(ctx, target, func(context.Context, PollMarkerTarget) ([]MarkerObservation, error) {
		queryCalls++
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) || queryCalls != 0 || clock.WaitCount() != 0 {
		t.Fatal("WaitForMarker() ignored cancellation before the first query")
	}
}

func TestBoundedMarkerPollerStopsWhenContextIsCanceledDuringWait(t *testing.T) {
	clock := newPollerTestClock(time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC))
	poller := NewBoundedMarkerPoller(clock, time.Second)
	target := newMarkerPollTarget(clock.Now())
	ctx, cancel := context.WithCancel(context.Background())
	clock.SetOnWait(cancel)
	queryCalls := 0

	_, err := poller.WaitForMarker(ctx, target, func(context.Context, PollMarkerTarget) ([]MarkerObservation, error) {
		queryCalls++
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) || queryCalls != 1 || clock.WaitCount() != 1 {
		t.Fatal("WaitForMarker() continued after cancellation during a polling wait")
	}
}

func newMarkerPollTarget(startedAt time.Time) PollMarkerTarget {
	return PollMarkerTarget{
		Marker:    "marker-t021-current-run",
		StartedAt: startedAt,
		Deadline:  startedAt.Add(5 * time.Second),
	}
}

func assertPollTarget(t *testing.T, got, want PollMarkerTarget) {
	t.Helper()
	if got.Marker != want.Marker || !got.StartedAt.Equal(want.StartedAt) || !got.Deadline.Equal(want.Deadline) {
		t.Fatal("poll query did not receive the bounded marker target")
	}
}

type pollerTestClock struct {
	mu        sync.Mutex
	now       time.Time
	waitCount int
	lastWait  time.Duration
	onWait    func()
}

func newPollerTestClock(now time.Time) *pollerTestClock {
	return &pollerTestClock{now: now}
}

func (c *pollerTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *pollerTestClock) Wait(ctx context.Context, duration time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	onWait := c.onWait
	c.mu.Unlock()
	if onWait != nil {
		onWait()
	}
	if err := ctx.Err(); err != nil {
		c.mu.Lock()
		c.waitCount++
		c.lastWait = duration
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
	c.waitCount++
	c.lastWait = duration
	return nil
}

func (c *pollerTestClock) WaitCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waitCount
}

func (c *pollerTestClock) LastWaitDuration() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastWait
}

func (c *pollerTestClock) SetOnWait(onWait func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onWait = onWait
}
