package smoke

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// T115 score worker 故障契约测试（RED 先行，T125 实现 score_failure_runner.go
// 使其 GREEN）。
//
// 覆盖的生产风险（FR-015 + data-model §9 状态机）：平台评分同步失败反噬业务、
// 重试破坏幂等、本地 eval 事实被抹除或改写。契约固定三个场景与一条总原则
// ——先确认本地 evidence 完整，再注入平台故障；业务结果与 eval 事实不得被
// 任何故障改写：
//
// 1. langfuse_api：baseline chat → evidence before（必须 Complete）→
//    FailLangfuseAPI → 故障期 chat（status/hash 与基线一致）→
//    RestoreLangfuseAPI → 有界轮询 projection 至 sent（Attempts >= 1 且
//    PlatformScoreCount == 1，重试幂等）→ evidence after digest 一致；
// 2. queue_full：FillScoreWorkerQueue → chat 不变 → 观察到
//    dropped_queue_full（声明的降级事实，必须入报告）→ Drain 恢复 →
//    evidence 完整；
// 3. shutdown：chat（projection 入队）→ ShutdownScoreWorker 必须返回
//    ErrScoreWorkerShutdownTimeout 哨兵 → RestartScoreWorker → 有界轮询
//    sent → evidence 完整。
//
// 报告证据键新增（T125 一并扩展 langfuse_score 允许集）：score_attempts、
// dropped_projections、local_evidence_intact、shutdown_timed_out；
// residual 新增 `langfuse-api-unavailable`、`score-worker-queue-full`。

type fakeScoreFailureBackend struct {
	evidenceFn      func(call int) (ScoreFailureEvidenceSnapshot, error)
	evidenceN       int
	projectionCalls [][]ScoreFailureProjectionObservation
	projectionErr   []error
	projectionN     int
	targets         []ScoreFailureProjectionTarget
	order           *[]string
}

func (f *fakeScoreFailureBackend) LocalEvidenceSnapshot(_ context.Context, _ ScoreFailureEvidenceTarget) (ScoreFailureEvidenceSnapshot, error) {
	call := f.evidenceN
	f.evidenceN++
	if f.order != nil {
		*f.order = append(*f.order, "evidence")
	}
	if f.evidenceFn == nil {
		return ScoreFailureEvidenceSnapshot{}, errors.New("fakeScoreFailureBackend: evidenceFn 未配置")
	}
	return f.evidenceFn(call)
}

func (f *fakeScoreFailureBackend) ScoreProjectionStates(_ context.Context, target ScoreFailureProjectionTarget) ([]ScoreFailureProjectionObservation, error) {
	call := f.projectionN
	f.projectionN++
	f.targets = append(f.targets, target)
	if f.order != nil {
		*f.order = append(*f.order, "projection")
	}
	if call < len(f.projectionErr) && f.projectionErr[call] != nil {
		return nil, f.projectionErr[call]
	}
	if call >= len(f.projectionCalls) {
		return nil, nil
	}
	return f.projectionCalls[call], nil
}

type fakeScoreFailureInjector struct {
	failLangfuseErr    error
	restoreLangfuseErr error
	fillQueueErr       error
	drainQueueErr      error
	shutdownErr        error
	restartErr         error
	failCalls          int
	restoreCalls       int
	fillCalls          int
	drainCalls         int
	shutdownCalls      int
	restartCalls       int
	order              *[]string
}

func (f *fakeScoreFailureInjector) FailLangfuseAPI(context.Context) error {
	f.failCalls++
	if f.order != nil {
		*f.order = append(*f.order, "fail-api")
	}
	return f.failLangfuseErr
}

func (f *fakeScoreFailureInjector) RestoreLangfuseAPI(context.Context) error {
	f.restoreCalls++
	if f.order != nil {
		*f.order = append(*f.order, "restore-api")
	}
	return f.restoreLangfuseErr
}

func (f *fakeScoreFailureInjector) FillScoreWorkerQueue(context.Context) error {
	f.fillCalls++
	if f.order != nil {
		*f.order = append(*f.order, "fill-queue")
	}
	return f.fillQueueErr
}

func (f *fakeScoreFailureInjector) DrainScoreWorkerQueue(context.Context) error {
	f.drainCalls++
	if f.order != nil {
		*f.order = append(*f.order, "drain-queue")
	}
	return f.drainQueueErr
}

func (f *fakeScoreFailureInjector) ShutdownScoreWorker(context.Context) error {
	f.shutdownCalls++
	if f.order != nil {
		*f.order = append(*f.order, "shutdown")
	}
	return f.shutdownErr
}

func (f *fakeScoreFailureInjector) RestartScoreWorker(context.Context) error {
	f.restartCalls++
	if f.order != nil {
		*f.order = append(*f.order, "restart")
	}
	return f.restartErr
}

type fakeScoreFailureTriggerCall struct {
	status   int
	bodyHash string
	err      error
}

type fakeScoreFailureTrigger struct {
	calls []fakeScoreFailureTriggerCall
	order *[]string
}

func (f *fakeScoreFailureTrigger) invoke(call fakeScoreFailureTriggerCall) ScoreWorkerFailureTrigger {
	return func(context.Context) (int, string, error) {
		f.calls = append(f.calls, call)
		if f.order != nil {
			*f.order = append(*f.order, "trigger")
		}
		return call.status, call.bodyHash, call.err
	}
}

func (f *fakeScoreFailureTrigger) sequence(calls ...fakeScoreFailureTriggerCall) ScoreWorkerFailureTrigger {
	return func(ctx context.Context) (int, string, error) {
		index := len(f.calls)
		if index >= len(calls) {
			return 0, "", errors.New("fakeScoreFailureTrigger: 调用次数超出预设")
		}
		return f.invoke(calls[index])(ctx)
	}
}

const scoreFailureTestBase = "2026-08-14T10:00:00Z"

func scoreFailureTestTime(t *testing.T) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, scoreFailureTestBase)
	if err != nil {
		t.Fatalf("解析测试基准时间失败: %v", err)
	}
	return parsed
}

func scoreFailureRequest(scenario ScoreWorkerFailureScenario, t *testing.T) ScoreWorkerFailureSmokeRequest {
	base := scoreFailureTestTime(t)
	return ScoreWorkerFailureSmokeRequest{
		Deadline:     base.Add(5 * time.Minute),
		Profile:      "grafana",
		Scenario:     scenario,
		EvidenceID:   "evidence-0001",
		ProjectionID: "projection-0001",
	}
}

func scoreFailureTestDeps(
	backend *fakeScoreFailureBackend,
	injector *fakeScoreFailureInjector,
	trigger ScoreWorkerFailureTrigger,
	clock PollerClock,
) ScoreWorkerFailureSmokeDependencies {
	return ScoreWorkerFailureSmokeDependencies{
		Backend:  backend,
		Injector: injector,
		Trigger:  trigger,
		Clock:    clock,
		IdentityFactory: func(context.Context) (ScoreWorkerFailureSmokeIdentity, error) {
			return ScoreWorkerFailureSmokeIdentity{RunID: "run-score-worker-failure-0001", Marker: "score-failure-marker-0001"}, nil
		},
		PollInterval: 5 * time.Second,
	}
}

func scoreFailureIntactEvidence() func(int) (ScoreFailureEvidenceSnapshot, error) {
	return func(int) (ScoreFailureEvidenceSnapshot, error) {
		return ScoreFailureEvidenceSnapshot{EvidenceID: "evidence-0001", Digest: "digest-aaaa", Complete: true}, nil
	}
}

func scoreFailureSentObservation(t *testing.T, at time.Time, attempts, platformCount int) []ScoreFailureProjectionObservation {
	t.Helper()
	return []ScoreFailureProjectionObservation{{
		ProjectionID:       "projection-0001",
		State:              "sent",
		Attempts:           attempts,
		PlatformScoreCount: platformCount,
		ObservedAt:         at,
	}}
}

func scoreFailurePassedCheck(t *testing.T, report *SmokeReport, backendName string) BackendCheck {
	t.Helper()
	if report.Status() != "passed" {
		t.Fatalf("report.Status() = %q, want passed", report.Status())
	}
	check := findCheck(t, report.Checks(), backendName)
	if check.Status != "passed" || check.FailureStage != "none" {
		t.Fatalf("%s check = %q/%q, want passed/none", backendName, check.Status, check.FailureStage)
	}
	return check
}

// langfuse_api 主路径：平台失败期间业务不变，恢复后重试幂等 sent，
// 本地 evidence 前后一致。
func TestRunScoreWorkerFailureSmokeLangfuseAPIFailureRetriesIdempotently(t *testing.T) {
	base := scoreFailureTestTime(t)
	clock := newPollerTestClock(base)
	var order []string
	backend := &fakeScoreFailureBackend{evidenceFn: scoreFailureIntactEvidence(), order: &order}
	backend.projectionCalls = [][]ScoreFailureProjectionObservation{
		nil,
		scoreFailureSentObservation(t, base.Add(8*time.Second), 2, 1),
	}
	injector := &fakeScoreFailureInjector{order: &order}
	triggerRecorder := &fakeScoreFailureTrigger{order: &order}
	trigger := triggerRecorder.sequence(
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
	)

	report, err := RunScoreWorkerFailureSmoke(context.Background(), scoreFailureRequest(ScoreWorkerFailureLangfuseAPI, t), scoreFailureTestDeps(backend, injector, trigger, clock))
	if err != nil {
		t.Fatalf("RunScoreWorkerFailureSmoke() = err %v, want nil", err)
	}
	if report.Scenario() != "score_worker_failure" {
		t.Errorf("report.Scenario() = %q, want score_worker_failure", report.Scenario())
	}

	apiCheck := scoreFailurePassedCheck(t, report, "api")
	if evidenceInt(t, apiCheck, "response_status") != 200 {
		t.Error("api check response_status != 200：平台失败期间业务响应不变")
	}

	scoreCheck := scoreFailurePassedCheck(t, report, "langfuse_score")
	if got := evidenceInt(t, scoreCheck, "matched_scores"); got != 1 {
		t.Errorf("matched_scores = %d, want 1：恢复后 projection 必须 sent", got)
	}
	if got := evidenceInt(t, scoreCheck, "score_attempts"); got != 2 {
		t.Errorf("score_attempts = %d, want 2：重试必须被记录", got)
	}
	if intact, ok := scoreCheck.Evidence["local_evidence_intact"]; !ok || intact != true {
		t.Errorf("local_evidence_intact = %v (ok=%v), want true：FR-015 本地证据不得被改写", intact, ok)
	}

	if injector.failCalls != 1 || injector.restoreCalls != 1 {
		t.Errorf("fail/restore 调用 = %d/%d, want 1/1", injector.failCalls, injector.restoreCalls)
	}
	wantOrder := []string{"trigger", "evidence", "fail-api", "trigger", "restore-api", "projection", "projection", "evidence"}
	if !slices.Equal(order, wantOrder) {
		t.Fatalf("执行顺序 = %v, want %v", order, wantOrder)
	}
	// 恢复窗口：轮询目标 deadline = 恢复后 + 120s（以 request.Deadline 为上界）。
	if len(backend.targets) == 0 {
		t.Fatal("backend 未收到 projection 查询")
	}
	wantDeadline := base.Add(ScoreWorkerFailureRecoveryWindow)
	for i, target := range backend.targets {
		if !target.Deadline.Equal(wantDeadline) {
			t.Errorf("第 %d 次查询 deadline = %v, want %v", i, target.Deadline, wantDeadline)
		}
	}
}

// 幂等破坏：同一 projection ID 在平台出现多个 score——重试必须失败，
// 不得静默容忍重复写。
func TestRunScoreWorkerFailureSmokeFailsWhenRetryBreaksIdempotency(t *testing.T) {
	base := scoreFailureTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakeScoreFailureBackend{evidenceFn: scoreFailureIntactEvidence()}
	backend.projectionCalls = [][]ScoreFailureProjectionObservation{
		nil,
		scoreFailureSentObservation(t, base.Add(8*time.Second), 3, 2),
	}
	injector := &fakeScoreFailureInjector{}
	triggerRecorder := &fakeScoreFailureTrigger{}
	trigger := triggerRecorder.sequence(
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
	)

	report, err := RunScoreWorkerFailureSmoke(context.Background(), scoreFailureRequest(ScoreWorkerFailureLangfuseAPI, t), scoreFailureTestDeps(backend, injector, trigger, clock))
	if err != nil {
		t.Fatalf("RunScoreWorkerFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	check := findCheck(t, report.Checks(), "langfuse_score")
	if check.ErrorClass != "unexpected_evidence" {
		t.Errorf("langfuse_score ErrorClass = %q, want unexpected_evidence", check.ErrorClass)
	}
}

// FR-015 违反：故障后本地 evidence digest 改变——必须失败。
func TestRunScoreWorkerFailureSmokeFailsWhenLocalEvidenceRewritten(t *testing.T) {
	base := scoreFailureTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakeScoreFailureBackend{evidenceFn: func(call int) (ScoreFailureEvidenceSnapshot, error) {
		if call == 0 {
			return ScoreFailureEvidenceSnapshot{EvidenceID: "evidence-0001", Digest: "digest-aaaa", Complete: true}, nil
		}
		return ScoreFailureEvidenceSnapshot{EvidenceID: "evidence-0001", Digest: "digest-bbbb", Complete: true}, nil
	}}
	backend.projectionCalls = [][]ScoreFailureProjectionObservation{
		nil,
		scoreFailureSentObservation(t, base.Add(8*time.Second), 1, 1),
	}
	injector := &fakeScoreFailureInjector{}
	triggerRecorder := &fakeScoreFailureTrigger{}
	trigger := triggerRecorder.sequence(
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
	)

	report, err := RunScoreWorkerFailureSmoke(context.Background(), scoreFailureRequest(ScoreWorkerFailureLangfuseAPI, t), scoreFailureTestDeps(backend, injector, trigger, clock))
	if err != nil {
		t.Fatalf("RunScoreWorkerFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
}

// 注入前本地 evidence 不完整：无法证明 FR-015，注入之前就失败。
func TestRunScoreWorkerFailureSmokeFailsWhenLocalEvidenceIncomplete(t *testing.T) {
	clock := newPollerTestClock(scoreFailureTestTime(t))
	backend := &fakeScoreFailureBackend{evidenceFn: func(int) (ScoreFailureEvidenceSnapshot, error) {
		return ScoreFailureEvidenceSnapshot{EvidenceID: "evidence-0001", Digest: "digest-aaaa", Complete: false}, nil
	}}
	injector := &fakeScoreFailureInjector{}
	triggerRecorder := &fakeScoreFailureTrigger{}
	trigger := triggerRecorder.sequence(
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
	)

	report, err := RunScoreWorkerFailureSmoke(context.Background(), scoreFailureRequest(ScoreWorkerFailureLangfuseAPI, t), scoreFailureTestDeps(backend, injector, trigger, clock))
	if err != nil {
		t.Fatalf("RunScoreWorkerFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	check := findCheck(t, report.Checks(), "langfuse_score")
	if check.ErrorClass != "unexpected_evidence" {
		t.Errorf("langfuse_score ErrorClass = %q, want unexpected_evidence", check.ErrorClass)
	}
	if injector.failCalls != 0 {
		t.Errorf("failCalls = %d, want 0：本地 evidence 不完整时不得注入平台失败", injector.failCalls)
	}
}

// 故障期 chat 响应被改写：业务结果被观测故障改变，必须失败。
func TestRunScoreWorkerFailureSmokeFailsWhenChatResponseChanged(t *testing.T) {
	clock := newPollerTestClock(scoreFailureTestTime(t))
	backend := &fakeScoreFailureBackend{evidenceFn: scoreFailureIntactEvidence()}
	injector := &fakeScoreFailureInjector{}
	triggerRecorder := &fakeScoreFailureTrigger{}
	trigger := triggerRecorder.sequence(
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
		fakeScoreFailureTriggerCall{status: 500, bodyHash: "different"},
	)

	report, err := RunScoreWorkerFailureSmoke(context.Background(), scoreFailureRequest(ScoreWorkerFailureLangfuseAPI, t), scoreFailureTestDeps(backend, injector, trigger, clock))
	if err != nil {
		t.Fatalf("RunScoreWorkerFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	apiCheck := findCheck(t, report.Checks(), "api")
	if apiCheck.Status != "failed" {
		t.Errorf("api check = %q, want failed", apiCheck.Status)
	}
	if injector.restoreCalls != 1 {
		t.Errorf("restoreCalls = %d, want 1：失败路径也必须恢复平台", injector.restoreCalls)
	}
}

// 恢复窗口耗尽仍未 sent：marker_missing 失败。
func TestRunScoreWorkerFailureSmokeFailsWhenProjectionNeverSent(t *testing.T) {
	clock := newPollerTestClock(scoreFailureTestTime(t))
	backend := &fakeScoreFailureBackend{evidenceFn: scoreFailureIntactEvidence()}
	injector := &fakeScoreFailureInjector{}
	triggerRecorder := &fakeScoreFailureTrigger{}
	trigger := triggerRecorder.sequence(
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
	)

	report, err := RunScoreWorkerFailureSmoke(context.Background(), scoreFailureRequest(ScoreWorkerFailureLangfuseAPI, t), scoreFailureTestDeps(backend, injector, trigger, clock))
	if err != nil {
		t.Fatalf("RunScoreWorkerFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	check := findCheck(t, report.Checks(), "langfuse_score")
	if check.ErrorClass != "marker_missing" {
		t.Errorf("langfuse_score ErrorClass = %q, want marker_missing", check.ErrorClass)
	}
}

// queue_full 主路径：降级事实 dropped_queue_full 入报告，业务与 evidence 不变。
func TestRunScoreWorkerFailureSmokeQueueFullDropsProjectionSafely(t *testing.T) {
	base := scoreFailureTestTime(t)
	clock := newPollerTestClock(base)
	var order []string
	backend := &fakeScoreFailureBackend{evidenceFn: scoreFailureIntactEvidence(), order: &order}
	backend.projectionCalls = [][]ScoreFailureProjectionObservation{
		{{ProjectionID: "projection-0001", State: "dropped_queue_full", Attempts: 0, PlatformScoreCount: 0, ObservedAt: base.Add(6 * time.Second)}},
	}
	injector := &fakeScoreFailureInjector{order: &order}
	triggerRecorder := &fakeScoreFailureTrigger{order: &order}
	trigger := triggerRecorder.sequence(
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
	)

	report, err := RunScoreWorkerFailureSmoke(context.Background(), scoreFailureRequest(ScoreWorkerFailureQueueFull, t), scoreFailureTestDeps(backend, injector, trigger, clock))
	if err != nil {
		t.Fatalf("RunScoreWorkerFailureSmoke() = err %v, want nil", err)
	}
	apiCheck := scoreFailurePassedCheck(t, report, "api")
	if evidenceInt(t, apiCheck, "response_status") != 200 {
		t.Error("queue full 期间业务响应不变")
	}
	scoreCheck := scoreFailurePassedCheck(t, report, "langfuse_score")
	if got := evidenceInt(t, scoreCheck, "dropped_projections"); got != 1 {
		t.Errorf("dropped_projections = %d, want 1：queue full 丢弃必须入报告", got)
	}
	if got := evidenceInt(t, scoreCheck, "matched_scores"); got != 0 {
		t.Errorf("matched_scores = %d, want 0", got)
	}
	if intact, ok := scoreCheck.Evidence["local_evidence_intact"]; !ok || intact != true {
		t.Errorf("local_evidence_intact = %v (ok=%v), want true", intact, ok)
	}
	if injector.fillCalls != 1 || injector.drainCalls != 1 {
		t.Errorf("fill/drain 调用 = %d/%d, want 1/1", injector.fillCalls, injector.drainCalls)
	}
	wantOrder := []string{"trigger", "evidence", "fill-queue", "trigger", "projection", "drain-queue", "evidence"}
	if !slices.Equal(order, wantOrder) {
		t.Fatalf("执行顺序 = %v, want %v", order, wantOrder)
	}
}

// queue full 未观察到 dropped_queue_full：降级事实缺失，不得通过。
func TestRunScoreWorkerFailureSmokeQueueFullRequiresDroppedEvidence(t *testing.T) {
	clock := newPollerTestClock(scoreFailureTestTime(t))
	backend := &fakeScoreFailureBackend{evidenceFn: scoreFailureIntactEvidence()}
	injector := &fakeScoreFailureInjector{}
	triggerRecorder := &fakeScoreFailureTrigger{}
	trigger := triggerRecorder.sequence(
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
	)

	report, err := RunScoreWorkerFailureSmoke(context.Background(), scoreFailureRequest(ScoreWorkerFailureQueueFull, t), scoreFailureTestDeps(backend, injector, trigger, clock))
	if err != nil {
		t.Fatalf("RunScoreWorkerFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	check := findCheck(t, report.Checks(), "langfuse_score")
	if check.ErrorClass != "unexpected_evidence" {
		t.Errorf("langfuse_score ErrorClass = %q, want unexpected_evidence", check.ErrorClass)
	}
}

// shutdown 主路径：超时哨兵被观察，重启后 projection 恢复 sent，evidence 完整。
func TestRunScoreWorkerFailureSmokeShutdownTimeoutRecovers(t *testing.T) {
	base := scoreFailureTestTime(t)
	clock := newPollerTestClock(base)
	var order []string
	backend := &fakeScoreFailureBackend{evidenceFn: scoreFailureIntactEvidence(), order: &order}
	backend.projectionCalls = [][]ScoreFailureProjectionObservation{
		nil,
		scoreFailureSentObservation(t, base.Add(8*time.Second), 1, 1),
	}
	injector := &fakeScoreFailureInjector{shutdownErr: ErrScoreWorkerShutdownTimeout, order: &order}
	triggerRecorder := &fakeScoreFailureTrigger{order: &order}
	trigger := triggerRecorder.sequence(
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
	)

	report, err := RunScoreWorkerFailureSmoke(context.Background(), scoreFailureRequest(ScoreWorkerFailureShutdown, t), scoreFailureTestDeps(backend, injector, trigger, clock))
	if err != nil {
		t.Fatalf("RunScoreWorkerFailureSmoke() = err %v, want nil", err)
	}
	scoreCheck := scoreFailurePassedCheck(t, report, "langfuse_score")
	if timedOut, ok := scoreCheck.Evidence["shutdown_timed_out"]; !ok || timedOut != true {
		t.Errorf("shutdown_timed_out = %v (ok=%v), want true", timedOut, ok)
	}
	if got := evidenceInt(t, scoreCheck, "matched_scores"); got != 1 {
		t.Errorf("matched_scores = %d, want 1：重启后 projection 必须恢复 sent", got)
	}
	if intact, ok := scoreCheck.Evidence["local_evidence_intact"]; !ok || intact != true {
		t.Errorf("local_evidence_intact = %v (ok=%v), want true", intact, ok)
	}
	if injector.shutdownCalls != 1 || injector.restartCalls != 1 {
		t.Errorf("shutdown/restart 调用 = %d/%d, want 1/1", injector.shutdownCalls, injector.restartCalls)
	}
	wantOrder := []string{"trigger", "evidence", "trigger", "shutdown", "restart", "projection", "projection", "evidence"}
	if !slices.Equal(order, wantOrder) {
		t.Fatalf("执行顺序 = %v, want %v", order, wantOrder)
	}
}

// shutdown 未产生超时：场景无法证明超时处理，失败。
func TestRunScoreWorkerFailureSmokeShutdownWithoutTimeoutFails(t *testing.T) {
	clock := newPollerTestClock(scoreFailureTestTime(t))
	backend := &fakeScoreFailureBackend{evidenceFn: scoreFailureIntactEvidence()}
	injector := &fakeScoreFailureInjector{}
	triggerRecorder := &fakeScoreFailureTrigger{}
	trigger := triggerRecorder.sequence(
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
	)

	report, err := RunScoreWorkerFailureSmoke(context.Background(), scoreFailureRequest(ScoreWorkerFailureShutdown, t), scoreFailureTestDeps(backend, injector, trigger, clock))
	if err != nil {
		t.Fatalf("RunScoreWorkerFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	check := findCheck(t, report.Checks(), "langfuse_score")
	if check.ErrorClass != "unexpected_evidence" {
		t.Errorf("langfuse_score ErrorClass = %q, want unexpected_evidence", check.ErrorClass)
	}
}

// 恢复平台失败：平台仍不可达——cleanup failed + residual + 整体 failed。
func TestRunScoreWorkerFailureSmokeRestoreFailureReportsResidual(t *testing.T) {
	base := scoreFailureTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakeScoreFailureBackend{evidenceFn: scoreFailureIntactEvidence()}
	backend.projectionCalls = [][]ScoreFailureProjectionObservation{
		nil,
		scoreFailureSentObservation(t, base.Add(8*time.Second), 2, 1),
	}
	injector := &fakeScoreFailureInjector{restoreLangfuseErr: errors.New("langfuse api still unreachable")}
	triggerRecorder := &fakeScoreFailureTrigger{}
	trigger := triggerRecorder.sequence(
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
		fakeScoreFailureTriggerCall{status: 200, bodyHash: "chat-hash"},
	)

	report, err := RunScoreWorkerFailureSmoke(context.Background(), scoreFailureRequest(ScoreWorkerFailureLangfuseAPI, t), scoreFailureTestDeps(backend, injector, trigger, clock))
	if err != nil {
		t.Fatalf("RunScoreWorkerFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	cleanup := report.Cleanup()
	if cleanup.Status != "failed" || !slices.Contains(cleanup.ResidualResources, "langfuse-api-unavailable") {
		t.Errorf("cleanup = %q %v, want failed + langfuse-api-unavailable", cleanup.Status, cleanup.ResidualResources)
	}
}

// 请求与依赖校验：非法输入直接报错。
func TestRunScoreWorkerFailureSmokeRejectsInvalidRequests(t *testing.T) {
	base := scoreFailureTestTime(t)
	validBackend := &fakeScoreFailureBackend{evidenceFn: scoreFailureIntactEvidence()}
	validDeps := scoreFailureTestDeps(validBackend, &fakeScoreFailureInjector{}, (&fakeScoreFailureTrigger{}).sequence(fakeScoreFailureTriggerCall{status: 200, bodyHash: "h"}), newPollerTestClock(base))
	validRequest := scoreFailureRequest(ScoreWorkerFailureLangfuseAPI, t)

	tests := []struct {
		name   string
		mutate func(*ScoreWorkerFailureSmokeRequest, *ScoreWorkerFailureSmokeDependencies)
	}{
		{"profile 不在允许集", func(r *ScoreWorkerFailureSmokeRequest, _ *ScoreWorkerFailureSmokeDependencies) { r.Profile = "unknown" }},
		{"deadline 为零值", func(r *ScoreWorkerFailureSmokeRequest, _ *ScoreWorkerFailureSmokeDependencies) {
			r.Deadline = time.Time{}
		}},
		{"deadline 已过期", func(r *ScoreWorkerFailureSmokeRequest, _ *ScoreWorkerFailureSmokeDependencies) {
			r.Deadline = base.Add(-time.Second)
		}},
		{"scenario 未知", func(r *ScoreWorkerFailureSmokeRequest, _ *ScoreWorkerFailureSmokeDependencies) {
			r.Scenario = "unknown_scenario"
		}},
		{"EvidenceID 为空", func(r *ScoreWorkerFailureSmokeRequest, _ *ScoreWorkerFailureSmokeDependencies) { r.EvidenceID = "" }},
		{"ProjectionID 为空", func(r *ScoreWorkerFailureSmokeRequest, _ *ScoreWorkerFailureSmokeDependencies) { r.ProjectionID = "" }},
		{"ProjectionID 含 shell 元字符", func(r *ScoreWorkerFailureSmokeRequest, _ *ScoreWorkerFailureSmokeDependencies) {
			r.ProjectionID = "p; rm -rf /"
		}},
		{"Backend 缺失", func(_ *ScoreWorkerFailureSmokeRequest, d *ScoreWorkerFailureSmokeDependencies) { d.Backend = nil }},
		{"Injector 缺失", func(_ *ScoreWorkerFailureSmokeRequest, d *ScoreWorkerFailureSmokeDependencies) { d.Injector = nil }},
		{"Trigger 缺失", func(_ *ScoreWorkerFailureSmokeRequest, d *ScoreWorkerFailureSmokeDependencies) { d.Trigger = nil }},
		{"Clock 缺失", func(_ *ScoreWorkerFailureSmokeRequest, d *ScoreWorkerFailureSmokeDependencies) { d.Clock = nil }},
		{"PollInterval 非正", func(_ *ScoreWorkerFailureSmokeRequest, d *ScoreWorkerFailureSmokeDependencies) { d.PollInterval = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mutatedRequest := validRequest
			mutatedDeps := validDeps
			tc.mutate(&mutatedRequest, &mutatedDeps)
			if _, err := RunScoreWorkerFailureSmoke(context.Background(), mutatedRequest, mutatedDeps); err == nil {
				t.Error("RunScoreWorkerFailureSmoke() = nil error, want 校验错误")
			}
		})
	}
}

// ctx 取消必须中止运行并返回错误。
func TestRunScoreWorkerFailureSmokeContextCancellationAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	backend := &fakeScoreFailureBackend{evidenceFn: scoreFailureIntactEvidence()}
	injector := &fakeScoreFailureInjector{}

	report, err := RunScoreWorkerFailureSmoke(ctx, scoreFailureRequest(ScoreWorkerFailureLangfuseAPI, t), scoreFailureTestDeps(backend, injector, (&fakeScoreFailureTrigger{}).sequence(fakeScoreFailureTriggerCall{status: 200, bodyHash: "h"}), newPollerTestClock(scoreFailureTestTime(t))))
	if err == nil {
		t.Fatalf("RunScoreWorkerFailureSmoke() = %v, want ctx 错误", report)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if injector.failCalls != 0 {
		t.Errorf("failCalls = %d, want 0", injector.failCalls)
	}
}
