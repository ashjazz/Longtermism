package smoke

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

var errInfrastructureSmokeFailed = errors.New("infrastructure smoke verification failed")

type InfrastructureSmokeBackend interface {
	QueryTempo(context.Context, PollMarkerTarget) ([]MarkerObservation, error)
	QueryLoki(context.Context, PollMarkerTarget) ([]MarkerObservation, error)
	BaselineHTTPRequestCount(context.Context) (int64, error)
	HTTPRequestCount(context.Context) (int64, error)
	QueryLangfuse(context.Context, PollMarkerTarget) (int, error)
	QueryAIPlane(context.Context, PollMarkerTarget) (int, error)
}

type InfrastructureSmokeIdentity struct{ RunID, Marker string }
type InfrastructureSmokeIdentityFactory func(context.Context) (InfrastructureSmokeIdentity, error)
type InfrastructureSmokeTrigger func(context.Context, InfrastructureSmokeIdentity) error

type InfrastructureSmokeRunnerDependencies struct {
	Backend         InfrastructureSmokeBackend
	IdentityFactory InfrastructureSmokeIdentityFactory
	Trigger         InfrastructureSmokeTrigger
	Clock           PollerClock
	PollInterval    time.Duration
}

type InfrastructureSmokeRequest struct {
	Deadline time.Time
	Profile  string
}

// RunInfrastructureSmoke owns the identity and execution order. Verification failures live in a
// report (not an error) so CI retains every low-sensitivity backend fact from the same run.
func RunInfrastructureSmoke(ctx context.Context, request InfrastructureSmokeRequest, deps InfrastructureSmokeRunnerDependencies) (*SmokeReport, error) {
	if deps.Backend == nil || deps.Trigger == nil || deps.Clock == nil || deps.PollInterval <= 0 || !contains(allowedProfiles, request.Profile) {
		return nil, errInfrastructureSmokeFailed
	}
	startedAt := deps.Clock.Now().UTC()
	if request.Deadline.IsZero() || !request.Deadline.After(startedAt) {
		return nil, errInfrastructureSmokeFailed
	}
	identityFactory := deps.IdentityFactory
	if identityFactory == nil {
		identityFactory = newInfrastructureSmokeIdentity
	}
	identity, err := identityFactory(ctx)
	if err != nil || !isSafePollMarker(identity.RunID) {
		return nil, errInfrastructureSmokeFailed
	}
	// The factory owns only the unique nonce/run ID. Deriving the telemetry marker here prevents
	// a caller or test double from replaying an arbitrary marker independently of that identity.
	identity.Marker = deriveInfrastructureSmokeMarker(identity.RunID)
	target := PollMarkerTarget{Marker: identity.Marker, StartedAt: startedAt, Deadline: request.Deadline}
	bounded, cancel := context.WithDeadline(ctx, request.Deadline)
	defer cancel()
	if bounded.Err() != nil {
		return nil, errInfrastructureSmokeFailed
	}

	checks := make([]BackendCheckInput, 0, 6)
	baseline, baselineErr := deps.Backend.BaselineHTTPRequestCount(bounded)
	if bounded.Err() != nil {
		checks = append(checks,
			outcomeCheck("api", false, "api", "backend_timeout", map[string]any{"response_status": int64(0)}),
			outcomeCheck("tempo", false, "query", "backend_timeout", map[string]any{"matched_spans": int64(0)}),
			outcomeCheck("loki", false, "query", "backend_timeout", map[string]any{"matched_logs": int64(0)}),
			outcomeCheck("prometheus", false, "query", "backend_timeout", map[string]any{"metric_delta": int64(0)}),
			outcomeCheck("langfuse_trace", false, "query", "backend_timeout", map[string]any{"matched_traces": int64(0)}),
			outcomeCheck("collector", false, "query", "backend_timeout", map[string]any{"marker_received": int64(0)}),
		)
		return buildInfrastructureSmokeReport(identity, request, startedAt, deps.Clock.Now().UTC(), checks)
	}
	triggerErr := deps.Trigger(bounded, identity)
	checks = append(checks, outcomeCheck("api", triggerErr == nil, "api", "backend_unavailable", map[string]any{"response_status": int64(0)}))
	poller := NewBoundedMarkerPoller(deps.Clock, deps.PollInterval)
	_, tempoErr := poller.WaitForMarker(bounded, target, deps.Backend.QueryTempo)
	checks = append(checks, markerCheck("tempo", tempoErr, "matched_spans"))
	_, lokiErr := poller.WaitForMarker(bounded, target, deps.Backend.QueryLoki)
	checks = append(checks, markerCheck("loki", lokiErr, "matched_logs"))
	after, afterErr := deps.Backend.HTTPRequestCount(bounded)
	promOK := baselineErr == nil && afterErr == nil && after > baseline
	promClass := "query_failed"
	if baselineErr == nil && afterErr == nil && after <= baseline {
		promClass = "metric_delta_missing"
	}
	delta := int64(0)
	if baselineErr == nil && afterErr == nil {
		delta = after - baseline
	}
	checks = append(checks, outcomeCheck("prometheus", promOK, "query", promClass, map[string]any{"metric_delta": delta}))
	langfuse, langfuseErr := deps.Backend.QueryLangfuse(bounded, target)
	checks = append(checks, negativeCheck("langfuse_trace", langfuse, langfuseErr, "matched_traces"))
	ai, aiErr := deps.Backend.QueryAIPlane(bounded, target)
	checks = append(checks, negativeCheck("collector", ai, aiErr, "marker_received"))
	return buildInfrastructureSmokeReport(identity, request, startedAt, deps.Clock.Now().UTC(), checks)
}

func buildInfrastructureSmokeReport(identity InfrastructureSmokeIdentity, request InfrastructureSmokeRequest, startedAt, finishedAt time.Time, checks []BackendCheckInput) (*SmokeReport, error) {
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}
	if finishedAt.After(request.Deadline) {
		finishedAt = request.Deadline
	}
	report, err := BuildSmokeReport(SmokeReportInput{RunID: identity.RunID, Marker: identity.Marker, Profile: request.Profile, Scenario: "infra", StartedAt: startedAt, Deadline: request.Deadline, FinishedAt: finishedAt, Checks: checks, Cleanup: SmokeCleanupInput{Status: "not_required", ResidualResources: []string{}, TemporaryCredentials: "not_created", TemporaryData: "not_created"}})
	if err != nil {
		return nil, errInfrastructureSmokeFailed
	}
	return report, nil
}

func outcomeCheck(backend string, passed bool, stage, class string, evidence map[string]any) BackendCheckInput {
	if passed {
		return BackendCheckInput{Backend: backend, Status: "passed", FailureStage: "none", Evidence: evidence}
	}
	return BackendCheckInput{Backend: backend, Status: "failed", FailureStage: stage, ErrorClass: class, Evidence: evidence}
}
func markerCheck(backend string, err error, key string) BackendCheckInput {
	return outcomeCheck(backend, err == nil, "query", markerErrorClass(err), map[string]any{key: int64(boolToInt(err == nil))})
}
func negativeCheck(backend string, count int, err error, key string) BackendCheckInput {
	if err != nil {
		return outcomeCheck(backend, false, "query", markerErrorClass(err), map[string]any{key: int64(0)})
	}
	return outcomeCheck(backend, count == 0, "query", "unexpected_evidence", map[string]any{key: int64(count)})
}
func markerErrorClass(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "backend_timeout"
	}
	var classified interface{ Class() string }
	if errors.As(err, &classified) && contains(allowedErrorClasses, classified.Class()) {
		return classified.Class()
	}
	return "query_failed"
}
func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func newInfrastructureSmokeIdentity(context.Context) (InfrastructureSmokeIdentity, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return InfrastructureSmokeIdentity{}, err
	}
	encoded := hex.EncodeToString(bytes)
	return InfrastructureSmokeIdentity{RunID: "run-" + encoded, Marker: "marker-" + encoded}, nil
}

func deriveInfrastructureSmokeMarker(runID string) string {
	return runID
}
