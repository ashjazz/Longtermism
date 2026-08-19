package smoke

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// T119 retention/volume 边界契约测试（RED 先行，T129 实现 retention_runner.go
// 使其 GREEN）。
//
// 覆盖的生产风险（data-model §14 数据生命周期 + §10 PayloadPolicy）：
// 保留窗口漂移导致证据不可追溯、普通原文进入可观测 retention unit 变成
// 敏感数据泄漏管道、已投递记录滞留持久队列、local raw 调试工件遗留。
// 契约固定：
//
// 1. 保留窗口逐 unit 校验：Prometheus 15 天、Loki 7 天、Tempo 7 天、
//    Langfuse 14 天、低敏 eval evidence/report 90 天（导出常量即契约）；
//    任何实际窗口与声明不一致 → retention_violation 失败；
// 2. 原文边界：trigger 携带合成 canary 原文后，prometheus/loki/tempo/
//    langfuse 任一 unit 出现该原文 → raw_payload_found=true 证据 +
//    retention_violation 失败（普通原文不得作为可观测 payload 保留）；
// 3. persistent queue 仅积压保留：投递完成后有界轮询队列回落至 0，
//    已投递记录不得继续滞留（queue_depth 证据）；
// 4. local raw 调试工件在运行结束（含失败路径）必须清理：残留 → cleanup
//    failed + temporary-debug-data residual + 整体 failed。
//
// 报告证据键新增（T129 一并扩展允许集）：prometheus/loki/tempo/
// langfuse_trace/api 的 `retention_days`（int）与四个后端 unit 的
// `raw_payload_found`（bool）；error_class 新增 `retention_violation`。

type fakeRetentionBackend struct {
	policies       map[RetentionUnit]RetentionPolicySnapshot
	policyErr      map[RetentionUnit]error
	policyCalls    []RetentionUnit
	rawPresent     map[RetentionUnit]bool
	rawErr         map[RetentionUnit]error
	rawTargets     map[RetentionUnit][]RawCanaryTarget
	queueSnapshots []RetentionQueueSnapshot
	queueErr       []error
	queueN         int
	order          *[]string
}

func (f *fakeRetentionBackend) RetentionPolicy(_ context.Context, unit RetentionUnit) (RetentionPolicySnapshot, error) {
	f.policyCalls = append(f.policyCalls, unit)
	if f.order != nil {
		*f.order = append(*f.order, "policy-"+string(unit))
	}
	if err, ok := f.policyErr[unit]; ok && err != nil {
		return RetentionPolicySnapshot{}, err
	}
	policy, ok := f.policies[unit]
	if !ok {
		return RetentionPolicySnapshot{}, errors.New("fakeRetentionBackend: policy not configured")
	}
	return policy, nil
}

func (f *fakeRetentionBackend) RawPayloadPresent(_ context.Context, unit RetentionUnit, target RawCanaryTarget) (bool, error) {
	f.rawTargets[unit] = append(f.rawTargets[unit], target)
	if f.order != nil {
		*f.order = append(*f.order, "raw-"+string(unit))
	}
	if err, ok := f.rawErr[unit]; ok && err != nil {
		return false, err
	}
	present, ok := f.rawPresent[unit]
	if !ok {
		return false, errors.New("fakeRetentionBackend: raw probe not configured")
	}
	return present, nil
}

func (f *fakeRetentionBackend) CollectorQueueSnapshot(context.Context) (RetentionQueueSnapshot, error) {
	call := f.queueN
	f.queueN++
	if f.order != nil {
		*f.order = append(*f.order, "queue")
	}
	if call < len(f.queueErr) && f.queueErr[call] != nil {
		return RetentionQueueSnapshot{}, f.queueErr[call]
	}
	if len(f.queueSnapshots) == 0 {
		return RetentionQueueSnapshot{}, errors.New("fakeRetentionBackend: queue snapshots not configured")
	}
	// 越界时重复最后一个快照：模拟"队列一直停留在该水位"（例如滞留测试
	// 需要 queue 永不回落，而不是让 fake 报错打断轮询）。
	if call >= len(f.queueSnapshots) {
		return f.queueSnapshots[len(f.queueSnapshots)-1], nil
	}
	return f.queueSnapshots[call], nil
}

type fakeRetentionTrigger struct {
	calls []string
	err   error
	order *[]string
}

func (f *fakeRetentionTrigger) invoke() RetentionTrigger {
	return func(_ context.Context, canary string) error {
		f.calls = append(f.calls, canary)
		if f.order != nil {
			*f.order = append(*f.order, "trigger")
		}
		return f.err
	}
}

type fakeRetentionLocalRaw struct {
	artifacts      []string
	removeErr      error
	listCalls      int
	removeCalls    int
	removeResidual []string
	order          *[]string
}

func (f *fakeRetentionLocalRaw) ListRunArtifacts(context.Context) ([]string, error) {
	f.listCalls++
	if f.order != nil {
		*f.order = append(*f.order, "list-raw")
	}
	return slices.Clone(f.artifacts), nil
}

func (f *fakeRetentionLocalRaw) RemoveRunArtifacts(context.Context) ([]string, error) {
	f.removeCalls++
	if f.order != nil {
		*f.order = append(*f.order, "remove-raw")
	}
	if f.removeErr != nil {
		return f.removeResidual, f.removeErr
	}
	f.artifacts = nil
	return nil, nil
}

const retentionTestBase = "2026-08-14T10:00:00Z"

func retentionTestTime(t *testing.T) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, retentionTestBase)
	if err != nil {
		t.Fatalf("解析测试基准时间失败: %v", err)
	}
	return parsed
}

func retentionTestRequest(t *testing.T) RetentionSmokeRequest {
	base := retentionTestTime(t)
	return RetentionSmokeRequest{
		Deadline: base.Add(5 * time.Minute),
		Profile:  "grafana",
	}
}

// 与 data-model §14 声明一致的合法策略夹具。
func retentionCompliantPolicies() map[RetentionUnit]RetentionPolicySnapshot {
	return map[RetentionUnit]RetentionPolicySnapshot{
		RetentionUnitPrometheus: {Unit: RetentionUnitPrometheus, MaxAgeDays: RetentionPrometheusDays},
		RetentionUnitLoki:       {Unit: RetentionUnitLoki, MaxAgeDays: RetentionLokiDays},
		RetentionUnitTempo:      {Unit: RetentionUnitTempo, MaxAgeDays: RetentionTempoDays},
		RetentionUnitLangfuse:   {Unit: RetentionUnitLangfuse, MaxAgeDays: RetentionLangfuseDays},
		RetentionUnitEvidence:   {Unit: RetentionUnitEvidence, MaxAgeDays: RetentionEvidenceDays},
	}
}

func retentionTestDeps(
	backend *fakeRetentionBackend,
	trigger RetentionTrigger,
	localRaw *fakeRetentionLocalRaw,
	clock PollerClock,
) RetentionSmokeDependencies {
	return RetentionSmokeDependencies{
		Backend:  backend,
		Trigger:  trigger,
		LocalRaw: localRaw,
		Clock:    clock,
		IdentityFactory: func(context.Context) (RetentionSmokeIdentity, error) {
			return RetentionSmokeIdentity{RunID: "run-retention-0001", Marker: "retention-marker-0001"}, nil
		},
		PollInterval: 5 * time.Second,
	}
}

func retentionFalseRawProbes() map[RetentionUnit]bool {
	return map[RetentionUnit]bool{
		RetentionUnitPrometheus: false,
		RetentionUnitLoki:       false,
		RetentionUnitTempo:      false,
		RetentionUnitLangfuse:   false,
	}
}

func retentionCleanBackend() *fakeRetentionBackend {
	backend := &fakeRetentionBackend{
		policies:   retentionCompliantPolicies(),
		rawPresent: retentionFalseRawProbes(),
		rawTargets: map[RetentionUnit][]RawCanaryTarget{},
		queueSnapshots: []RetentionQueueSnapshot{
			{QueueSize: 40, QueueCapacity: 10000},
			{QueueSize: 0, QueueCapacity: 10000},
		},
	}
	return backend
}

// 主路径：五类保留窗口逐 unit 验证、原文零命中、队列回落、raw 工件清理。
func TestRunRetentionSmokeVerifiesAllUnitsAndCleansRawArtifacts(t *testing.T) {
	base := retentionTestTime(t)
	clock := newPollerTestClock(base)
	var order []string
	backend := retentionCleanBackend()
	backend.order = &order
	triggerRecorder := &fakeRetentionTrigger{order: &order}
	localRaw := &fakeRetentionLocalRaw{artifacts: []string{"raw-debug-1.json", "raw-debug-2.json"}, order: &order}

	report, err := RunRetentionSmoke(context.Background(), retentionTestRequest(t), retentionTestDeps(backend, triggerRecorder.invoke(), localRaw, clock))
	if err != nil {
		t.Fatalf("RunRetentionSmoke() = err %v, want nil", err)
	}
	if report.Scenario() != "retention" {
		t.Errorf("report.Scenario() = %q, want retention", report.Scenario())
	}
	if report.Status() != "passed" {
		t.Fatalf("report.Status() = %q, want passed", report.Status())
	}

	prometheusCheck := findCheck(t, report.Checks(), "prometheus")
	if evidenceInt(t, prometheusCheck, "retention_days") != RetentionPrometheusDays {
		t.Errorf("prometheus retention_days = %d, want %d", evidenceInt(t, prometheusCheck, "retention_days"), RetentionPrometheusDays)
	}
	if rawFound, ok := prometheusCheck.Evidence["raw_payload_found"]; !ok || rawFound != false {
		t.Errorf("prometheus raw_payload_found = %v (ok=%v), want false", rawFound, ok)
	}
	lokiCheck := findCheck(t, report.Checks(), "loki")
	if evidenceInt(t, lokiCheck, "retention_days") != RetentionLokiDays {
		t.Errorf("loki retention_days = %d, want %d", evidenceInt(t, lokiCheck, "retention_days"), RetentionLokiDays)
	}
	tempoCheck := findCheck(t, report.Checks(), "tempo")
	if evidenceInt(t, tempoCheck, "retention_days") != RetentionTempoDays {
		t.Errorf("tempo retention_days = %d, want %d", evidenceInt(t, tempoCheck, "retention_days"), RetentionTempoDays)
	}
	langfuseCheck := findCheck(t, report.Checks(), "langfuse_trace")
	if evidenceInt(t, langfuseCheck, "retention_days") != RetentionLangfuseDays {
		t.Errorf("langfuse retention_days = %d, want %d", evidenceInt(t, langfuseCheck, "retention_days"), RetentionLangfuseDays)
	}
	if rawFound, ok := langfuseCheck.Evidence["raw_payload_found"]; !ok || rawFound != false {
		t.Errorf("langfuse raw_payload_found = %v (ok=%v), want false", rawFound, ok)
	}
	apiCheck := findCheck(t, report.Checks(), "api")
	if evidenceInt(t, apiCheck, "retention_days") != RetentionEvidenceDays {
		t.Errorf("api retention_days = %d, want %d（低敏 evidence/report 90 天）", evidenceInt(t, apiCheck, "retention_days"), RetentionEvidenceDays)
	}
	collectorCheck := findCheck(t, report.Checks(), "collector")
	if evidenceInt(t, collectorCheck, "queue_depth") != 0 {
		t.Errorf("queue_depth = %d, want 0：已投递记录不得滞留持久队列", evidenceInt(t, collectorCheck, "queue_depth"))
	}

	if len(triggerRecorder.calls) != 1 || triggerRecorder.calls[0] == "" {
		t.Errorf("trigger canary = %v, want 一次非空 canary 注入", triggerRecorder.calls)
	}
	canary := triggerRecorder.calls[0]
	for _, unit := range []RetentionUnit{RetentionUnitPrometheus, RetentionUnitLoki, RetentionUnitTempo, RetentionUnitLangfuse} {
		targets := backend.rawTargets[unit]
		if len(targets) != 1 || targets[0].Canary != canary {
			t.Errorf("unit %s 的原文探测目标 = %+v, want 与注入 canary 一致", unit, targets)
		}
	}
	if localRaw.removeCalls != 1 || len(localRaw.artifacts) != 0 {
		t.Errorf("local raw 清理 = removeCalls %d artifacts %v, want 运行结束全部删除", localRaw.removeCalls, localRaw.artifacts)
	}
	if cleanup := report.Cleanup(); cleanup.Status != "completed" {
		t.Errorf("cleanup.Status = %q, want completed", cleanup.Status)
	}

	wantOrder := []string{
		"trigger",
		"policy-prometheus", "raw-prometheus",
		"policy-loki", "raw-loki",
		"policy-tempo", "raw-tempo",
		"policy-langfuse", "raw-langfuse",
		"policy-evidence",
		"queue", "queue",
		"list-raw", "remove-raw",
	}
	if !slices.Equal(order, wantOrder) {
		t.Fatalf("执行顺序 = %v, want %v", order, wantOrder)
	}
}

// 保留窗口漂移：任一 unit 实际窗口与声明不一致 → retention_violation。
func TestRunRetentionSmokeFailsOnRetentionWindowMismatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		unit   RetentionUnit
		mutate func(map[RetentionUnit]RetentionPolicySnapshot)
	}{
		{"prometheus 缩短到 10 天", RetentionUnitPrometheus, func(p map[RetentionUnit]RetentionPolicySnapshot) {
			p[RetentionUnitPrometheus] = RetentionPolicySnapshot{Unit: RetentionUnitPrometheus, MaxAgeDays: 10}
		}},
		{"loki 延长到 30 天", RetentionUnitLoki, func(p map[RetentionUnit]RetentionPolicySnapshot) {
			p[RetentionUnitLoki] = RetentionPolicySnapshot{Unit: RetentionUnitLoki, MaxAgeDays: 30}
		}},
		{"evidence 缩短到 30 天", RetentionUnitEvidence, func(p map[RetentionUnit]RetentionPolicySnapshot) {
			p[RetentionUnitEvidence] = RetentionPolicySnapshot{Unit: RetentionUnitEvidence, MaxAgeDays: 30}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := retentionCleanBackend()
			tc.mutate(backend.policies)
			clock := newPollerTestClock(retentionTestTime(t))

			report, err := RunRetentionSmoke(context.Background(), retentionTestRequest(t), retentionTestDeps(backend, (&fakeRetentionTrigger{}).invoke(), &fakeRetentionLocalRaw{}, clock))
			if err != nil {
				t.Fatalf("RunRetentionSmoke() = err %v, want 报告内失败", err)
			}
			if report.Status() != "failed" {
				t.Errorf("report.Status() = %q, want failed", report.Status())
			}
			check := findCheck(t, report.Checks(), retentionCheckBackendName(tc.unit))
			if check.ErrorClass != "retention_violation" {
				t.Errorf("check ErrorClass = %q, want retention_violation", check.ErrorClass)
			}
		})
	}
}

func retentionCheckBackendName(unit RetentionUnit) string {
	switch unit {
	case RetentionUnitPrometheus:
		return "prometheus"
	case RetentionUnitLoki:
		return "loki"
	case RetentionUnitTempo:
		return "tempo"
	case RetentionUnitLangfuse:
		return "langfuse_trace"
	default:
		return "api"
	}
}

// 原文进入 retention unit：raw_payload_found=true + retention_violation——
// 观测保留单元绝不能变成敏感原文的泄漏管道。
func TestRunRetentionSmokeFailsWhenRawPayloadRetained(t *testing.T) {
	backend := retentionCleanBackend()
	backend.rawPresent[RetentionUnitTempo] = true
	clock := newPollerTestClock(retentionTestTime(t))

	report, err := RunRetentionSmoke(context.Background(), retentionTestRequest(t), retentionTestDeps(backend, (&fakeRetentionTrigger{}).invoke(), &fakeRetentionLocalRaw{}, clock))
	if err != nil {
		t.Fatalf("RunRetentionSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	tempoCheck := findCheck(t, report.Checks(), "tempo")
	if tempoCheck.ErrorClass != "retention_violation" {
		t.Errorf("tempo ErrorClass = %q, want retention_violation", tempoCheck.ErrorClass)
	}
	if rawFound, ok := tempoCheck.Evidence["raw_payload_found"]; !ok || rawFound != true {
		t.Errorf("tempo raw_payload_found = %v (ok=%v), want true：原文命中必须写入证据", rawFound, ok)
	}
}

// 队列滞留：投递完成后有界窗口内队列未回落 → retention_violation。
func TestRunRetentionSmokeFailsWhenQueueRetainsDeliveredRecords(t *testing.T) {
	backend := retentionCleanBackend()
	backend.queueSnapshots = []RetentionQueueSnapshot{{QueueSize: 40, QueueCapacity: 10000}}
	clock := newPollerTestClock(retentionTestTime(t))

	report, err := RunRetentionSmoke(context.Background(), retentionTestRequest(t), retentionTestDeps(backend, (&fakeRetentionTrigger{}).invoke(), &fakeRetentionLocalRaw{}, clock))
	if err != nil {
		t.Fatalf("RunRetentionSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	collectorCheck := findCheck(t, report.Checks(), "collector")
	if collectorCheck.ErrorClass != "retention_violation" {
		t.Errorf("collector ErrorClass = %q, want retention_violation", collectorCheck.ErrorClass)
	}
}

// local raw 清理失败：残留工件必须进入 cleanup failed + temporary-debug-data
// residual + 整体 failed——失败路径也要清理，清理失败不能静默。
func TestRunRetentionSmokeReportsRawCleanupResidual(t *testing.T) {
	backend := retentionCleanBackend()
	clock := newPollerTestClock(retentionTestTime(t))
	localRaw := &fakeRetentionLocalRaw{
		artifacts:      []string{"raw-debug-1.json"},
		removeErr:      errors.New("permission denied"),
		removeResidual: []string{"raw-debug-1.json"},
	}

	report, err := RunRetentionSmoke(context.Background(), retentionTestRequest(t), retentionTestDeps(backend, (&fakeRetentionTrigger{}).invoke(), localRaw, clock))
	if err != nil {
		t.Fatalf("RunRetentionSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	cleanup := report.Cleanup()
	if cleanup.Status != "failed" || !slices.Contains(cleanup.ResidualResources, "temporary-debug-data") {
		t.Errorf("cleanup = %q %v, want failed + temporary-debug-data residual", cleanup.Status, cleanup.ResidualResources)
	}
	if localRaw.removeCalls != 1 {
		t.Errorf("RemoveRunArtifacts 调用 = %d, want 1", localRaw.removeCalls)
	}
}

// 无 raw 工件：清理端口仍应被执行（幂等清理），cleanup completed。
func TestRunRetentionSmokeCleansUpEvenWithoutArtifacts(t *testing.T) {
	backend := retentionCleanBackend()
	clock := newPollerTestClock(retentionTestTime(t))
	localRaw := &fakeRetentionLocalRaw{}

	report, err := RunRetentionSmoke(context.Background(), retentionTestRequest(t), retentionTestDeps(backend, (&fakeRetentionTrigger{}).invoke(), localRaw, clock))
	if err != nil {
		t.Fatalf("RunRetentionSmoke() = err %v, want nil", err)
	}
	if report.Status() != "passed" {
		t.Errorf("report.Status() = %q, want passed", report.Status())
	}
	if localRaw.listCalls != 1 || localRaw.removeCalls != 1 {
		t.Errorf("list/remove 调用 = %d/%d, want 1/1", localRaw.listCalls, localRaw.removeCalls)
	}
}

// 窗口校验失败路径上 raw 工件也必须被清理（任何退出路径清理）。
func TestRunRetentionSmokeCleansRawArtifactsOnFailurePath(t *testing.T) {
	backend := retentionCleanBackend()
	backend.rawPresent[RetentionUnitLoki] = true
	clock := newPollerTestClock(retentionTestTime(t))
	localRaw := &fakeRetentionLocalRaw{artifacts: []string{"raw-debug-1.json"}}

	report, err := RunRetentionSmoke(context.Background(), retentionTestRequest(t), retentionTestDeps(backend, (&fakeRetentionTrigger{}).invoke(), localRaw, clock))
	if err != nil {
		t.Fatalf("RunRetentionSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	if len(localRaw.artifacts) != 0 || localRaw.removeCalls != 1 {
		t.Errorf("失败路径未清理 raw 工件: artifacts=%v removeCalls=%d", localRaw.artifacts, localRaw.removeCalls)
	}
}

// 请求与依赖校验：非法输入直接报错。
func TestRunRetentionSmokeRejectsInvalidRequests(t *testing.T) {
	base := retentionTestTime(t)
	validDeps := retentionTestDeps(retentionCleanBackend(), (&fakeRetentionTrigger{}).invoke(), &fakeRetentionLocalRaw{}, newPollerTestClock(base))
	validRequest := retentionTestRequest(t)

	tests := []struct {
		name   string
		mutate func(*RetentionSmokeRequest, *RetentionSmokeDependencies)
	}{
		{"profile 不在允许集", func(r *RetentionSmokeRequest, _ *RetentionSmokeDependencies) { r.Profile = "unknown" }},
		{"deadline 为零值", func(r *RetentionSmokeRequest, _ *RetentionSmokeDependencies) { r.Deadline = time.Time{} }},
		{"deadline 已过期", func(r *RetentionSmokeRequest, _ *RetentionSmokeDependencies) { r.Deadline = base.Add(-time.Second) }},
		{"Backend 缺失", func(_ *RetentionSmokeRequest, d *RetentionSmokeDependencies) { d.Backend = nil }},
		{"Trigger 缺失", func(_ *RetentionSmokeRequest, d *RetentionSmokeDependencies) { d.Trigger = nil }},
		{"LocalRaw 缺失", func(_ *RetentionSmokeRequest, d *RetentionSmokeDependencies) { d.LocalRaw = nil }},
		{"Clock 缺失", func(_ *RetentionSmokeRequest, d *RetentionSmokeDependencies) { d.Clock = nil }},
		{"PollInterval 非正", func(_ *RetentionSmokeRequest, d *RetentionSmokeDependencies) { d.PollInterval = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mutatedRequest := validRequest
			mutatedDeps := validDeps
			tc.mutate(&mutatedRequest, &mutatedDeps)
			if _, err := RunRetentionSmoke(context.Background(), mutatedRequest, mutatedDeps); err == nil {
				t.Error("RunRetentionSmoke() = nil error, want 校验错误")
			}
		})
	}
}

// ctx 取消必须中止运行并返回错误。
func TestRunRetentionSmokeContextCancellationAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := RunRetentionSmoke(ctx, retentionTestRequest(t), retentionTestDeps(retentionCleanBackend(), (&fakeRetentionTrigger{}).invoke(), &fakeRetentionLocalRaw{}, newPollerTestClock(retentionTestTime(t))))
	if err == nil {
		t.Fatalf("RunRetentionSmoke() = %v, want ctx 错误", report)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
