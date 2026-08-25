package smoke

// T139：SigNoz 备选 profile E2E runner 的 RED 契约测试。
// 实现前本文件必须编译失败（RunSignozInfrastructureSmoke 等符号不存在）；
// T144 在 signoz_runner.go 落地实现后本文件转 GREEN。
//
// 契约沿用 Grafana 主线 runner 的全部安全语义（infra_runner.go / chat_runner.go）：
//   1. 离线：测试只使用 fake query clients 与 fake clock，绝不启动 Docker 或连接后端；
//      真实 Make E2E 必须以查询闭环验收，禁止用 compose healthy 代替查询。
//   2. identity 由 runner 派生与校验：marker 从 run ID 派生，调用方不能重放任意 marker；
//      chat observation 必须逐字段匹配 request/trace identity（不放宽主线断言）。
//   3. 验证失败保留在 schema-valid 的低敏 report 中，错误分类有限、raw 响应不落盘。
//   4. 所有查询共享本次 run 的限定窗口（StartedAt/Deadline），context deadline 不晚于它。
// 与主线的差异只在后端面：三信号来自 SigNoz（signoz_traces/signoz_logs/signoz_metrics），
// AI 平面仍查询 Langfuse（langfuse_trace），并新增 chat 场景的 langfuse_score 投影检查。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── fake SigNoz infra backend：脚本化响应 + 查询边界记录 ─────────────────────

type fakeSignozInfrastructureBackend struct {
	mu sync.Mutex

	tracesResponses []markerQueryResponse
	logsResponses   []markerQueryResponse
	before          int64
	after           int64
	langfuseMatches int
	aiPlaneMatches  int

	tracesQueries   int
	logsQueries     int
	langfuseQueries int
	aiPlaneQueries  int
	queryTargets    []PollMarkerTarget
	queryDeadlines  []time.Time
	countDeadlines  []time.Time
}

func (f *fakeSignozInfrastructureBackend) QuerySignozTraces(ctx context.Context, target PollMarkerTarget) ([]MarkerObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordQueryBoundaryLocked(ctx, target)
	f.tracesQueries++
	response := nextMarkerQueryResponse(f.tracesResponses, f.tracesQueries)
	return response.observations, response.err
}

func (f *fakeSignozInfrastructureBackend) QuerySignozLogs(ctx context.Context, target PollMarkerTarget) ([]MarkerObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordQueryBoundaryLocked(ctx, target)
	f.logsQueries++
	response := nextMarkerQueryResponse(f.logsResponses, f.logsQueries)
	return response.observations, response.err
}

func (f *fakeSignozInfrastructureBackend) BaselineHTTPRequestCount(ctx context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCountBoundaryLocked(ctx)
	return f.before, nil
}

func (f *fakeSignozInfrastructureBackend) HTTPRequestCount(ctx context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCountBoundaryLocked(ctx)
	return f.after, nil
}

func (f *fakeSignozInfrastructureBackend) QueryLangfuse(ctx context.Context, target PollMarkerTarget) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordQueryBoundaryLocked(ctx, target)
	f.langfuseQueries++
	return nextNegativeQueryResponse(nil, f.langfuseQueries, f.langfuseMatches)
}

func (f *fakeSignozInfrastructureBackend) QueryAIPlane(ctx context.Context, target PollMarkerTarget) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordQueryBoundaryLocked(ctx, target)
	f.aiPlaneQueries++
	return nextNegativeQueryResponse(nil, f.aiPlaneQueries, f.aiPlaneMatches)
}

func (f *fakeSignozInfrastructureBackend) recordQueryBoundaryLocked(ctx context.Context, target PollMarkerTarget) {
	f.queryTargets = append(f.queryTargets, target)
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Time{}
	}
	f.queryDeadlines = append(f.queryDeadlines, deadline)
}

func (f *fakeSignozInfrastructureBackend) recordCountBoundaryLocked(ctx context.Context) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Time{}
	}
	f.countDeadlines = append(f.countDeadlines, deadline)
}

// ── fake SigNoz chat backend ────────────────────────────────────────────────

type chatObservationResponse struct {
	observations []ChatObservation
	err          error
}

type fakeSignozChatBackend struct {
	mu sync.Mutex

	tracesResponses        []chatObservationResponse
	logsResponses          []chatObservationResponse
	langfuseTraceResponses []chatObservationResponse
	langfuseScores         int
	before                 int64
	after                  int64

	tracesQueries        int
	logsQueries          int
	langfuseTraceQueries int
	scoreQueries         int
	queryTargets         []ChatSmokeTarget
	queryDeadlines       []time.Time
}

func (f *fakeSignozChatBackend) QuerySignozTracesChat(ctx context.Context, target ChatSmokeTarget) ([]ChatObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordBoundaryLocked(ctx, target)
	f.tracesQueries++
	response := nextChatObservationResponse(f.tracesResponses, f.tracesQueries)
	return response.observations, response.err
}

func (f *fakeSignozChatBackend) QuerySignozLogsChat(ctx context.Context, target ChatSmokeTarget) ([]ChatObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordBoundaryLocked(ctx, target)
	f.logsQueries++
	response := nextChatObservationResponse(f.logsResponses, f.logsQueries)
	return response.observations, response.err
}

func (f *fakeSignozChatBackend) QueryLangfuseChat(ctx context.Context, target ChatSmokeTarget) ([]ChatObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordBoundaryLocked(ctx, target)
	f.langfuseTraceQueries++
	response := nextChatObservationResponse(f.langfuseTraceResponses, f.langfuseTraceQueries)
	return response.observations, response.err
}

func (f *fakeSignozChatBackend) QueryLangfuseScore(ctx context.Context, target ChatSmokeTarget) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordBoundaryLocked(ctx, target)
	f.scoreQueries++
	return f.langfuseScores, nil
}

func (f *fakeSignozChatBackend) BaselineLLMRequestCount(ctx context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.before, nil
}

func (f *fakeSignozChatBackend) LLMRequestCount(ctx context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.after, nil
}

func (f *fakeSignozChatBackend) recordBoundaryLocked(ctx context.Context, target ChatSmokeTarget) {
	f.queryTargets = append(f.queryTargets, target)
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Time{}
	}
	f.queryDeadlines = append(f.queryDeadlines, deadline)
}

func nextChatObservationResponse(responses []chatObservationResponse, queryNumber int) chatObservationResponse {
	if len(responses) == 0 {
		return chatObservationResponse{}
	}
	return responses[min(queryNumber-1, len(responses)-1)]
}

// TestSignozInfrastructureSmokeRunnerContract 固定备选 profile 的 infra 验收边界：
// SigNoz 三信号正向证据 + AI 平面负向证据（AI negative），异步投递允许轮询，
// 任何失败保留在 schema-valid 低敏 report 中，raw 响应不得进入报告。
func TestSignozInfrastructureSmokeRunnerContract(t *testing.T) {
	startedAt := time.Now().UTC()
	deadline := startedAt.Add(time.Minute)

	tests := []struct {
		name                string
		backend             *fakeSignozInfrastructureBackend
		runID               string
		wantStatus          string
		wantFailedBackend   string
		wantFailureStage    string
		wantErrorClass      string
		wantTracesQueries   int
		wantLogsQueries     int
		forbiddenReportText string
	}{
		{
			name: "polls delayed signoz observations before the smoke deadline",
			backend: &fakeSignozInfrastructureBackend{
				tracesResponses: []markerQueryResponse{
					{},
					{observations: []MarkerObservation{{Marker: "run-signoz-infra-ok", ObservedAt: startedAt.Add(time.Second)}}},
				},
				logsResponses: []markerQueryResponse{
					{},
					{observations: []MarkerObservation{{Marker: "run-signoz-infra-ok", ObservedAt: startedAt.Add(time.Second)}}},
				},
				before: 7,
				after:  9,
			},
			runID:             "run-signoz-infra-ok",
			wantStatus:        "passed",
			wantTracesQueries: 2,
			wantLogsQueries:   2,
		},
		{
			name: "recovers after a transient signoz trace query failure without leaking raw response",
			backend: &fakeSignozInfrastructureBackend{
				tracesResponses: []markerQueryResponse{
					{err: classifiedInfrastructureQueryError{class: "backend_unavailable", raw: "raw-signoz-trace-response"}},
					{observations: []MarkerObservation{{Marker: "run-signoz-infra-flaky", ObservedAt: startedAt.Add(time.Second)}}},
				},
				logsResponses: []markerQueryResponse{
					{observations: []MarkerObservation{{Marker: "run-signoz-infra-flaky", ObservedAt: startedAt.Add(time.Second)}}},
				},
				before: 7,
				after:  8,
			},
			runID:               "run-signoz-infra-flaky",
			wantStatus:          "passed",
			wantTracesQueries:   2,
			wantLogsQueries:     1,
			forbiddenReportText: "raw-signoz-trace-response",
		},
		{
			name: "records nonzero langfuse evidence as an AI-plane leak failure",
			backend: &fakeSignozInfrastructureBackend{
				tracesResponses: []markerQueryResponse{
					{observations: []MarkerObservation{{Marker: "run-signoz-infra-leak", ObservedAt: startedAt.Add(time.Second)}}},
				},
				logsResponses: []markerQueryResponse{
					{observations: []MarkerObservation{{Marker: "run-signoz-infra-leak", ObservedAt: startedAt.Add(time.Second)}}},
				},
				before:          7,
				after:           8,
				langfuseMatches: 1,
			},
			runID:             "run-signoz-infra-leak",
			wantStatus:        "failed",
			wantFailedBackend: "langfuse_trace",
			wantFailureStage:  "query",
			wantErrorClass:    "unexpected_evidence",
			wantTracesQueries: 1,
			wantLogsQueries:   1,
		},
		{
			name: "skips AI negative checks when infra signals never arrive",
			backend: &fakeSignozInfrastructureBackend{
				before: 7,
				after:  8,
			},
			runID: "run-signoz-infra-missing",
			// 窗口耗尽时每个信号完成一整段轮询（fake clock 每 interval 推进一次直到
			// deadline），有过成功空查询的信号归类为 marker_missing 而非 backend_timeout。
			wantStatus:        "failed",
			wantFailedBackend: "signoz_traces",
			wantFailureStage:  "query",
			wantErrorClass:    "marker_missing",
			// 并发 poller 共享同一个 fake clock：谁先推进 clock 谁拿走剩余轮询机会，
			// 计数依赖调度顺序，这里只断言类别与状态，不断言精确查询数。
			wantTracesQueries: -1,
			wantLogsQueries:   -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := tt.backend
			clock := newPollerTestClock(startedAt)
			report, err := RunSignozInfrastructureSmoke(context.Background(), SignozInfrastructureSmokeRequest{Deadline: deadline, Profile: "signoz"}, SignozInfrastructureSmokeRunnerDependencies{
				Backend:      backend,
				Clock:        clock,
				PollInterval: time.Second,
				// Marker 故意与 runID 不同：runner 必须从 runID 派生 marker，
				// 防止调用方或测试替身独立重放任意 marker（与主线 infra runner 同一条边界）。
				IdentityFactory: func(context.Context) (SignozSmokeIdentity, error) {
					return SignozSmokeIdentity{RunID: tt.runID, Marker: "arbitrary-caller-marker"}, nil
				},
				Trigger: func(ctx context.Context, identity SignozSmokeIdentity) error {
					if bounded, ok := ctx.Deadline(); !ok || bounded.IsZero() {
						t.Fatal("trigger did not receive a bounded deadline")
					}
					if identity.Marker == "arbitrary-caller-marker" {
						t.Fatal("trigger received caller-provided marker instead of runner-derived marker")
					}
					return nil
				},
			})
			if err != nil {
				t.Fatalf("RunSignozInfrastructureSmoke() error = %v, want nil (failures belong to the report)", err)
			}
			document := validateSignozSmokeReport(t, report, "infra")
			if document.Status != tt.wantStatus {
				t.Fatalf("report status = %q, want %q", document.Status, tt.wantStatus)
			}
			if tt.wantFailedBackend != "" {
				assertSignozFailure(t, document.Checks, tt.wantFailedBackend, tt.wantFailureStage, tt.wantErrorClass)
			}
			// 检查完整性：api、三信号与 AI 平面（passed/failed/skipped）都必须在报告里。
			for _, backend := range []string{"api", "signoz_traces", "signoz_logs", "signoz_metrics", "langfuse_trace", "collector"} {
				found := false
				for _, check := range document.Checks {
					if check.Backend == backend {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("checks = %#v, want a low-sensitivity %s check", document.Checks, backend)
				}
			}
			if tt.wantTracesQueries >= 0 && (backend.tracesQueries != tt.wantTracesQueries || backend.logsQueries != tt.wantLogsQueries) {
				t.Fatalf("queries = traces:%d logs:%d, want traces:%d logs:%d", backend.tracesQueries, backend.logsQueries, tt.wantTracesQueries, tt.wantLogsQueries)
			}
			if tt.forbiddenReportText != "" && strings.Contains(mustMarshalSignozReport(t, report), tt.forbiddenReportText) {
				t.Fatalf("report leaked raw backend response %q", tt.forbiddenReportText)
			}
		})
	}

	t.Run("shares one bounded smoke window across signoz and langfuse queries", func(t *testing.T) {
		backend := fakeSignozInfrastructureBackend{
			tracesResponses: []markerQueryResponse{{observations: []MarkerObservation{{Marker: "run-signoz-window", ObservedAt: startedAt.Add(time.Second)}}}},
			logsResponses:   []markerQueryResponse{{observations: []MarkerObservation{{Marker: "run-signoz-window", ObservedAt: startedAt.Add(time.Second)}}}},
			before:          7,
			after:           8,
		}
		clock := newPollerTestClock(startedAt)
		report, err := RunSignozInfrastructureSmoke(context.Background(), SignozInfrastructureSmokeRequest{Deadline: deadline, Profile: "signoz"}, SignozInfrastructureSmokeRunnerDependencies{
			Backend: &backend, Clock: clock, PollInterval: time.Second,
			IdentityFactory: func(context.Context) (SignozSmokeIdentity, error) {
				return SignozSmokeIdentity{RunID: "run-signoz-window"}, nil
			},
			Trigger: func(context.Context, SignozSmokeIdentity) error { return nil },
		})
		if err != nil {
			t.Fatalf("RunSignozInfrastructureSmoke() error = %v", err)
		}
		validateSignozSmokeReport(t, report, "infra")
		// 限定窗口：每个 marker 查询共享 StartedAt/Deadline，context deadline 不晚于 smoke deadline。
		if len(backend.queryTargets) == 0 || len(backend.queryTargets) != len(backend.queryDeadlines) {
			t.Fatalf("query boundaries = targets:%d deadlines:%d, want one bounded boundary per backend query", len(backend.queryTargets), len(backend.queryDeadlines))
		}
		for index, target := range backend.queryTargets {
			if !target.StartedAt.Equal(startedAt) || !target.Deadline.Equal(deadline) {
				t.Fatalf("query target %d = %#v, want shared smoke window", index, target)
			}
			if backend.queryDeadlines[index].IsZero() || backend.queryDeadlines[index].After(deadline) {
				t.Fatalf("query context deadline %d = %s, want a deadline no later than %s", index, backend.queryDeadlines[index], deadline)
			}
		}
		if len(backend.countDeadlines) != 2 {
			t.Fatalf("metric count query calls = %d, want baseline and after queries", len(backend.countDeadlines))
		}
	})
}

// TestSignozChatSmokeRunnerContract 固定备选 profile 的 chat 验收边界：
// SigNoz 三信号 + Langfuse trace/score 投影，identity 逐字段匹配，
// 不复制也不放宽 Grafana 主线的 identity 断言。
func TestSignozChatSmokeRunnerContract(t *testing.T) {
	startedAt := time.Now().UTC()
	deadline := startedAt.Add(time.Minute)
	identity := SignozSmokeIdentity{RunID: "run-signoz-chat", Marker: "marker-signoz-chat"}
	apiResult := ChatSmokeAPIResult{RequestID: "req-signoz-chat", AITraceID: "ai-signoz-chat", ServiceTraceID: "svc-signoz-chat", SpanID: "span-signoz-chat"}
	matching := func(marker string) []ChatObservation {
		return []ChatObservation{{
			Marker: marker, RequestID: apiResult.RequestID, AITraceID: apiResult.AITraceID,
			ServiceTraceID: apiResult.ServiceTraceID, SpanID: apiResult.SpanID, ObservedAt: startedAt.Add(time.Second),
		}}
	}

	t.Run("passes when signoz signals and langfuse trace and score all match", func(t *testing.T) {
		backend := fakeSignozChatBackend{
			tracesResponses:        []chatObservationResponse{{observations: matching(identity.Marker)}},
			logsResponses:          []chatObservationResponse{{observations: matching(identity.Marker)}},
			langfuseTraceResponses: []chatObservationResponse{{observations: matching(identity.Marker)}},
			langfuseScores:         1,
			before:                 3,
			after:                  5,
		}
		report, err := RunSignozChatSmoke(context.Background(), SignozChatSmokeRequest{Deadline: deadline, Profile: "signoz"}, signozChatDependencies(&backend, startedAt, identity, apiResult))
		if err != nil {
			t.Fatalf("RunSignozChatSmoke() error = %v, want nil", err)
		}
		document := validateSignozSmokeReport(t, report, "chat")
		if document.Status != "passed" {
			t.Fatalf("report status = %q, want passed (checks = %#v)", document.Status, document.Checks)
		}
		for _, backend := range []string{"api", "signoz_traces", "signoz_logs", "langfuse_trace", "langfuse_score", "signoz_metrics"} {
			found := false
			for _, check := range document.Checks {
				if check.Backend == backend && check.Status == "passed" {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("checks = %#v, want a passed %s check", document.Checks, backend)
			}
		}
		// 限定窗口：chat 查询也必须共享本次 run 的窗口。
		for index, target := range backend.queryTargets {
			if !target.StartedAt.Equal(startedAt) || !target.Deadline.Equal(deadline) {
				t.Fatalf("chat query target %d = %#v, want shared smoke window", index, target)
			}
			if backend.queryDeadlines[index].IsZero() || backend.queryDeadlines[index].After(deadline) {
				t.Fatalf("chat query context deadline %d = %s, want no later than %s", index, backend.queryDeadlines[index], deadline)
			}
		}
	})

	t.Run("fails on identity mismatch instead of accepting foreign observations", func(t *testing.T) {
		foreign := []ChatObservation{{
			Marker: identity.Marker, RequestID: "req-someone-else", AITraceID: apiResult.AITraceID,
			ServiceTraceID: apiResult.ServiceTraceID, SpanID: apiResult.SpanID, ObservedAt: startedAt.Add(time.Second),
		}}
		backend := fakeSignozChatBackend{
			tracesResponses:        []chatObservationResponse{{observations: foreign}},
			logsResponses:          []chatObservationResponse{{observations: matching(identity.Marker)}},
			langfuseTraceResponses: []chatObservationResponse{{observations: matching(identity.Marker)}},
			langfuseScores:         1,
			before:                 3,
			after:                  5,
		}
		report, err := RunSignozChatSmoke(context.Background(), SignozChatSmokeRequest{Deadline: deadline, Profile: "signoz"}, signozChatDependencies(&backend, startedAt, identity, apiResult))
		if err != nil {
			t.Fatalf("RunSignozChatSmoke() error = %v, want nil", err)
		}
		document := validateSignozSmokeReport(t, report, "chat")
		if document.Status != "failed" {
			t.Fatalf("report status = %q, want failed on identity mismatch", document.Status)
		}
		assertSignozFailure(t, document.Checks, "signoz_traces", "query", "identity_mismatch")
	})

	t.Run("fails when the langfuse score projection is missing", func(t *testing.T) {
		backend := fakeSignozChatBackend{
			tracesResponses:        []chatObservationResponse{{observations: matching(identity.Marker)}},
			logsResponses:          []chatObservationResponse{{observations: matching(identity.Marker)}},
			langfuseTraceResponses: []chatObservationResponse{{observations: matching(identity.Marker)}},
			langfuseScores:         0,
			before:                 3,
			after:                  5,
		}
		report, err := RunSignozChatSmoke(context.Background(), SignozChatSmokeRequest{Deadline: deadline, Profile: "signoz"}, signozChatDependencies(&backend, startedAt, identity, apiResult))
		if err != nil {
			t.Fatalf("RunSignozChatSmoke() error = %v, want nil", err)
		}
		document := validateSignozSmokeReport(t, report, "chat")
		if document.Status != "failed" {
			t.Fatalf("report status = %q, want failed when score projection is missing", document.Status)
		}
		assertSignozFailure(t, document.Checks, "langfuse_score", "query", "score_projection_missing")
	})

	t.Run("keeps a schema-valid low-sensitivity report when the chat API itself fails", func(t *testing.T) {
		backend := fakeSignozChatBackend{before: 3, after: 3}
		deps := signozChatDependencies(&backend, startedAt, identity, apiResult)
		deps.Trigger = func(context.Context, SignozSmokeIdentity) (ChatSmokeAPIResult, error) {
			return ChatSmokeAPIResult{}, classifiedInfrastructureQueryError{class: "backend_unavailable", raw: "raw-provider-body-must-not-leak"}
		}
		report, err := RunSignozChatSmoke(context.Background(), SignozChatSmokeRequest{Deadline: deadline, Profile: "signoz"}, deps)
		if err == nil {
			t.Fatal("RunSignozChatSmoke() error = nil, want the original trigger error returned to the caller")
		}
		document := validateSignozSmokeReport(t, report, "chat")
		assertSignozFailure(t, document.Checks, "api", "api", "backend_unavailable")
		if encoded := mustMarshalSignozReport(t, report); strings.Contains(encoded, "raw-provider-body-must-not-leak") {
			t.Fatal("report leaked raw provider error body")
		}
	})
}

// TestSignozSmokeRunnerRejectsInvalidDependencies 防御性契约：
// 备选 runner 只接受 signoz profile，且依赖不完整时 fail closed，不得产出半份报告。
func TestSignozSmokeRunnerRejectsInvalidDependencies(t *testing.T) {
	deadline := time.Now().UTC().Add(time.Minute)
	clock := newPollerTestClock(time.Now().UTC())
	backend := &fakeSignozInfrastructureBackend{}
	validTrigger := func(context.Context, SignozSmokeIdentity) error { return nil }

	tests := []struct {
		name    string
		request SignozInfrastructureSmokeRequest
		deps    SignozInfrastructureSmokeRunnerDependencies
	}{
		{
			name:    "wrong profile",
			request: SignozInfrastructureSmokeRequest{Deadline: deadline, Profile: "grafana"},
			deps:    SignozInfrastructureSmokeRunnerDependencies{Backend: backend, Clock: clock, PollInterval: time.Second, Trigger: validTrigger},
		},
		{
			name:    "missing backend",
			request: SignozInfrastructureSmokeRequest{Deadline: deadline, Profile: "signoz"},
			deps:    SignozInfrastructureSmokeRunnerDependencies{Clock: clock, PollInterval: time.Second, Trigger: validTrigger},
		},
		{
			name:    "missing trigger",
			request: SignozInfrastructureSmokeRequest{Deadline: deadline, Profile: "signoz"},
			deps:    SignozInfrastructureSmokeRunnerDependencies{Backend: backend, Clock: clock, PollInterval: time.Second},
		},
		{
			name:    "non-positive poll interval",
			request: SignozInfrastructureSmokeRequest{Deadline: deadline, Profile: "signoz"},
			deps:    SignozInfrastructureSmokeRunnerDependencies{Backend: backend, Clock: clock, Trigger: validTrigger},
		},
		{
			name:    "zero deadline",
			request: SignozInfrastructureSmokeRequest{Profile: "signoz"},
			deps:    SignozInfrastructureSmokeRunnerDependencies{Backend: backend, Clock: clock, PollInterval: time.Second, Trigger: validTrigger},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := RunSignozInfrastructureSmoke(context.Background(), tt.request, tt.deps); !errors.Is(err, errSignozSmokeFailed) {
				t.Fatalf("RunSignozInfrastructureSmoke() error = %v, want errSignozSmokeFailed", err)
			}
			// 同一条防御性契约也必须约束 chat runner：按用例名同步构造对应缺陷，
			// 而不是用一个完整依赖的 chat runner 冒充通过。
			var chatBackend SignozChatSmokeBackend = &fakeSignozChatBackend{}
			if tt.name == "missing backend" {
				chatBackend = nil
			}
			var chatTrigger func(context.Context, SignozSmokeIdentity) (ChatSmokeAPIResult, error) = func(context.Context, SignozSmokeIdentity) (ChatSmokeAPIResult, error) {
				return ChatSmokeAPIResult{}, nil
			}
			if tt.name == "missing trigger" {
				chatTrigger = nil
			}
			chatInterval := time.Second
			if tt.name == "non-positive poll interval" {
				chatInterval = 0
			}
			chatDeps := SignozChatSmokeRunnerDependencies{
				Backend:      chatBackend,
				Clock:        clock,
				PollInterval: chatInterval,
				IdentityFactory: func(context.Context) (SignozSmokeIdentity, error) {
					return SignozSmokeIdentity{RunID: "run-signoz-chat"}, nil
				},
				Trigger: chatTrigger,
			}
			if _, err := RunSignozChatSmoke(context.Background(), SignozChatSmokeRequest{Deadline: tt.request.Deadline, Profile: tt.request.Profile}, chatDeps); !errors.Is(err, errSignozSmokeFailed) {
				t.Fatalf("RunSignozChatSmoke() error = %v, want errSignozSmokeFailed for the same defensive contract", err)
			}
		})
	}
}

func signozChatDependencies(backend *fakeSignozChatBackend, startedAt time.Time, identity SignozSmokeIdentity, apiResult ChatSmokeAPIResult) SignozChatSmokeRunnerDependencies {
	return SignozChatSmokeRunnerDependencies{
		Backend:         backend,
		Clock:           newPollerTestClock(startedAt),
		PollInterval:    time.Second,
		IdentityFactory: func(context.Context) (SignozSmokeIdentity, error) { return identity, nil },
		Trigger: func(context.Context, SignozSmokeIdentity) (ChatSmokeAPIResult, error) {
			return apiResult, nil
		},
	}
}

type signozSmokeReportDocument struct {
	Profile  string         `json:"profile"`
	Scenario string         `json:"scenario"`
	Status   string         `json:"status"`
	Checks   []BackendCheck `json:"checks"`
	Cleanup  SmokeCleanup   `json:"cleanup"`
}

func validateSignozSmokeReport(t *testing.T, report *SmokeReport, scenario string) signozSmokeReportDocument {
	t.Helper()
	encoded := mustMarshalSignozReport(t, report)
	validator, err := NewSmokeReportSchemaValidator(loadSmokeReportSchema(t))
	if err != nil {
		t.Fatalf("NewSmokeReportSchemaValidator() error = %v", err)
	}
	if err := validator.ValidateJSON([]byte(encoded)); err != nil {
		t.Fatalf("version-controlled schema validation error = %v", err)
	}
	var document signozSmokeReportDocument
	if err := json.Unmarshal([]byte(encoded), &document); err != nil {
		t.Fatalf("UnmarshalJSON() error = %v", err)
	}
	if document.Profile != "signoz" {
		t.Fatalf("report profile = %q, want signoz", document.Profile)
	}
	if document.Scenario != scenario {
		t.Fatalf("report scenario = %q, want %q", document.Scenario, scenario)
	}
	return document
}

func mustMarshalSignozReport(t *testing.T, report *SmokeReport) string {
	t.Helper()
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("MarshalJSON() error = %v", err)
	}
	return string(encoded)
}

func assertSignozFailure(t *testing.T, checks []BackendCheck, backend, stage, errorClass string) {
	t.Helper()
	for _, check := range checks {
		if check.Backend == backend && check.Status == "failed" && check.FailureStage == stage && check.ErrorClass == errorClass {
			return
		}
	}
	t.Fatalf("checks = %#v, want failed %s check with failure_stage=%q and error_class=%q", checks, backend, stage, errorClass)
}
