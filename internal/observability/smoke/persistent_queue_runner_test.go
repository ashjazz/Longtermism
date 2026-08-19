package smoke

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// T113 跨 Collector 重启的持久队列恢复契约测试（RED 先行，T123 实现
// persistent_queue_runner.go 使其 GREEN）。
//
// 覆盖的生产风险（FR-008 + US3 验收场景 2）：中断期间产生的待发送记录在
// Collector 重启后无法恢复、恢复被宣称成 exactly-once、或超出恢复窗口的
// 迟到记录被误计入通过。契约固定：
//
// 1. 执行顺序：快照 → pause 后端 → 产生 marker 流量 → 快照（积压证据）→
//    restart Collector → unpause → 有界 drain 轮询 → 最终快照；
// 2. drain 窗口默认 120 秒（PersistentQueueDrainWindow），轮询目标必须
//    携带 resume+window 的有界 deadline，ObservedAt 越过窗口的迟到
//    marker 必须被隔离（不得计入通过）；
// 3. 投递语义显式声明 at-least-once：duplicate delivery（sent delta 大于
//    积压量）不得失败，但必须作为 `duplicate_delivered` 证据写入报告，
//    不得静默忽略，也不得宣称 exactly-once；
// 4. 任何退出路径（restart 失败、drain 超时、unpause 失败）都必须恢复
//    backend；unpause 失败进入 cleanup failed + `paused-service` residual。
//
// 报告证据键：collector 复用 `queue_depth`（pause 期积压高水位）与
// `exporter_sent`（drain 期 sent delta），新增 `duplicate_delivered` 需要
// T123 同步扩展 Go 侧 evidence 允许集（schema additionalProperties 已开放）。

type fakePersistentQueueBackend struct {
	snapshotFn  func(call int) (PersistentQueueSnapshot, error)
	snapshots   [][]PersistentQueueSnapshot
	snapshotErr []error
	snapshotN   int
	markerCalls [][]MarkerObservation
	markerErr   []error
	markerN     int
	targets     []PollMarkerTarget
	order       *[]string
}

func (f *fakePersistentQueueBackend) SnapshotCollectorQueue(context.Context) (PersistentQueueSnapshot, error) {
	index := f.snapshotN
	f.snapshotN++
	if f.order != nil {
		*f.order = append(*f.order, "snapshot")
	}
	if f.snapshotFn != nil {
		return f.snapshotFn(index)
	}
	if index < len(f.snapshotErr) && f.snapshotErr[index] != nil {
		return PersistentQueueSnapshot{}, f.snapshotErr[index]
	}
	if index >= len(f.snapshots) {
		return PersistentQueueSnapshot{}, errors.New("fakePersistentQueueBackend: unexpected snapshot call")
	}
	return f.snapshots[index][0], nil
}

func (f *fakePersistentQueueBackend) QueryBackendMarker(_ context.Context, target PollMarkerTarget) ([]MarkerObservation, error) {
	index := f.markerN
	f.markerN++
	f.targets = append(f.targets, target)
	if f.order != nil {
		*f.order = append(*f.order, "query")
	}
	if index < len(f.markerErr) && f.markerErr[index] != nil {
		return nil, f.markerErr[index]
	}
	if index >= len(f.markerCalls) {
		return nil, nil
	}
	return f.markerCalls[index], nil
}

type fakePersistentQueueInjector struct {
	pauseCalls   []string
	unpauseCalls []string
	restartCalls int
	pauseErr     error
	unpauseErr   error
	restartErr   error
	order        *[]string
}

func (f *fakePersistentQueueInjector) Pause(_ context.Context, service string) error {
	f.pauseCalls = append(f.pauseCalls, service)
	if f.order != nil {
		*f.order = append(*f.order, "pause")
	}
	return f.pauseErr
}

func (f *fakePersistentQueueInjector) Unpause(_ context.Context, service string) error {
	f.unpauseCalls = append(f.unpauseCalls, service)
	if f.order != nil {
		*f.order = append(*f.order, "unpause")
	}
	return f.unpauseErr
}

func (f *fakePersistentQueueInjector) RestartCollector(context.Context) error {
	f.restartCalls++
	if f.order != nil {
		*f.order = append(*f.order, "restart")
	}
	return f.restartErr
}

type fakePersistentQueueTrigger struct {
	calls int
	order *[]string
	err   error
}

func (f *fakePersistentQueueTrigger) invoke() PersistentQueueTrigger {
	return func(context.Context) error {
		f.calls++
		if f.order != nil {
			*f.order = append(*f.order, "trigger")
		}
		return f.err
	}
}

const persistentQueueTestBase = "2026-08-14T10:00:00Z"

func persistentQueueTestTime(t *testing.T) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, persistentQueueTestBase)
	if err != nil {
		t.Fatalf("解析测试基准时间失败: %v", err)
	}
	return parsed
}

func persistentQueueTestRequest(t *testing.T) PersistentQueueSmokeRequest {
	base := persistentQueueTestTime(t)
	return PersistentQueueSmokeRequest{
		Deadline:       base.Add(5 * time.Minute),
		Profile:        "grafana",
		BackendService: "tempo",
		ComponentID:    "otlp/tempo",
	}
}

func persistentQueueTestIdentity() PersistentQueueSmokeIdentity {
	return PersistentQueueSmokeIdentity{RunID: "run-persistent-queue-0001", Marker: "queue-marker-0001"}
}

// 三快照夹具：pause 期间积压 25 条，drain 后 sent 增加 25 且队列回落。
func persistentQueueSnapshots() [][]PersistentQueueSnapshot {
	return [][]PersistentQueueSnapshot{
		{{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, Sent: 1000}},
		{{ComponentID: "otlp/tempo", QueueSize: 25, QueueCapacity: 10000, Sent: 1000}},
		{{ComponentID: "otlp/tempo", QueueSize: 3, QueueCapacity: 10000, Sent: 1025}},
	}
}

func persistentQueueMarkerAt(t *testing.T, at time.Time) []MarkerObservation {
	t.Helper()
	return []MarkerObservation{{Marker: "queue-marker-0001", ObservedAt: at}}
}

func persistentQueueTestDeps(
	backend *fakePersistentQueueBackend,
	injector *fakePersistentQueueInjector,
	trigger PersistentQueueTrigger,
	clock PollerClock,
) PersistentQueueSmokeDependencies {
	return PersistentQueueSmokeDependencies{
		Backend:         backend,
		Injector:        injector,
		Trigger:         trigger,
		Clock:           clock,
		IdentityFactory: func(context.Context) (PersistentQueueSmokeIdentity, error) { return persistentQueueTestIdentity(), nil },
		PollInterval:    5 * time.Second,
	}
}

// 主路径：积压在重启后于 120 秒窗口内恢复，报告分别记录积压高水位、
// drain sent delta、marker 命中数与 duplicate 计数（0）。
func TestRunPersistentQueueSmokeRecoversBacklogWithinDrainWindow(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakePersistentQueueBackend{snapshots: persistentQueueSnapshots()}
	backend.markerCalls = [][]MarkerObservation{nil, persistentQueueMarkerAt(t, base.Add(8*time.Second))}
	injector := &fakePersistentQueueInjector{}
	triggerRecorder := &fakePersistentQueueTrigger{}

	report, err := RunPersistentQueueSmoke(context.Background(), persistentQueueTestRequest(t), persistentQueueTestDeps(backend, injector, triggerRecorder.invoke(), clock))
	if err != nil {
		t.Fatalf("RunPersistentQueueSmoke() = err %v, want nil", err)
	}
	if report.Status() != "passed" {
		t.Fatalf("report.Status() = %q, want passed", report.Status())
	}
	if report.Scenario() != "persistent_queue" {
		t.Errorf("report.Scenario() = %q, want persistent_queue", report.Scenario())
	}

	tempoCheck := findCheck(t, report.Checks(), "tempo")
	if tempoCheck.Status != "passed" || tempoCheck.FailureStage != "none" {
		t.Errorf("tempo check = %q/%q, want passed/none", tempoCheck.Status, tempoCheck.FailureStage)
	}
	if evidenceInt(t, tempoCheck, "matched_spans") != 1 {
		t.Error("tempo matched_spans != 1：恢复后 marker 必须可查询")
	}

	collectorCheck := findCheck(t, report.Checks(), "collector")
	if collectorCheck.Status != "passed" {
		t.Errorf("collector check = %q, want passed", collectorCheck.Status)
	}
	if got := evidenceInt(t, collectorCheck, "queue_depth"); got != 25 {
		t.Errorf("queue_depth = %d, want 25：pause 期积压高水位必须进入报告", got)
	}
	if got := evidenceInt(t, collectorCheck, "exporter_sent"); got != 25 {
		t.Errorf("exporter_sent = %d, want 25：drain 期 sent delta", got)
	}
	if got := evidenceInt(t, collectorCheck, "duplicate_delivered"); got != 0 {
		t.Errorf("duplicate_delivered = %d, want 0", got)
	}

	if len(injector.pauseCalls) != 1 || injector.pauseCalls[0] != "tempo" {
		t.Errorf("pauseCalls = %v, want 仅 [tempo]", injector.pauseCalls)
	}
	if len(injector.unpauseCalls) != 1 || injector.unpauseCalls[0] != "tempo" {
		t.Errorf("unpauseCalls = %v, want 仅 [tempo]", injector.unpauseCalls)
	}
	if injector.restartCalls != 1 {
		t.Errorf("restartCalls = %d, want 1", injector.restartCalls)
	}
	if triggerRecorder.calls != 1 {
		t.Errorf("trigger 调用次数 = %d, want 1", triggerRecorder.calls)
	}
}

// 执行顺序契约：快照基线必须在 pause 前、restart 在积压快照之后、
// 最终快照在 drain 轮询之后。
func TestRunPersistentQueueSmokeExecutionOrder(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	var order []string
	backend := &fakePersistentQueueBackend{snapshots: persistentQueueSnapshots(), order: &order}
	backend.markerCalls = [][]MarkerObservation{nil, persistentQueueMarkerAt(t, base.Add(8*time.Second))}
	injector := &fakePersistentQueueInjector{order: &order}
	triggerRecorder := &fakePersistentQueueTrigger{order: &order}

	if _, err := RunPersistentQueueSmoke(context.Background(), persistentQueueTestRequest(t), persistentQueueTestDeps(backend, injector, triggerRecorder.invoke(), clock)); err != nil {
		t.Fatalf("RunPersistentQueueSmoke() = err %v", err)
	}

	wantOrder := []string{"snapshot", "pause", "trigger", "snapshot", "restart", "unpause", "query", "query", "snapshot"}
	if !slices.Equal(order, wantOrder) {
		t.Fatalf("执行顺序 = %v, want %v", order, wantOrder)
	}
}

// drain 窗口默认 120 秒：轮询目标的 deadline 必须是 resume+120s（以
// request.Deadline 为上界），保证恢复窗口受限（FR-012 受限时间窗口）。
func TestRunPersistentQueueSmokeDefaultsTo120SecondDrainWindow(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakePersistentQueueBackend{snapshots: persistentQueueSnapshots()}
	backend.markerCalls = [][]MarkerObservation{nil, persistentQueueMarkerAt(t, base.Add(8*time.Second))}
	injector := &fakePersistentQueueInjector{}

	if _, err := RunPersistentQueueSmoke(context.Background(), persistentQueueTestRequest(t), persistentQueueTestDeps(backend, injector, (&fakePersistentQueueTrigger{}).invoke(), clock)); err != nil {
		t.Fatalf("RunPersistentQueueSmoke() = err %v", err)
	}
	if len(backend.targets) == 0 {
		t.Fatal("backend 未收到任何 marker 查询")
	}
	wantDeadline := base.Add(PersistentQueueDrainWindow)
	for i, target := range backend.targets {
		if !target.Deadline.Equal(wantDeadline) {
			t.Errorf("第 %d 次查询 deadline = %v, want %v（resume+120s）", i, target.Deadline, wantDeadline)
		}
	}
}

// 显式 DrainWindow 覆盖默认值：查询 deadline 必须跟随请求配置。
func TestRunPersistentQueueSmokeHonorsExplicitDrainWindow(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakePersistentQueueBackend{snapshots: persistentQueueSnapshots()}
	backend.markerCalls = [][]MarkerObservation{nil, persistentQueueMarkerAt(t, base.Add(2*time.Second))}
	injector := &fakePersistentQueueInjector{}

	request := persistentQueueTestRequest(t)
	request.DrainWindow = 10 * time.Second
	if _, err := RunPersistentQueueSmoke(context.Background(), request, persistentQueueTestDeps(backend, injector, (&fakePersistentQueueTrigger{}).invoke(), clock)); err != nil {
		t.Fatalf("RunPersistentQueueSmoke() = err %v", err)
	}
	wantDeadline := base.Add(10 * time.Second)
	if !backend.targets[len(backend.targets)-1].Deadline.Equal(wantDeadline) {
		t.Errorf("查询 deadline = %v, want %v", backend.targets[len(backend.targets)-1].Deadline, wantDeadline)
	}
}

// drain 窗口耗尽仍未观察到 marker：报告失败（marker_missing），且 backend
// 必须已恢复——超时不得静默忽略，也不得把服务留在 paused 状态。
func TestRunPersistentQueueSmokeFailsWhenDrainWindowExpires(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakePersistentQueueBackend{snapshots: persistentQueueSnapshots()}
	injector := &fakePersistentQueueInjector{}

	report, err := RunPersistentQueueSmoke(context.Background(), persistentQueueTestRequest(t), persistentQueueTestDeps(backend, injector, (&fakePersistentQueueTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunPersistentQueueSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	tempoCheck := findCheck(t, report.Checks(), "tempo")
	if tempoCheck.Status != "failed" || tempoCheck.ErrorClass != "marker_missing" {
		t.Errorf("tempo check = %q/%q, want failed/marker_missing", tempoCheck.Status, tempoCheck.ErrorClass)
	}
	if len(injector.unpauseCalls) != 1 {
		t.Error("drain 超时后未恢复 backend")
	}
}

// 迟到 marker 隔离：ObservedAt 越过恢复窗口的观察必须被剔除，不得把
// 迟到的投递当作窗口内的恢复证据。
func TestRunPersistentQueueSmokeIsolatesLateMarker(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakePersistentQueueBackend{snapshots: persistentQueueSnapshots()}
	lateTime := base.Add(2 * time.Hour)
	backend.markerCalls = [][]MarkerObservation{persistentQueueMarkerAt(t, lateTime)}
	injector := &fakePersistentQueueInjector{}

	report, err := RunPersistentQueueSmoke(context.Background(), persistentQueueTestRequest(t), persistentQueueTestDeps(backend, injector, (&fakePersistentQueueTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunPersistentQueueSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed：迟到 marker 不得计入通过", report.Status())
	}
	tempoCheck := findCheck(t, report.Checks(), "tempo")
	if tempoCheck.Status != "failed" || tempoCheck.ErrorClass != "marker_missing" {
		t.Errorf("tempo check = %q/%q, want failed/marker_missing", tempoCheck.Status, tempoCheck.ErrorClass)
	}
}

// 积压为零：场景没有产生待发送记录，恢复能力无从证明——事实缺失不得
// 被猜测为通过。快照源永远返回零积压（覆盖轮询语义：有界等待后仍未见
// 积压上涨，如实按 no-backlog 失败）。
func TestRunPersistentQueueSmokeFailsWhenBacklogMissing(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakePersistentQueueBackend{snapshotFn: func(call int) (PersistentQueueSnapshot, error) {
		return PersistentQueueSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, Sent: 1000}, nil
	}}
	injector := &fakePersistentQueueInjector{}

	report, err := RunPersistentQueueSmoke(context.Background(), persistentQueueTestRequest(t), persistentQueueTestDeps(backend, injector, (&fakePersistentQueueTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunPersistentQueueSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	collectorCheck := findCheck(t, report.Checks(), "collector")
	if collectorCheck.ErrorClass != "unexpected_evidence" {
		t.Errorf("collector check ErrorClass = %q, want unexpected_evidence", collectorCheck.ErrorClass)
	}
}

// drain 不完整：marker 不可见且 sent delta 小于积压量，两份证据都说明积压
// 未被完全处理，不得判为通过。（marker 已命中的情形由 marker 一等证据承载
// 通过——scrape 相位下 sent 会计在 restart 后不可比，不能单独否决恢复事实。）
func TestRunPersistentQueueSmokeFailsWhenDrainIncomplete(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakePersistentQueueBackend{snapshotFn: func(call int) (PersistentQueueSnapshot, error) {
		switch {
		case call == 0:
			return PersistentQueueSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, Sent: 1000}, nil
		case call == 1:
			return PersistentQueueSnapshot{ComponentID: "otlp/tempo", QueueSize: 25, QueueCapacity: 10000, Sent: 1000}, nil
		default:
			return PersistentQueueSnapshot{ComponentID: "otlp/tempo", QueueSize: 10, QueueCapacity: 10000, Sent: 1015}, nil
		}
	}}
	injector := &fakePersistentQueueInjector{}

	report, err := RunPersistentQueueSmoke(context.Background(), persistentQueueTestRequest(t), persistentQueueTestDeps(backend, injector, (&fakePersistentQueueTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunPersistentQueueSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	collectorCheck := findCheck(t, report.Checks(), "collector")
	if collectorCheck.ErrorClass != "unexpected_evidence" {
		t.Errorf("collector check ErrorClass = %q, want unexpected_evidence", collectorCheck.ErrorClass)
	}
}

// at-least-once 语义：sent delta 大于积压量说明发生重复投递。报告必须
// 通过（声明语义内），但 duplicate_delivered 必须如实写入——duplicate
// 不得静默忽略，也不得宣称 exactly-once。
func TestRunPersistentQueueSmokeSurfacesDuplicateDelivery(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	snapshots := [][]PersistentQueueSnapshot{
		{{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, Sent: 1000}},
		{{ComponentID: "otlp/tempo", QueueSize: 25, QueueCapacity: 10000, Sent: 1000}},
		{{ComponentID: "otlp/tempo", QueueSize: 2, QueueCapacity: 10000, Sent: 1028}},
	}
	backend := &fakePersistentQueueBackend{snapshots: snapshots}
	backend.markerCalls = [][]MarkerObservation{nil, persistentQueueMarkerAt(t, base.Add(8*time.Second))}
	injector := &fakePersistentQueueInjector{}

	report, err := RunPersistentQueueSmoke(context.Background(), persistentQueueTestRequest(t), persistentQueueTestDeps(backend, injector, (&fakePersistentQueueTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunPersistentQueueSmoke() = err %v, want nil", err)
	}
	if report.Status() != "passed" {
		t.Errorf("report.Status() = %q, want passed：duplicate 是 at-least-once 语义内事实", report.Status())
	}
	collectorCheck := findCheck(t, report.Checks(), "collector")
	if got := evidenceInt(t, collectorCheck, "duplicate_delivered"); got != 3 {
		t.Errorf("duplicate_delivered = %d, want 3：重复投递必须被识别并写入报告", got)
	}
	if PersistentQueueDeliveryGuarantee != "at-least-once" {
		t.Errorf("PersistentQueueDeliveryGuarantee = %q, want at-least-once", PersistentQueueDeliveryGuarantee)
	}
}

// restart 失败：注入路径失败必须报告 export/export_failed，且 backend
// 必须恢复——restart 失败不是放弃恢复的借口。
func TestRunPersistentQueueSmokeRestartFailureStillRestoresBackend(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakePersistentQueueBackend{snapshots: persistentQueueSnapshots()}
	injector := &fakePersistentQueueInjector{restartErr: errors.New("compose: otel-collector crashed on restart")}

	report, err := RunPersistentQueueSmoke(context.Background(), persistentQueueTestRequest(t), persistentQueueTestDeps(backend, injector, (&fakePersistentQueueTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunPersistentQueueSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	collectorCheck := findCheck(t, report.Checks(), "collector")
	if collectorCheck.FailureStage != "export" || collectorCheck.ErrorClass != "export_failed" {
		t.Errorf("collector check = %q/%q, want export/export_failed", collectorCheck.FailureStage, collectorCheck.ErrorClass)
	}
	if len(injector.unpauseCalls) != 1 {
		t.Error("restart 失败后未恢复 backend")
	}
}

// pause 失败说明故障未注入：不得产生流量、不得 restart、不得 unpause。
func TestRunPersistentQueueSmokePauseFailureAborts(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakePersistentQueueBackend{snapshots: persistentQueueSnapshots()}
	injector := &fakePersistentQueueInjector{pauseErr: errors.New("compose: no such service")}
	triggerRecorder := &fakePersistentQueueTrigger{}

	report, err := RunPersistentQueueSmoke(context.Background(), persistentQueueTestRequest(t), persistentQueueTestDeps(backend, injector, triggerRecorder.invoke(), clock))
	if err != nil {
		t.Fatalf("RunPersistentQueueSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	if triggerRecorder.calls != 0 {
		t.Errorf("trigger 调用次数 = %d, want 0", triggerRecorder.calls)
	}
	if injector.restartCalls != 0 {
		t.Errorf("restartCalls = %d, want 0", injector.restartCalls)
	}
	if len(injector.unpauseCalls) != 0 {
		t.Errorf("unpauseCalls = %v, want 空", injector.unpauseCalls)
	}
}

// unpause 失败：归因可能全部正确，但 backend 可能仍处于 paused——
// cleanup failed + paused-service residual + 整体 failed。
func TestRunPersistentQueueSmokeUnpauseFailureReportsResidual(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakePersistentQueueBackend{snapshots: persistentQueueSnapshots()}
	backend.markerCalls = [][]MarkerObservation{nil, persistentQueueMarkerAt(t, base.Add(8*time.Second))}
	injector := &fakePersistentQueueInjector{unpauseErr: errors.New("compose: container restarting")}

	report, err := RunPersistentQueueSmoke(context.Background(), persistentQueueTestRequest(t), persistentQueueTestDeps(backend, injector, (&fakePersistentQueueTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunPersistentQueueSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	cleanup := report.Cleanup()
	if cleanup.Status != "failed" {
		t.Errorf("cleanup.Status = %q, want failed", cleanup.Status)
	}
	if !slices.Contains(cleanup.ResidualResources, "paused-service") {
		t.Errorf("ResidualResources = %v, 必须包含 paused-service", cleanup.ResidualResources)
	}
}

// 请求与依赖校验：非法输入必须直接报错，不产生任何外部调用。
func TestRunPersistentQueueSmokeRejectsInvalidRequests(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	validBackend := &fakePersistentQueueBackend{snapshots: persistentQueueSnapshots()}
	validBackend.markerCalls = [][]MarkerObservation{nil, persistentQueueMarkerAt(t, base.Add(8*time.Second))}
	validDeps := persistentQueueTestDeps(validBackend, &fakePersistentQueueInjector{}, (&fakePersistentQueueTrigger{}).invoke(), clock)
	validRequest := persistentQueueTestRequest(t)

	tests := []struct {
		name   string
		mutate func(*PersistentQueueSmokeRequest, *PersistentQueueSmokeDependencies)
	}{
		{"profile 不在允许集", func(r *PersistentQueueSmokeRequest, _ *PersistentQueueSmokeDependencies) { r.Profile = "unknown" }},
		{"deadline 为零值", func(r *PersistentQueueSmokeRequest, _ *PersistentQueueSmokeDependencies) { r.Deadline = time.Time{} }},
		{"deadline 已过期", func(r *PersistentQueueSmokeRequest, _ *PersistentQueueSmokeDependencies) {
			r.Deadline = base.Add(-time.Second)
		}},
		{"service 为空", func(r *PersistentQueueSmokeRequest, _ *PersistentQueueSmokeDependencies) { r.BackendService = "" }},
		{"component 为空", func(r *PersistentQueueSmokeRequest, _ *PersistentQueueSmokeDependencies) { r.ComponentID = "" }},
		{"service 含 shell 元字符", func(r *PersistentQueueSmokeRequest, _ *PersistentQueueSmokeDependencies) {
			r.BackendService = "tempo; touch /tmp/x"
		}},
		{"DrainWindow 为负", func(r *PersistentQueueSmokeRequest, _ *PersistentQueueSmokeDependencies) {
			r.DrainWindow = -time.Second
		}},
		{"Backend 缺失", func(_ *PersistentQueueSmokeRequest, d *PersistentQueueSmokeDependencies) { d.Backend = nil }},
		{"Injector 缺失", func(_ *PersistentQueueSmokeRequest, d *PersistentQueueSmokeDependencies) { d.Injector = nil }},
		{"Trigger 缺失", func(_ *PersistentQueueSmokeRequest, d *PersistentQueueSmokeDependencies) { d.Trigger = nil }},
		{"Clock 缺失", func(_ *PersistentQueueSmokeRequest, d *PersistentQueueSmokeDependencies) { d.Clock = nil }},
		{"PollInterval 非正", func(_ *PersistentQueueSmokeRequest, d *PersistentQueueSmokeDependencies) { d.PollInterval = 0 }},
		{"IdentityFactory 返回不安全 marker", func(_ *PersistentQueueSmokeRequest, d *PersistentQueueSmokeDependencies) {
			d.IdentityFactory = func(context.Context) (PersistentQueueSmokeIdentity, error) {
				return PersistentQueueSmokeIdentity{RunID: "run-persistent-queue-0001", Marker: "bad marker with spaces"}, nil
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mutatedRequest := validRequest
			mutatedDeps := validDeps
			tc.mutate(&mutatedRequest, &mutatedDeps)
			if _, err := RunPersistentQueueSmoke(context.Background(), mutatedRequest, mutatedDeps); err == nil {
				t.Error("RunPersistentQueueSmoke() = nil error, want 校验错误")
			}
		})
	}
}

// ctx 取消必须中止运行并返回错误，不得产出半成品报告。
func TestRunPersistentQueueSmokeContextCancellationAborts(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakePersistentQueueBackend{snapshots: persistentQueueSnapshots()}
	injector := &fakePersistentQueueInjector{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := RunPersistentQueueSmoke(ctx, persistentQueueTestRequest(t), persistentQueueTestDeps(backend, injector, (&fakePersistentQueueTrigger{}).invoke(), clock))
	if err == nil {
		t.Fatalf("RunPersistentQueueSmoke() = %v, want ctx 错误", report)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(injector.pauseCalls) != 0 {
		t.Errorf("pauseCalls = %v, want 空：取消发生在任何注入之前", injector.pauseCalls)
	}
}

// M-2（审查修复）：跨 Collector 重启的 sent counter 会归零——reset-aware
// 会计的两个分支此前零契约覆盖。钉死：counter 回退 + marker 命中 → 通过
// 且 exporter_sent 呈现新进程绝对值、duplicate 记 0 不宣称。
func TestRunPersistentQueueSmokeCounterResetWithMarkerPasses(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakePersistentQueueBackend{snapshotFn: func(call int) (PersistentQueueSnapshot, error) {
		switch {
		case call == 0:
			return PersistentQueueSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, Sent: 1000}, nil
		case call == 1:
			return PersistentQueueSnapshot{ComponentID: "otlp/tempo", QueueSize: 4, QueueCapacity: 10000, Sent: 1000}, nil
		default:
			// 重启后新进程：sent 从 2 开始累计（< duringPause 的 1000 → reset）。
			return PersistentQueueSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, Sent: 2}, nil
		}
	}}
	backend.markerCalls = [][]MarkerObservation{nil, persistentQueueMarkerAt(t, base.Add(8*time.Second))}
	injector := &fakePersistentQueueInjector{}

	report, err := RunPersistentQueueSmoke(context.Background(), persistentQueueTestRequest(t), persistentQueueTestDeps(backend, injector, (&fakePersistentQueueTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunPersistentQueueSmoke() = err %v, want nil", err)
	}
	if report.Status() != "passed" {
		t.Fatalf("report.Status() = %q, want passed：marker 命中是恢复的一等证据", report.Status())
	}
	collectorCheck := findCheck(t, report.Checks(), "collector")
	if got := evidenceInt(t, collectorCheck, "exporter_sent"); got != 2 {
		t.Errorf("exporter_sent = %d, want 2（新进程绝对值）", got)
	}
	if got := evidenceInt(t, collectorCheck, "duplicate_delivered"); got != 0 {
		t.Errorf("duplicate_delivered = %d, want 0（跨重启不可推导、不宣称）", got)
	}
}

// M-2：counter 回退 + marker 缺失 → no-marker 失败。这是"无 marker 不可能
// 通过"的回归钉：reset 分支绝不能成为绕过恢复证据的假通过路径。
func TestRunPersistentQueueSmokeCounterResetWithoutMarkerFails(t *testing.T) {
	base := persistentQueueTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakePersistentQueueBackend{snapshotFn: func(call int) (PersistentQueueSnapshot, error) {
		switch {
		case call == 0:
			return PersistentQueueSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, Sent: 1000}, nil
		case call == 1:
			return PersistentQueueSnapshot{ComponentID: "otlp/tempo", QueueSize: 4, QueueCapacity: 10000, Sent: 1000}, nil
		default:
			return PersistentQueueSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, Sent: 2}, nil
		}
	}}
	// markerCalls 为空：drain 窗口内 marker 永不出现。
	injector := &fakePersistentQueueInjector{}

	report, err := RunPersistentQueueSmoke(context.Background(), persistentQueueTestRequest(t), persistentQueueTestDeps(backend, injector, (&fakePersistentQueueTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunPersistentQueueSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Fatalf("report.Status() = %q, want failed：reset 分支不得绕过 marker 证据", report.Status())
	}
	for _, check := range report.Checks() {
		if check.Backend == "tempo" && check.Status == "passed" {
			t.Error("tempo check passed，want marker_missing 失败")
		}
	}
}
