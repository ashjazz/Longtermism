package smoke

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

var (
	errInvalidPollMarkerTarget    = errors.New("invalid smoke marker poll target")
	errInvalidPollerConfiguration = errors.New("invalid smoke marker poller configuration")
	errSmokeMarkerQuery           = errors.New("smoke marker query failed")
	errSmokeMarkerWait            = errors.New("smoke marker polling wait failed")
	safePollMarkerPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{7,127}$`)
)

// PollMarkerTarget is the complete query boundary for one smoke run. The marker alone is never
// sufficient: retaining the time window prevents a delayed observation from an earlier run being
// accepted by a later run that happens to reuse a backend index.
type PollMarkerTarget struct {
	Marker    string
	StartedAt time.Time
	Deadline  time.Time
}

// MarkerObservation is the low-sensitivity result returned by a backend query adapter.
type MarkerObservation struct {
	Marker     string
	ObservedAt time.Time
}

// PollerClock keeps the bounded polling algorithm deterministic in Level 0 tests. Production
// adapters may use a real clock; tests use a fake clock and therefore never need time.Sleep.
type PollerClock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}

// MarkerQuery is implemented by backend adapters. It receives the full immutable target so an
// adapter cannot accidentally query by marker without the run's inclusive time window.
type MarkerQuery func(context.Context, PollMarkerTarget) ([]MarkerObservation, error)

// BoundedMarkerPoller repeatedly queries one backend until the current run's marker is observed
// inside its time window, the caller cancels, or the bounded window expires.
type BoundedMarkerPoller struct {
	clock        PollerClock
	pollInterval time.Duration
}

func NewBoundedMarkerPoller(clock PollerClock, pollInterval time.Duration) BoundedMarkerPoller {
	return BoundedMarkerPoller{clock: clock, pollInterval: pollInterval}
}

func (p BoundedMarkerPoller) WaitForMarker(ctx context.Context, target PollMarkerTarget, query MarkerQuery) (MarkerObservation, error) {
	if err := validatePollMarkerTarget(target); err != nil {
		return MarkerObservation{}, err
	}
	if p.clock == nil || p.pollInterval <= 0 || query == nil {
		return MarkerObservation{}, errInvalidPollerConfiguration
	}
	if err := ctx.Err(); err != nil {
		return MarkerObservation{}, err
	}

	// The injected clock controls polling decisions. A real timeout context signals cancellation to
	// cooperative backend adapters, so a slow query can stop when the smoke window expires.
	remaining := target.Deadline.Sub(p.clock.Now())
	if remaining <= 0 {
		return MarkerObservation{}, context.DeadlineExceeded
	}
	boundedContext, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	var lastQueryErr error
	hadSuccessfulQuery := false

	for {
		if err := boundedContext.Err(); err != nil {
			return MarkerObservation{}, err
		}
		// The query window is inclusive. When a wait advances exactly to deadline, issue one final
		// query before failing; otherwise a backend record written at the upper bound is lost.
		if p.clock.Now().After(target.Deadline) {
			return MarkerObservation{}, context.DeadlineExceeded
		}

		observations, err := query(boundedContext, target)
		if err != nil {
			if boundedContext.Err() != nil {
				return MarkerObservation{}, boundedContext.Err()
			}
			// Backend indexes can briefly reject a just-arrived query while the same marker is
			// already on its way through an asynchronous exporter. Preserve the failure only if
			// the bounded window expires; do not let one transient response end the smoke early.
			lastQueryErr = safeMarkerQueryError(err)
			if !retryableMarkerQueryError(lastQueryErr) {
				return MarkerObservation{}, lastQueryErr
			}
		} else {
			hadSuccessfulQuery = true
			lastQueryErr = nil
			if observation, found := currentRunObservation(observations, target); found {
				return observation, nil
			}
		}

		remaining = target.Deadline.Sub(p.clock.Now())
		if remaining <= 0 {
			if lastQueryErr != nil {
				return MarkerObservation{}, lastQueryErr
			}
			if hadSuccessfulQuery {
				return MarkerObservation{}, classifiedMarkerQueryError{class: "marker_missing"}
			}
			return MarkerObservation{}, context.DeadlineExceeded
		}
		if err := p.clock.Wait(boundedContext, minimumDuration(p.pollInterval, remaining)); err != nil {
			if boundedContext.Err() != nil {
				return MarkerObservation{}, boundedContext.Err()
			}
			return MarkerObservation{}, errSmokeMarkerWait
		}
	}
}

func retryableMarkerQueryError(err error) bool {
	var classified interface{ Class() string }
	if !errors.As(err, &classified) {
		return true
	}
	switch classified.Class() {
	case "backend_unavailable", "backend_timeout", "query_failed":
		return true
	default:
		return false
	}
}

// classifiedMarkerQueryError deliberately keeps only the finite adapter class. The original
// error may contain a backend response, query or credential and must not survive into reports.
type classifiedMarkerQueryError struct{ class string }

func (e classifiedMarkerQueryError) Error() string { return "smoke marker query failed: " + e.class }
func (e classifiedMarkerQueryError) Class() string { return e.class }

func safeMarkerQueryError(err error) error {
	var classified interface{ Class() string }
	if errors.As(err, &classified) && safeMarkerQueryErrorClass(classified.Class()) {
		return classifiedMarkerQueryError{class: classified.Class()}
	}
	return errSmokeMarkerQuery
}

func safeMarkerQueryErrorClass(class string) bool {
	switch class {
	case "authentication_failed", "backend_timeout", "backend_unavailable", "invalid_query", "malformed_response", "query_failed":
		return true
	default:
		return false
	}
}

func validatePollMarkerTarget(target PollMarkerTarget) error {
	if !isSafePollMarker(target.Marker) || target.StartedAt.IsZero() || target.Deadline.IsZero() || !target.Deadline.After(target.StartedAt) {
		return errInvalidPollMarkerTarget
	}
	return nil
}

func isSafePollMarker(marker string) bool {
	if !safePollMarkerPattern.MatchString(marker) {
		return false
	}
	lowerMarker := strings.ToLower(marker)
	for _, forbidden := range []string{"authorization", "bearer", "credential", "payload", "token", "secret"} {
		if strings.Contains(lowerMarker, forbidden) {
			return false
		}
	}
	return true
}

func currentRunObservation(observations []MarkerObservation, target PollMarkerTarget) (MarkerObservation, bool) {
	for _, observation := range observations {
		if observation.Marker != target.Marker || observation.ObservedAt.Before(target.StartedAt) || observation.ObservedAt.After(target.Deadline) {
			continue
		}
		return observation, true
	}
	return MarkerObservation{}, false
}

func minimumDuration(first, second time.Duration) time.Duration {
	if first < second {
		return first
	}
	return second
}
