package smoke

import (
	"context"
	"errors"
	"testing"
	"time"
)

// T112 独立 exporter 故障场景契约测试（RED 先行，T122 实现 exporter_failure_runner.go
// 使其 GREEN）。
//
// 覆盖的生产风险：单出口故障被误判为整体观测失效、或故障归因到错误组件。
// 契约要求 runner 以「基线 HTTP → snapshot before → pause 单后端 → 故障期 HTTP →
// snapshot after → unpause（任何退出路径）」的顺序执行，并输出三组可机器判读的
// 事实：
//
// 1. api check：故障前/故障中 HTTP status 与 body hash 完全一致（FR-007 业务结果
//    不被观测故障改写），evidence 携带 `response_status`；
// 2. collector check：12 个组件限定证据键 `<prefix>_sent_delta`、
//    `<prefix>_failed_delta`、`<prefix>_enqueue_delta`、`<prefix>_queue_delta`
//    （prefix ∈ tempo/loki/langfuse，1:1 对应 Collector exporter 组件），
//    被注入组件 failed_delta > 0、其它组件 failed_delta == 0 且至少一个其它
//    出口 sent_delta > 0（正面「继续投递」证据，不能只证明「没失败」）；
// 3. cleanup：unpause 失败时 status=failed 且 ResidualResources 含
//    `paused-service`——服务可能仍处于 paused 状态，必须进入报告。
//
// 报告模型说明：schema 的 evidence additionalProperties 本就开放标量键；Go 侧
// `allowedEvidenceKeysByBackend` 需要 T122 同步补充上述 12 个 collector 键与
// `paused-service` residual，这是「报告分别记录 component delta」门控的必然要求。

type fakeExporterFailureClock struct{ now time.Time }

func (f *fakeExporterFailureClock) Now() time.Time { return f.now }

// Wait 同步推进时钟：runner 的有界轮询依赖 clock.Now() 前进来终止循环，
// 固定不走的时钟会把"等证据"变成死循环。
func (f *fakeExporterFailureClock) Wait(_ context.Context, duration time.Duration) error {
	f.now = f.now.Add(duration)
	return nil
}

type fakeExporterFailureBackend struct {
	snapshots   [][]ExporterHealthSnapshot
	snapshotErr []error
	snapshotFn  func(call int) ([]ExporterHealthSnapshot, error)
	calls       int
	order       *[]string
}

func (f *fakeExporterFailureBackend) SnapshotCollectorHealth(context.Context) ([]ExporterHealthSnapshot, error) {
	index := f.calls
	f.calls++
	if f.order != nil {
		*f.order = append(*f.order, "snapshot")
	}
	if f.snapshotFn != nil {
		return f.snapshotFn(index)
	}
	if index < len(f.snapshotErr) && f.snapshotErr[index] != nil {
		return nil, f.snapshotErr[index]
	}
	if index >= len(f.snapshots) {
		return nil, errors.New("fakeExporterFailureBackend: unexpected snapshot call")
	}
	return f.snapshots[index], nil
}

type fakeExporterFailureInjector struct {
	pauseCalls   []string
	unpauseCalls []string
	pauseErr     error
	unpauseErr   error
	order        *[]string
}

func (f *fakeExporterFailureInjector) Pause(_ context.Context, service string) error {
	f.pauseCalls = append(f.pauseCalls, service)
	if f.order != nil {
		*f.order = append(*f.order, "pause")
	}
	return f.pauseErr
}

func (f *fakeExporterFailureInjector) Unpause(_ context.Context, service string) error {
	f.unpauseCalls = append(f.unpauseCalls, service)
	if f.order != nil {
		*f.order = append(*f.order, "unpause")
	}
	return f.unpauseErr
}

type fakeExporterFailureTriggerCall struct {
	status   int
	bodyHash string
	err      error
}

type fakeExporterFailureTrigger struct {
	calls []fakeExporterFailureTriggerCall
	order *[]string
}

func (f *fakeExporterFailureTrigger) invoke(call fakeExporterFailureTriggerCall) ExporterFailureTrigger {
	return func(context.Context) (int, string, error) {
		f.calls = append(f.calls, call)
		if f.order != nil {
			*f.order = append(*f.order, "trigger")
		}
		return call.status, call.bodyHash, call.err
	}
}

func exporterFailureTarget() ExporterFailureSmokeTarget {
	return ExporterFailureSmokeTarget{
		BackendService: "tempo",
		ComponentID:    "otlp/tempo",
		EvidencePrefix: "tempo",
	}
}

// 健康快照夹具：故障注入点在前两个快照之间，tempo 失败、loki 继续投递、
// langfuse 无流量但未失败。
func exporterFailureSnapshots() [][]ExporterHealthSnapshot {
	return [][]ExporterHealthSnapshot{
		{
			{ComponentID: "otlp/tempo", Sent: 100, SendFailed: 0, QueueSize: 0, QueueCapacity: 10000},
			{ComponentID: "otlphttp/loki", Sent: 50, SendFailed: 0, QueueSize: 0, QueueCapacity: 10000},
			{ComponentID: "otlphttp/langfuse", Sent: 30, SendFailed: 0, QueueSize: 0, QueueCapacity: 10000},
		},
		{
			{ComponentID: "otlp/tempo", Sent: 100, SendFailed: 12, EnqueueFailed: 0, QueueSize: 340, QueueCapacity: 10000},
			{ComponentID: "otlphttp/loki", Sent: 57, SendFailed: 0, QueueSize: 2, QueueCapacity: 10000},
			{ComponentID: "otlphttp/langfuse", Sent: 30, SendFailed: 0, QueueSize: 0, QueueCapacity: 10000},
		},
	}
}

func exporterFailureTestDeps(
	backend *fakeExporterFailureBackend,
	injector *fakeExporterFailureInjector,
	trigger ExporterFailureTrigger,
) ExporterFailureSmokeDependencies {
	return ExporterFailureSmokeDependencies{
		Backend:      backend,
		Injector:     injector,
		Trigger:      trigger,
		Clock:        &fakeExporterFailureClock{now: time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)},
		PollInterval: time.Second,
	}
}

func exporterFailureRequest() ExporterFailureSmokeRequest {
	clock := &fakeExporterFailureClock{now: time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)}
	return ExporterFailureSmokeRequest{
		Deadline: clock.now.Add(2 * time.Minute),
		Profile:  "grafana",
		Target:   exporterFailureTarget(),
	}
}

func evidenceInt(t *testing.T, check BackendCheck, key string) int64 {
	t.Helper()
	value, ok := check.Evidence[key]
	if !ok {
		t.Fatalf("check[%s] 缺少证据键 %q", check.Backend, key)
	}
	typed, ok := value.(int64)
	if !ok {
		t.Fatalf("check[%s] 证据键 %q 类型 = %T, want int64", check.Backend, key, value)
	}
	return typed
}

func findCheck(t *testing.T, checks []BackendCheck, backend string) BackendCheck {
	t.Helper()
	for _, check := range checks {
		if check.Backend == backend {
			return check
		}
	}
	t.Fatalf("报告缺少 backend %q 的 check", backend)
	return BackendCheck{}
}

// 主路径：tempo 单出口故障期间业务结果不变，报告分别记录 tempo 的失败/积压
// delta、loki 的继续投递成功证据与 langfuse 的未受影响事实。
func TestRunExporterFailureSmokeAttributesFaultAndPreservesHTTPResult(t *testing.T) {
	backend := &fakeExporterFailureBackend{snapshots: exporterFailureSnapshots()}
	injector := &fakeExporterFailureInjector{}
	recorder := &fakeExporterFailureTrigger{}
	trigger := func(ctx context.Context) (int, string, error) {
		return recorder.invoke(fakeExporterFailureTriggerCall{status: 200, bodyHash: "hash-baseline"})(ctx)
	}

	report, err := RunExporterFailureSmoke(context.Background(), exporterFailureRequest(), exporterFailureTestDeps(backend, injector, trigger))
	if err != nil {
		t.Fatalf("RunExporterFailureSmoke() = err %v, want nil", err)
	}
	if report.Status() != "passed" {
		t.Fatalf("report.Status() = %q, want passed", report.Status())
	}
	if report.Scenario() != "exporter_failure" {
		t.Errorf("report.Scenario() = %q, want exporter_failure", report.Scenario())
	}

	checks := report.Checks()
	apiCheck := findCheck(t, checks, "api")
	if apiCheck.Status != "passed" || apiCheck.FailureStage != "none" {
		t.Errorf("api check = %q/%q, want passed/none", apiCheck.Status, apiCheck.FailureStage)
	}
	if evidenceInt(t, apiCheck, "response_status") != 200 {
		t.Error("api check 的 response_status 不是故障期 HTTP 200")
	}

	collectorCheck := findCheck(t, checks, "collector")
	if collectorCheck.Status != "passed" {
		t.Errorf("collector check = %q, want passed", collectorCheck.Status)
	}
	if got := evidenceInt(t, collectorCheck, "tempo_failed_delta"); got != 12 {
		t.Errorf("tempo_failed_delta = %d, want 12：send_failed 必须归因到被注入组件", got)
	}
	if got := evidenceInt(t, collectorCheck, "tempo_queue_delta"); got != 340 {
		t.Errorf("tempo_queue_delta = %d, want 340：故障期队列积压必须进入报告", got)
	}
	if got := evidenceInt(t, collectorCheck, "tempo_sent_delta"); got != 0 {
		t.Errorf("tempo_sent_delta = %d, want 0", got)
	}
	if got := evidenceInt(t, collectorCheck, "loki_sent_delta"); got != 7 {
		t.Errorf("loki_sent_delta = %d, want 7：其它出口继续投递的成功证据", got)
	}
	if got := evidenceInt(t, collectorCheck, "loki_failed_delta"); got != 0 {
		t.Errorf("loki_failed_delta = %d, want 0：其它出口不得被拖垮", got)
	}
	if got := evidenceInt(t, collectorCheck, "langfuse_failed_delta"); got != 0 {
		t.Errorf("langfuse_failed_delta = %d, want 0", got)
	}
	if got := evidenceInt(t, collectorCheck, "langfuse_sent_delta"); got != 0 {
		t.Errorf("langfuse_sent_delta = %d, want 0", got)
	}
	if got := evidenceInt(t, collectorCheck, "langfuse_queue_delta"); got != 0 {
		t.Errorf("langfuse_queue_delta = %d, want 0", got)
	}

	if cleanup := report.Cleanup(); cleanup.Status != "completed" {
		t.Errorf("cleanup.Status = %q, want completed", cleanup.Status)
	}
	if len(injector.pauseCalls) != 1 || injector.pauseCalls[0] != "tempo" {
		t.Errorf("pauseCalls = %v, want 仅 [tempo]", injector.pauseCalls)
	}
	if len(injector.unpauseCalls) != 1 || injector.unpauseCalls[0] != "tempo" {
		t.Errorf("unpauseCalls = %v, want 仅 [tempo]", injector.unpauseCalls)
	}
}

// 执行顺序是安全契约：基线 HTTP → before 快照 → pause → 故障期 HTTP →
// after 快照 → unpause。顺序错乱会产生无法归因的假证据（例如 pause 前取
// after 快照会把故障计入错误的 delta 窗口）。
func TestRunExporterFailureSmokeExecutionOrder(t *testing.T) {
	var order []string
	backend := &fakeExporterFailureBackend{snapshots: exporterFailureSnapshots(), order: &order}
	injector := &fakeExporterFailureInjector{order: &order}
	recorder := &fakeExporterFailureTrigger{order: &order}
	trigger := func(ctx context.Context) (int, string, error) {
		return recorder.invoke(fakeExporterFailureTriggerCall{status: 200, bodyHash: "h"})(ctx)
	}

	if _, err := RunExporterFailureSmoke(context.Background(), exporterFailureRequest(), exporterFailureTestDeps(backend, injector, trigger)); err != nil {
		t.Fatalf("RunExporterFailureSmoke() = err %v", err)
	}

	wantOrder := []string{"trigger", "snapshot", "pause", "trigger", "snapshot", "unpause"}
	if len(order) != len(wantOrder) {
		t.Fatalf("执行顺序 = %v, want %v", order, wantOrder)
	}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("执行顺序 = %v, want %v", order, wantOrder)
		}
	}
}

// 基线 HTTP 本身失败（业务侧故障）时不得继续注入：报告失败于 api stage，
// 且 pause 绝不能发生——故障注入场景不能建立在已经失败的业务基线上。
func TestRunExporterFailureSmokeBaselineHTTPFailureAbortsBeforeInjection(t *testing.T) {
	backend := &fakeExporterFailureBackend{snapshots: exporterFailureSnapshots()}
	injector := &fakeExporterFailureInjector{}
	trigger := (&fakeExporterFailureTrigger{}).invoke(fakeExporterFailureTriggerCall{status: 503, bodyHash: "down", err: nil})

	report, err := RunExporterFailureSmoke(context.Background(), exporterFailureRequest(), exporterFailureTestDeps(backend, injector, trigger))
	if err != nil {
		t.Fatalf("RunExporterFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	apiCheck := findCheck(t, report.Checks(), "api")
	if apiCheck.Status != "failed" || apiCheck.FailureStage != "api" {
		t.Errorf("api check = %q/%q, want failed/api", apiCheck.Status, apiCheck.FailureStage)
	}
	if len(injector.pauseCalls) != 0 {
		t.Errorf("pauseCalls = %v, want 空：基线失败后不得注入故障", injector.pauseCalls)
	}
}

// 故障期 HTTP 结果被改写（status 或 body hash 变化）是 FR-007 违反：
// 即使 exporter 归因全部正确，报告也必须失败。
func TestRunExporterFailureSmokeFailsWhenHTTPResultChanged(t *testing.T) {
	for _, changed := range []fakeExporterFailureTriggerCall{
		{status: 200, bodyHash: "different-hash"},
		{status: 500, bodyHash: "hash-baseline"},
	} {
		backend := &fakeExporterFailureBackend{snapshots: exporterFailureSnapshots()}
		injector := &fakeExporterFailureInjector{}
		recorder := &fakeExporterFailureTrigger{}
		// 第一次调用为基线（成功），第二次返回被改写的故障期结果。
		sequence := triggerSequence(recorder, fakeExporterFailureTriggerCall{status: 200, bodyHash: "hash-baseline"}, changed)

		report, err := RunExporterFailureSmoke(context.Background(), exporterFailureRequest(), exporterFailureTestDeps(backend, injector, sequence))
		if err != nil {
			t.Fatalf("RunExporterFailureSmoke() = err %v, want 报告内失败", err)
		}
		if report.Status() != "failed" {
			t.Errorf("changed=%+v 时 report.Status() = %q, want failed", changed, report.Status())
		}
		apiCheck := findCheck(t, report.Checks(), "api")
		if apiCheck.Status != "failed" {
			t.Errorf("changed=%+v 时 api check = %q, want failed", changed, apiCheck.Status)
		}
		if len(injector.unpauseCalls) != 1 {
			t.Errorf("changed=%+v 时 unpause 次数 = %d, want 1：失败路径也必须恢复", changed, len(injector.unpauseCalls))
		}
	}
}

// triggerSequence 按顺序返回预设调用结果。
func triggerSequence(recorder *fakeExporterFailureTrigger, calls ...fakeExporterFailureTriggerCall) ExporterFailureTrigger {
	return func(ctx context.Context) (int, string, error) {
		index := len(recorder.calls)
		if index >= len(calls) {
			return 0, "", errors.New("triggerSequence: 调用次数超出预设")
		}
		return recorder.invoke(calls[index])(ctx)
	}
}

// before 快照查询失败：报告失败于 query stage，且 unpause 必须发生
// （pause 已执行，服务不能留在 paused 状态）。
func TestRunExporterFailureSmokeSnapshotQueryFailureStillRestores(t *testing.T) {
	backend := &fakeExporterFailureBackend{
		snapshots:   [][]ExporterHealthSnapshot{nil},
		snapshotErr: []error{nil, errors.New("prometheus query timeout")},
	}
	injector := &fakeExporterFailureInjector{}
	recorder := &fakeExporterFailureTrigger{}
	trigger := func(ctx context.Context) (int, string, error) {
		return recorder.invoke(fakeExporterFailureTriggerCall{status: 200, bodyHash: "h"})(ctx)
	}

	report, err := RunExporterFailureSmoke(context.Background(), exporterFailureRequest(), exporterFailureTestDeps(backend, injector, trigger))
	if err != nil {
		t.Fatalf("RunExporterFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	collectorCheck := findCheck(t, report.Checks(), "collector")
	if collectorCheck.FailureStage != "query" || collectorCheck.ErrorClass != "query_failed" {
		t.Errorf("collector check = %q/%q, want query/query_failed", collectorCheck.FailureStage, collectorCheck.ErrorClass)
	}
	if len(injector.unpauseCalls) != 1 {
		t.Error("快照失败后未执行 unpause：服务可能被留在 paused 状态")
	}
}

// pause 失败说明注入未生效：不得执行故障期快照比对，也不得 unpause
// （与 failure 包 WithRestore 契约一致：未注入则无恢复义务）。
func TestRunExporterFailureSmokePauseFailureAbortsWithoutUnpause(t *testing.T) {
	backend := &fakeExporterFailureBackend{snapshots: exporterFailureSnapshots()}
	injector := &fakeExporterFailureInjector{pauseErr: errors.New("compose: no such service")}
	recorder := &fakeExporterFailureTrigger{}
	trigger := func(ctx context.Context) (int, string, error) {
		return recorder.invoke(fakeExporterFailureTriggerCall{status: 200, bodyHash: "h"})(ctx)
	}

	report, err := RunExporterFailureSmoke(context.Background(), exporterFailureRequest(), exporterFailureTestDeps(backend, injector, trigger))
	if err != nil {
		t.Fatalf("RunExporterFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	collectorCheck := findCheck(t, report.Checks(), "collector")
	if collectorCheck.FailureStage != "export" || collectorCheck.ErrorClass != "export_failed" {
		t.Errorf("collector check = %q/%q, want export/export_failed", collectorCheck.FailureStage, collectorCheck.ErrorClass)
	}
	if len(injector.unpauseCalls) != 0 {
		t.Errorf("unpauseCalls = %v, want 空", injector.unpauseCalls)
	}
}

// 被注入组件没有产生 send_failed 证据：注入无效或指标映射错误，必须失败
// 而不是默认通过——观测事实缺失不允许被猜测。快照源恒定无失败计数，
// 覆盖轮询语义：有界等待耗尽后按 unexpected_evidence 失败。
func TestRunExporterFailureSmokeFailsWhenNoFailureEvidenceOnTarget(t *testing.T) {
	backend := &fakeExporterFailureBackend{snapshotFn: func(call int) ([]ExporterHealthSnapshot, error) {
		switch {
		case call == 0:
			return []ExporterHealthSnapshot{
				{ComponentID: "otlp/tempo", Sent: 100, QueueCapacity: 10000},
				{ComponentID: "otlphttp/loki", Sent: 50, QueueCapacity: 10000},
				{ComponentID: "otlphttp/langfuse", Sent: 30, QueueCapacity: 10000},
			}, nil
		default:
			return []ExporterHealthSnapshot{
				{ComponentID: "otlp/tempo", Sent: 105, QueueCapacity: 10000},
				{ComponentID: "otlphttp/loki", Sent: 57, QueueCapacity: 10000},
				{ComponentID: "otlphttp/langfuse", Sent: 30, QueueCapacity: 10000},
			}, nil
		}
	}}
	injector := &fakeExporterFailureInjector{}
	recorder := &fakeExporterFailureTrigger{}
	trigger := func(ctx context.Context) (int, string, error) {
		return recorder.invoke(fakeExporterFailureTriggerCall{status: 200, bodyHash: "h"})(ctx)
	}

	report, err := RunExporterFailureSmoke(context.Background(), exporterFailureRequest(), exporterFailureTestDeps(backend, injector, trigger))
	if err != nil {
		t.Fatalf("RunExporterFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	collectorCheck := findCheck(t, report.Checks(), "collector")
	if collectorCheck.ErrorClass != "unexpected_evidence" {
		t.Errorf("collector check ErrorClass = %q, want unexpected_evidence", collectorCheck.ErrorClass)
	}
}

// 其它出口出现 send_failed 说明故障扩散（例如 pause 了错误的后端），
// 属于归因失败而非通过。
func TestRunExporterFailureSmokeFailsWhenOtherExporterAlsoFailed(t *testing.T) {
	snapshots := [][]ExporterHealthSnapshot{
		{
			{ComponentID: "otlp/tempo", Sent: 100, SendFailed: 0, QueueSize: 0, QueueCapacity: 10000},
			{ComponentID: "otlphttp/loki", Sent: 50, SendFailed: 0, QueueSize: 0, QueueCapacity: 10000},
			{ComponentID: "otlphttp/langfuse", Sent: 30, SendFailed: 0, QueueSize: 0, QueueCapacity: 10000},
		},
		{
			{ComponentID: "otlp/tempo", Sent: 100, SendFailed: 12, QueueSize: 340, QueueCapacity: 10000},
			{ComponentID: "otlphttp/loki", Sent: 57, SendFailed: 3, QueueSize: 0, QueueCapacity: 10000},
			{ComponentID: "otlphttp/langfuse", Sent: 30, SendFailed: 0, QueueSize: 0, QueueCapacity: 10000},
		},
	}
	backend := &fakeExporterFailureBackend{snapshots: snapshots}
	injector := &fakeExporterFailureInjector{}
	recorder := &fakeExporterFailureTrigger{}
	trigger := func(ctx context.Context) (int, string, error) {
		return recorder.invoke(fakeExporterFailureTriggerCall{status: 200, bodyHash: "h"})(ctx)
	}

	report, err := RunExporterFailureSmoke(context.Background(), exporterFailureRequest(), exporterFailureTestDeps(backend, injector, trigger))
	if err != nil {
		t.Fatalf("RunExporterFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
}

// unpause 失败：归因可能全部正确，但服务可能仍处于 paused——cleanup 必须
// failed 且报告 `paused-service` residual，整体状态 failed。
func TestRunExporterFailureSmokeReportsPausedServiceResidual(t *testing.T) {
	backend := &fakeExporterFailureBackend{snapshots: exporterFailureSnapshots()}
	injector := &fakeExporterFailureInjector{unpauseErr: errors.New("compose: container restarting")}
	recorder := &fakeExporterFailureTrigger{}
	trigger := func(ctx context.Context) (int, string, error) {
		return recorder.invoke(fakeExporterFailureTriggerCall{status: 200, bodyHash: "h"})(ctx)
	}

	report, err := RunExporterFailureSmoke(context.Background(), exporterFailureRequest(), exporterFailureTestDeps(backend, injector, trigger))
	if err != nil {
		t.Fatalf("RunExporterFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	cleanup := report.Cleanup()
	if cleanup.Status != "failed" {
		t.Errorf("cleanup.Status = %q, want failed", cleanup.Status)
	}
	residualFound := false
	for _, resource := range cleanup.ResidualResources {
		if resource == "paused-service" {
			residualFound = true
		}
	}
	if !residualFound {
		t.Errorf("ResidualResources = %v, 必须包含 paused-service", cleanup.ResidualResources)
	}
}

// 依赖与请求校验：无效 profile、过期 deadline、缺失注入目标、危险 service
// 名必须直接报错，不进入任何外部调用。
func TestRunExporterFailureSmokeRejectsInvalidRequests(t *testing.T) {
	clock := &fakeExporterFailureClock{now: time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)}
	validDeps := exporterFailureTestDeps(&fakeExporterFailureBackend{snapshots: exporterFailureSnapshots()}, &fakeExporterFailureInjector{}, func(context.Context) (int, string, error) { return 200, "h", nil })

	request := exporterFailureRequest()
	tests := []struct {
		name   string
		mutate func(*ExporterFailureSmokeRequest, *ExporterFailureSmokeDependencies)
	}{
		{"profile 不在允许集", func(r *ExporterFailureSmokeRequest, _ *ExporterFailureSmokeDependencies) { r.Profile = "unknown" }},
		{"deadline 为零值", func(r *ExporterFailureSmokeRequest, _ *ExporterFailureSmokeDependencies) { r.Deadline = time.Time{} }},
		{"deadline 已过期", func(r *ExporterFailureSmokeRequest, _ *ExporterFailureSmokeDependencies) {
			r.Deadline = clock.now.Add(-time.Second)
		}},
		{"目标 service 为空", func(r *ExporterFailureSmokeRequest, _ *ExporterFailureSmokeDependencies) {
			r.Target.BackendService = ""
		}},
		{"目标 component 为空", func(r *ExporterFailureSmokeRequest, _ *ExporterFailureSmokeDependencies) { r.Target.ComponentID = "" }},
		{"证据前缀为空", func(r *ExporterFailureSmokeRequest, _ *ExporterFailureSmokeDependencies) {
			r.Target.EvidencePrefix = ""
		}},
		{"service 含 shell 元字符", func(r *ExporterFailureSmokeRequest, _ *ExporterFailureSmokeDependencies) {
			r.Target.BackendService = "tempo; touch /tmp/x"
		}},
		{"证据前缀格式非法", func(r *ExporterFailureSmokeRequest, _ *ExporterFailureSmokeDependencies) {
			r.Target.EvidencePrefix = "TEMPO!"
		}},
		{"Backend 缺失", func(_ *ExporterFailureSmokeRequest, d *ExporterFailureSmokeDependencies) { d.Backend = nil }},
		{"Injector 缺失", func(_ *ExporterFailureSmokeRequest, d *ExporterFailureSmokeDependencies) { d.Injector = nil }},
		{"Trigger 缺失", func(_ *ExporterFailureSmokeRequest, d *ExporterFailureSmokeDependencies) { d.Trigger = nil }},
		{"Clock 缺失", func(_ *ExporterFailureSmokeRequest, d *ExporterFailureSmokeDependencies) { d.Clock = nil }},
		{"PollInterval 非正", func(_ *ExporterFailureSmokeRequest, d *ExporterFailureSmokeDependencies) { d.PollInterval = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mutatedRequest := request
			mutatedDeps := validDeps
			tc.mutate(&mutatedRequest, &mutatedDeps)
			if _, err := RunExporterFailureSmoke(context.Background(), mutatedRequest, mutatedDeps); err == nil {
				t.Error("RunExporterFailureSmoke() = nil error, want 校验错误")
			}
		})
	}
}

// ctx 取消必须中止运行并返回错误，不得产出半成品报告。
func TestRunExporterFailureSmokeContextCancellationAborts(t *testing.T) {
	backend := &fakeExporterFailureBackend{snapshots: exporterFailureSnapshots()}
	injector := &fakeExporterFailureInjector{}
	recorder := &fakeExporterFailureTrigger{}
	trigger := func(ctx context.Context) (int, string, error) {
		return recorder.invoke(fakeExporterFailureTriggerCall{status: 200, bodyHash: "h"})(ctx)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := RunExporterFailureSmoke(ctx, exporterFailureRequest(), exporterFailureTestDeps(backend, injector, trigger))
	if err == nil {
		t.Fatalf("RunExporterFailureSmoke() = %v, want ctx 错误", report)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(injector.unpauseCalls) != 0 {
		t.Errorf("unpauseCalls = %v, want 空：取消发生在任何注入之前", injector.unpauseCalls)
	}
}

// M-1（审查修复）：失败证据的可见性受 Prometheus scrape 相位影响——
// pause 后前几次快照拿不到 send_failed 增量。runner 必须有界轮询直到
// 归因证据完整，而不是单次快照即判 unexpected_evidence。
func TestRunExporterFailureSmokeWaitsForScrapePhaseEvidence(t *testing.T) {
	healthy := []ExporterHealthSnapshot{
		{ComponentID: "otlp/tempo", QueueCapacity: 10000},
		{ComponentID: "otlphttp/loki", QueueCapacity: 10000},
		{ComponentID: "otlphttp/langfuse", QueueCapacity: 10000},
	}
	// 前两次快照与 pause 后前两次都无失败计数（scrape 相位），第 5 次起证据完整。
	backend := &fakeExporterFailureBackend{snapshotFn: func(call int) ([]ExporterHealthSnapshot, error) {
		if call < 5 {
			return healthy, nil
		}
		return []ExporterHealthSnapshot{
			{ComponentID: "otlp/tempo", SendFailed: 3, QueueSize: 3, QueueCapacity: 10000},
			{ComponentID: "otlphttp/loki", Sent: 7, QueueCapacity: 10000},
			{ComponentID: "otlphttp/langfuse", QueueCapacity: 10000},
		}, nil
	}}
	injector := &fakeExporterFailureInjector{}
	recorder := &fakeExporterFailureTrigger{}
	trigger := func(ctx context.Context) (int, string, error) {
		return recorder.invoke(exporterFailureTriggerResult2())(ctx)
	}

	report, err := RunExporterFailureSmoke(context.Background(), exporterFailureRequest(), exporterFailureTestDeps(backend, injector, trigger))
	if err != nil {
		t.Fatalf("RunExporterFailureSmoke() = err %v, want nil", err)
	}
	if report.Status() != "passed" {
		t.Fatalf("report.Status() = %q, want passed：scrape 相位后证据必须等到", report.Status())
	}
	if backend.calls < 5 {
		t.Fatalf("snapshot calls = %d, want >= 5（轮询直到证据可见）", backend.calls)
	}
}

func exporterFailureTriggerResult2() fakeExporterFailureTriggerCall {
	return fakeExporterFailureTriggerCall{status: 200, bodyHash: "hash-baseline"}
}
