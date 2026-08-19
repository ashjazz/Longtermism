package smoke

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// T114 storage/queue 极限故障契约测试（RED 先行，T124 实现 storage_failure_runner.go
// 使其 GREEN）。
//
// 覆盖的生产风险：queue 耗尽静默丢数据、磁盘不可写无证据、shutdown 超时被当作
// 正常停机、配置问题混入运行时故障归因。契约固定三个场景与一条 preflight 前置：
//
// 1. queue_exhaustion：有界循环产生流量直到 QueueSize >= QueueCapacity，
//    必须同时取得 dropped/enqueue_failed 证据；deadline 内未耗尽 → 失败，
//    不得猜测通过；
// 2. unwritable_disk：MakeStorageUnwritable → 流量 → 快照观察到
//    StorageWritable=false → 任何退出路径 RestoreStorageWritable →
//    VerifyCollectorHealthy（恢复后必须验证 Collector writable/healthy）；
// 3. shutdown_timeout：StopCollector 必须返回 ErrCollectorShutdownTimeout
//    哨兵（否则视为未产生超时 → 失败），随后 RestartCollector +
//    VerifyCollectorHealthy，报告携带 shutdown_timed_out 与 dropped 证据；
// 4. preflight：在任何注入/流量之前执行，失败 → failure_stage=preflight、
//    稳定 error_class=invalid_configuration，零外部调用；类别哨兵与
//    obs-config-check 的 invalid_collector_pipeline / storage_path_unavailable
//    一一对应（shell 侧静态门禁已由 config_check_test.sh 覆盖，这里是
//    runner 契约层的启动前拒绝语义）。
//
// 业务失败不要求：三场景都只断言观测事实，不依赖 api check。
// 报告证据键新增（T124 一并扩展 Go 侧允许集，schema additionalProperties
// 已开放）：enqueue_failed_delta、dropped_delta、storage_writable、
// shutdown_timed_out；residual 新增 `unwritable-storage`。

type fakeStorageFailurePreflight struct {
	err   error
	calls int
	order *[]string
}

func (f *fakeStorageFailurePreflight) Check(context.Context) error {
	f.calls++
	if f.order != nil {
		*f.order = append(*f.order, "preflight")
	}
	return f.err
}

type fakeStorageFailureBackend struct {
	snapshotFn  func(call int) (StorageHealthSnapshot, error)
	snapshotN   int
	verifyErr   error
	verifyCalls int
	order       *[]string
}

func (f *fakeStorageFailureBackend) SnapshotCollectorStorage(context.Context) (StorageHealthSnapshot, error) {
	call := f.snapshotN
	f.snapshotN++
	if f.order != nil {
		*f.order = append(*f.order, "snapshot")
	}
	if f.snapshotFn == nil {
		return StorageHealthSnapshot{}, errors.New("fakeStorageFailureBackend: snapshotFn 未配置")
	}
	return f.snapshotFn(call)
}

func (f *fakeStorageFailureBackend) VerifyCollectorHealthy(context.Context) error {
	f.verifyCalls++
	if f.order != nil {
		*f.order = append(*f.order, "verify")
	}
	return f.verifyErr
}

type fakeStorageFailureInjector struct {
	makeUnwritableErr    error
	restoreWritableErr   error
	stopErr              error
	restartErr           error
	makeUnwritableCalls  int
	restoreWritableCalls int
	stopCalls            int
	restartCalls         int
	order                *[]string
}

func (f *fakeStorageFailureInjector) MakeStorageUnwritable(context.Context) error {
	f.makeUnwritableCalls++
	if f.order != nil {
		*f.order = append(*f.order, "make-unwritable")
	}
	return f.makeUnwritableErr
}

func (f *fakeStorageFailureInjector) RestoreStorageWritable(context.Context) error {
	f.restoreWritableCalls++
	if f.order != nil {
		*f.order = append(*f.order, "restore-writable")
	}
	return f.restoreWritableErr
}

func (f *fakeStorageFailureInjector) StopCollector(context.Context) error {
	f.stopCalls++
	if f.order != nil {
		*f.order = append(*f.order, "stop")
	}
	return f.stopErr
}

func (f *fakeStorageFailureInjector) RestartCollector(context.Context) error {
	f.restartCalls++
	if f.order != nil {
		*f.order = append(*f.order, "restart")
	}
	return f.restartErr
}

type fakeStorageFailureTrigger struct {
	calls int
	err   error
	order *[]string
}

func (f *fakeStorageFailureTrigger) invoke() StorageFailureTrigger {
	return func(context.Context) error {
		f.calls++
		if f.order != nil {
			*f.order = append(*f.order, "trigger")
		}
		return f.err
	}
}

const storageFailureTestBase = "2026-08-14T10:00:00Z"

func storageFailureTestTime(t *testing.T) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, storageFailureTestBase)
	if err != nil {
		t.Fatalf("解析测试基准时间失败: %v", err)
	}
	return parsed
}

func storageFailureRequest(scenario StorageFailureScenario, t *testing.T) StorageFailureSmokeRequest {
	base := storageFailureTestTime(t)
	return StorageFailureSmokeRequest{
		Deadline:    base.Add(5 * time.Minute),
		Profile:     "grafana",
		Scenario:    scenario,
		Service:     "otel-collector",
		ComponentID: "otlp/tempo",
	}
}

func storageFailureTestDeps(
	preflight *fakeStorageFailurePreflight,
	backend *fakeStorageFailureBackend,
	injector *fakeStorageFailureInjector,
	trigger StorageFailureTrigger,
	clock PollerClock,
) StorageFailureSmokeDependencies {
	return StorageFailureSmokeDependencies{
		Preflight: preflight,
		Backend:   backend,
		Injector:  injector,
		Trigger:   trigger,
		Clock:     clock,
		IdentityFactory: func(context.Context) (StorageFailureSmokeIdentity, error) {
			return StorageFailureSmokeIdentity{RunID: "run-storage-failure-0001", Marker: "storage-marker-0001"}, nil
		},
		PollInterval: 5 * time.Second,
	}
}

func storageFailurePassedCollectorCheck(t *testing.T, report *SmokeReport) BackendCheck {
	t.Helper()
	if report.Status() != "passed" {
		t.Fatalf("report.Status() = %q, want passed", report.Status())
	}
	check := findCheck(t, report.Checks(), "collector")
	if check.Status != "passed" || check.FailureStage != "none" {
		t.Fatalf("collector check = %q/%q, want passed/none", check.Status, check.FailureStage)
	}
	return check
}

// queue exhaustion 主路径：达到容量上限且产生 dropped/enqueue_failed 证据。
func TestRunStorageFailureSmokeQueueExhaustionObservesDroppedEvidence(t *testing.T) {
	clock := newPollerTestClock(storageFailureTestTime(t))
	var order []string
	preflight := &fakeStorageFailurePreflight{order: &order}
	// 每次调用队列水位 8000→10000（第 4 次达到容量），第 4 次同时产生
	// enqueue_failed 3 / dropped 3 证据。
	backend := &fakeStorageFailureBackend{snapshotFn: func(call int) (StorageHealthSnapshot, error) {
		sizes := []int64{8000, 9000, 9500, 10000}
		if call < len(sizes) {
			snapshot := StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: sizes[call], QueueCapacity: 10000, StorageWritable: true}
			if sizes[call] >= 10000 {
				snapshot.EnqueueFailed = 3
				snapshot.Dropped = 3
			}
			return snapshot, nil
		}
		return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 10000, QueueCapacity: 10000, EnqueueFailed: 3, Dropped: 3, StorageWritable: true}, nil
	}, order: &order}
	injector := &fakeStorageFailureInjector{order: &order}
	triggerRecorder := &fakeStorageFailureTrigger{order: &order}

	report, err := RunStorageFailureSmoke(context.Background(), storageFailureRequest(StorageFailureQueueExhaustion, t), storageFailureTestDeps(preflight, backend, injector, triggerRecorder.invoke(), clock))
	if err != nil {
		t.Fatalf("RunStorageFailureSmoke() = err %v, want nil", err)
	}
	check := storageFailurePassedCollectorCheck(t, report)
	if got := evidenceInt(t, check, "queue_depth"); got != 10000 {
		t.Errorf("queue_depth = %d, want 10000：耗尽水位必须进入报告", got)
	}
	if got := evidenceInt(t, check, "enqueue_failed_delta"); got != 3 {
		t.Errorf("enqueue_failed_delta = %d, want 3", got)
	}
	if got := evidenceInt(t, check, "dropped_delta"); got != 3 {
		t.Errorf("dropped_delta = %d, want 3：queue 耗尽必须保留 dropped 证据", got)
	}
	if report.Scenario() != "storage_failure" {
		t.Errorf("report.Scenario() = %q, want storage_failure", report.Scenario())
	}
	// 注入器在本场景中完全不被调用：queue exhaustion 只靠流量，不靠容器操作。
	if injector.makeUnwritableCalls != 0 || injector.stopCalls != 0 || injector.restartCalls != 0 {
		t.Errorf("queue_exhaustion 场景调用了注入器（unwritable=%d stop=%d restart=%d），want 全部为 0",
			injector.makeUnwritableCalls, injector.stopCalls, injector.restartCalls)
	}
	// 顺序契约：preflight 必须发生在任何快照/流量之前。
	wantPrefix := []string{"preflight", "snapshot"}
	if !slices.Equal(order[:len(wantPrefix)], wantPrefix) {
		t.Errorf("执行顺序前缀 = %v, want %v", order[:len(wantPrefix)], wantPrefix)
	}
	if triggerRecorder.calls < 3 {
		t.Errorf("trigger 调用次数 = %d, want >= 3：需要多轮流量逼近容量", triggerRecorder.calls)
	}
}

// deadline 内未达到容量：不能证明 queue 极限行为，失败而不是默认通过。
func TestRunStorageFailureSmokeQueueExhaustionFailsWhenNeverExhausted(t *testing.T) {
	clock := newPollerTestClock(storageFailureTestTime(t))
	preflight := &fakeStorageFailurePreflight{}
	backend := &fakeStorageFailureBackend{snapshotFn: func(int) (StorageHealthSnapshot, error) {
		return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 10, QueueCapacity: 10000, StorageWritable: true}, nil
	}}
	injector := &fakeStorageFailureInjector{}

	report, err := RunStorageFailureSmoke(context.Background(), storageFailureRequest(StorageFailureQueueExhaustion, t), storageFailureTestDeps(preflight, backend, injector, (&fakeStorageFailureTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunStorageFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	check := findCheck(t, report.Checks(), "collector")
	if check.ErrorClass != "unexpected_evidence" {
		t.Errorf("collector check ErrorClass = %q, want unexpected_evidence", check.ErrorClass)
	}
}

// 耗尽但缺少 dropped/failed 证据：容量到达却没有失败计数，说明证据源缺失，
// 同样不得通过。
func TestRunStorageFailureSmokeQueueExhaustionRequiresFailureEvidence(t *testing.T) {
	clock := newPollerTestClock(storageFailureTestTime(t))
	preflight := &fakeStorageFailurePreflight{}
	backend := &fakeStorageFailureBackend{snapshotFn: func(int) (StorageHealthSnapshot, error) {
		return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 10000, QueueCapacity: 10000, StorageWritable: true}, nil
	}}
	injector := &fakeStorageFailureInjector{}

	report, err := RunStorageFailureSmoke(context.Background(), storageFailureRequest(StorageFailureQueueExhaustion, t), storageFailureTestDeps(preflight, backend, injector, (&fakeStorageFailureTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunStorageFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
}

// unwritable_disk 主路径：观察到不可写事实 → 恢复 → 验证 healthy。
func TestRunStorageFailureSmokeUnwritableDiskRestoresAndVerifies(t *testing.T) {
	clock := newPollerTestClock(storageFailureTestTime(t))
	var order []string
	preflight := &fakeStorageFailurePreflight{order: &order}
	backend := &fakeStorageFailureBackend{snapshotFn: func(call int) (StorageHealthSnapshot, error) {
		if call == 0 {
			return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, StorageWritable: true, EnqueueFailed: 0}, nil
		}
		return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, StorageWritable: false, EnqueueFailed: 7}, nil
	}, order: &order}
	injector := &fakeStorageFailureInjector{order: &order}
	triggerRecorder := &fakeStorageFailureTrigger{order: &order}

	report, err := RunStorageFailureSmoke(context.Background(), storageFailureRequest(StorageFailureUnwritableDisk, t), storageFailureTestDeps(preflight, backend, injector, triggerRecorder.invoke(), clock))
	if err != nil {
		t.Fatalf("RunStorageFailureSmoke() = err %v, want nil", err)
	}
	check := storageFailurePassedCollectorCheck(t, report)
	if got := evidenceInt(t, check, "enqueue_failed_delta"); got != 7 {
		t.Errorf("enqueue_failed_delta = %d, want 7", got)
	}
	if storageWritable, ok := check.Evidence["storage_writable"]; !ok || storageWritable != false {
		t.Errorf("storage_writable 证据 = %v (ok=%v), want false：不可写事实必须写入报告", storageWritable, ok)
	}
	if injector.makeUnwritableCalls != 1 || injector.restoreWritableCalls != 1 {
		t.Errorf("make/restore 调用 = %d/%d, want 1/1", injector.makeUnwritableCalls, injector.restoreWritableCalls)
	}
	if backend.verifyCalls != 1 {
		t.Errorf("VerifyCollectorHealthy 调用 = %d, want 1：恢复后必须验证 writable/healthy", backend.verifyCalls)
	}
	if triggerRecorder.calls != 1 {
		t.Errorf("trigger 调用 = %d, want 1", triggerRecorder.calls)
	}
	wantOrder := []string{"preflight", "snapshot", "make-unwritable", "trigger", "snapshot", "restore-writable", "verify"}
	if !slices.Equal(order, wantOrder) {
		t.Fatalf("执行顺序 = %v, want %v", order, wantOrder)
	}
}

// 恢复后 Collector 不健康：storage_unavailable 失败——恢复验证不能走过场。
func TestRunStorageFailureSmokeUnwritableDiskVerifyFails(t *testing.T) {
	clock := newPollerTestClock(storageFailureTestTime(t))
	preflight := &fakeStorageFailurePreflight{}
	backend := &fakeStorageFailureBackend{snapshotFn: func(call int) (StorageHealthSnapshot, error) {
		if call == 0 {
			return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, StorageWritable: true}, nil
		}
		return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, StorageWritable: false, EnqueueFailed: 7}, nil
	}, verifyErr: errors.New("collector not healthy after restore")}
	injector := &fakeStorageFailureInjector{}

	report, err := RunStorageFailureSmoke(context.Background(), storageFailureRequest(StorageFailureUnwritableDisk, t), storageFailureTestDeps(preflight, backend, injector, (&fakeStorageFailureTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunStorageFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	check := findCheck(t, report.Checks(), "collector")
	if check.ErrorClass != "storage_unavailable" {
		t.Errorf("collector check ErrorClass = %q, want storage_unavailable", check.ErrorClass)
	}
}

// 恢复写入能力失败：磁盘可能仍不可写——cleanup failed + unwritable-storage
// residual + 整体 failed。
func TestRunStorageFailureSmokeUnwritableDiskRestoreFailureReportsResidual(t *testing.T) {
	clock := newPollerTestClock(storageFailureTestTime(t))
	preflight := &fakeStorageFailurePreflight{}
	backend := &fakeStorageFailureBackend{snapshotFn: func(call int) (StorageHealthSnapshot, error) {
		if call == 0 {
			return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, StorageWritable: true}, nil
		}
		return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, StorageWritable: false, EnqueueFailed: 7}, nil
	}}
	injector := &fakeStorageFailureInjector{restoreWritableErr: errors.New("chmod failed")}

	report, err := RunStorageFailureSmoke(context.Background(), storageFailureRequest(StorageFailureUnwritableDisk, t), storageFailureTestDeps(preflight, backend, injector, (&fakeStorageFailureTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunStorageFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	cleanup := report.Cleanup()
	if cleanup.Status != "failed" || !slices.Contains(cleanup.ResidualResources, "unwritable-storage") {
		t.Errorf("cleanup = %q %v, want failed + unwritable-storage residual", cleanup.Status, cleanup.ResidualResources)
	}
}

// make-unwritable 失败：注入未生效，不得继续产生流量或验证。
func TestRunStorageFailureSmokeUnwritableDiskInjectionFailureAborts(t *testing.T) {
	clock := newPollerTestClock(storageFailureTestTime(t))
	preflight := &fakeStorageFailurePreflight{}
	backend := &fakeStorageFailureBackend{snapshotFn: func(int) (StorageHealthSnapshot, error) {
		return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, StorageWritable: true}, nil
	}}
	injector := &fakeStorageFailureInjector{makeUnwritableErr: errors.New("docker exec permission denied")}
	triggerRecorder := &fakeStorageFailureTrigger{}

	report, err := RunStorageFailureSmoke(context.Background(), storageFailureRequest(StorageFailureUnwritableDisk, t), storageFailureTestDeps(preflight, backend, injector, triggerRecorder.invoke(), clock))
	if err != nil {
		t.Fatalf("RunStorageFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	if triggerRecorder.calls != 0 {
		t.Errorf("trigger 调用 = %d, want 0", triggerRecorder.calls)
	}
	if backend.verifyCalls != 0 {
		t.Errorf("VerifyCollectorHealthy 调用 = %d, want 0", backend.verifyCalls)
	}
	if injector.restoreWritableCalls != 0 {
		t.Errorf("restoreWritableCalls = %d, want 0：未注入则无恢复义务", injector.restoreWritableCalls)
	}
}

// shutdown_timeout 主路径：StopCollector 返回超时哨兵 + dropped 证据，
// restart + verify healthy 后通过。
func TestRunStorageFailureSmokeShutdownTimeoutObservesAndRecovers(t *testing.T) {
	clock := newPollerTestClock(storageFailureTestTime(t))
	var order []string
	preflight := &fakeStorageFailurePreflight{order: &order}
	backend := &fakeStorageFailureBackend{snapshotFn: func(call int) (StorageHealthSnapshot, error) {
		if call == 0 {
			return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 40, QueueCapacity: 10000, Dropped: 0, StorageWritable: true}, nil
		}
		return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 2, QueueCapacity: 10000, Dropped: 38, StorageWritable: true}, nil
	}, order: &order}
	injector := &fakeStorageFailureInjector{stopErr: ErrCollectorShutdownTimeout, order: &order}

	report, err := RunStorageFailureSmoke(context.Background(), storageFailureRequest(StorageFailureShutdownTimeout, t), storageFailureTestDeps(preflight, backend, injector, (&fakeStorageFailureTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunStorageFailureSmoke() = err %v, want nil", err)
	}
	check := storageFailurePassedCollectorCheck(t, report)
	if shutdownTimedOut, ok := check.Evidence["shutdown_timed_out"]; !ok || shutdownTimedOut != true {
		t.Errorf("shutdown_timed_out 证据 = %v (ok=%v), want true", shutdownTimedOut, ok)
	}
	if got := evidenceInt(t, check, "dropped_delta"); got != 38 {
		t.Errorf("dropped_delta = %d, want 38：shutdown 超时丢记录必须进入报告", got)
	}
	if injector.stopCalls != 1 || injector.restartCalls != 1 {
		t.Errorf("stop/restart 调用 = %d/%d, want 1/1", injector.stopCalls, injector.restartCalls)
	}
	if backend.verifyCalls != 1 {
		t.Errorf("VerifyCollectorHealthy 调用 = %d, want 1", backend.verifyCalls)
	}
	wantOrder := []string{"preflight", "snapshot", "stop", "restart", "snapshot", "verify"}
	if !slices.Equal(order, wantOrder) {
		t.Fatalf("执行顺序 = %v, want %v", order, wantOrder)
	}
}

// StopCollector 未产生超时：场景无法证明 shutdown 超时处理，失败。
func TestRunStorageFailureSmokeShutdownWithoutTimeoutFails(t *testing.T) {
	clock := newPollerTestClock(storageFailureTestTime(t))
	preflight := &fakeStorageFailurePreflight{}
	backend := &fakeStorageFailureBackend{snapshotFn: func(int) (StorageHealthSnapshot, error) {
		return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, StorageWritable: true}, nil
	}}
	injector := &fakeStorageFailureInjector{}

	report, err := RunStorageFailureSmoke(context.Background(), storageFailureRequest(StorageFailureShutdownTimeout, t), storageFailureTestDeps(preflight, backend, injector, (&fakeStorageFailureTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunStorageFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	check := findCheck(t, report.Checks(), "collector")
	if check.ErrorClass != "unexpected_evidence" {
		t.Errorf("collector check ErrorClass = %q, want unexpected_evidence", check.ErrorClass)
	}
}

// StopCollector 真实错误（非超时哨兵）：仍然尝试 restart + verify 恢复，
// 报告 export/export_failed。
func TestRunStorageFailureSmokeStopErrorStillRecovers(t *testing.T) {
	clock := newPollerTestClock(storageFailureTestTime(t))
	preflight := &fakeStorageFailurePreflight{}
	backend := &fakeStorageFailureBackend{snapshotFn: func(int) (StorageHealthSnapshot, error) {
		return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, StorageWritable: true}, nil
	}}
	injector := &fakeStorageFailureInjector{stopErr: errors.New("docker compose stop failed")}

	report, err := RunStorageFailureSmoke(context.Background(), storageFailureRequest(StorageFailureShutdownTimeout, t), storageFailureTestDeps(preflight, backend, injector, (&fakeStorageFailureTrigger{}).invoke(), clock))
	if err != nil {
		t.Fatalf("RunStorageFailureSmoke() = err %v, want 报告内失败", err)
	}
	if report.Status() != "failed" {
		t.Errorf("report.Status() = %q, want failed", report.Status())
	}
	check := findCheck(t, report.Checks(), "collector")
	if check.FailureStage != "export" || check.ErrorClass != "export_failed" {
		t.Errorf("collector check = %q/%q, want export/export_failed", check.FailureStage, check.ErrorClass)
	}
	if injector.restartCalls != 1 || backend.verifyCalls != 1 {
		t.Errorf("restart/verify 调用 = %d/%d, want 1/1：stop 失败也必须尝试恢复", injector.restartCalls, backend.verifyCalls)
	}
}

// preflight 拒绝：类别哨兵（invalid_collector_pipeline /
// storage_path_unavailable）→ failure_stage=preflight + 稳定
// error_class=invalid_configuration + 零外部调用；preflight 与 runtime
// storage failure 必须在报告中可区分。
func TestRunStorageFailureSmokePreflightRejectionIsStableAndInjectsNothing(t *testing.T) {
	for _, preflightErr := range []error{ErrCollectorPipelineInvalid, ErrStoragePathUnavailable} {
		preflight := &fakeStorageFailurePreflight{err: preflightErr}
		backend := &fakeStorageFailureBackend{snapshotFn: func(int) (StorageHealthSnapshot, error) {
			return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, StorageWritable: true}, nil
		}}
		injector := &fakeStorageFailureInjector{}
		triggerRecorder := &fakeStorageFailureTrigger{}

		report, err := RunStorageFailureSmoke(context.Background(), storageFailureRequest(StorageFailureUnwritableDisk, t), storageFailureTestDeps(preflight, backend, injector, triggerRecorder.invoke(), newPollerTestClock(storageFailureTestTime(t))))
		if err != nil {
			t.Fatalf("RunStorageFailureSmoke() = err %v, want 报告内失败", err)
		}
		if report.Status() != "failed" {
			t.Errorf("preflight=%v: report.Status() = %q, want failed", preflightErr, report.Status())
		}
		check := findCheck(t, report.Checks(), "collector")
		if check.FailureStage != "preflight" {
			t.Errorf("preflight=%v: FailureStage = %q, want preflight（与 runtime storage failure 区分）", preflightErr, check.FailureStage)
		}
		if check.ErrorClass != "invalid_configuration" {
			t.Errorf("preflight=%v: ErrorClass = %q, want invalid_configuration", preflightErr, check.ErrorClass)
		}
		if triggerRecorder.calls != 0 || injector.makeUnwritableCalls != 0 || injector.stopCalls != 0 {
			t.Errorf("preflight=%v: 触发/注入调用 = %d/%d/%d, want 全部为 0：启动前拒绝不得产生任何外部调用",
				preflightErr, triggerRecorder.calls, injector.makeUnwritableCalls, injector.stopCalls)
		}
		if backend.snapshotN != 0 {
			t.Errorf("preflight=%v: 快照调用 = %d, want 0", preflightErr, backend.snapshotN)
		}
	}
}

// 请求与依赖校验：非法输入直接报错。
func TestRunStorageFailureSmokeRejectsInvalidRequests(t *testing.T) {
	base := storageFailureTestTime(t)
	validDeps := storageFailureTestDeps(&fakeStorageFailurePreflight{}, &fakeStorageFailureBackend{snapshotFn: func(int) (StorageHealthSnapshot, error) {
		return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, StorageWritable: true}, nil
	}}, &fakeStorageFailureInjector{}, (&fakeStorageFailureTrigger{}).invoke(), newPollerTestClock(base))
	validRequest := storageFailureRequest(StorageFailureUnwritableDisk, t)

	tests := []struct {
		name   string
		mutate func(*StorageFailureSmokeRequest, *StorageFailureSmokeDependencies)
	}{
		{"profile 不在允许集", func(r *StorageFailureSmokeRequest, _ *StorageFailureSmokeDependencies) { r.Profile = "unknown" }},
		{"deadline 为零值", func(r *StorageFailureSmokeRequest, _ *StorageFailureSmokeDependencies) { r.Deadline = time.Time{} }},
		{"deadline 已过期", func(r *StorageFailureSmokeRequest, _ *StorageFailureSmokeDependencies) {
			r.Deadline = base.Add(-time.Second)
		}},
		{"scenario 未知", func(r *StorageFailureSmokeRequest, _ *StorageFailureSmokeDependencies) {
			r.Scenario = "unknown_scenario"
		}},
		{"service 为空", func(r *StorageFailureSmokeRequest, _ *StorageFailureSmokeDependencies) { r.Service = "" }},
		{"component 为空", func(r *StorageFailureSmokeRequest, _ *StorageFailureSmokeDependencies) { r.ComponentID = "" }},
		{"service 含 shell 元字符", func(r *StorageFailureSmokeRequest, _ *StorageFailureSmokeDependencies) {
			r.Service = "collector; rm -rf /"
		}},
		{"Preflight 缺失", func(_ *StorageFailureSmokeRequest, d *StorageFailureSmokeDependencies) { d.Preflight = nil }},
		{"Backend 缺失", func(_ *StorageFailureSmokeRequest, d *StorageFailureSmokeDependencies) { d.Backend = nil }},
		{"Injector 缺失", func(_ *StorageFailureSmokeRequest, d *StorageFailureSmokeDependencies) { d.Injector = nil }},
		{"Trigger 缺失", func(_ *StorageFailureSmokeRequest, d *StorageFailureSmokeDependencies) { d.Trigger = nil }},
		{"Clock 缺失", func(_ *StorageFailureSmokeRequest, d *StorageFailureSmokeDependencies) { d.Clock = nil }},
		{"PollInterval 非正", func(_ *StorageFailureSmokeRequest, d *StorageFailureSmokeDependencies) { d.PollInterval = 0 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mutatedRequest := validRequest
			mutatedDeps := validDeps
			tc.mutate(&mutatedRequest, &mutatedDeps)
			if _, err := RunStorageFailureSmoke(context.Background(), mutatedRequest, mutatedDeps); err == nil {
				t.Error("RunStorageFailureSmoke() = nil error, want 校验错误")
			}
		})
	}
}

// ctx 取消必须中止运行并返回错误。
func TestRunStorageFailureSmokeContextCancellationAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	preflight := &fakeStorageFailurePreflight{}
	backend := &fakeStorageFailureBackend{snapshotFn: func(int) (StorageHealthSnapshot, error) {
		return StorageHealthSnapshot{ComponentID: "otlp/tempo", QueueSize: 0, QueueCapacity: 10000, StorageWritable: true}, nil
	}}
	injector := &fakeStorageFailureInjector{}

	report, err := RunStorageFailureSmoke(ctx, storageFailureRequest(StorageFailureUnwritableDisk, t), storageFailureTestDeps(preflight, backend, injector, (&fakeStorageFailureTrigger{}).invoke(), newPollerTestClock(storageFailureTestTime(t))))
	if err == nil {
		t.Fatalf("RunStorageFailureSmoke() = %v, want ctx 错误", report)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if injector.makeUnwritableCalls != 0 {
		t.Errorf("makeUnwritableCalls = %d, want 0", injector.makeUnwritableCalls)
	}
}
