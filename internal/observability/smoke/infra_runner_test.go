package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TestInfrastructureSmokeRunnerContract 固定 infra smoke 的生产验收边界。Tempo/Loki 证据
// 是异步出现的，不能把首次空结果当作失败；而任何验证失败都必须留下低敏、可被正式 schema
// 校验的报告，才能让 CI 判断失败位置，而不是只得到一个丢失上下文的 process error。
func TestInfrastructureSmokeRunnerContract(t *testing.T) {
	startedAt := time.Now().UTC()
	deadline := startedAt.Add(time.Minute)

	tests := []struct {
		name                 string
		backend              fakeInfrastructureBackend
		wantStatus           string
		wantFailedBackend    string
		wantFailureStage     string
		wantErrorClass       string
		wantTempoQueries     int
		wantLokiQueries      int
		wantHTTPRequestDelta int64
		wantNegativeQueries  bool
	}{
		{
			name: "polls delayed backend observations before the smoke deadline",
			backend: fakeInfrastructureBackend{
				marker: "infra-t064a-marker",
				tempoResponses: []markerQueryResponse{
					{observations: []MarkerObservation{{Marker: "infra-t064a-marker", ObservedAt: startedAt.Add(-time.Second)}}},
					{observations: []MarkerObservation{{Marker: "infra-t064a-marker", ObservedAt: startedAt.Add(time.Second)}}},
				},
				lokiResponses: []markerQueryResponse{
					{observations: []MarkerObservation{{Marker: "infra-t064a-marker", ObservedAt: deadline.Add(time.Second)}}},
					{observations: []MarkerObservation{{Marker: "infra-t064a-marker", ObservedAt: startedAt.Add(2 * time.Second)}}},
				},
				before: 41,
				after:  42,
			},
			wantStatus:           "passed",
			wantTempoQueries:     2,
			wantLokiQueries:      2,
			wantHTTPRequestDelta: 1,
			wantNegativeQueries:  true,
		},
		{
			name: "records a Tempo query failure as stable report evidence",
			backend: fakeInfrastructureBackend{
				marker:         "infra-t064a-marker",
				tempoResponses: []markerQueryResponse{{err: errors.New("synthetic tempo backend failure")}},
				lokiResponses: []markerQueryResponse{{observations: []MarkerObservation{
					{Marker: "infra-t064a-marker", ObservedAt: startedAt.Add(time.Second)},
				}}},
				before: 41,
				after:  42,
			},
			wantStatus:           "failed",
			wantFailedBackend:    "tempo",
			wantFailureStage:     "query",
			wantErrorClass:       "query_failed",
			wantTempoQueries:     1,
			wantLokiQueries:      1,
			wantNegativeQueries:  true,
			wantHTTPRequestDelta: 1,
		},
		{
			name: "records a zero protected HTTP metric delta as stable report evidence",
			backend: fakeInfrastructureBackend{
				marker: "infra-t064a-marker",
				tempoResponses: []markerQueryResponse{{observations: []MarkerObservation{
					{Marker: "infra-t064a-marker", ObservedAt: startedAt.Add(time.Second)},
				}}},
				lokiResponses: []markerQueryResponse{{observations: []MarkerObservation{
					{Marker: "infra-t064a-marker", ObservedAt: startedAt.Add(time.Second)},
				}}},
				before: 41,
				after:  41,
			},
			wantStatus:          "failed",
			wantFailedBackend:   "prometheus",
			wantFailureStage:    "query",
			wantErrorClass:      "metric_delta_missing",
			wantTempoQueries:    1,
			wantLokiQueries:     1,
			wantNegativeQueries: true,
		},
		{
			name: "records nonzero Langfuse evidence without returning a nil report",
			backend: fakeInfrastructureBackend{
				marker: "infra-t064a-marker",
				tempoResponses: []markerQueryResponse{{observations: []MarkerObservation{
					{Marker: "infra-t064a-marker", ObservedAt: startedAt.Add(time.Second)},
				}}},
				lokiResponses: []markerQueryResponse{{observations: []MarkerObservation{
					{Marker: "infra-t064a-marker", ObservedAt: startedAt.Add(time.Second)},
				}}},
				before:          41,
				after:           42,
				langfuseMatches: 1,
			},
			wantStatus:           "failed",
			wantFailedBackend:    "langfuse_trace",
			wantFailureStage:     "query",
			wantErrorClass:       "unexpected_evidence",
			wantTempoQueries:     1,
			wantLokiQueries:      1,
			wantNegativeQueries:  true,
			wantHTTPRequestDelta: 1,
		},
		{
			name: "records nonzero AI plane evidence without returning a nil report",
			backend: fakeInfrastructureBackend{
				marker: "infra-t064a-marker",
				tempoResponses: []markerQueryResponse{{observations: []MarkerObservation{
					{Marker: "infra-t064a-marker", ObservedAt: startedAt.Add(time.Second)},
				}}},
				lokiResponses: []markerQueryResponse{{observations: []MarkerObservation{
					{Marker: "infra-t064a-marker", ObservedAt: startedAt.Add(time.Second)},
				}}},
				before:         41,
				after:          42,
				aiPlaneMatches: 1,
			},
			wantStatus:          "failed",
			wantFailedBackend:   "collector",
			wantFailureStage:    "query",
			wantErrorClass:      "unexpected_evidence",
			wantTempoQueries:    1,
			wantLokiQueries:     1,
			wantNegativeQueries: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := tt.backend
			report, err := RunInfrastructureSmoke(context.Background(), InfrastructureSmokeRequest{
				RunID:     "infra-t064a-run",
				Marker:    backend.marker,
				StartedAt: startedAt,
				Deadline:  deadline,
				Profile:   "grafana",
			}, &backend)
			if err != nil {
				t.Fatalf("RunInfrastructureSmoke() error = %v, want a report-owned verification result", err)
			}
			if report == nil {
				t.Fatal("RunInfrastructureSmoke() report = nil, want schema-valid failure evidence")
			}

			document := validateInfrastructureSmokeReport(t, report)
			if got := document.Status; got != tt.wantStatus {
				t.Fatalf("report status = %q, want %q", got, tt.wantStatus)
			}
			if got := document.Cleanup.Status; got != "not_required" {
				t.Fatalf("cleanup status = %q, want not_required for an infra-only smoke", got)
			}
			if tt.wantFailedBackend != "" {
				assertInfrastructureFailure(t, document.Checks, tt.wantFailedBackend, tt.wantFailureStage, tt.wantErrorClass)
			}
			assertCompleteInfrastructureChecks(t, document.Checks)
			if got := backend.tempoQueries; got != tt.wantTempoQueries {
				t.Fatalf("Tempo query count = %d, want %d", got, tt.wantTempoQueries)
			}
			if got := backend.lokiQueries; got != tt.wantLokiQueries {
				t.Fatalf("Loki query count = %d, want %d", got, tt.wantLokiQueries)
			}
			if tt.wantHTTPRequestDelta != 0 && backend.after-backend.before != tt.wantHTTPRequestDelta {
				t.Fatalf("HTTP metric delta = %d, want %d", backend.after-backend.before, tt.wantHTTPRequestDelta)
			}
			if tt.wantNegativeQueries {
				if got := backend.langfuseQueries; got != 1 {
					t.Fatalf("Langfuse negative query count = %d, want 1", got)
				}
				if got := backend.aiPlaneQueries; got != 1 {
					t.Fatalf("AI-plane negative query count = %d, want 1", got)
				}
			}
			assertSharedSmokeWindow(t, backend.queryTargets, backend.queryDeadlines, startedAt, deadline)
			assertCountQueriesUseSmokeDeadline(t, backend.countDeadlines, deadline)
		})
	}
}

type markerQueryResponse struct {
	observations []MarkerObservation
	err          error
}

type fakeInfrastructureBackend struct {
	marker          string
	tempoResponses  []markerQueryResponse
	lokiResponses   []markerQueryResponse
	before          int64
	after           int64
	aiPlaneMatches  int
	langfuseMatches int

	tempoQueries    int
	lokiQueries     int
	langfuseQueries int
	aiPlaneQueries  int
	queryTargets    []PollMarkerTarget
	queryDeadlines  []time.Time
	countDeadlines  []time.Time
}

func (f *fakeInfrastructureBackend) QueryTempo(ctx context.Context, target PollMarkerTarget) ([]MarkerObservation, error) {
	f.recordQueryBoundary(ctx, target)
	response := f.tempoResponse()
	return response.observations, response.err
}

func (f *fakeInfrastructureBackend) QueryLoki(ctx context.Context, target PollMarkerTarget) ([]MarkerObservation, error) {
	f.recordQueryBoundary(ctx, target)
	response := f.lokiResponse()
	return response.observations, response.err
}

func (f *fakeInfrastructureBackend) HTTPRequestCount(ctx context.Context) (int64, error) {
	f.recordCountBoundary(ctx)
	return f.after, nil
}

func (f *fakeInfrastructureBackend) BaselineHTTPRequestCount(ctx context.Context) (int64, error) {
	f.recordCountBoundary(ctx)
	return f.before, nil
}

func (f *fakeInfrastructureBackend) QueryLangfuse(ctx context.Context, target PollMarkerTarget) (int, error) {
	f.recordQueryBoundary(ctx, target)
	f.langfuseQueries++
	return f.langfuseMatches, nil
}

func (f *fakeInfrastructureBackend) QueryAIPlane(ctx context.Context, target PollMarkerTarget) (int, error) {
	f.recordQueryBoundary(ctx, target)
	f.aiPlaneQueries++
	return f.aiPlaneMatches, nil
}

func (f *fakeInfrastructureBackend) recordQueryBoundary(ctx context.Context, target PollMarkerTarget) {
	f.queryTargets = append(f.queryTargets, target)
	deadline, ok := ctx.Deadline()
	if !ok {
		f.queryDeadlines = append(f.queryDeadlines, time.Time{})
		return
	}
	f.queryDeadlines = append(f.queryDeadlines, deadline)
}

func (f *fakeInfrastructureBackend) recordCountBoundary(ctx context.Context) {
	deadline, ok := ctx.Deadline()
	if !ok {
		f.countDeadlines = append(f.countDeadlines, time.Time{})
		return
	}
	f.countDeadlines = append(f.countDeadlines, deadline)
}

func (f *fakeInfrastructureBackend) tempoResponse() markerQueryResponse {
	f.tempoQueries++
	return nextMarkerQueryResponse(f.tempoResponses, f.tempoQueries)
}

func (f *fakeInfrastructureBackend) lokiResponse() markerQueryResponse {
	f.lokiQueries++
	return nextMarkerQueryResponse(f.lokiResponses, f.lokiQueries)
}

func nextMarkerQueryResponse(responses []markerQueryResponse, queryNumber int) markerQueryResponse {
	if len(responses) == 0 {
		return markerQueryResponse{}
	}
	index := min(queryNumber-1, len(responses)-1)
	return responses[index]
}

type infrastructureSmokeReportDocument struct {
	Status  string         `json:"status"`
	Checks  []BackendCheck `json:"checks"`
	Cleanup SmokeCleanup   `json:"cleanup"`
}

func validateInfrastructureSmokeReport(t *testing.T, report *SmokeReport) infrastructureSmokeReportDocument {
	t.Helper()
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("MarshalJSON() error = %v", err)
	}
	validator, err := NewSmokeReportSchemaValidator(loadSmokeReportSchema(t))
	if err != nil {
		t.Fatalf("NewSmokeReportSchemaValidator() error = %v", err)
	}
	if err := validator.ValidateJSON(encoded); err != nil {
		t.Fatalf("version-controlled schema validation error = %v", err)
	}
	var document infrastructureSmokeReportDocument
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("UnmarshalJSON() error = %v", err)
	}
	return document
}

func assertInfrastructureFailure(t *testing.T, checks []BackendCheck, backend, stage, errorClass string) {
	t.Helper()
	for _, check := range checks {
		if check.Backend == backend && check.Status == "failed" && check.FailureStage == stage && check.ErrorClass == errorClass {
			return
		}
	}
	t.Fatalf("checks = %#v, want failed %s check with failure_stage=%q and error_class=%q", checks, backend, stage, errorClass)
}

func assertCompleteInfrastructureChecks(t *testing.T, checks []BackendCheck) {
	t.Helper()
	for _, backend := range []string{"tempo", "loki", "prometheus", "langfuse_trace", "collector"} {
		for _, check := range checks {
			if check.Backend == backend {
				goto nextBackend
			}
		}
		t.Fatalf("checks = %#v, want a low-sensitivity %s check", checks, backend)
	nextBackend:
	}
}

func assertSharedSmokeWindow(t *testing.T, targets []PollMarkerTarget, deadlines []time.Time, startedAt, deadline time.Time) {
	t.Helper()
	if len(targets) != len(deadlines) || len(targets) == 0 {
		t.Fatalf("query boundaries = targets:%d deadlines:%d, want one bounded boundary per backend query", len(targets), len(deadlines))
	}
	for index, target := range targets {
		if !target.StartedAt.Equal(startedAt) || !target.Deadline.Equal(deadline) {
			t.Fatalf("query target %d = %#v, want shared smoke window", index, target)
		}
		if !deadlines[index].Equal(deadline) {
			t.Fatalf("query context deadline %d = %s, want %s", index, deadlines[index], deadline)
		}
	}
}

func assertCountQueriesUseSmokeDeadline(t *testing.T, deadlines []time.Time, want time.Time) {
	t.Helper()
	if len(deadlines) != 2 {
		t.Fatalf("Prometheus count query calls = %d, want baseline and after queries", len(deadlines))
	}
	for index, got := range deadlines {
		if !got.Equal(want) {
			t.Fatalf("Prometheus count context deadline %d = %s, want %s", index, got, want)
		}
	}
}
