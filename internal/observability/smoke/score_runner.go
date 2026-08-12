package smoke

import (
	"context"
	"errors"
	"time"
)

const (
	maximumScoreSmokeWindow       = 2 * time.Minute
	maximumScoreSmokeObservations = 100
)

var errScoreSmokeFailed = errors.New("score smoke verification failed")

type ScoreSmokeEvidenceStore interface {
	Find(context.Context, string) ([]ScoreSmokeEvidence, error)
}

type ScoreSmokeBackend interface {
	IsConfigured(context.Context) bool
	ProjectionStates(context.Context, ScoreSmokeProjectionTarget) ([]ScoreSmokeProjectionObservation, error)
}

type ScoreSmokeIdentity struct{ RunID, Marker string }

// ScoreSmokeEvidence is the locally persisted source of truth. The platform result may confirm
// this identity, but must never create or repair it after the fact.
type ScoreSmokeEvidence struct {
	EvalRunID, ProjectionID, RequestID, AITraceID string
	PlatformTraceID, PlatformObservationID        string
}

type ScoreSmokeProjectionTarget struct {
	RunID, Marker, ProjectionID, EvalRunID string
	RequestID, AITraceID                   string
	PlatformTraceID, PlatformObservationID string
	StartedAt, Deadline                    time.Time
	Limit                                  int
}

type ScoreSmokeProjectionObservation struct {
	ProjectionID string
	Status       string
	Attempt      int
	ObservedAt   time.Time
}

type ScoreSmokeRequest struct {
	Deadline time.Time
	Profile  string
}

type ScoreSmokeRunnerDependencies struct {
	EvidenceStore   ScoreSmokeEvidenceStore
	Backend         ScoreSmokeBackend
	Clock           PollerClock
	PollInterval    time.Duration
	IdentityFactory func(context.Context) (ScoreSmokeIdentity, error)
}

// RunScoreSmoke verifies an asynchronous platform projection without treating Langfuse as the
// evidence source. It first resolves one immutable local record, then polls only that identity.
func RunScoreSmoke(ctx context.Context, request ScoreSmokeRequest, deps ScoreSmokeRunnerDependencies) (*SmokeReport, error) {
	startedAt, identity, bounded, cancel, err := prepareScoreSmoke(ctx, request, deps)
	if err != nil {
		return nil, err
	}
	defer cancel()

	evidence, check := loadScoreSmokeEvidence(bounded, identity.RunID, deps.EvidenceStore)
	if check != nil {
		return buildScoreSmokeReport(identity, ScoreSmokeEvidence{}, request, startedAt, deps.Clock.Now(), *check)
	}
	if !validLocalScoreSmokeEvidence(evidence) {
		check := failedScoreCheck("preflight", "unexpected_evidence", 0)
		return buildScoreSmokeReport(identity, ScoreSmokeEvidence{}, request, startedAt, deps.Clock.Now(), check)
	}
	if !deps.Backend.IsConfigured(bounded) {
		return buildScoreSmokeReport(identity, evidence, request, startedAt, deps.Clock.Now(), skippedScoreCheck())
	}
	if !validScoreSmokeEvidence(evidence) {
		check := failedScoreCheck("preflight", "unexpected_evidence", 0)
		return buildScoreSmokeReport(identity, ScoreSmokeEvidence{}, request, startedAt, deps.Clock.Now(), check)
	}
	target := scoreProjectionTarget(identity, evidence, startedAt, request.Deadline)
	check = pollScoreProjection(bounded, target, deps)
	return buildScoreSmokeReport(identity, evidence, request, startedAt, deps.Clock.Now(), *check)
}

func prepareScoreSmoke(ctx context.Context, request ScoreSmokeRequest, deps ScoreSmokeRunnerDependencies) (time.Time, ScoreSmokeIdentity, context.Context, context.CancelFunc, error) {
	if ctx == nil || deps.EvidenceStore == nil || deps.Backend == nil || deps.Clock == nil || deps.IdentityFactory == nil || deps.PollInterval <= 0 || !contains(allowedProfiles, request.Profile) {
		return time.Time{}, ScoreSmokeIdentity{}, nil, nil, errScoreSmokeFailed
	}
	startedAt := deps.Clock.Now().UTC()
	if request.Deadline.IsZero() || !request.Deadline.After(startedAt) || request.Deadline.Sub(startedAt) > maximumScoreSmokeWindow {
		return time.Time{}, ScoreSmokeIdentity{}, nil, nil, errScoreSmokeFailed
	}
	identity, err := deps.IdentityFactory(ctx)
	if err != nil || !isSafePollMarker(identity.RunID) || !isSafePollMarker(identity.Marker) || ctx.Err() != nil {
		return time.Time{}, ScoreSmokeIdentity{}, nil, nil, errScoreSmokeFailed
	}
	bounded, cancel := boundedChatContext(ctx, request.Deadline)
	return startedAt, identity, bounded, cancel, nil
}

func loadScoreSmokeEvidence(ctx context.Context, runID string, store ScoreSmokeEvidenceStore) (ScoreSmokeEvidence, *BackendCheckInput) {
	records, err := store.Find(ctx, runID)
	if err != nil {
		check := failedScoreCheck("preflight", "storage_unavailable", 0)
		return ScoreSmokeEvidence{}, &check
	}
	if len(records) != 1 {
		check := failedScoreCheck("preflight", "unexpected_evidence", 0)
		return ScoreSmokeEvidence{}, &check
	}
	return records[0], nil
}

func validScoreSmokeEvidence(evidence ScoreSmokeEvidence) bool {
	return validLocalScoreSmokeEvidence(evidence) && isSafePollMarker(evidence.ProjectionID) &&
		isSafePollMarker(evidence.PlatformTraceID) &&
		(evidence.PlatformObservationID == "" || isSafePollMarker(evidence.PlatformObservationID))
}

func validLocalScoreSmokeEvidence(evidence ScoreSmokeEvidence) bool {
	return isSafePollMarker(evidence.EvalRunID) && isSafePollMarker(evidence.RequestID) && isSafePollMarker(evidence.AITraceID)
}

func scoreProjectionTarget(identity ScoreSmokeIdentity, evidence ScoreSmokeEvidence, startedAt, deadline time.Time) ScoreSmokeProjectionTarget {
	return ScoreSmokeProjectionTarget{
		RunID: identity.RunID, Marker: identity.Marker, ProjectionID: evidence.ProjectionID,
		EvalRunID: evidence.EvalRunID, RequestID: evidence.RequestID, AITraceID: evidence.AITraceID,
		PlatformTraceID: evidence.PlatformTraceID, PlatformObservationID: evidence.PlatformObservationID,
		StartedAt: startedAt, Deadline: deadline, Limit: maximumScoreSmokeObservations,
	}
}

func pollScoreProjection(ctx context.Context, target ScoreSmokeProjectionTarget, deps ScoreSmokeRunnerDependencies) *BackendCheckInput {
	lastQueryFailed := false
	lastAttempt := 0
	var sentCandidate *ScoreSmokeProjectionObservation
	for {
		observations, err := deps.Backend.ProjectionStates(ctx, target)
		if err != nil {
			lastQueryFailed = true
			sentCandidate = nil
		} else {
			lastQueryFailed = false
			check, candidate := inspectScoreObservations(observations, target, &lastAttempt)
			if check != nil {
				return check
			}
			// Langfuse indexes asynchronously. Require the exact same singleton sent projection in
			// two consecutive snapshots so a duplicate that appears one refresh later cannot pass.
			if candidate != nil && sentCandidate != nil && sameScoreObservation(*candidate, *sentCandidate) {
				passed := passedScoreCheck()
				return &passed
			}
			sentCandidate = candidate
		}
		if !deps.Clock.Now().Before(target.Deadline) {
			class := "backend_timeout"
			if lastQueryFailed {
				class = "query_failed"
			}
			check := failedScoreCheck("query", class, 0)
			return &check
		}
		if err := deps.Clock.Wait(ctx, minimumDuration(deps.PollInterval, target.Deadline.Sub(deps.Clock.Now()))); err != nil {
			check := failedScoreCheck("query", markerErrorClass(err), 0)
			return &check
		}
	}
}

func inspectScoreObservations(observations []ScoreSmokeProjectionObservation, target ScoreSmokeProjectionTarget, lastAttempt *int) (*BackendCheckInput, *ScoreSmokeProjectionObservation) {
	if len(observations) > maximumScoreSmokeObservations {
		check := failedScoreCheck("query", "unexpected_evidence", 0)
		return &check, nil
	}
	current := make([]ScoreSmokeProjectionObservation, 0, len(observations))
	for _, observation := range observations {
		if observation.ObservedAt.Before(target.StartedAt) || observation.ObservedAt.After(target.Deadline) {
			continue
		}
		if observation.ProjectionID != target.ProjectionID {
			check := failedScoreCheck("query", "unexpected_evidence", 0)
			return &check, nil
		}
		current = append(current, observation)
	}
	if len(current) > 1 {
		check := failedScoreCheck("query", "unexpected_evidence", 0)
		return &check, nil
	}
	if len(current) == 0 {
		return nil, nil
	}
	observation := current[0]
	if observation.Attempt < 0 || observation.Attempt < *lastAttempt {
		check := failedScoreCheck("query", "unexpected_evidence", 0)
		return &check, nil
	}
	*lastAttempt = observation.Attempt
	switch observation.Status {
	case "queued", "sending", "retry_wait":
		return nil, nil
	case "sent":
		candidate := observation
		return nil, &candidate
	case "failed_permanent", "failed_shutdown_timeout", "dropped_queue_full":
		check := failedScoreCheck("query", "export_failed", 0)
		return &check, nil
	default:
		check := failedScoreCheck("query", "unexpected_evidence", 0)
		return &check, nil
	}
}

func sameScoreObservation(first, second ScoreSmokeProjectionObservation) bool {
	return first.ProjectionID == second.ProjectionID && first.Status == second.Status &&
		first.Attempt == second.Attempt && first.ObservedAt.Equal(second.ObservedAt)
}

func passedScoreCheck() BackendCheckInput {
	return outcomeCheck("langfuse_score", true, "query", "", map[string]any{"matched_scores": int64(1)})
}

func failedScoreCheck(stage, class string, matched int64) BackendCheckInput {
	return outcomeCheck("langfuse_score", false, stage, class, map[string]any{"matched_scores": matched})
}

func skippedScoreCheck() BackendCheckInput {
	return BackendCheckInput{Backend: "langfuse_score", Status: "skipped", FailureStage: "none", Evidence: map[string]any{"matched_scores": int64(0)}}
}

func buildScoreSmokeReport(identity ScoreSmokeIdentity, evidence ScoreSmokeEvidence, request ScoreSmokeRequest, startedAt, finishedAt time.Time, check BackendCheckInput) (*SmokeReport, error) {
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}
	if finishedAt.After(request.Deadline) {
		finishedAt = request.Deadline
	}
	report, err := BuildSmokeReport(SmokeReportInput{
		RunID: identity.RunID, Marker: identity.Marker, Profile: request.Profile, Scenario: "score",
		RequestID: evidence.RequestID, AITraceID: evidence.AITraceID,
		StartedAt: startedAt, Deadline: request.Deadline, FinishedAt: finishedAt,
		Checks:  []BackendCheckInput{check},
		Cleanup: SmokeCleanupInput{Status: "not_required", ResidualResources: []string{}, TemporaryCredentials: "not_created", TemporaryData: "not_created"},
	})
	if err != nil {
		return nil, errScoreSmokeFailed
	}
	return report, nil
}
