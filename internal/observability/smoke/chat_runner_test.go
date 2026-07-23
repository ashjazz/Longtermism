package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestChatSmokeRunnerContract protects the first real business-observability loop. A chat smoke
// must correlate one API response with both infrastructure facts and AI-plane facts inside the
// same 60-second window. The fakes keep this Level 0 contract offline: a passing unit test must
// never be mistaken for evidence that a real model or backend was contacted.
func TestChatSmokeRunnerContract(t *testing.T) {
	startedAt := time.Date(2026, time.July, 23, 10, 0, 0, 0, time.UTC)
	deadline := startedAt.Add(time.Minute)
	identity := ChatSmokeIdentity{RunID: "chat-run-t085", Marker: "chat-marker-t085"}
	response := ChatSmokeAPIResult{
		RequestID:      "request-t085",
		AITraceID:      "ai-trace-t085",
		ServiceTraceID: "service-trace-t085",
		SpanID:         "span-t085",
	}

	tests := []struct {
		name             string
		triggerErr       error
		backend          fakeChatSmokeBackend
		wantReportStatus string
		wantRunnerErr    error
		wantFailure      chatSmokeFailure
		wantMetricDelta  int64
		wantTempoQueries int
	}{
		{
			name: "correlates delayed infrastructure and AI facts within sixty seconds",
			backend: fakeChatSmokeBackend{
				tempoResponses: []chatSmokeQueryResponse{
					{observations: []ChatObservation{{Marker: identity.Marker, RequestID: response.RequestID, AITraceID: response.AITraceID, ServiceTraceID: response.ServiceTraceID, SpanID: response.SpanID, ObservedAt: startedAt.Add(-time.Second)}}},
					{observations: []ChatObservation{{Marker: identity.Marker, RequestID: response.RequestID, AITraceID: response.AITraceID, ServiceTraceID: response.ServiceTraceID, SpanID: response.SpanID, ObservedAt: startedAt.Add(time.Second)}}},
				},
				lokiResponses:       []chatSmokeQueryResponse{{observations: []ChatObservation{{Marker: identity.Marker, RequestID: response.RequestID, AITraceID: response.AITraceID, ServiceTraceID: response.ServiceTraceID, SpanID: response.SpanID, ObservedAt: startedAt.Add(2 * time.Second)}}}},
				langfuseResponses:   []chatSmokeQueryResponse{{observations: []ChatObservation{{Marker: identity.Marker, RequestID: response.RequestID, AITraceID: response.AITraceID, ServiceTraceID: response.ServiceTraceID, SpanID: response.SpanID, ObservedAt: startedAt.Add(3 * time.Second)}}}},
				baselineLLMRequests: 41,
				llmRequests:         42,
			},
			wantReportStatus: "passed",
			wantMetricDelta:  1,
			wantTempoQueries: 2,
		},
		{
			name: "records telemetry failure in the report without rewriting a successful model result",
			backend: fakeChatSmokeBackend{
				tempoResponses:      []chatSmokeQueryResponse{{err: errors.New("synthetic telemetry outage")}},
				baselineLLMRequests: 41,
				llmRequests:         42,
			},
			wantReportStatus: "failed",
			wantFailure:      chatSmokeFailure{backend: "tempo", stage: "query", class: "query_failed"},
			wantMetricDelta:  1,
			wantTempoQueries: 61,
		},
		{
			name: "rejects a marker hit when the AI identity belongs to another chat",
			backend: fakeChatSmokeBackend{
				tempoResponses:      []chatSmokeQueryResponse{{observations: []ChatObservation{{Marker: identity.Marker, RequestID: response.RequestID, AITraceID: "ai-trace-other", ServiceTraceID: response.ServiceTraceID, SpanID: response.SpanID, ObservedAt: startedAt.Add(time.Second)}}}},
				baselineLLMRequests: 41,
				llmRequests:         42,
			},
			wantReportStatus: "failed",
			wantFailure:      chatSmokeFailure{backend: "tempo", stage: "query", class: "identity_mismatch"},
			wantMetricDelta:  1,
			wantTempoQueries: 61,
		},
		{
			name:             "preserves the original model failure while returning a schema-valid API failure report",
			triggerErr:       errSyntheticModelFailure,
			backend:          fakeChatSmokeBackend{},
			wantReportStatus: "failed",
			wantRunnerErr:    errSyntheticModelFailure,
			wantFailure:      chatSmokeFailure{backend: "api", stage: "api", class: "backend_unavailable"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := tt.backend
			clock := newPollerTestClock(startedAt)
			triggerCalls := 0
			report, err := RunChatSmoke(context.Background(), ChatSmokeRequest{Deadline: deadline, Profile: "grafana"}, ChatSmokeRunnerDependencies{
				Backend:      &backend,
				Clock:        clock,
				PollInterval: time.Second,
				IdentityFactory: func(context.Context) (ChatSmokeIdentity, error) {
					return identity, nil
				},
				Trigger: func(ctx context.Context, got ChatSmokeIdentity) (ChatSmokeAPIResult, error) {
					triggerCalls++
					if got != identity {
						t.Fatal("chat trigger did not receive the runner-owned marker identity")
					}
					if gotDeadline, ok := ctx.Deadline(); !ok || !gotDeadline.Equal(deadline) {
						t.Fatal("chat trigger did not receive the 60-second smoke deadline")
					}
					return response, tt.triggerErr
				},
			})
			if !errors.Is(err, tt.wantRunnerErr) {
				t.Fatalf("RunChatSmoke() error = %v, want errors.Is(_, %v) = true", err, tt.wantRunnerErr)
			}
			if report == nil {
				t.Fatal("RunChatSmoke() report = nil, want a schema-valid report for every completed API attempt")
			}
			if triggerCalls != 1 {
				t.Fatalf("chat trigger calls = %d, want 1", triggerCalls)
			}

			document := validateChatSmokeReport(t, report)
			if document.Status != tt.wantReportStatus || document.Scenario != "chat" || document.RequestID != response.RequestID || document.AITraceID != response.AITraceID {
				t.Fatalf("chat smoke report identity/status = %#v, want schema-valid %q report correlated to the API response", document, tt.wantReportStatus)
			}
			if tt.wantFailure.backend != "" {
				assertChatSmokeFailure(t, document.Checks, tt.wantFailure)
			}
			if tt.wantRunnerErr == nil {
				assertCompleteChatSmokeChecks(t, document.Checks)
				assertChatSmokeTargets(t, backend.queryTargets, identity, response, startedAt, deadline)
				if got := backend.llmRequests - backend.baselineLLMRequests; got != tt.wantMetricDelta {
					t.Fatalf("LLM request metric delta = %d, want %d", got, tt.wantMetricDelta)
				}
			}
			if got := backend.tempoQueries; got != tt.wantTempoQueries {
				t.Fatalf("Tempo query calls = %d, want %d", got, tt.wantTempoQueries)
			}
		})
	}
}

var errSyntheticModelFailure = errors.New("synthetic model failure")

type chatSmokeFailure struct{ backend, stage, class string }

type chatSmokeQueryResponse struct {
	observations []ChatObservation
	err          error
}

type fakeChatSmokeBackend struct {
	mu                  sync.Mutex
	tempoResponses      []chatSmokeQueryResponse
	lokiResponses       []chatSmokeQueryResponse
	langfuseResponses   []chatSmokeQueryResponse
	baselineLLMRequests int64
	llmRequests         int64
	tempoQueries        int
	lokiQueries         int
	langfuseQueries     int
	queryTargets        []ChatSmokeTarget
}

func (f *fakeChatSmokeBackend) QueryTempoChat(ctx context.Context, target ChatSmokeTarget) ([]ChatObservation, error) {
	return f.query(ctx, target, f.tempoResponses, &f.tempoQueries)
}

func (f *fakeChatSmokeBackend) QueryLokiChat(ctx context.Context, target ChatSmokeTarget) ([]ChatObservation, error) {
	return f.query(ctx, target, f.lokiResponses, &f.lokiQueries)
}

func (f *fakeChatSmokeBackend) QueryLangfuseChat(ctx context.Context, target ChatSmokeTarget) ([]ChatObservation, error) {
	return f.query(ctx, target, f.langfuseResponses, &f.langfuseQueries)
}

func (f *fakeChatSmokeBackend) BaselineLLMRequestCount(context.Context) (int64, error) {
	return f.baselineLLMRequests, nil
}

func (f *fakeChatSmokeBackend) LLMRequestCount(context.Context) (int64, error) {
	return f.llmRequests, nil
}

func (f *fakeChatSmokeBackend) query(ctx context.Context, target ChatSmokeTarget, responses []chatSmokeQueryResponse, queryCount *int) ([]ChatObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("unbounded backend query")
	}
	*queryCount++
	f.queryTargets = append(f.queryTargets, target)
	if len(responses) == 0 {
		return nil, nil
	}
	response := responses[min(*queryCount-1, len(responses)-1)]
	return response.observations, response.err
}

type chatSmokeReportDocument struct {
	Status    string         `json:"status"`
	Scenario  string         `json:"scenario"`
	RequestID string         `json:"request_id"`
	AITraceID string         `json:"ai_trace_id"`
	Checks    []BackendCheck `json:"checks"`
}

func validateChatSmokeReport(t *testing.T, report *SmokeReport) chatSmokeReportDocument {
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
		t.Fatalf("chat smoke schema validation error = %v", err)
	}
	var document chatSmokeReportDocument
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("unmarshal chat smoke report = %v", err)
	}
	return document
}

func assertChatSmokeFailure(t *testing.T, checks []BackendCheck, want chatSmokeFailure) {
	t.Helper()
	for _, check := range checks {
		if check.Backend == want.backend && check.Status == "failed" && check.FailureStage == want.stage && check.ErrorClass == want.class {
			return
		}
	}
	t.Fatalf("chat smoke checks do not contain the expected stable failure: backend=%q stage=%q class=%q", want.backend, want.stage, want.class)
}

func assertCompleteChatSmokeChecks(t *testing.T, checks []BackendCheck) {
	t.Helper()
	for _, backend := range []string{"api", "tempo", "loki", "prometheus", "langfuse_trace"} {
		for _, check := range checks {
			if check.Backend == backend {
				goto nextBackend
			}
		}
		t.Fatalf("chat smoke report is missing the %q backend check", backend)
	nextBackend:
	}
}

func assertChatSmokeTargets(t *testing.T, targets []ChatSmokeTarget, run ChatSmokeIdentity, response ChatSmokeAPIResult, startedAt, deadline time.Time) {
	t.Helper()
	for index, target := range targets {
		if target.Marker != run.Marker || target.RequestID != response.RequestID || target.AITraceID != response.AITraceID || target.ServiceTraceID != response.ServiceTraceID || target.SpanID != response.SpanID || !target.StartedAt.Equal(startedAt) || !target.Deadline.Equal(deadline) {
			t.Fatalf("backend query target %d does not match the API response identity and smoke window", index)
		}
	}
}
