package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPrivacySmokeRunnerContract keeps raw platform responses outside the runner. Query clients
// return only exact forbidden-marker counts; reports carry the aggregate count, never the marker,
// surface name, response body, or query. The counting transport is a Level 0 tripwire: it must
// remain unused because this test neither starts Docker nor contacts a real backend.
func TestPrivacySmokeRunnerContract(t *testing.T) {
	startedAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	deadline := startedAt.Add(time.Minute)
	const canary = "synthetic-private-canary-t087"
	identity := PrivacySmokeIdentity{RunID: "privacy-run-t087", Marker: "privacy-marker-t087"}
	contractSurfaces := []PrivacySmokeSurface{PrivacySmokeSurfaceAPI, PrivacySmokeSurfaceApplicationLog, PrivacySmokeSurfaceCollectorQueue, PrivacySmokeSurfaceTempo, PrivacySmokeSurfaceLoki, PrivacySmokeSurfaceLangfuseTrace, PrivacySmokeSurfaceLangfuseScore, PrivacySmokeSurfaceReport}

	tests := []struct {
		name           string
		hits           map[PrivacySmokeSurface]int
		queryErr       error
		wantStatus     string
		wantHits       int64
		wantErrorClass string
	}{
		{name: "all platform-visible surfaces have zero unredacted hits", hits: map[PrivacySmokeSurface]int{}, wantStatus: "passed", wantHits: 0},
		{name: "one Langfuse score hit fails without exposing the marker", hits: map[PrivacySmokeSurface]int{PrivacySmokeSurfaceLangfuseScore: 1}, wantStatus: "failed", wantHits: 1, wantErrorClass: "unexpected_evidence"},
		{name: "backend query failure is classified without echoing the marker", queryErr: errors.New("synthetic-private-canary-t087 backend body"), wantStatus: "failed", wantErrorClass: "query_failed"},
		{name: "negative backend count fails closed", hits: map[PrivacySmokeSurface]int{PrivacySmokeSurfaceTempo: -1}, wantStatus: "failed", wantErrorClass: "malformed_response"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakePrivacySmokeBackend{hits: tt.hits, queryErr: tt.queryErr}
			transport := &countingPrivacySmokeTransport{}
			report, err := RunPrivacySmoke(context.Background(), PrivacySmokeRequest{Deadline: deadline, Profile: "grafana", ForbiddenCanary: canary}, PrivacySmokeRunnerDependencies{
				Backend:         backend,
				Transport:       transport,
				Clock:           newPollerTestClock(startedAt),
				IdentityFactory: func(context.Context) (PrivacySmokeIdentity, error) { return identity, nil },
			})
			if err != nil {
				if strings.Contains(err.Error(), canary) {
					t.Fatal("privacy runner error reflected a forbidden marker")
				}
				t.Fatal("RunPrivacySmoke() must keep privacy verification failures in the report")
			}
			if report == nil {
				t.Fatal("RunPrivacySmoke() report = nil, want report-owned privacy evidence")
			}
			if transport.calls() != 0 || backend.calls() != len(contractSurfaces) {
				t.Fatal("privacy RED test must use every fake surface and no network transport")
			}
			for _, surface := range contractSurfaces {
				if backend.callsFor(surface) != 1 {
					t.Fatal("privacy runner must query each platform-visible surface exactly once")
				}
			}
			for _, target := range backend.targetsSnapshot() {
				if target.RunID != identity.RunID || target.Marker != identity.Marker || target.ForbiddenCanary != canary || !target.StartedAt.Equal(startedAt) || !target.Deadline.Equal(deadline) {
					t.Fatal("privacy query target did not retain the runner identity and bounded window")
				}
			}
			encoded := validatePrivacySmokeReport(t, report)
			if strings.Contains(encoded, canary) {
				t.Fatal("privacy runner reflected a forbidden marker")
			}
			if report.Status() != tt.wantStatus {
				t.Fatal("privacy smoke report must fail when a forbidden-marker hit or malformed count is found")
			}
			check := findPrivacySmokeCheck(t, report.Checks())
			if check.Status != tt.wantStatus || check.ErrorClass != tt.wantErrorClass || check.Evidence["forbidden_marker_hits"] != tt.wantHits {
				t.Fatal("privacy check does not have the expected safe status, hit count, and error class")
			}
		})
	}
}

type fakePrivacySmokeBackend struct {
	mu             sync.Mutex
	hits           map[PrivacySmokeSurface]int
	queryErr       error
	targets        []PrivacySmokeTarget
	callsBySurface map[PrivacySmokeSurface]int
}

func (b *fakePrivacySmokeBackend) Search(ctx context.Context, target PrivacySmokeTarget) (int, error) {
	if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(target.Deadline) {
		return 0, context.DeadlineExceeded
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.targets = append(b.targets, target)
	if b.callsBySurface == nil {
		b.callsBySurface = make(map[PrivacySmokeSurface]int)
	}
	b.callsBySurface[target.Surface]++
	if b.queryErr != nil {
		return 0, b.queryErr
	}
	return b.hits[target.Surface], nil
}

func (b *fakePrivacySmokeBackend) calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.targets)
}
func (b *fakePrivacySmokeBackend) callsFor(surface PrivacySmokeSurface) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.callsBySurface[surface]
}
func (b *fakePrivacySmokeBackend) targetsSnapshot() []PrivacySmokeTarget {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]PrivacySmokeTarget(nil), b.targets...)
}

type countingPrivacySmokeTransport struct {
	mu    sync.Mutex
	count int
}

func (t *countingPrivacySmokeTransport) Send(context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count++
}
func (t *countingPrivacySmokeTransport) calls() int { t.mu.Lock(); defer t.mu.Unlock(); return t.count }

func validatePrivacySmokeReport(t *testing.T, report *SmokeReport) string {
	t.Helper()
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("SmokeReport.MarshalJSON() error = %v", err)
	}
	validator, err := NewSmokeReportSchemaValidator(loadSmokeReportSchema(t))
	if err != nil {
		t.Fatalf("NewSmokeReportSchemaValidator() error = %v", err)
	}
	if err := validator.ValidateJSON(encoded); err != nil {
		t.Fatalf("privacy smoke schema validation error = %v", err)
	}
	return string(encoded)
}

func findPrivacySmokeCheck(t *testing.T, checks []BackendCheck) BackendCheck {
	t.Helper()
	for _, check := range checks {
		if check.Backend == "privacy" {
			return check
		}
	}
	t.Fatal("privacy smoke report is missing a privacy check")
	return BackendCheck{}
}
