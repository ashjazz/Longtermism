package smoke

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

// T114 契约的 GREEN 实现：queue/storage/shutdown 极限故障场景。
//
// 生产约束（FR-009 资产侧 + data-model §13 状态机）：
// - preflight（obs-config-check 同语义的启动前拒绝）必须先于任何注入/流量，
//   失败进入 failure_stage=preflight + 稳定 error_class=invalid_configuration，
//   与 runtime storage failure（export/query stage）在报告中可区分；
// - queue exhaustion 必须有界逼近容量并取得 dropped/enqueue_failed 证据，
//   deadline 内未耗尽或证据缺失都判失败，不猜测通过；
// - unwritable_disk：观察到不可写事实后，任何退出路径都恢复写入能力，
//   恢复后必须 VerifyCollectorHealthy；恢复失败 → unwritable-storage residual；
// - shutdown_timeout：StopCollector 必须返回 ErrCollectorShutdownTimeout
//   哨兵（否则视为未产生超时 → 失败），重启后验证 healthy，报告携带
//   shutdown_timed_out 与 dropped 证据；
// - 业务失败不要求：本场景只断言观测事实。

var (
	errStorageFailureSmokeFailed = errors.New("storage failure smoke verification failed")

	// ErrCollectorPipelineInvalid 对应 obs-config-check 的
	// invalid_collector_pipeline 类别：无效 Collector pipeline 的启动前拒绝。
	ErrCollectorPipelineInvalid = errors.New("invalid collector pipeline")

	// ErrStoragePathUnavailable 对应 obs-config-check 的 storage_path_unavailable
	// 类别：缺失/非目录/不可写 storage path 的启动前拒绝。
	ErrStoragePathUnavailable = errors.New("storage path unavailable")

	// ErrCollectorShutdownTimeout 表示 Collector 停机超过 grace period，
	// 是 shutdown_timeout 场景必须观察到的故障事实。
	ErrCollectorShutdownTimeout = errors.New("collector shutdown timeout")
)

// StorageFailureScenario 标识三个极限场景之一。
type StorageFailureScenario string

const (
	StorageFailureQueueExhaustion StorageFailureScenario = "queue_exhaustion"
	StorageFailureUnwritableDisk  StorageFailureScenario = "unwritable_disk"
	StorageFailureShutdownTimeout StorageFailureScenario = "shutdown_timeout"
)

// StorageHealthSnapshot 是 Collector 存储/队列健康的按组件快照。
type StorageHealthSnapshot struct {
	ComponentID     string
	QueueSize       int64
	QueueCapacity   int64
	EnqueueFailed   int64
	Dropped         int64
	SendFailed      int64
	StorageWritable bool
}

// StorageFailurePreflight 在注入前执行启动前检查（Collector pipeline 与
// storage path 有效性）。返回的哨兵错误类别必须稳定（见上方两个 Err 常量）。
type StorageFailurePreflight interface {
	Check(context.Context) error
}

type StorageFailureSmokeBackend interface {
	SnapshotCollectorStorage(context.Context) (StorageHealthSnapshot, error)
	VerifyCollectorHealthy(context.Context) error
}

type StorageFailureInjector interface {
	MakeStorageUnwritable(context.Context) error
	RestoreStorageWritable(context.Context) error
	StopCollector(context.Context) error
	RestartCollector(context.Context) error
}

type StorageFailureTrigger func(context.Context) error

type StorageFailureSmokeIdentity struct{ RunID, Marker string }

type StorageFailureSmokeIdentityFactory func(context.Context) (StorageFailureSmokeIdentity, error)

type StorageFailureSmokeRequest struct {
	Deadline    time.Time
	Profile     string
	Scenario    StorageFailureScenario
	Service     string
	ComponentID string
}

type StorageFailureSmokeDependencies struct {
	Preflight       StorageFailurePreflight
	Backend         StorageFailureSmokeBackend
	Injector        StorageFailureInjector
	Trigger         StorageFailureTrigger
	Clock           PollerClock
	IdentityFactory StorageFailureSmokeIdentityFactory
	PollInterval    time.Duration
}

// RunStorageFailureSmoke 按 request.Scenario 执行对应极限场景。参数/依赖
// 非法返回 error；验证失败写入报告。
func RunStorageFailureSmoke(ctx context.Context, request StorageFailureSmokeRequest, deps StorageFailureSmokeDependencies) (*SmokeReport, error) {
	if err := validateStorageFailureRun(ctx, request, deps); err != nil {
		return nil, err
	}
	clock := deps.Clock
	startedAt := clock.Now().UTC()

	identityFactory := deps.IdentityFactory
	if identityFactory == nil {
		identityFactory = newStorageFailureSmokeIdentity
	}
	identity, err := identityFactory(ctx)
	if err != nil || !isOpaqueSmokeIdentity(identity.RunID) || !isSafePollMarker(identity.Marker) {
		return nil, errStorageFailureSmokeFailed
	}

	// preflight 必须先于任何注入/快照/流量：启动前拒绝与 runtime storage
	// failure 在报告中必须是两种可区分的事实（T124 门控）。
	if err := deps.Preflight.Check(ctx); err != nil {
		return buildStorageFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, []BackendCheckInput{
			{Backend: "collector", Status: "failed", FailureStage: "preflight", ErrorClass: "invalid_configuration"},
		})
	}

	switch request.Scenario {
	case StorageFailureQueueExhaustion:
		return runStorageFailureQueueExhaustion(ctx, request, deps, identity, startedAt)
	case StorageFailureUnwritableDisk:
		return runStorageFailureUnwritableDisk(ctx, request, deps, identity, startedAt)
	case StorageFailureShutdownTimeout:
		return runStorageFailureShutdownTimeout(ctx, request, deps, identity, startedAt)
	}
	return nil, errStorageFailureSmokeFailed
}

// runStorageFailureQueueExhaustion：有界循环产生流量，直到队列达到容量且
// 出现 dropped/enqueue_failed 证据。注入器在本场景零调用（只靠流量）。
func runStorageFailureQueueExhaustion(ctx context.Context, request StorageFailureSmokeRequest, deps StorageFailureSmokeDependencies, identity StorageFailureSmokeIdentity, startedAt time.Time) (*SmokeReport, error) {
	clock := deps.Clock
	var first StorageHealthSnapshot
	firstTaken := false
	for clock.Now().Before(request.Deadline) {
		snapshot, err := deps.Backend.SnapshotCollectorStorage(ctx)
		if err != nil {
			return buildStorageFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, []BackendCheckInput{
				{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "query_failed"},
			})
		}
		if !firstTaken {
			first = snapshot
			firstTaken = true
		}
		if snapshot.QueueSize >= snapshot.QueueCapacity &&
			(snapshot.EnqueueFailed > first.EnqueueFailed || snapshot.Dropped > first.Dropped) {
			return buildStorageFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, []BackendCheckInput{
				{
					Backend:      "collector",
					Status:       "passed",
					FailureStage: "none",
					Evidence: map[string]any{
						"queue_depth":          snapshot.QueueSize,
						"enqueue_failed_delta": snapshot.EnqueueFailed - first.EnqueueFailed,
						"dropped_delta":        snapshot.Dropped - first.Dropped,
					},
				},
			})
		}
		_ = deps.Trigger(ctx)
		if err := clock.Wait(ctx, deps.PollInterval); err != nil {
			return nil, err
		}
	}
	return buildStorageFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, []BackendCheckInput{
		{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "unexpected_evidence"},
	})
}

// runStorageFailureUnwritableDisk：注入不可写 → 流量 → 观察不可写事实 →
// 恢复写入能力 → 验证 Collector healthy。
func runStorageFailureUnwritableDisk(ctx context.Context, request StorageFailureSmokeRequest, deps StorageFailureSmokeDependencies, identity StorageFailureSmokeIdentity, startedAt time.Time) (*SmokeReport, error) {
	clock := deps.Clock
	before, err := deps.Backend.SnapshotCollectorStorage(ctx)
	if err != nil {
		return buildStorageFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, []BackendCheckInput{
			{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "query_failed"},
		})
	}

	if err := deps.Injector.MakeStorageUnwritable(ctx); err != nil {
		return buildStorageFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, []BackendCheckInput{
			{Backend: "collector", Status: "failed", FailureStage: "export", ErrorClass: "export_failed"},
		})
	}

	_ = deps.Trigger(ctx)
	after, snapshotErr := deps.Backend.SnapshotCollectorStorage(ctx)

	// 任何退出路径都恢复写入能力；恢复失败 → unwritable-storage residual。
	cleanupStatus := "completed"
	var residualResources []string
	if restoreErr := deps.Injector.RestoreStorageWritable(ctx); restoreErr != nil {
		cleanupStatus = "failed"
		residualResources = []string{"unwritable-storage"}
	}

	if snapshotErr != nil {
		return buildStorageFailureReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, residualResources, []BackendCheckInput{
			{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "query_failed"},
		})
	}

	enqueueDelta := after.EnqueueFailed - before.EnqueueFailed
	droppedDelta := after.Dropped - before.Dropped

	check := BackendCheckInput{
		Backend: "collector",
		Evidence: map[string]any{
			"storage_writable":     after.StorageWritable,
			"enqueue_failed_delta": enqueueDelta,
			"dropped_delta":        droppedDelta,
		},
	}
	switch {
	case after.StorageWritable:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "unexpected_evidence"
	case enqueueDelta <= 0 && droppedDelta <= 0:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "unexpected_evidence"
	case cleanupStatus == "failed":
		// 归因可能正确，但磁盘可能仍不可写：cleanup 已标记 failed，
		// collector check 保持 passed，由 cleanup 决定整体失败。
		check.Status, check.FailureStage = "passed", "none"
	default:
		check.Status, check.FailureStage = "passed", "none"
	}

	// 恢复后验证 Collector writable/healthy（T124 门控）；恢复失败时跳过
	// 验证（磁盘仍不可写，验证无意义）。
	if cleanupStatus == "completed" {
		if verifyErr := deps.Backend.VerifyCollectorHealthy(ctx); verifyErr != nil {
			check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "storage_unavailable"
		}
	}
	return buildStorageFailureReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, residualResources, []BackendCheckInput{check})
}

// runStorageFailureShutdownTimeout：观察 shutdown 超时哨兵 → 重启 →
// 快照 dropped 证据 → 验证 healthy。
func runStorageFailureShutdownTimeout(ctx context.Context, request StorageFailureSmokeRequest, deps StorageFailureSmokeDependencies, identity StorageFailureSmokeIdentity, startedAt time.Time) (*SmokeReport, error) {
	clock := deps.Clock
	before, err := deps.Backend.SnapshotCollectorStorage(ctx)
	if err != nil {
		return buildStorageFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, []BackendCheckInput{
			{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "query_failed"},
		})
	}

	stopErr := deps.Injector.StopCollector(ctx)
	timeoutObserved := errors.Is(stopErr, ErrCollectorShutdownTimeout)
	stopFailed := stopErr != nil && !timeoutObserved

	// stop 无论成功、超时还是失败，collector 都可能已停止：必须重启恢复。
	restartErr := deps.Injector.RestartCollector(ctx)
	cleanupStatus := "completed"
	if restartErr != nil {
		cleanupStatus = "failed"
	}

	after, snapshotErr := deps.Backend.SnapshotCollectorStorage(ctx)
	if snapshotErr != nil {
		return buildStorageFailureReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, nil, []BackendCheckInput{
			{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "query_failed"},
		})
	}

	droppedDelta := after.Dropped - before.Dropped
	check := BackendCheckInput{
		Backend: "collector",
		Evidence: map[string]any{
			"shutdown_timed_out": timeoutObserved,
			"dropped_delta":      droppedDelta,
		},
	}

	switch {
	case stopFailed:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "export", "export_failed"
	case !timeoutObserved:
		// stop 未产生超时：场景无法证明 shutdown 超时处理能力。
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "unexpected_evidence"
	case droppedDelta <= 0:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "unexpected_evidence"
	case restartErr != nil:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "export", "export_failed"
	default:
		check.Status, check.FailureStage = "passed", "none"
	}

	if restartErr == nil {
		if verifyErr := deps.Backend.VerifyCollectorHealthy(ctx); verifyErr != nil && check.Status == "passed" {
			check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "storage_unavailable"
		}
	}
	return buildStorageFailureReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, nil, []BackendCheckInput{check})
}

func validateStorageFailureRun(ctx context.Context, request StorageFailureSmokeRequest, deps StorageFailureSmokeDependencies) error {
	if ctx == nil {
		return errStorageFailureSmokeFailed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if deps.Preflight == nil || deps.Backend == nil || deps.Injector == nil || deps.Trigger == nil || deps.Clock == nil || deps.PollInterval <= 0 {
		return errStorageFailureSmokeFailed
	}
	if !contains(allowedProfiles, request.Profile) {
		return errStorageFailureSmokeFailed
	}
	if request.Deadline.IsZero() || !request.Deadline.After(deps.Clock.Now()) {
		return errStorageFailureSmokeFailed
	}
	switch request.Scenario {
	case StorageFailureQueueExhaustion, StorageFailureUnwritableDisk, StorageFailureShutdownTimeout:
	default:
		return errStorageFailureSmokeFailed
	}
	if !safeExporterScopePattern.MatchString(request.Service) ||
		!safeExporterComponentPattern.MatchString(request.ComponentID) {
		return errStorageFailureSmokeFailed
	}
	return nil
}

func buildStorageFailureReport(request StorageFailureSmokeRequest, identity StorageFailureSmokeIdentity, startedAt, finishedAt time.Time, cleanupStatus string, residualResources []string, checks []BackendCheckInput) (*SmokeReport, error) {
	return BuildSmokeReport(SmokeReportInput{
		RunID:      identity.RunID,
		Marker:     identity.Marker,
		Profile:    request.Profile,
		Scenario:   "storage_failure",
		StartedAt:  startedAt,
		Deadline:   request.Deadline,
		FinishedAt: finishedAt,
		Checks:     checks,
		Cleanup: SmokeCleanupInput{
			Status:               cleanupStatus,
			ResidualResources:    residualResources,
			TemporaryCredentials: "not_created",
			TemporaryData:        "not_created",
		},
	})
}

func newStorageFailureSmokeIdentity(context.Context) (StorageFailureSmokeIdentity, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return StorageFailureSmokeIdentity{}, err
	}
	encoded := hex.EncodeToString(bytes)
	return StorageFailureSmokeIdentity{RunID: "run-" + encoded, Marker: "marker-" + encoded}, nil
}
