package backend

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

func TestPrivacySmokeBackendRoutesAllEightSurfacesUsingOneTrustedFixture(t *testing.T) {
	startedAt := time.Now().UTC()
	fixtureQuery := &t180PrivacySurfaceQuery{counts: map[smoke.PrivacySmokeSurface]int{
		smoke.PrivacySmokeSurfaceAPI: 0, smoke.PrivacySmokeSurfaceApplicationLog: 0,
		smoke.PrivacySmokeSurfaceCollectorQueue: 0, smoke.PrivacySmokeSurfaceReport: 0,
	}}
	remoteQuery := &t180PrivacySurfaceQuery{counts: map[smoke.PrivacySmokeSurface]int{
		smoke.PrivacySmokeSurfaceTempo: 0, smoke.PrivacySmokeSurfaceLoki: 0,
		smoke.PrivacySmokeSurfaceLangfuseTrace: 0, smoke.PrivacySmokeSurfaceLangfuseScore: 0,
	}}
	manifest := &t180PrivacyManifestBinder{fixture: t180BoundPrivacyFixture(startedAt)}
	backend, err := newPrivacySmokeBackendPort(privacySmokeBackendPortConfig{Manifest: manifest, Fixture: fixtureQuery, Remote: remoteQuery, SurfaceTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewPrivacySmokeBackend() error = %v", err)
	}
	for _, surface := range t180PrivacySurfaces() {
		count, searchErr := backend.Search(context.Background(), t180PrivacyTarget(startedAt, surface))
		if searchErr != nil || count != 0 {
			t.Fatalf("Search(%s) = (%d,%v), want confirmed zero", surface, count, searchErr)
		}
	}
	wantFixture := []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceAPI, smoke.PrivacySmokeSurfaceApplicationLog, smoke.PrivacySmokeSurfaceCollectorQueue, smoke.PrivacySmokeSurfaceReport}
	wantRemote := []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceTempo, smoke.PrivacySmokeSurfaceLoki, smoke.PrivacySmokeSurfaceLangfuseTrace, smoke.PrivacySmokeSurfaceLangfuseScore}
	if !reflect.DeepEqual(fixtureQuery.calls, wantFixture) || !reflect.DeepEqual(remoteQuery.calls, wantRemote) {
		t.Fatalf("fixture calls=%v remote calls=%v, want registered local artifacts and four bounded remote queries", fixtureQuery.calls, remoteQuery.calls)
	}
	if manifest.calls != len(t180PrivacySurfaces()) {
		t.Fatalf("trusted manifest resolves = %d, want once before each surface", manifest.calls)
	}
	for _, request := range append(fixtureQuery.requests, remoteQuery.requests...) {
		target := request.Target
		if target.RunID != "run-t180" || target.Marker != "marker-t180" || target.ForbiddenCanary != "T180_SYNTHETIC_CANARY" ||
			target.RequestID != "req-t180" || target.AITraceID != "ai-t1800" || target.ServiceTraceID != "1234567890abcdef1234567890abcdef" ||
			target.SpanID != "1234567890abcdef" || target.ManifestRef != "manifest-t180.json" || target.Limit != 100 ||
			!target.StartedAt.Equal(startedAt) || !target.Deadline.Equal(startedAt.Add(time.Minute)) {
			t.Fatalf("routed target = %#v, want exact fixture identity/window/limit", target)
		}
		if request.Fixture.RunID != target.RunID || request.Fixture.Marker != target.Marker || request.Fixture.ChatReportRef != "chat-report-t180.json" {
			t.Fatalf("resolved fixture = %#v, want same-run registered artifact binding", request.Fixture)
		}
		wantArtifactRef := map[smoke.PrivacySmokeSurface]string{
			smoke.PrivacySmokeSurfaceAPI:            request.Fixture.APISummaryRef,
			smoke.PrivacySmokeSurfaceApplicationLog: request.Fixture.ApplicationLogRef,
			smoke.PrivacySmokeSurfaceCollectorQueue: request.Fixture.CollectorArtifactRef,
			smoke.PrivacySmokeSurfaceReport:         request.Fixture.ChatReportRef,
		}[target.Surface]
		if request.ArtifactRef != wantArtifactRef {
			t.Fatalf("%s artifact = %q, want registered ref %q", target.Surface, request.ArtifactRef, wantArtifactRef)
		}
	}
	for _, hasDeadline := range append(fixtureQuery.deadlines, remoteQuery.deadlines...) {
		if !hasDeadline {
			t.Fatal("surface query did not receive an independent bounded context")
		}
	}
	for _, remaining := range append(fixtureQuery.remaining, remoteQuery.remaining...) {
		if remaining <= 0 || remaining > time.Second {
			t.Fatalf("surface deadline remaining = %v, want independent timeout <= 1s", remaining)
		}
	}
}

func TestPrivacySmokeBackendRequiresBothTrustedQueryPlanes(t *testing.T) {
	query := &t180PrivacySurfaceQuery{}
	for _, config := range []privacySmokeBackendPortConfig{
		{Remote: query, SurfaceTimeout: time.Second},
		{Manifest: &t180PrivacyManifestBinder{}, Fixture: query, SurfaceTimeout: time.Second},
		{Manifest: &t180PrivacyManifestBinder{}, Remote: query, SurfaceTimeout: time.Second},
		{Manifest: &t180PrivacyManifestBinder{}, Fixture: query, Remote: query},
	} {
		if backend, err := newPrivacySmokeBackendPort(config); err == nil || backend != nil || len(query.calls) != 0 {
			t.Fatalf("newPrivacySmokeBackendPort(%#v) = (%#v,%v), want fail-fast configuration error", config, backend, err)
		}
	}
}

func TestPrivacySmokeBackendRejectsUnsafeOrUnregisteredTargetsBeforeAnyQuery(t *testing.T) {
	startedAt := time.Now().UTC()
	tests := []struct {
		name   string
		mutate func(*smoke.PrivacySmokeTarget)
	}{
		{name: "missing manifest", mutate: func(target *smoke.PrivacySmokeTarget) { target.ManifestRef = "" }},
		{name: "path escape", mutate: func(target *smoke.PrivacySmokeTarget) { target.ManifestRef = "../privacy-report.json" }},
		{name: "current privacy report", mutate: func(target *smoke.PrivacySmokeTarget) { target.ManifestRef = "privacy-report-current.json" }},
		{name: "query injection", mutate: func(target *smoke.PrivacySmokeTarget) { target.ForbiddenCanary = `canary" |~ ".*` }},
		{name: "foreign trace", mutate: func(target *smoke.PrivacySmokeTarget) { target.ServiceTraceID = "foreign" }},
		{name: "zero limit", mutate: func(target *smoke.PrivacySmokeTarget) { target.Limit = 0 }},
		{name: "excess limit", mutate: func(target *smoke.PrivacySmokeTarget) { target.Limit = 101 }},
		{name: "reversed window", mutate: func(target *smoke.PrivacySmokeTarget) { target.Deadline = target.StartedAt }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := &t180PrivacySurfaceQuery{}
			backend, err := newPrivacySmokeBackendPort(privacySmokeBackendPortConfig{Manifest: &t180PrivacyManifestBinder{fixture: t180BoundPrivacyFixture(startedAt)}, Fixture: query, Remote: query, SurfaceTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			target := t180PrivacyTarget(startedAt, smoke.PrivacySmokeSurfaceTempo)
			tt.mutate(&target)
			_, err = backend.Search(context.Background(), target)
			leakedManifest := target.ManifestRef != "" && strings.Contains(err.Error(), target.ManifestRef)
			if err == nil || len(query.calls) != 0 || strings.Contains(err.Error(), target.ForbiddenCanary) || leakedManifest {
				t.Fatalf("Search() error=%v calls=%d, want low-sensitive preflight rejection", err, len(query.calls))
			}
		})
	}
}

func TestPrivacySmokeBackendRejectsForeignManifestBeforeSurfaceQuery(t *testing.T) {
	startedAt := time.Now().UTC()
	tests := []struct {
		name   string
		mutate func(*PrivacyBoundFixture)
	}{
		{name: "foreign run", mutate: func(fixture *PrivacyBoundFixture) { fixture.RunID = "foreign-run" }},
		{name: "foreign marker", mutate: func(fixture *PrivacyBoundFixture) { fixture.Marker = "foreign-marker" }},
		{name: "foreign request", mutate: func(fixture *PrivacyBoundFixture) { fixture.RequestID = "foreign-request" }},
		{name: "foreign AI trace", mutate: func(fixture *PrivacyBoundFixture) { fixture.AITraceID = "foreign-ai" }},
		{name: "foreign native trace", mutate: func(fixture *PrivacyBoundFixture) { fixture.ServiceTraceID = "abcdefabcdefabcdefabcdefabcdefab" }},
		{name: "foreign native span", mutate: func(fixture *PrivacyBoundFixture) { fixture.SpanID = "abcdefabcdefabcd" }},
		{name: "foreign window", mutate: func(fixture *PrivacyBoundFixture) { fixture.StartedAt = fixture.StartedAt.Add(-time.Second) }},
		{name: "foreign deadline", mutate: func(fixture *PrivacyBoundFixture) { fixture.Deadline = fixture.Deadline.Add(time.Second) }},
		{name: "unregistered report", mutate: func(fixture *PrivacyBoundFixture) { fixture.ChatReportRef = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := t180BoundPrivacyFixture(startedAt)
			tt.mutate(&fixture)
			query := &t180PrivacySurfaceQuery{}
			backend, err := newPrivacySmokeBackendPort(privacySmokeBackendPortConfig{Manifest: &t180PrivacyManifestBinder{fixture: fixture}, Fixture: query, Remote: query, SurfaceTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			_, err = backend.Search(context.Background(), t180PrivacyTarget(startedAt, smoke.PrivacySmokeSurfaceReport))
			if err == nil || len(query.calls) != 0 {
				t.Fatalf("Search() error=%v calls=%d, want manifest-bound fail closed", err, len(query.calls))
			}
		})
	}
}

func TestPrivacySmokeBackendTreatsUnattemptedOrMalformedSurfaceAsFailureNotZero(t *testing.T) {
	startedAt := time.Now().UTC()
	tests := []struct {
		name  string
		query *t180PrivacySurfaceQuery
	}{
		{name: "surface not attempted", query: &t180PrivacySurfaceQuery{attempted: false}},
		{name: "query not sent", query: &t180PrivacySurfaceQuery{attempted: true, querySent: boolPointer(false)}},
		{name: "negative count", query: &t180PrivacySurfaceQuery{attempted: true, counts: map[smoke.PrivacySmokeSurface]int{smoke.PrivacySmokeSurfaceLoki: -1}}},
		{name: "backend failure", query: &t180PrivacySurfaceQuery{attempted: true, err: errors.New("raw-t180-backend-body")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, err := newPrivacySmokeBackendPort(privacySmokeBackendPortConfig{Manifest: &t180PrivacyManifestBinder{fixture: t180BoundPrivacyFixture(startedAt)}, Fixture: tt.query, Remote: tt.query, SurfaceTimeout: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			_, err = backend.Search(context.Background(), t180PrivacyTarget(startedAt, smoke.PrivacySmokeSurfaceLoki))
			if err == nil || strings.Contains(err.Error(), "raw-t180-backend-body") {
				t.Fatalf("Search() error = %v, want stable low-sensitive failure", err)
			}
		})
	}
}

func TestPrivacySmokeBackendRejectsUnknownSurfaceWithoutQuery(t *testing.T) {
	startedAt := time.Now().UTC()
	query := &t180PrivacySurfaceQuery{}
	backend, err := newPrivacySmokeBackendPort(privacySmokeBackendPortConfig{Manifest: &t180PrivacyManifestBinder{fixture: t180BoundPrivacyFixture(startedAt)}, Fixture: query, Remote: query, SurfaceTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.Search(context.Background(), t180PrivacyTarget(startedAt, smoke.PrivacySmokeSurface("unknown")))
	if err == nil || len(query.calls) != 0 {
		t.Fatalf("Search(unknown) error=%v calls=%d, want closed-surface rejection", err, len(query.calls))
	}
}

func t180PrivacyTarget(startedAt time.Time, surface smoke.PrivacySmokeSurface) smoke.PrivacySmokeTarget {
	return smoke.PrivacySmokeTarget{
		RunID: "run-t180", Marker: "marker-t180", ForbiddenCanary: "T180_SYNTHETIC_CANARY",
		RequestID: "req-t180", AITraceID: "ai-t1800", ServiceTraceID: "1234567890abcdef1234567890abcdef",
		SpanID: "1234567890abcdef", ManifestRef: "manifest-t180.json", Surface: surface,
		StartedAt: startedAt, Deadline: startedAt.Add(time.Minute), Limit: 100,
	}
}

func t180PrivacySurfaces() []smoke.PrivacySmokeSurface {
	return []smoke.PrivacySmokeSurface{
		smoke.PrivacySmokeSurfaceAPI, smoke.PrivacySmokeSurfaceApplicationLog,
		smoke.PrivacySmokeSurfaceCollectorQueue, smoke.PrivacySmokeSurfaceTempo,
		smoke.PrivacySmokeSurfaceLoki, smoke.PrivacySmokeSurfaceLangfuseTrace,
		smoke.PrivacySmokeSurfaceLangfuseScore, smoke.PrivacySmokeSurfaceReport,
	}
}

func t180BoundPrivacyFixture(startedAt time.Time) PrivacyBoundFixture {
	return PrivacyBoundFixture{
		RunID: "run-t180", Marker: "marker-t180", RequestID: "req-t180", AITraceID: "ai-t1800",
		ServiceTraceID: "1234567890abcdef1234567890abcdef", SpanID: "1234567890abcdef",
		StartedAt: startedAt, Deadline: startedAt.Add(time.Minute), APISummaryRef: "api-t180.json",
		ApplicationLogRef: "application-log-t180.json", CollectorArtifactRef: "collector-t180.json",
		ChatReportRef: "chat-report-t180.json", ChatReportKind: "chat_fixture_report",
	}
}

type t180PrivacyManifestBinder struct {
	calls   int
	fixture PrivacyBoundFixture
	err     error
}

func (binder *t180PrivacyManifestBinder) Resolve(_ context.Context, manifestRef string) (PrivacyBoundFixture, error) {
	binder.calls++
	if manifestRef != "manifest-t180.json" {
		return PrivacyBoundFixture{}, errors.New("invalid fixture manifest")
	}
	return binder.fixture, binder.err
}

type t180PrivacySurfaceQuery struct {
	calls     []smoke.PrivacySmokeSurface
	requests  []PrivacySurfaceQueryRequest
	counts    map[smoke.PrivacySmokeSurface]int
	attempted bool
	querySent *bool
	err       error
	deadlines []bool
	remaining []time.Duration
}

func (query *t180PrivacySurfaceQuery) Search(ctx context.Context, request PrivacySurfaceQueryRequest) (PrivacySurfaceQueryResult, error) {
	target := request.Target
	query.calls = append(query.calls, target.Surface)
	query.requests = append(query.requests, request)
	deadline, hasDeadline := ctx.Deadline()
	query.deadlines = append(query.deadlines, hasDeadline)
	if hasDeadline {
		query.remaining = append(query.remaining, time.Until(deadline))
	}
	attempted := query.attempted
	if query.counts != nil {
		attempted = true
	}
	querySent := attempted
	if query.querySent != nil {
		querySent = *query.querySent
	}
	return PrivacySurfaceQueryResult{Count: query.counts[target.Surface], Attempted: attempted, QuerySent: querySent}, query.err
}

func boolPointer(value bool) *bool { return &value }

var _ smoke.PrivacySmokeBackend = (*PrivacySmokeBackend)(nil)
