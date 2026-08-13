package backend

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

var errPrivacyBackendQuery = errors.New("privacy:query_failed")

type PrivacyBoundFixture struct {
	RunID, Marker, RequestID, AITraceID, ServiceTraceID, SpanID string
	StartedAt, Deadline                                         time.Time
	APISummaryRef, ApplicationLogRef                            string
	CollectorArtifactRef, ChatReportRef                         string
	ChatReportKind                                              string
}

type PrivacyManifestResolver interface {
	Resolve(context.Context, string) (PrivacyBoundFixture, error)
}

type PrivacySurfaceQueryRequest struct {
	Target      smoke.PrivacySmokeTarget
	Fixture     PrivacyBoundFixture
	ArtifactRef string
}

type PrivacySurfaceQueryResult struct {
	Count                int
	Attempted, QuerySent bool
}

type PrivacySurfaceQuery interface {
	Search(context.Context, PrivacySurfaceQueryRequest) (PrivacySurfaceQueryResult, error)
}

type PrivacySmokeBackendConfig struct {
	Manifest        PrivacyManifestResolver
	Fixture, Remote PrivacySurfaceQuery
	SurfaceTimeout  time.Duration
}

type PrivacySmokeBackend struct {
	manifest        PrivacyManifestResolver
	fixture, remote PrivacySurfaceQuery
	timeout         time.Duration
}

func NewPrivacySmokeBackend(config PrivacySmokeBackendConfig) (*PrivacySmokeBackend, error) {
	if config.Manifest == nil || config.Fixture == nil || config.Remote == nil || config.SurfaceTimeout <= 0 || config.SurfaceTimeout > maximumBackendQueryTimeout {
		return nil, errPrivacyBackendQuery
	}
	return &PrivacySmokeBackend{manifest: config.Manifest, fixture: config.Fixture, remote: config.Remote, timeout: config.SurfaceTimeout}, nil
}

func (backend *PrivacySmokeBackend) Search(ctx context.Context, target smoke.PrivacySmokeTarget) (int, error) {
	if backend == nil || ctx == nil || ctx.Err() != nil || !validPrivacySmokeTarget(target) {
		return 0, errPrivacyBackendQuery
	}
	fixture, err := backend.manifest.Resolve(ctx, target.ManifestRef)
	if err != nil || !privacyFixtureMatchesTarget(fixture, target) {
		return 0, errPrivacyBackendQuery
	}
	query, artifactRef, ok := backend.route(target.Surface, fixture)
	if !ok {
		return 0, errPrivacyBackendQuery
	}
	bounded, cancel := context.WithTimeout(ctx, backend.timeout)
	defer cancel()
	result, err := query.Search(bounded, PrivacySurfaceQueryRequest{Target: target, Fixture: fixture, ArtifactRef: artifactRef})
	if err != nil || !result.Attempted || !result.QuerySent || result.Count < 0 {
		return 0, errPrivacyBackendQuery
	}
	return result.Count, nil
}

func (backend *PrivacySmokeBackend) route(surface smoke.PrivacySmokeSurface, fixture PrivacyBoundFixture) (PrivacySurfaceQuery, string, bool) {
	switch surface {
	case smoke.PrivacySmokeSurfaceAPI:
		return backend.fixture, fixture.APISummaryRef, true
	case smoke.PrivacySmokeSurfaceApplicationLog:
		return backend.fixture, fixture.ApplicationLogRef, true
	case smoke.PrivacySmokeSurfaceCollectorQueue:
		return backend.fixture, fixture.CollectorArtifactRef, true
	case smoke.PrivacySmokeSurfaceReport:
		return backend.fixture, fixture.ChatReportRef, true
	case smoke.PrivacySmokeSurfaceTempo, smoke.PrivacySmokeSurfaceLoki, smoke.PrivacySmokeSurfaceLangfuseTrace, smoke.PrivacySmokeSurfaceLangfuseScore:
		return backend.remote, "", true
	default:
		return nil, "", false
	}
}

func validPrivacySmokeTarget(target smoke.PrivacySmokeTarget) bool {
	return safeManifestIDValue(target.RunID) && safeManifestIDValue(target.Marker) && safeManifestIDValue(target.ForbiddenCanary) &&
		safeManifestIDValue(target.RequestID) && safeManifestIDValue(target.AITraceID) && chatTraceIDPattern.MatchString(target.ServiceTraceID) &&
		chatSpanIDPattern.MatchString(target.SpanID) && safePrivacyManifestRef(target.ManifestRef) && target.Limit > 0 && target.Limit <= 100 &&
		!target.StartedAt.IsZero() && target.Deadline.After(target.StartedAt) && target.Deadline.Sub(target.StartedAt) <= time.Minute && validPrivacySurface(target.Surface)
}

func safePrivacyManifestRef(value string) bool {
	return strings.HasSuffix(value, ".json") && !strings.Contains(value, "/") && !strings.Contains(value, `\`) && len(value) <= 133 && safeManifestIDValue(strings.TrimSuffix(value, ".json"))
}

func validPrivacySurface(surface smoke.PrivacySmokeSurface) bool {
	switch surface {
	case smoke.PrivacySmokeSurfaceAPI, smoke.PrivacySmokeSurfaceApplicationLog, smoke.PrivacySmokeSurfaceCollectorQueue,
		smoke.PrivacySmokeSurfaceTempo, smoke.PrivacySmokeSurfaceLoki, smoke.PrivacySmokeSurfaceLangfuseTrace,
		smoke.PrivacySmokeSurfaceLangfuseScore, smoke.PrivacySmokeSurfaceReport:
		return true
	default:
		return false
	}
}

func privacyFixtureMatchesTarget(fixture PrivacyBoundFixture, target smoke.PrivacySmokeTarget) bool {
	return fixture.RunID == target.RunID && fixture.Marker == target.Marker && fixture.RequestID == target.RequestID && fixture.AITraceID == target.AITraceID &&
		fixture.ServiceTraceID == target.ServiceTraceID && fixture.SpanID == target.SpanID && fixture.StartedAt.Equal(target.StartedAt) && fixture.Deadline.Equal(target.Deadline) &&
		safePrivacyManifestRef(fixture.APISummaryRef) && safePrivacyManifestRef(fixture.ApplicationLogRef) && safePrivacyManifestRef(fixture.CollectorArtifactRef) &&
		safePrivacyManifestRef(fixture.ChatReportRef) && fixture.ChatReportKind == "chat_fixture_report" && uniquePrivacyArtifactRefs(fixture)
}

func uniquePrivacyArtifactRefs(fixture PrivacyBoundFixture) bool {
	refs := []string{fixture.APISummaryRef, fixture.ApplicationLogRef, fixture.CollectorArtifactRef, fixture.ChatReportRef}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if _, exists := seen[ref]; exists {
			return false
		}
		seen[ref] = struct{}{}
	}
	return true
}

var _ smoke.PrivacySmokeBackend = (*PrivacySmokeBackend)(nil)
