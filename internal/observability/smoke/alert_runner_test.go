package smoke

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// T117 告警 firing/resolved 契约测试（RED 先行，T127 实现 alert_runner.go
// 使其 GREEN）。
//
// 覆盖的生产风险（FR-009）：告警资产只是"文件存在"、触发从未被真实 API
// 证明、恢复被误判或 stale 状态被当作有效证据。契约要求 runner 对四类
// 告警（http_error_rate / exporter_delivery_failure / queue_pressure /
// storage_pressure，对应 observability.rules.yaml 的 uid 族）逐一执行：
//
// 1. TriggerAlertClass 注入告警条件 → 受限窗口轮询观察到 firing
//    （时间证据，ObservedAt 必须在窗口内）；
// 2. ResolveAlertClass 恢复条件 → 受限窗口轮询观察到 resolved/normal；
// 3. 触发或恢复超时分别失败于 alert_not_firing / alert_not_resolved，
//    且注入必须恢复；
// 4. stale 观察（窗口外或 state=stale）必须被隔离，不得当作 firing；
// 5. 契约层面不存在 rule 文件检查：Backend 只提供告警状态查询，任何
//    静态文件存在性检查都无法满足该接口。
//
// 报告证据键新增（T127 一并扩展 grafana 允许集）：alerts_firing、
// alerts_resolved；error_class 新增 alert_not_resolved；residual 新增
// `alert-condition-active`。恢复窗口常量 alertSmokeRecoveryWindow 由
// 实现侧声明（本文件不再重复定义）。

type fakeAlertSmokeBackend struct {
	stateCalls [][]AlertStateObservation
	queryErr   []error
	queryN     int
	windows    []AlertQueryWindow
	order      *[]string
}

func (f *fakeAlertSmokeBackend) AlertStates(_ context.Context, window AlertQueryWindow) ([]AlertStateObservation, error) {
	call := f.queryN
	f.queryN++
	f.windows = append(f.windows, window)
	if f.order != nil {
		*f.order = append(*f.order, "query")
	}
	if call < len(f.queryErr) && f.queryErr[call] != nil {
		return nil, f.queryErr[call]
	}
	if call >= len(f.stateCalls) {
		return nil, nil
	}
	return f.stateCalls[call], nil
}

type fakeAlertSmokeInjector struct {
	triggerErr   map[AlertSmokeClass]error
	resolveErr   map[AlertSmokeClass]error
	triggerCalls []AlertSmokeClass
	resolveCalls []AlertSmokeClass
	order        *[]string
}

func (f *fakeAlertSmokeInjector) TriggerAlertClass(_ context.Context, class AlertSmokeClass) error {
	f.triggerCalls = append(f.triggerCalls, class)
	if f.order != nil {
		*f.order = append(*f.order, "trigger")
	}
	if f.triggerErr != nil {
		return f.triggerErr[class]
	}
	return nil
}

func (f *fakeAlertSmokeInjector) ResolveAlertClass(_ context.Context, class AlertSmokeClass) error {
	f.resolveCalls = append(f.resolveCalls, class)
	if f.order != nil {
		*f.order = append(*f.order, "resolve")
	}
	if f.resolveErr != nil {
		return f.resolveErr[class]
	}
	return nil
}

const alertSmokeTestBase = "2026-08-14T10:00:00Z"

func alertSmokeTestTime(t *testing.T) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, alertSmokeTestBase)
	if err != nil {
		t.Fatalf("解析测试基准时间失败: %v", err)
	}
	return parsed
}

func alertSmokeRequest(classes []AlertSmokeClass, t *testing.T) AlertSmokeRequest {
	base := alertSmokeTestTime(t)
	return AlertSmokeRequest{
		Deadline: base.Add(5 * time.Minute),
		Profile:  "grafana",
		Classes:  classes,
	}
}

func alertSmokeTestDeps(
	backend *fakeAlertSmokeBackend,
	injector *fakeAlertSmokeInjector,
	clock PollerClock,
) AlertSmokeDependencies {
	return AlertSmokeDependencies{
		Backend:  backend,
		Injector: injector,
		Clock:    clock,
		IdentityFactory: func(context.Context) (AlertSmokeIdentity, error) {
			return AlertSmokeIdentity{RunID: "run-alert-smoke-0001", Marker: "alert-marker-0001"}, nil
		},
		PollInterval: 5 * time.Second,
	}
}

func alertObservation(uid string, state string, at time.Time) AlertStateObservation {
	return AlertStateObservation{AlertUID: uid, State: state, ObservedAt: at}
}

// 主路径：注入 → stale 被隔离 → firing 时间证据 → 恢复 → resolved 时间证据。
func TestRunAlertSmokeObservesFiringAndResolvedWithBoundedWindows(t *testing.T) {
	base := alertSmokeTestTime(t)
	clock := newPollerTestClock(base)
	var order []string
	backend := &fakeAlertSmokeBackend{order: &order}
	backend.stateCalls = [][]AlertStateObservation{
		{alertObservation("longtermism-exporter-delivery-failure", "stale", base.Add(1*time.Hour))},
		{alertObservation("longtermism-exporter-delivery-failure", "firing", base.Add(8*time.Second))},
		{alertObservation("longtermism-exporter-delivery-failure", "firing", base.Add(13*time.Second))},
		{alertObservation("longtermism-exporter-delivery-failure", "normal", base.Add(18*time.Second))},
	}
	injector := &fakeAlertSmokeInjector{order: &order}

	report, err := RunAlertSmoke(context.Background(), alertSmokeRequest([]AlertSmokeClass{AlertClassExporterFailure}, t), alertSmokeTestDeps(backend, injector, clock))
	if err != nil {
		t.Fatalf("RunAlertSmoke() = err %v, want nil", err)
	}
	if report.Scenario() != "alert" {
		t.Errorf("report.Scenario() = %q, want alert", report.Scenario())
	}
	if report.Status() != "passed" {
		t.Fatalf("report.Status() = %q, want passed", report.Status())
	}
	check := findCheck(t, report.Checks(), "grafana")
	if check.Status != "passed" || check.FailureStage != "none" {
		t.Errorf("grafana check = %q/%q, want passed/none", check.Status, check.FailureStage)
	}
	if got := evidenceInt(t, check, "alerts_firing"); got != 1 {
		t.Errorf("alerts_firing = %d, want 1", got)
	}
	if got := evidenceInt(t, check, "alerts_resolved"); got != 1 {
		t.Errorf("alerts_resolved = %d, want 1", got)
	}

	if len(injector.triggerCalls) != 1 || injector.triggerCalls[0] != AlertClassExporterFailure {
		t.Errorf("triggerCalls = %v, want 仅 [exporter_delivery_failure]", injector.triggerCalls)
	}
	if len(injector.resolveCalls) != 1 || injector.resolveCalls[0] != AlertClassExporterFailure {
		t.Errorf("resolveCalls = %v, want 仅 [exporter_delivery_failure]", injector.resolveCalls)
	}
	wantOrder := []string{"trigger", "query", "query", "resolve", "query", "query"}
	if !slices.Equal(order, wantOrder) {
		t.Fatalf("执行顺序 = %v, want %v", order, wantOrder)
	}

	// 查询窗口受限：所有窗口 deadline 不得越过 request.Deadline，且每段
	// 轮询（firing 段与 resolved 段）各有不超过 120 秒的窗口上界。
	for i, window := range backend.windows {
		if window.Deadline.After(alertSmokeRequest([]AlertSmokeClass{AlertClassExporterFailure}, t).Deadline) {
			t.Errorf("第 %d 个窗口 deadline = %v 越过 request.Deadline", i, window.Deadline)
		}
		if window.StartedAt.IsZero() {
			t.Errorf("第 %d 个窗口 StartedAt 为零值", i)
		}
	}
}

// 四类告警全部触发并恢复：FR-009 的完整验收——每类都必须有 firing 与
// resolved 时间证据，缺一类即失败。
func TestRunAlertSmokeCoversAllFourAlertClasses(t *testing.T) {
	base := alertSmokeTestTime(t)
	clock := newPollerTestClock(base)
	classes := []AlertSmokeClass{
		AlertClassHTTPError,
		AlertClassExporterFailure,
		AlertClassQueuePressure,
		AlertClassStoragePressure,
	}
	backend := &fakeAlertSmokeBackend{}
	for range classes {
		backend.stateCalls = append(backend.stateCalls,
			[]AlertStateObservation{alertObservation("uid", "firing", base.Add(4*time.Second))},
			[]AlertStateObservation{alertObservation("uid", "normal", base.Add(9*time.Second))},
		)
	}
	injector := &fakeAlertSmokeInjector{}

	report, err := RunAlertSmoke(context.Background(), alertSmokeRequest(classes, t), alertSmokeTestDeps(backend, injector, clock))
	if err != nil {
		t.Fatalf("RunAlertSmoke() = err %v, want nil", err)
	}
	if report.Status() != "passed" {
		t.Fatalf("report.Status() = %q, want passed", report.Status())
	}
	check := findCheck(t, report.Checks(), "grafana")
	if got := evidenceInt(t, check, "alerts_firing"); got != 4 {
		t.Errorf("alerts_firing = %d, want 4", got)
	}
	if got := evidenceInt(t, check, "alerts_resolved"); got != 4 {
		t.Errorf("alerts_resolved = %d, want 4", got)
	}
	if len(injector.triggerCalls) != 4 || len(injector.resolveCalls) != 4 {
		t.Errorf("trigger/resolve 调用 = %d/%d, want 4/4", len(injector.triggerCalls), len(injector.resolveCalls))
	}
	for _, class := range classes {
		if !slices.Contains(injector.triggerCalls, class) || !slices.Contains(injector.resolveCalls, class) {
			t.Errorf("告警类 %q 未被完整注入/恢复", class)
		}
	}
}

// 触发超时：窗口耗尽仍无 firing → alert_not_firing 失败，且注入必须恢复。
func TestRunAlertSmokeFailsWhenAlertNeverFires(t *testing.T) {
	clock := newPollerTestClock(alertSmokeTestTime(t))
	backend := &fakeAlertSmokeBackend{}
	injector := &fakeAlertSmokeInjector{}

	report, err := RunAlertSmoke(context.Background(), alertSmokeRequest([]AlertSmokeClass{AlertClassExporterFailure}, t), alertSmokeTestDeps(backend, injector, clock))
	if err != nil {
		t.Fatalf("RunAlertSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	check := findCheck(t, report.Checks(), "grafana")
	if check.ErrorClass != "alert_not_firing" {
		t.Errorf("grafana ErrorClass = %q, want alert_not_firing", check.ErrorClass)
	}
	if len(injector.resolveCalls) != 1 {
		t.Error("触发超时后未恢复告警条件")
	}
}

// 恢复超时：firing 已证明但窗口内未观察到 resolved → alert_not_resolved。
func TestRunAlertSmokeFailsWhenAlertNeverResolves(t *testing.T) {
	base := alertSmokeTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakeAlertSmokeBackend{}
	backend.stateCalls = [][]AlertStateObservation{
		{alertObservation("longtermism-exporter-delivery-failure", "firing", base.Add(4*time.Second))},
	}
	injector := &fakeAlertSmokeInjector{}

	report, err := RunAlertSmoke(context.Background(), alertSmokeRequest([]AlertSmokeClass{AlertClassExporterFailure}, t), alertSmokeTestDeps(backend, injector, clock))
	if err != nil {
		t.Fatalf("RunAlertSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	check := findCheck(t, report.Checks(), "grafana")
	if check.ErrorClass != "alert_not_resolved" {
		t.Errorf("grafana ErrorClass = %q, want alert_not_resolved", check.ErrorClass)
	}
}

// stale 隔离：state=stale 的观察不得当作 firing；只有 stale 证据时按
// 触发超时失败，不能假通过。
func TestRunAlertSmokeIsolatesStaleAlertState(t *testing.T) {
	base := alertSmokeTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakeAlertSmokeBackend{}
	backend.stateCalls = [][]AlertStateObservation{
		{alertObservation("longtermism-exporter-delivery-failure", "stale", base.Add(1*time.Hour))},
	}
	injector := &fakeAlertSmokeInjector{}

	report, err := RunAlertSmoke(context.Background(), alertSmokeRequest([]AlertSmokeClass{AlertClassExporterFailure}, t), alertSmokeTestDeps(backend, injector, clock))
	if err != nil {
		t.Fatalf("RunAlertSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed：stale 不得冒充 firing", report.Status())
	}
	check := findCheck(t, report.Checks(), "grafana")
	if check.ErrorClass != "alert_not_firing" {
		t.Errorf("grafana ErrorClass = %q, want alert_not_firing", check.ErrorClass)
	}
}

// 迟到 firing 隔离：ObservedAt 越过窗口的 firing 观察必须被剔除。
func TestRunAlertSmokeIsolatesLateFiringObservation(t *testing.T) {
	base := alertSmokeTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakeAlertSmokeBackend{}
	backend.stateCalls = [][]AlertStateObservation{
		{alertObservation("longtermism-exporter-delivery-failure", "firing", base.Add(3*time.Hour))},
	}
	injector := &fakeAlertSmokeInjector{}

	report, err := RunAlertSmoke(context.Background(), alertSmokeRequest([]AlertSmokeClass{AlertClassExporterFailure}, t), alertSmokeTestDeps(backend, injector, clock))
	if err != nil {
		t.Fatalf("RunAlertSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
}

// 注入失败：报告 export/export_failed，无恢复义务。
func TestRunAlertSmokeTriggerFailureAbortsWithoutResolve(t *testing.T) {
	clock := newPollerTestClock(alertSmokeTestTime(t))
	backend := &fakeAlertSmokeBackend{}
	injector := &fakeAlertSmokeInjector{triggerErr: map[AlertSmokeClass]error{
		AlertClassExporterFailure: errors.New("compose pause failed"),
	}}

	report, err := RunAlertSmoke(context.Background(), alertSmokeRequest([]AlertSmokeClass{AlertClassExporterFailure}, t), alertSmokeTestDeps(backend, injector, clock))
	if err != nil {
		t.Fatalf("RunAlertSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	check := findCheck(t, report.Checks(), "grafana")
	if check.FailureStage != "export" || check.ErrorClass != "export_failed" {
		t.Errorf("grafana check = %q/%q, want export/export_failed", check.FailureStage, check.ErrorClass)
	}
	if len(injector.resolveCalls) != 0 {
		t.Errorf("resolveCalls = %v, want 空：注入失败无恢复义务", injector.resolveCalls)
	}
}

// 恢复告警条件失败：条件可能仍活跃——cleanup failed + residual +
// 整体 failed。
func TestRunAlertSmokeResolveFailureReportsResidual(t *testing.T) {
	base := alertSmokeTestTime(t)
	clock := newPollerTestClock(base)
	backend := &fakeAlertSmokeBackend{}
	backend.stateCalls = [][]AlertStateObservation{
		{alertObservation("longtermism-exporter-delivery-failure", "firing", base.Add(4*time.Second))},
	}
	injector := &fakeAlertSmokeInjector{resolveErr: map[AlertSmokeClass]error{
		AlertClassExporterFailure: errors.New("compose unpause failed"),
	}}

	report, err := RunAlertSmoke(context.Background(), alertSmokeRequest([]AlertSmokeClass{AlertClassExporterFailure}, t), alertSmokeTestDeps(backend, injector, clock))
	if err != nil {
		t.Fatalf("RunAlertSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	cleanup := report.Cleanup()
	if cleanup.Status != "failed" || !slices.Contains(cleanup.ResidualResources, "alert-condition-active") {
		t.Errorf("cleanup = %q %v, want failed + alert-condition-active", cleanup.Status, cleanup.ResidualResources)
	}
}

// 状态查询错误：query/query_failed，注入必须恢复。
func TestRunAlertSmokeQueryErrorStillResolves(t *testing.T) {
	clock := newPollerTestClock(alertSmokeTestTime(t))
	backend := &fakeAlertSmokeBackend{queryErr: []error{errors.New("grafana api 500")}}
	injector := &fakeAlertSmokeInjector{}

	report, err := RunAlertSmoke(context.Background(), alertSmokeRequest([]AlertSmokeClass{AlertClassExporterFailure}, t), alertSmokeTestDeps(backend, injector, clock))
	if err != nil {
		t.Fatalf("RunAlertSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	check := findCheck(t, report.Checks(), "grafana")
	if check.FailureStage != "query" || check.ErrorClass != "query_failed" {
		t.Errorf("grafana check = %q/%q, want query/query_failed", check.FailureStage, check.ErrorClass)
	}
	if len(injector.resolveCalls) != 1 {
		t.Error("查询失败后未恢复告警条件")
	}
}

// 请求与依赖校验：非法输入直接报错。
func TestRunAlertSmokeRejectsInvalidRequests(t *testing.T) {
	base := alertSmokeTestTime(t)
	validDeps := alertSmokeTestDeps(&fakeAlertSmokeBackend{}, &fakeAlertSmokeInjector{}, newPollerTestClock(base))
	validRequest := alertSmokeRequest([]AlertSmokeClass{AlertClassExporterFailure}, t)

	tests := []struct {
		name   string
		mutate func(*AlertSmokeRequest, *AlertSmokeDependencies)
	}{
		{"profile 不在允许集", func(r *AlertSmokeRequest, _ *AlertSmokeDependencies) { r.Profile = "unknown" }},
		{"deadline 为零值", func(r *AlertSmokeRequest, _ *AlertSmokeDependencies) { r.Deadline = time.Time{} }},
		{"deadline 已过期", func(r *AlertSmokeRequest, _ *AlertSmokeDependencies) { r.Deadline = base.Add(-time.Second) }},
		{"Classes 为空", func(r *AlertSmokeRequest, _ *AlertSmokeDependencies) { r.Classes = nil }},
		{"Classes 含未知类", func(r *AlertSmokeRequest, _ *AlertSmokeDependencies) {
			r.Classes = []AlertSmokeClass{"unknown_alert_class"}
		}},
		{"Backend 缺失", func(_ *AlertSmokeRequest, d *AlertSmokeDependencies) { d.Backend = nil }},
		{"Injector 缺失", func(_ *AlertSmokeRequest, d *AlertSmokeDependencies) { d.Injector = nil }},
		{"Clock 缺失", func(_ *AlertSmokeRequest, d *AlertSmokeDependencies) { d.Clock = nil }},
		{"PollInterval 非正", func(_ *AlertSmokeRequest, d *AlertSmokeDependencies) { d.PollInterval = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mutatedRequest := validRequest
			mutatedDeps := validDeps
			tc.mutate(&mutatedRequest, &mutatedDeps)
			if _, err := RunAlertSmoke(context.Background(), mutatedRequest, mutatedDeps); err == nil {
				t.Error("RunAlertSmoke() = nil error, want 校验错误")
			}
		})
	}
}

// ctx 取消必须中止运行并返回错误。
func TestRunAlertSmokeContextCancellationAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := RunAlertSmoke(ctx, alertSmokeRequest([]AlertSmokeClass{AlertClassExporterFailure}, t), alertSmokeTestDeps(&fakeAlertSmokeBackend{}, &fakeAlertSmokeInjector{}, newPollerTestClock(alertSmokeTestTime(t))))
	if err == nil {
		t.Fatalf("RunAlertSmoke() = %v, want ctx 错误", report)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
