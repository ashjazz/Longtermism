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
		wantLangfuseQueries  int
		wantAIPlaneQueries   int
		forbiddenReportText  string
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
			wantLangfuseQueries:  2,
			wantAIPlaneQueries:   2,
		},
		{
			name: "recovers after a transient Tempo query failure",
			backend: fakeInfrastructureBackend{
				marker: "infra-t172-tempo",
				tempoResponses: []markerQueryResponse{
					{err: classifiedInfrastructureQueryError{class: "backend_unavailable", raw: "raw-t172-tempo-response"}},
					{observations: []MarkerObservation{{Marker: "infra-t172-tempo", ObservedAt: startedAt.Add(time.Second)}}},
				},
				lokiResponses: []markerQueryResponse{{observations: []MarkerObservation{{Marker: "infra-t172-tempo", ObservedAt: startedAt.Add(time.Second)}}}},
				before:        41, after: 42,
			},
			wantStatus: "passed", wantTempoQueries: 2, wantLokiQueries: 1,
			wantLangfuseQueries: 2, wantAIPlaneQueries: 2, wantHTTPRequestDelta: 1,
			forbiddenReportText: "raw-t172-tempo-response",
		},
		{
			name: "recovers after a transient Loki query failure",
			backend: fakeInfrastructureBackend{
				marker:         "infra-t172-loki",
				tempoResponses: []markerQueryResponse{{observations: []MarkerObservation{{Marker: "infra-t172-loki", ObservedAt: startedAt.Add(time.Second)}}}},
				lokiResponses: []markerQueryResponse{
					{err: classifiedInfrastructureQueryError{class: "backend_unavailable", raw: "raw-t172-loki-response"}},
					{observations: []MarkerObservation{{Marker: "infra-t172-loki", ObservedAt: startedAt.Add(time.Second)}}},
				},
				before: 41, after: 42,
			},
			wantStatus: "passed", wantTempoQueries: 1, wantLokiQueries: 2,
			wantLangfuseQueries: 2, wantAIPlaneQueries: 2, wantHTTPRequestDelta: 1,
			forbiddenReportText: "raw-t172-loki-response",
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
				after:  42,
			},
			wantStatus:          "passed",
			wantTempoQueries:    1,
			wantLokiQueries:     1,
			wantLangfuseQueries: 2,
			wantAIPlaneQueries:  2,
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
			wantLangfuseQueries:  1,
			wantAIPlaneQueries:   2,
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
			wantLangfuseQueries: 2,
			wantAIPlaneQueries:  1,
		},
		{
			name: "fails when forbidden AI plane evidence appears during the stable window",
			backend: fakeInfrastructureBackend{
				marker: "infra-t064a-marker",
				tempoResponses: []markerQueryResponse{{observations: []MarkerObservation{
					{Marker: "infra-t064a-marker", ObservedAt: startedAt.Add(time.Second)},
				}}},
				lokiResponses: []markerQueryResponse{{observations: []MarkerObservation{
					{Marker: "infra-t064a-marker", ObservedAt: startedAt.Add(time.Second)},
				}}},
				before: 41,
				after:  42,
				aiPlaneResponses: []negativeQueryResponse{
					{count: 0},
					{count: 1},
				},
			},
			wantStatus:           "failed",
			wantFailedBackend:    "collector",
			wantFailureStage:     "query",
			wantErrorClass:       "unexpected_evidence",
			wantTempoQueries:     1,
			wantLokiQueries:      1,
			wantLangfuseQueries:  2,
			wantAIPlaneQueries:   2,
			wantHTTPRequestDelta: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := tt.backend
			clock := newPollerTestClock(startedAt)
			identity := InfrastructureSmokeIdentity{RunID: backend.marker, Marker: backend.marker}
			triggerCalls := 0
			report, err := RunInfrastructureSmoke(context.Background(), InfrastructureSmokeRequest{Deadline: deadline, Profile: "grafana"}, InfrastructureSmokeRunnerDependencies{
				Backend: &backend, Clock: clock, PollInterval: time.Second,
				IdentityFactory: func(context.Context) (InfrastructureSmokeIdentity, error) { return identity, nil },
				Trigger: func(ctx context.Context, got InfrastructureSmokeIdentity) error {
					triggerCalls++
					if got != identity {
						t.Fatal("trigger did not receive runner-owned identity")
					}
					if gotDeadline, ok := ctx.Deadline(); !ok || !gotDeadline.Equal(deadline) {
						t.Fatal("trigger did not receive smoke deadline")
					}
					return nil
				},
			})
			if err != nil {
				t.Fatalf("RunInfrastructureSmoke() error = %v, want a report-owned verification result", err)
			}
			if report == nil {
				t.Fatal("RunInfrastructureSmoke() report = nil, want schema-valid failure evidence")
			}
			if triggerCalls != 1 {
				t.Fatalf("protected request calls = %d, want 1", triggerCalls)
			}

			document := validateInfrastructureSmokeReport(t, report)
			if got := document.Status; got != tt.wantStatus {
				t.Fatalf("report status = %q, want %q", got, tt.wantStatus)
			}
			if got := document.Cleanup.Status; got != "not_required" {
				t.Fatalf("cleanup status = %q, want not_required for an infra-only smoke", got)
			}
			if tt.forbiddenReportText != "" {
				encoded, marshalErr := json.Marshal(report)
				if marshalErr != nil {
					t.Fatalf("MarshalJSON() error = %v", marshalErr)
				}
				if strings.Contains(string(encoded), tt.forbiddenReportText) {
					t.Fatal("schema-valid smoke report leaked a raw backend response")
				}
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
			if got := backend.langfuseQueries; got != tt.wantLangfuseQueries {
				t.Fatalf("Langfuse negative query count = %d, want %d", got, tt.wantLangfuseQueries)
			}
			if got := backend.aiPlaneQueries; got != tt.wantAIPlaneQueries {
				t.Fatalf("AI-plane negative query count = %d, want %d", got, tt.wantAIPlaneQueries)
			}
			assertSharedSmokeWindow(t, backend.queryTargets, backend.queryDeadlines, startedAt, deadline)
			assertCountQueriesUseSmokeDeadline(t, backend.countDeadlines, deadline)
		})
	}
}

// 后端短暂不可用可以重试，但只有完整窗口耗尽后才能失败；届时保留的只能是 adapter
// 给出的有限安全分类，不能把 raw response 包装进 runner/report 错误链。
func TestInfrastructureMarkerPollingPreservesSafeFailureClassAtDeadline(t *testing.T) {
	startedAt := time.Date(2026, time.August, 12, 1, 2, 3, 0, time.UTC)
	clock := newPollerTestClock(startedAt)
	target := PollMarkerTarget{Marker: "infra-t172-persistent", StartedAt: startedAt, Deadline: startedAt.Add(2 * time.Second)}
	queryCalls := 0

	_, err := NewBoundedMarkerPoller(clock, time.Second).WaitForMarker(context.Background(), target, func(context.Context, PollMarkerTarget) ([]MarkerObservation, error) {
		queryCalls++
		return nil, classifiedInfrastructureQueryError{class: "backend_unavailable", raw: "raw-t172-persistent-response"}
	})
	if queryCalls != 3 || !clock.Now().Equal(target.Deadline) {
		t.Fatalf("polling boundary = calls:%d now:%s, want three inclusive queries through %s", queryCalls, clock.Now(), target.Deadline)
	}
	var classified interface{ Class() string }
	if !errors.As(err, &classified) || classified.Class() != "backend_unavailable" {
		t.Fatalf("polling error class = %v, want backend_unavailable only after deadline", err)
	}
	if strings.Contains(err.Error(), "raw-t172-persistent-response") {
		t.Fatal("polling error leaked the raw backend response")
	}
}

// A missing counter sample is a failed acceptance fact, not an exporter timeout. This distinction
// tells an operator whether Prometheus was reachable but did not observe the protected route.
func TestWaitForHTTPRequestIncreaseReturnsLastCountWhenDeadlineHasNoDelta(t *testing.T) {
	startedAt := time.Now().UTC()
	backend := &fakeInfrastructureBackend{before: 41, after: 41}
	count, err := waitForHTTPRequestIncrease(context.Background(), backend, 41, startedAt.Add(2*time.Second), newPollerTestClock(startedAt), time.Second)
	if err != nil {
		t.Fatalf("waitForHTTPRequestIncrease() error = %v, want a reportable zero delta", err)
	}
	if count != 41 {
		t.Fatalf("waitForHTTPRequestIncrease() count = %d, want last observed baseline", count)
	}
}

type markerQueryResponse struct {
	observations []MarkerObservation
	err          error
}

// classifiedInfrastructureQueryError 模拟 adapter 已完成脱敏后的有限错误分类；raw 仅用于
// 证明 runner/report 边界不会把平台响应原文持久化。
type classifiedInfrastructureQueryError struct {
	class string
	raw   string
}

func (e classifiedInfrastructureQueryError) Error() string { return e.raw }
func (e classifiedInfrastructureQueryError) Class() string { return e.class }

type negativeQueryResponse struct {
	count int
	err   error
}

type fakeInfrastructureBackend struct {
	mu                sync.Mutex
	marker            string
	tempoResponses    []markerQueryResponse
	lokiResponses     []markerQueryResponse
	before            int64
	after             int64
	aiPlaneMatches    int
	langfuseMatches   int
	aiPlaneResponses  []negativeQueryResponse
	langfuseResponses []negativeQueryResponse

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
	f.mu.Lock()
	defer f.mu.Unlock()
	f.langfuseQueries++
	return nextNegativeQueryResponse(f.langfuseResponses, f.langfuseQueries, f.langfuseMatches)
}

func (f *fakeInfrastructureBackend) QueryAIPlane(ctx context.Context, target PollMarkerTarget) (int, error) {
	f.recordQueryBoundary(ctx, target)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aiPlaneQueries++
	return nextNegativeQueryResponse(f.aiPlaneResponses, f.aiPlaneQueries, f.aiPlaneMatches)
}

func nextNegativeQueryResponse(responses []negativeQueryResponse, queryNumber, fallback int) (int, error) {
	if len(responses) == 0 {
		return fallback, nil
	}
	response := responses[min(queryNumber-1, len(responses)-1)]
	return response.count, response.err
}

func (f *fakeInfrastructureBackend) recordQueryBoundary(ctx context.Context, target PollMarkerTarget) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queryTargets = append(f.queryTargets, target)
	deadline, ok := ctx.Deadline()
	if !ok {
		f.queryDeadlines = append(f.queryDeadlines, time.Time{})
		return
	}
	f.queryDeadlines = append(f.queryDeadlines, deadline)
}

func (f *fakeInfrastructureBackend) recordCountBoundary(ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	deadline, ok := ctx.Deadline()
	if !ok {
		f.countDeadlines = append(f.countDeadlines, time.Time{})
		return
	}
	f.countDeadlines = append(f.countDeadlines, deadline)
}

func (f *fakeInfrastructureBackend) tempoResponse() markerQueryResponse {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tempoQueries++
	return nextMarkerQueryResponse(f.tempoResponses, f.tempoQueries)
}

func (f *fakeInfrastructureBackend) lokiResponse() markerQueryResponse {
	f.mu.Lock()
	defer f.mu.Unlock()
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
		if deadlines[index].IsZero() || deadlines[index].After(deadline) {
			t.Fatalf("query context deadline %d = %s, want a deadline no later than %s", index, deadlines[index], deadline)
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
