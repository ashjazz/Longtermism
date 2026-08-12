package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

var errPrivacySmokeFailed = errors.New("privacy smoke verification failed")

// PrivacySmokeSurface is a closed, low-cardinality set of storage/query boundaries. Surface names
// are used only to route exact canary searches and are never persisted in the smoke report.
type PrivacySmokeSurface string

const (
	PrivacySmokeSurfaceAPI            PrivacySmokeSurface = "api"
	PrivacySmokeSurfaceApplicationLog PrivacySmokeSurface = "application_log"
	PrivacySmokeSurfaceCollectorQueue PrivacySmokeSurface = "collector_queue"
	PrivacySmokeSurfaceTempo          PrivacySmokeSurface = "tempo"
	PrivacySmokeSurfaceLoki           PrivacySmokeSurface = "loki"
	PrivacySmokeSurfaceLangfuseTrace  PrivacySmokeSurface = "langfuse_trace"
	PrivacySmokeSurfaceLangfuseScore  PrivacySmokeSurface = "langfuse_score"
	PrivacySmokeSurfaceReport         PrivacySmokeSurface = "report"
)

var privacySmokeSurfaces = [...]PrivacySmokeSurface{
	PrivacySmokeSurfaceAPI,
	PrivacySmokeSurfaceApplicationLog,
	PrivacySmokeSurfaceCollectorQueue,
	PrivacySmokeSurfaceTempo,
	PrivacySmokeSurfaceLoki,
	PrivacySmokeSurfaceLangfuseTrace,
	PrivacySmokeSurfaceLangfuseScore,
	PrivacySmokeSurfaceReport,
}

type PrivacySmokeIdentity struct{ RunID, Marker string }

type PrivacySmokeTarget struct {
	RunID, Marker, ForbiddenCanary string
	Surface                        PrivacySmokeSurface
	StartedAt, Deadline            time.Time
}

// PrivacySmokeBackend must execute an exact, bounded count query and return only the count. Raw
// response bodies stay inside adapters so the runner cannot accidentally echo leaked material.
type PrivacySmokeBackend interface {
	Search(context.Context, PrivacySmokeTarget) (int, error)
}

// PrivacySmokeTransport is deliberately a tripwire, not a sender. T107 verifies already-visible
// facts; triggering new network traffic here would mix injection with verification and leak canary.
type PrivacySmokeTransport interface {
	Send(context.Context)
}

type PrivacySmokeRequest struct {
	Deadline        time.Time
	Profile         string
	ForbiddenCanary string
}

type PrivacySmokeRunnerDependencies struct {
	Backend         PrivacySmokeBackend
	Transport       PrivacySmokeTransport
	Clock           PollerClock
	IdentityFactory func(context.Context) (PrivacySmokeIdentity, error)
}

// RunPrivacySmoke fails closed across every backend-visible surface. Query errors and malformed
// counts are report-owned; only invalid orchestration input prevents construction of a report.
func RunPrivacySmoke(ctx context.Context, request PrivacySmokeRequest, deps PrivacySmokeRunnerDependencies) (*SmokeReport, error) {
	startedAt, identity, bounded, cancel, err := preparePrivacySmoke(ctx, request, deps)
	if err != nil {
		return nil, err
	}
	defer cancel()

	totalHits, class := queryPrivacySurfaces(bounded, identity, request, startedAt, deps.Backend)
	check := privacySmokeCheck(totalHits, class)
	return buildPrivacySmokeReport(identity, request, startedAt, deps.Clock.Now(), check)
}

func preparePrivacySmoke(ctx context.Context, request PrivacySmokeRequest, deps PrivacySmokeRunnerDependencies) (time.Time, PrivacySmokeIdentity, context.Context, context.CancelFunc, error) {
	if ctx == nil || deps.Backend == nil || deps.Clock == nil || deps.IdentityFactory == nil || !contains(allowedProfiles, request.Profile) {
		return time.Time{}, PrivacySmokeIdentity{}, nil, nil, errPrivacySmokeFailed
	}
	startedAt := deps.Clock.Now().UTC()
	if request.Deadline.IsZero() || !request.Deadline.After(startedAt) || request.Deadline.Sub(startedAt) > time.Minute || !isSafePollMarker(request.ForbiddenCanary) {
		return time.Time{}, PrivacySmokeIdentity{}, nil, nil, errPrivacySmokeFailed
	}
	identity, err := deps.IdentityFactory(ctx)
	if err != nil || !isSafePollMarker(identity.RunID) || !isSafePollMarker(identity.Marker) ||
		strings.Contains(identity.RunID, request.ForbiddenCanary) || strings.Contains(identity.Marker, request.ForbiddenCanary) || ctx.Err() != nil {
		return time.Time{}, PrivacySmokeIdentity{}, nil, nil, errPrivacySmokeFailed
	}
	bounded, cancel := boundedChatContext(ctx, request.Deadline)
	return startedAt, identity, bounded, cancel, nil
}

func queryPrivacySurfaces(ctx context.Context, identity PrivacySmokeIdentity, request PrivacySmokeRequest, startedAt time.Time, backend PrivacySmokeBackend) (int64, string) {
	var total int64
	class := ""
	for _, surface := range privacySmokeSurfaces {
		target := PrivacySmokeTarget{RunID: identity.RunID, Marker: identity.Marker, ForbiddenCanary: request.ForbiddenCanary, Surface: surface, StartedAt: startedAt, Deadline: request.Deadline}
		hits, err := backend.Search(ctx, target)
		if err != nil {
			class = higherPrivacyFailure(class, "query_failed")
			continue
		}
		if hits < 0 {
			class = higherPrivacyFailure(class, "malformed_response")
			continue
		}
		if int64(hits) > math.MaxInt64-total {
			class = higherPrivacyFailure(class, "malformed_response")
			continue
		}
		total += int64(hits)
	}
	// A confirmed unredacted hit is the primary security fact even when another surface is
	// unavailable. Secondary diagnostics must never hide known leakage.
	if total > 0 {
		class = "unexpected_evidence"
	}
	return total, class
}

func higherPrivacyFailure(current, candidate string) string {
	priority := map[string]int{"": 0, "malformed_response": 1, "query_failed": 2, "unexpected_evidence": 3}
	if priority[candidate] > priority[current] {
		return candidate
	}
	return current
}

func privacySmokeCheck(totalHits int64, class string) BackendCheckInput {
	if class == "" {
		return outcomeCheck("privacy", true, "query", "", map[string]any{"forbidden_marker_hits": totalHits})
	}
	return outcomeCheck("privacy", false, "query", class, map[string]any{"forbidden_marker_hits": totalHits})
}

func buildPrivacySmokeReport(identity PrivacySmokeIdentity, request PrivacySmokeRequest, startedAt, finishedAt time.Time, check BackendCheckInput) (*SmokeReport, error) {
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}
	if finishedAt.After(request.Deadline) {
		finishedAt = request.Deadline
	}
	report, err := newPrivacySmokeReport(identity, request, startedAt, finishedAt, check)
	if err != nil {
		return nil, errPrivacySmokeFailed
	}
	// Backend adapters verify already-visible report/queue artifacts. This local serialization
	// guard separately proves the report generated by this run does not become a new leak source.
	encoded, err := json.Marshal(report)
	if err != nil {
		return nil, errPrivacySmokeFailed
	}
	if bytes.Contains(encoded, []byte(request.ForbiddenCanary)) {
		return nil, errPrivacySmokeFailed
	}
	return report, nil
}

func newPrivacySmokeReport(identity PrivacySmokeIdentity, request PrivacySmokeRequest, startedAt, finishedAt time.Time, check BackendCheckInput) (*SmokeReport, error) {
	return BuildSmokeReport(SmokeReportInput{
		RunID: identity.RunID, Marker: identity.Marker, Profile: request.Profile, Scenario: "privacy",
		StartedAt: startedAt, Deadline: request.Deadline, FinishedAt: finishedAt,
		Checks:  []BackendCheckInput{check},
		Cleanup: SmokeCleanupInput{Status: "not_required", ResidualResources: []string{}, TemporaryCredentials: "not_created", TemporaryData: "not_created"},
	})
}
