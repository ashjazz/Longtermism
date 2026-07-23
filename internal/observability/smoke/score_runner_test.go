package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestScoreSmokeRunnerContract fixes the asynchronous score-projection acceptance boundary. A
// Langfuse query is evidence about a projection, not the source of truth: the local evidence
// record must already exist, and retry observations must retain the same idempotency identity.
func TestScoreSmokeRunnerContract(t *testing.T) {
	startedAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	deadline := startedAt.Add(2 * time.Minute)
	identity := ScoreSmokeIdentity{RunID: "score-run-t086", Marker: "score-marker-t086"}
	evidence := ScoreSmokeEvidence{EvalRunID: "eval-run-t086", ProjectionID: "projection-t086", RequestID: "request-t086", AITraceID: "ai-trace-t086", PlatformTraceID: "platform-trace-t086", PlatformObservationID: "platform-observation-t086"}

	tests := []struct {
		name             string
		store            fakeScoreSmokeEvidenceStore
		backend          fakeScoreSmokeBackend
		wantReportStatus string
		wantCheckStatus  string
		wantFailure      scoreSmokeFailure
		wantQueryCount   int
	}{
		{
			name:  "confirms an asynchronous score sent within the two minute deadline",
			store: fakeScoreSmokeEvidenceStore{evidence: []ScoreSmokeEvidence{evidence}},
			backend: fakeScoreSmokeBackend{responses: []scoreSmokeQueryResponse{
				{observations: []ScoreSmokeProjectionObservation{{ProjectionID: evidence.ProjectionID, Status: "queued", ObservedAt: startedAt.Add(time.Second)}}},
				{observations: []ScoreSmokeProjectionObservation{{ProjectionID: evidence.ProjectionID, Status: "sent", ObservedAt: startedAt.Add(2 * time.Second)}}},
			}},
			wantReportStatus: "passed",
			wantCheckStatus:  "passed",
			wantQueryCount:   2,
		},
		{
			name:             "treats not configured projection as an explicit skipped backend state",
			store:            fakeScoreSmokeEvidenceStore{evidence: []ScoreSmokeEvidence{evidence}},
			backend:          fakeScoreSmokeBackend{notConfigured: true},
			wantReportStatus: "skipped",
			wantCheckStatus:  "skipped",
			wantQueryCount:   0,
		},
		{
			name:  "accepts a retry only when every observation keeps the stable projection identity",
			store: fakeScoreSmokeEvidenceStore{evidence: []ScoreSmokeEvidence{evidence}},
			backend: fakeScoreSmokeBackend{responses: []scoreSmokeQueryResponse{
				{observations: []ScoreSmokeProjectionObservation{{ProjectionID: evidence.ProjectionID, Status: "retry_wait", Attempt: 1, ObservedAt: startedAt.Add(time.Second)}}},
				{observations: []ScoreSmokeProjectionObservation{{ProjectionID: evidence.ProjectionID, Status: "sent", Attempt: 1, ObservedAt: startedAt.Add(2 * time.Second)}}},
			}},
			wantReportStatus: "passed",
			wantCheckStatus:  "passed",
			wantQueryCount:   2,
		},
		{
			name:             "records permanent projection failure without erasing local evidence",
			store:            fakeScoreSmokeEvidenceStore{evidence: []ScoreSmokeEvidence{evidence}},
			backend:          fakeScoreSmokeBackend{responses: []scoreSmokeQueryResponse{{observations: []ScoreSmokeProjectionObservation{{ProjectionID: evidence.ProjectionID, Status: "failed_permanent", ObservedAt: startedAt.Add(time.Second)}}}}},
			wantReportStatus: "failed",
			wantCheckStatus:  "failed",
			wantFailure:      scoreSmokeFailure{backend: "langfuse_score", stage: "query", class: "export_failed"},
			wantQueryCount:   1,
		},
		{
			name:  "rejects a sent score that arrived after the two minute window",
			store: fakeScoreSmokeEvidenceStore{evidence: []ScoreSmokeEvidence{evidence}},
			backend: fakeScoreSmokeBackend{responses: []scoreSmokeQueryResponse{
				{observations: []ScoreSmokeProjectionObservation{{ProjectionID: evidence.ProjectionID, Status: "sent", ObservedAt: deadline.Add(time.Second)}}},
			}},
			wantReportStatus: "failed",
			wantCheckStatus:  "failed",
			wantFailure:      scoreSmokeFailure{backend: "langfuse_score", stage: "query", class: "backend_timeout"},
			wantQueryCount:   121,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store
			backend := tt.backend
			order := &scoreSmokeEventOrder{}
			store.order = order
			backend.order = order
			report, err := RunScoreSmoke(context.Background(), ScoreSmokeRequest{Deadline: deadline, Profile: "grafana"}, ScoreSmokeRunnerDependencies{
				EvidenceStore: &store,
				Backend:       &backend,
				Clock:         newPollerTestClock(startedAt),
				PollInterval:  time.Second,
				IdentityFactory: func(context.Context) (ScoreSmokeIdentity, error) {
					return identity, nil
				},
			})
			if err != nil {
				t.Fatalf("RunScoreSmoke() error = %v, want verification failures represented by a report", err)
			}
			if report == nil {
				t.Fatal("RunScoreSmoke() report = nil, want a schema-valid score projection report")
			}
			storeSnapshot := store.snapshot()
			backendSnapshot := backend.snapshot()
			if storeSnapshot.readCalls != 1 || storeSnapshot.findRunID != identity.RunID || !store.readBeforeBackend(backendSnapshot) {
				t.Fatal("score smoke must verify locally persisted evidence before querying the projection backend")
			}

			document := validateScoreSmokeReport(t, report)
			if document.Status != tt.wantReportStatus || document.Scenario != "score" {
				t.Fatalf("score smoke report = %#v, want schema-valid %q score report", document, tt.wantReportStatus)
			}
			check := findScoreSmokeCheck(t, document.Checks, "langfuse_score")
			if check.Status != tt.wantCheckStatus {
				t.Fatalf("score projection check status = %q, want %q", check.Status, tt.wantCheckStatus)
			}
			if tt.wantFailure.backend != "" {
				assertScoreSmokeFailure(t, document.Checks, tt.wantFailure)
			}
			if got := backendSnapshot.configurationChecks; got != 1 {
				t.Fatalf("projection backend configuration checks = %d, want 1", got)
			}
			if got := backendSnapshot.queryCalls; got != tt.wantQueryCount {
				t.Fatalf("projection backend query calls = %d, want %d", got, tt.wantQueryCount)
			}
			for index, target := range backendSnapshot.targets {
				if target.RunID != identity.RunID || target.Marker != identity.Marker || target.ProjectionID != evidence.ProjectionID || target.EvalRunID != evidence.EvalRunID || target.RequestID != evidence.RequestID || target.AITraceID != evidence.AITraceID || target.PlatformTraceID != evidence.PlatformTraceID || target.PlatformObservationID != evidence.PlatformObservationID || !target.StartedAt.Equal(startedAt) || !target.Deadline.Equal(deadline) {
					t.Fatalf("projection query target %d does not preserve local evidence identity and the 120-second window", index)
				}
			}
		})
	}
}

func TestScoreSmokeRunnerRejectsDuplicateScoresForOneStableProjectionID(t *testing.T) {
	startedAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	deadline := startedAt.Add(2 * time.Minute)
	identity := ScoreSmokeIdentity{RunID: "score-run-t086-duplicate", Marker: "score-marker-t086-duplicate"}
	evidence := ScoreSmokeEvidence{EvalRunID: "eval-run-t086-duplicate", ProjectionID: "projection-t086-duplicate", RequestID: "request-t086-duplicate", AITraceID: "ai-trace-t086-duplicate", PlatformTraceID: "platform-trace-t086-duplicate", PlatformObservationID: "platform-observation-t086-duplicate"}
	store := fakeScoreSmokeEvidenceStore{evidence: []ScoreSmokeEvidence{evidence}}
	backend := fakeScoreSmokeBackend{responses: []scoreSmokeQueryResponse{{observations: []ScoreSmokeProjectionObservation{
		{ProjectionID: evidence.ProjectionID, Status: "sent", ObservedAt: startedAt.Add(time.Second)},
		{ProjectionID: evidence.ProjectionID, Status: "sent", ObservedAt: startedAt.Add(2 * time.Second)},
	}}}}
	order := &scoreSmokeEventOrder{}
	store.order = order
	backend.order = order

	report, err := RunScoreSmoke(context.Background(), ScoreSmokeRequest{Deadline: deadline, Profile: "grafana"}, ScoreSmokeRunnerDependencies{
		EvidenceStore: &store,
		Backend:       &backend,
		Clock:         newPollerTestClock(startedAt),
		PollInterval:  time.Second,
		IdentityFactory: func(context.Context) (ScoreSmokeIdentity, error) {
			return identity, nil
		},
	})
	if err != nil || report == nil {
		t.Fatalf("RunScoreSmoke() = (%#v, %v), want report-owned duplicate diagnosis", report, err)
	}
	document := validateScoreSmokeReport(t, report)
	if document.Status != "failed" {
		t.Fatalf("duplicate projection report status = %q, want failed", document.Status)
	}
	assertScoreSmokeFailure(t, document.Checks, scoreSmokeFailure{backend: "langfuse_score", stage: "query", class: "unexpected_evidence"})
}

func TestScoreSmokeRunnerRejectsForeignProjectionAlongsideTheExpectedScore(t *testing.T) {
	startedAt := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	deadline := startedAt.Add(2 * time.Minute)
	identity := ScoreSmokeIdentity{RunID: "score-run-t086-foreign", Marker: "score-marker-t086-foreign"}
	evidence := ScoreSmokeEvidence{EvalRunID: "eval-run-t086-foreign", ProjectionID: "projection-t086-foreign", RequestID: "request-t086-foreign", AITraceID: "ai-trace-t086-foreign", PlatformTraceID: "platform-trace-t086-foreign", PlatformObservationID: "platform-observation-t086-foreign"}
	store := fakeScoreSmokeEvidenceStore{evidence: []ScoreSmokeEvidence{evidence}}
	backend := fakeScoreSmokeBackend{responses: []scoreSmokeQueryResponse{{observations: []ScoreSmokeProjectionObservation{
		{ProjectionID: evidence.ProjectionID, Status: "sent", ObservedAt: startedAt.Add(time.Second)},
		{ProjectionID: "projection-from-another-run", Status: "sent", ObservedAt: startedAt.Add(2 * time.Second)},
	}}}}
	order := &scoreSmokeEventOrder{}
	store.order = order
	backend.order = order

	report, err := RunScoreSmoke(context.Background(), ScoreSmokeRequest{Deadline: deadline, Profile: "grafana"}, ScoreSmokeRunnerDependencies{
		EvidenceStore: &store,
		Backend:       &backend,
		Clock:         newPollerTestClock(startedAt),
		PollInterval:  time.Second,
		IdentityFactory: func(context.Context) (ScoreSmokeIdentity, error) {
			return identity, nil
		},
	})
	if err != nil || report == nil {
		t.Fatalf("RunScoreSmoke() = (%#v, %v), want report-owned foreign projection diagnosis", report, err)
	}
	document := validateScoreSmokeReport(t, report)
	if document.Status != "failed" {
		t.Fatalf("foreign projection report status = %q, want failed", document.Status)
	}
	assertScoreSmokeFailure(t, document.Checks, scoreSmokeFailure{backend: "langfuse_score", stage: "query", class: "unexpected_evidence"})
}

type scoreSmokeFailure struct{ backend, stage, class string }

type scoreSmokeQueryResponse struct {
	observations []ScoreSmokeProjectionObservation
	err          error
}

type fakeScoreSmokeEvidenceStore struct {
	mu        sync.Mutex
	evidence  []ScoreSmokeEvidence
	readCalls int
	readOrder int
	findRunID string
	order     *scoreSmokeEventOrder
}

func (f *fakeScoreSmokeEvidenceStore) Find(_ context.Context, runID string) ([]ScoreSmokeEvidence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readCalls++
	f.readOrder = f.order.nextEvent()
	f.findRunID = runID
	return append([]ScoreSmokeEvidence(nil), f.evidence...), nil
}

func (f *fakeScoreSmokeEvidenceStore) readBeforeBackend(backend scoreSmokeBackendSnapshot) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readOrder > 0 && (backend.firstQueryOrder == 0 || f.readOrder < backend.firstQueryOrder)
}

type scoreSmokeEvidenceStoreSnapshot struct {
	readCalls int
	findRunID string
}

func (f *fakeScoreSmokeEvidenceStore) snapshot() scoreSmokeEvidenceStoreSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return scoreSmokeEvidenceStoreSnapshot{readCalls: f.readCalls, findRunID: f.findRunID}
}

type fakeScoreSmokeBackend struct {
	mu                  sync.Mutex
	responses           []scoreSmokeQueryResponse
	notConfigured       bool
	configurationChecks int
	queryCalls          int
	firstQueryOrder     int
	targets             []ScoreSmokeProjectionTarget
	order               *scoreSmokeEventOrder
}

func (f *fakeScoreSmokeBackend) IsConfigured(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configurationChecks++
	return !f.notConfigured
}

func (f *fakeScoreSmokeBackend) ProjectionStates(ctx context.Context, target ScoreSmokeProjectionTarget) ([]ScoreSmokeProjectionObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("unbounded projection query")
	}
	if gotDeadline, _ := ctx.Deadline(); !gotDeadline.Equal(target.Deadline) {
		return nil, errors.New("projection query deadline does not match target")
	}
	f.queryCalls++
	if f.firstQueryOrder == 0 {
		f.firstQueryOrder = f.order.nextEvent()
	}
	f.targets = append(f.targets, target)
	if len(f.responses) == 0 {
		return nil, nil
	}
	response := f.responses[min(f.queryCalls-1, len(f.responses)-1)]
	return append([]ScoreSmokeProjectionObservation(nil), response.observations...), response.err
}

type scoreSmokeBackendSnapshot struct {
	configurationChecks int
	queryCalls          int
	firstQueryOrder     int
	targets             []ScoreSmokeProjectionTarget
}

func (f *fakeScoreSmokeBackend) snapshot() scoreSmokeBackendSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return scoreSmokeBackendSnapshot{
		configurationChecks: f.configurationChecks,
		queryCalls:          f.queryCalls,
		firstQueryOrder:     f.firstQueryOrder,
		targets:             append([]ScoreSmokeProjectionTarget(nil), f.targets...),
	}
}

type scoreSmokeEventOrder struct {
	mu   sync.Mutex
	next int
}

func (o *scoreSmokeEventOrder) nextEvent() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.next++
	return o.next
}

type scoreSmokeReportDocument struct {
	Status   string         `json:"status"`
	Scenario string         `json:"scenario"`
	Checks   []BackendCheck `json:"checks"`
}

func validateScoreSmokeReport(t *testing.T, report *SmokeReport) scoreSmokeReportDocument {
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
		t.Fatalf("score smoke schema validation error = %v", err)
	}
	var document scoreSmokeReportDocument
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("unmarshal score smoke report = %v", err)
	}
	return document
}

func findScoreSmokeCheck(t *testing.T, checks []BackendCheck, backend string) BackendCheck {
	t.Helper()
	for _, check := range checks {
		if check.Backend == backend {
			return check
		}
	}
	t.Fatalf("score smoke report is missing %q check", backend)
	return BackendCheck{}
}

func assertScoreSmokeFailure(t *testing.T, checks []BackendCheck, want scoreSmokeFailure) {
	t.Helper()
	for _, check := range checks {
		if check.Backend == want.backend && check.Status == "failed" && check.FailureStage == want.stage && check.ErrorClass == want.class {
			return
		}
	}
	t.Fatalf("score smoke checks do not contain the expected stable failure: backend=%q stage=%q class=%q", want.backend, want.stage, want.class)
}
