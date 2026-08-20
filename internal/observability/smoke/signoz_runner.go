package smoke

// SigNoz 备选 profile 的 E2E 查询闭环（T144）。两个 runner 分别镜像主线
// infra_runner.go / chat_runner.go 的执行语义，只在后端面不同：
//   - 三信号来自 SigNoz（checks 记为 signoz_traces / signoz_logs / signoz_metrics）；
//   - AI 平面仍查询 Langfuse（langfuse_trace），chat 场景额外验证 score 投影
//     （langfuse_score，缺失是独立失败类别 score_projection_missing）。
// 复用而不复制的部分：report 构建、marker poller 与轮询节奏、负向证据的
// “先见正向再证负向”语义、chat observation 的逐字段 identity 断言——备选
// profile 不得放宽主线任何一条隐私或 identity 边界（T144 门控）。
// 防御性契约比主线更窄：profile 只接受 "signoz"，避免备选 runner 被主线
// 调用方误用后产出归属不清的报告。

import (
	"context"
	"errors"
	"time"
)

var errSignozSmokeFailed = errors.New("signoz smoke verification failed")

// SignozSmokeIdentity is shared by both alternate-profile runners. The runner derives
// the telemetry marker from the run ID so a caller or test double cannot replay an
// arbitrary marker independently of the identity it handed over.
type SignozSmokeIdentity struct{ RunID, Marker string }

// ── infra：三信号正向证据 + AI 平面负向证据 ─────────────────────────────────

type SignozInfrastructureSmokeBackend interface {
	QuerySignozTraces(context.Context, PollMarkerTarget) ([]MarkerObservation, error)
	QuerySignozLogs(context.Context, PollMarkerTarget) ([]MarkerObservation, error)
	BaselineHTTPRequestCount(context.Context) (int64, error)
	HTTPRequestCount(context.Context) (int64, error)
	QueryLangfuse(context.Context, PollMarkerTarget) (int, error)
	QueryAIPlane(context.Context, PollMarkerTarget) (int, error)
}

type SignozInfrastructureSmokeRequest struct {
	Deadline time.Time
	Profile  string
}

type SignozInfrastructureSmokeRunnerDependencies struct {
	Backend         SignozInfrastructureSmokeBackend
	IdentityFactory func(context.Context) (SignozSmokeIdentity, error)
	Trigger         func(context.Context, SignozSmokeIdentity) error
	Clock           PollerClock
	PollInterval    time.Duration
}

// RunSignozInfrastructureSmoke owns identity and execution order. Verification failures
// live in a schema-valid report (not an error) so CI retains every low-sensitivity
// backend fact from the same run, exactly like the mainline infrastructure runner.
func RunSignozInfrastructureSmoke(ctx context.Context, request SignozInfrastructureSmokeRequest, deps SignozInfrastructureSmokeRunnerDependencies) (*SmokeReport, error) {
	startedAt, identity, bounded, cancel, ok := prepareSignozSmoke(ctx, request.Deadline, request.Profile, deps.Clock, deps.PollInterval, deps.Backend != nil && deps.Trigger != nil, true, func(factoryCtx context.Context) (SignozSmokeIdentity, error) {
		if deps.IdentityFactory == nil {
			return newSignozSmokeIdentity(factoryCtx)
		}
		return deps.IdentityFactory(factoryCtx)
	})
	if !ok {
		return nil, errSignozSmokeFailed
	}
	defer cancel()

	target := PollMarkerTarget{Marker: identity.Marker, StartedAt: startedAt, Deadline: request.Deadline}
	checks := make([]BackendCheckInput, 0, 6)
	baseline, baselineErr := deps.Backend.BaselineHTTPRequestCount(bounded)
	if bounded.Err() != nil {
		checks = append(checks,
			outcomeCheck("api", false, "api", "backend_timeout", map[string]any{"response_status": int64(0)}),
			outcomeCheck("signoz_traces", false, "query", "backend_timeout", map[string]any{"matched_spans": int64(0)}),
			outcomeCheck("signoz_logs", false, "query", "backend_timeout", map[string]any{"matched_logs": int64(0)}),
			outcomeCheck("signoz_metrics", false, "query", "backend_timeout", map[string]any{"metric_delta": int64(0)}),
			outcomeCheck("langfuse_trace", false, "query", "backend_timeout", map[string]any{"matched_traces": int64(0)}),
			outcomeCheck("collector", false, "query", "backend_timeout", map[string]any{"marker_received": int64(0)}),
		)
		return buildSignozSmokeReport(identity, request, "infra", startedAt, deps.Clock.Now().UTC(), checks)
	}
	triggerErr := deps.Trigger(bounded, identity)
	checks = append(checks, outcomeCheck("api", triggerErr == nil, "api", "backend_unavailable", map[string]any{"response_status": int64(0)}))
	poller := NewBoundedMarkerPoller(deps.Clock, deps.PollInterval)
	// SigNoz delivery is asynchronous and signal-independent. The positive evidence
	// queries run under one deadline so a delayed log cannot consume the whole window
	// before the trace or metric queries get their turn.
	type markerResult struct {
		backend, key string
		err          error
	}
	type countResult struct {
		count int64
		err   error
	}
	markerResults := make(chan markerResult, 2)
	countResults := make(chan countResult, 1)
	go func() {
		_, err := poller.WaitForMarker(bounded, target, deps.Backend.QuerySignozTraces)
		markerResults <- markerResult{backend: "signoz_traces", key: "matched_spans", err: err}
	}()
	go func() {
		_, err := poller.WaitForMarker(bounded, target, deps.Backend.QuerySignozLogs)
		markerResults <- markerResult{backend: "signoz_logs", key: "matched_logs", err: err}
	}()
	go func() {
		count, err := waitForHTTPRequestIncrease(bounded, signozInfraCountAdapter{backend: deps.Backend}, baseline, target.Deadline, deps.Clock, deps.PollInterval)
		countResults <- countResult{count: count, err: err}
	}()
	markerChecks := make(map[string]BackendCheckInput, 2)
	for range 2 {
		result := <-markerResults
		markerChecks[result.backend] = markerCheck(result.backend, result.err, result.key)
	}
	checks = append(checks, markerChecks["signoz_traces"], markerChecks["signoz_logs"])
	afterResult := <-countResults
	after, afterErr := afterResult.count, afterResult.err
	metricsOK := baselineErr == nil && afterErr == nil && after > baseline
	metricsClass := "query_failed"
	if baselineErr == nil && afterErr == nil && after <= baseline {
		metricsClass = "metric_delta_missing"
	}
	delta := int64(0)
	if baselineErr == nil && afterErr == nil {
		delta = after - baseline
	}
	checks = append(checks, outcomeCheck("signoz_metrics", metricsOK, "query", metricsClass, map[string]any{"metric_delta": delta}))
	// A negative query is only meaningful after both SigNoz projections have appeared;
	// otherwise an async exporter could make a one-shot zero look like proof that an
	// AI-plane leak never happened (same mainline boundary, kept verbatim).
	if markerChecks["signoz_traces"].Status == "passed" && markerChecks["signoz_logs"].Status == "passed" {
		langfuseCount, langfuseErr := waitForNegativeEvidence(bounded, target, deps.Backend.QueryLangfuse, deps.Clock, deps.PollInterval)
		collectorCount, collectorErr := waitForNegativeEvidence(bounded, target, deps.Backend.QueryAIPlane, deps.Clock, deps.PollInterval)
		checks = append(checks,
			negativeCheck("langfuse_trace", langfuseCount, langfuseErr, "matched_traces"),
			negativeCheck("collector", collectorCount, collectorErr, "marker_received"),
		)
	} else {
		checks = append(checks,
			BackendCheckInput{Backend: "langfuse_trace", Status: "skipped", FailureStage: "none", Evidence: map[string]any{"matched_traces": int64(0)}},
			BackendCheckInput{Backend: "collector", Status: "skipped", FailureStage: "none", Evidence: map[string]any{"marker_received": int64(0)}},
		)
	}
	return buildSignozSmokeReport(identity, request, "infra", startedAt, deps.Clock.Now().UTC(), checks)
}

// signozInfraCountAdapter adapts the alternate-profile backend to the shared
// baseline/after polling helper without copying its logic.
type signozInfraCountAdapter struct {
	backend SignozInfrastructureSmokeBackend
}

func (a signozInfraCountAdapter) BaselineHTTPRequestCount(ctx context.Context) (int64, error) {
	return a.backend.BaselineHTTPRequestCount(ctx)
}

func (a signozInfraCountAdapter) HTTPRequestCount(ctx context.Context) (int64, error) {
	return a.backend.HTTPRequestCount(ctx)
}

// ── chat：三信号 + Langfuse trace/score 投影 ────────────────────────────────

type SignozChatSmokeBackend interface {
	QuerySignozTracesChat(context.Context, ChatSmokeTarget) ([]ChatObservation, error)
	QuerySignozLogsChat(context.Context, ChatSmokeTarget) ([]ChatObservation, error)
	QueryLangfuseChat(context.Context, ChatSmokeTarget) ([]ChatObservation, error)
	QueryLangfuseScore(context.Context, ChatSmokeTarget) (int, error)
	BaselineLLMRequestCount(context.Context) (int64, error)
	LLMRequestCount(context.Context) (int64, error)
}

type SignozChatSmokeRequest struct {
	Deadline time.Time
	Profile  string
}

type SignozChatSmokeRunnerDependencies struct {
	Backend         SignozChatSmokeBackend
	Clock           PollerClock
	PollInterval    time.Duration
	IdentityFactory func(context.Context) (SignozSmokeIdentity, error)
	Trigger         func(context.Context, SignozSmokeIdentity) (ChatSmokeAPIResult, error)
}

// RunSignozChatSmoke keeps the model result and telemetry verification in separate
// error domains, mirroring the mainline chat runner: a completed API attempt always
// yields a low-sensitivity report; only the original model error is returned to the
// caller while delayed or missing telemetry stays report-owned evidence.
func RunSignozChatSmoke(ctx context.Context, request SignozChatSmokeRequest, deps SignozChatSmokeRunnerDependencies) (*SmokeReport, error) {
	startedAt, identity, bounded, cancel, ok := prepareSignozSmoke(ctx, request.Deadline, request.Profile, deps.Clock, deps.PollInterval, deps.Backend != nil && deps.Trigger != nil, false, func(factoryCtx context.Context) (SignozSmokeIdentity, error) {
		if deps.IdentityFactory == nil {
			return newSignozSmokeIdentity(factoryCtx)
		}
		return deps.IdentityFactory(factoryCtx)
	})
	if !ok {
		return nil, errSignozSmokeFailed
	}
	defer cancel()

	type metricResult struct {
		count int64
		err   error
	}
	baselineResult := make(chan metricResult, 1)
	baselineCtx, stopBaseline := context.WithTimeout(bounded, chatBaselineTimeout)
	go func() {
		count, queryErr := deps.Backend.BaselineLLMRequestCount(baselineCtx)
		baselineResult <- metricResult{count: count, err: queryErr}
	}()
	result, triggerErr := deps.Trigger(bounded, identity)
	checks := []BackendCheckInput{outcomeCheck("api", triggerErr == nil, "api", triggerErrorClass(triggerErr), map[string]any{"response_status": int64(boolToInt(triggerErr == nil))})}
	if triggerErr != nil {
		stopBaseline()
		report, buildErr := buildSignozChatReport(identity, safeFailedChatResult(result), request, startedAt, deps.Clock.Now(), checks)
		if buildErr != nil {
			return nil, errors.Join(triggerErr, errSignozSmokeFailed)
		}
		return report, triggerErr
	}
	if !validChatAPIResult(result) {
		stopBaseline()
		checks[0] = outcomeCheck("api", false, "api", "malformed_response", map[string]any{"response_status": int64(1)})
		return buildSignozChatReport(identity, ChatSmokeAPIResult{}, request, startedAt, deps.Clock.Now(), checks)
	}

	target := ChatSmokeTarget{Marker: identity.Marker, RequestID: result.RequestID, AITraceID: result.AITraceID, ServiceTraceID: result.ServiceTraceID, SpanID: result.SpanID, StartedAt: startedAt, Deadline: request.Deadline, Limit: maximumChatObservations}
	baseline := <-baselineResult
	stopBaseline()
	afterCtx, stopAfter := context.WithTimeout(bounded, chatBaselineTimeout)
	after, afterErr := deps.Backend.LLMRequestCount(afterCtx)
	stopAfter()
	checks = append(checks, pollSignozChatBackends(bounded, target, deps)...)
	// Langfuse score 投影是独立证据面：AI trace 与 score 分开验收，缺失投影
	// 不是“AI 平面泄漏”而是 score_projection_missing。
	scoreCount, scoreErr := deps.Backend.QueryLangfuseScore(bounded, target)
	checks = append(checks, signozScoreCheck(scoreCount, scoreErr))
	delta := int64(0)
	if baseline.err == nil && afterErr == nil {
		delta = after - baseline.count
	}
	checks = append(checks, outcomeCheck("signoz_metrics", baseline.err == nil && afterErr == nil && delta > 0, "query", metricErrorClass(baseline.err, afterErr, delta), map[string]any{"metric_delta": delta}))
	return buildSignozChatReport(identity, result, request, startedAt, deps.Clock.Now(), checks)
}

// pollSignozChatBackends reuses the mainline per-backend poll state machine; only the
// backend names and query closures differ, so the identity-match semantics stay shared.
func pollSignozChatBackends(ctx context.Context, target ChatSmokeTarget, deps SignozChatSmokeRunnerDependencies) []BackendCheckInput {
	states := []*chatPollState{
		{backend: "signoz_traces", evidenceKey: "matched_spans", query: deps.Backend.QuerySignozTracesChat},
		{backend: "signoz_logs", evidenceKey: "matched_logs", query: deps.Backend.QuerySignozLogsChat},
		{backend: "langfuse_trace", evidenceKey: "matched_traces", query: deps.Backend.QueryLangfuseChat},
	}
	for {
		allPassed := true
		for _, state := range states {
			if !state.passed {
				pollSignozChatBackend(ctx, target, state)
			}
			allPassed = allPassed && state.passed
		}
		if allPassed || !deps.Clock.Now().Before(target.Deadline) {
			return chatPollChecks(states)
		}
		if err := deps.Clock.Wait(ctx, minimumDuration(deps.PollInterval, target.Deadline.Sub(deps.Clock.Now()))); err != nil {
			for _, state := range states {
				if !state.passed {
					state.lastErr = err
				}
			}
			return chatPollChecks(states)
		}
	}
}

// pollSignozChatBackend mirrors pollChatBackend with one backend-specific difference:
// signoz_logs stores the HTTP completion log whose span is its own real root span
// (not the manifest bridge span), the same reason Loki has a dedicated branch on
// the mainline. Everything else — window bounds, observation cap, identity fields —
// is unchanged.
func pollSignozChatBackend(ctx context.Context, target ChatSmokeTarget, state *chatPollState) {
	queryCtx, cancel := context.WithTimeout(ctx, chatQueryTimeout)
	observations, err := state.query(queryCtx, target)
	cancel()
	if err != nil {
		state.lastErr = err
		return
	}
	if len(observations) > maximumChatObservations {
		state.lastErr = errSignozSmokeFailed
		return
	}
	for _, observation := range observations {
		if observation.ObservedAt.Before(target.StartedAt) || observation.ObservedAt.After(target.Deadline) || observation.Marker != target.Marker {
			continue
		}
		if signozChatObservationMatches(state.backend, observation, target) {
			state.passed = true
			return
		}
		state.sawMismatch = true
	}
}

func signozChatObservationMatches(backend string, observation ChatObservation, target ChatSmokeTarget) bool {
	if observation.RequestID != target.RequestID || observation.AITraceID != target.AITraceID || observation.ServiceTraceID != target.ServiceTraceID {
		return false
	}
	return (backend == "signoz_logs" && isSafePollMarker(observation.SpanID)) || observation.SpanID == target.SpanID
}

func signozScoreCheck(count int, err error) BackendCheckInput {
	if err != nil {
		return outcomeCheck("langfuse_score", false, "query", markerErrorClass(err), map[string]any{"matched_scores": int64(0)})
	}
	if count >= 1 {
		return outcomeCheck("langfuse_score", true, "query", "none", map[string]any{"matched_scores": int64(count)})
	}
	return outcomeCheck("langfuse_score", false, "query", "score_projection_missing", map[string]any{"matched_scores": int64(0)})
}

// ── 共享准备与报告构建 ──────────────────────────────────────────────────────

func prepareSignozSmoke(ctx context.Context, deadline time.Time, profile string, clock PollerClock, pollInterval time.Duration, dependenciesPresent, deriveMarker bool, identityFactory func(context.Context) (SignozSmokeIdentity, error)) (time.Time, SignozSmokeIdentity, context.Context, context.CancelFunc, bool) {
	if ctx == nil || clock == nil || !dependenciesPresent || pollInterval <= 0 || profile != "signoz" {
		return time.Time{}, SignozSmokeIdentity{}, nil, nil, false
	}
	startedAt := clock.Now().UTC()
	if deadline.IsZero() || !deadline.After(startedAt) || deadline.Sub(startedAt) > time.Minute {
		return time.Time{}, SignozSmokeIdentity{}, nil, nil, false
	}
	identity, err := identityFactory(ctx)
	if err != nil || !isSafePollMarker(identity.RunID) {
		return time.Time{}, SignozSmokeIdentity{}, nil, nil, false
	}
	if deriveMarker {
		// Infra smoke mirrors the mainline boundary: the factory owns only the nonce/run ID
		// and the marker is derived here, so a caller cannot replay an arbitrary marker.
		identity.Marker = identity.RunID
	}
	if !isSafePollMarker(identity.Marker) {
		return time.Time{}, SignozSmokeIdentity{}, nil, nil, false
	}
	if ctx.Err() != nil {
		return time.Time{}, SignozSmokeIdentity{}, nil, nil, false
	}
	bounded, cancel := boundedChatContext(ctx, deadline)
	return startedAt, identity, bounded, cancel, true
}

func buildSignozSmokeReport(identity SignozSmokeIdentity, request SignozInfrastructureSmokeRequest, scenario string, startedAt, finishedAt time.Time, checks []BackendCheckInput) (*SmokeReport, error) {
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}
	if finishedAt.After(request.Deadline) {
		finishedAt = request.Deadline
	}
	return BuildSmokeReport(SmokeReportInput{RunID: identity.RunID, Marker: identity.Marker, Profile: request.Profile, Scenario: scenario, StartedAt: startedAt, Deadline: request.Deadline, FinishedAt: finishedAt, Checks: checks, Cleanup: SmokeCleanupInput{Status: "not_required", ResidualResources: []string{}, TemporaryCredentials: "not_created", TemporaryData: "not_created"}})
}

func buildSignozChatReport(identity SignozSmokeIdentity, result ChatSmokeAPIResult, request SignozChatSmokeRequest, startedAt, finishedAt time.Time, checks []BackendCheckInput) (*SmokeReport, error) {
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}
	if finishedAt.After(request.Deadline) {
		finishedAt = request.Deadline
	}
	return BuildSmokeReport(SmokeReportInput{RunID: identity.RunID, Marker: identity.Marker, Profile: request.Profile, Scenario: "chat", RequestID: result.RequestID, AITraceID: result.AITraceID, StartedAt: startedAt, Deadline: request.Deadline, FinishedAt: finishedAt, Checks: checks, Cleanup: SmokeCleanupInput{Status: "not_required", ResidualResources: []string{}, TemporaryCredentials: "not_created", TemporaryData: "not_created"}})
}

func newSignozSmokeIdentity(context.Context) (SignozSmokeIdentity, error) {
	identity, err := newChatSmokeIdentity(context.Background())
	if err != nil {
		return SignozSmokeIdentity{}, err
	}
	return SignozSmokeIdentity{RunID: identity.RunID, Marker: identity.Marker}, nil
}
