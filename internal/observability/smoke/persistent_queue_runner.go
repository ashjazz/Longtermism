package smoke

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

// T113 契约的 GREEN 实现：跨 Collector 重启的持久队列恢复场景。
//
// 生产约束（FR-008 + US3 验收场景 2）：
// - 中断期间产生的待发送记录在 Collector 重启后必须于受限窗口内被投递，
//   投递语义显式声明 at-least-once（PersistentQueueDeliveryGuarantee），
//   绝不宣称 exactly-once；
// - duplicate delivery（sent delta 大于积压量）是语义内事实：报告必须
//   通过，但 duplicate_delivered 必须如实写入，不得静默忽略；
// - drain 窗口默认 120 秒，轮询目标携带 resume+window 的有界 deadline，
//   迟到 marker（ObservedAt 越过窗口）必须被隔离；
// - 任何退出路径（restart 失败、drain 超时、unpause 失败）都恢复 backend。

var errPersistentQueueSmokeFailed = errors.New("persistent queue smoke verification failed")

// PersistentQueueDrainWindow 是恢复窗口默认上界（T113 契约）。
const PersistentQueueDrainWindow = 120 * time.Second

// PersistentQueueDeliveryGuarantee 是本场景声明的投递语义：允许重复投递，
// 但重复必须被识别并写入报告，而不是被宣称成 exactly-once 或静默吞掉。
const PersistentQueueDeliveryGuarantee = "at-least-once"

// PersistentQueueSnapshot 是 Collector 持久队列/出口计数器的按组件快照。
type PersistentQueueSnapshot struct {
	ComponentID   string
	QueueSize     int64
	QueueCapacity int64
	Sent          int64
	EnqueueFailed int64
	SendFailed    int64
	Dropped       int64
}

type PersistentQueueSmokeBackend interface {
	SnapshotCollectorQueue(context.Context) (PersistentQueueSnapshot, error)
	QueryBackendMarker(context.Context, PollMarkerTarget) ([]MarkerObservation, error)
}

type PersistentQueueInjector interface {
	Pause(context.Context, string) error
	Unpause(context.Context, string) error
	RestartCollector(context.Context) error
}

type PersistentQueueTrigger func(context.Context) error

type PersistentQueueSmokeIdentity struct{ RunID, Marker string }

type PersistentQueueSmokeIdentityFactory func(context.Context) (PersistentQueueSmokeIdentity, error)

type PersistentQueueSmokeRequest struct {
	Deadline       time.Time
	Profile        string
	BackendService string
	ComponentID    string
	// DrainWindow 是恢复窗口上界；0 表示使用 PersistentQueueDrainWindow。
	DrainWindow time.Duration
}

type PersistentQueueSmokeDependencies struct {
	Backend         PersistentQueueSmokeBackend
	Injector        PersistentQueueInjector
	Trigger         PersistentQueueTrigger
	Clock           PollerClock
	IdentityFactory PersistentQueueSmokeIdentityFactory
	PollInterval    time.Duration
}

// RunPersistentQueueSmoke 执行跨重启恢复验证。参数/依赖非法返回 error；
// 验证失败写入报告。
func RunPersistentQueueSmoke(ctx context.Context, request PersistentQueueSmokeRequest, deps PersistentQueueSmokeDependencies) (*SmokeReport, error) {
	if err := validatePersistentQueueRun(ctx, request, deps); err != nil {
		return nil, err
	}
	clock := deps.Clock
	startedAt := clock.Now().UTC()

	identityFactory := deps.IdentityFactory
	if identityFactory == nil {
		identityFactory = NewPersistentQueueSmokeIdentity
	}
	identity, err := identityFactory(ctx)
	if err != nil || !isOpaqueSmokeIdentity(identity.RunID) || !isSafePollMarker(identity.Marker) {
		return nil, errPersistentQueueSmokeFailed
	}

	drainWindow := request.DrainWindow
	if drainWindow == 0 {
		drainWindow = PersistentQueueDrainWindow
	}

	checks := make([]BackendCheckInput, 0, 2)
	cleanupStatus := "completed"
	var residualResources []string

	// 1. 基线快照。
	before, err := deps.Backend.SnapshotCollectorQueue(ctx)
	if err != nil {
		return buildPersistentQueueReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, []BackendCheckInput{
			persistentQueueCollectorCheck("failed-query", 0, 0, 0),
			persistentQueueTempoCheck("skipped", 0),
		})
	}

	// 2. 暂停 backend。
	if err := deps.Injector.Pause(ctx, request.BackendService); err != nil {
		return buildPersistentQueueReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, []BackendCheckInput{
			persistentQueueCollectorCheck("failed-injection", 0, 0, 0),
			persistentQueueTempoCheck("skipped", 0),
		})
	}

	// 3. 产生 marker 流量。trigger 失败不直接判死：积压证据会如实暴露
	// "没有产生待发送记录"这一事实（backlog 检查）。
	_ = deps.Trigger(ctx)

	// 4. 积压快照。真实装配的快照来自 Prometheus scrape（15s 周期），而
	// pause→trigger 后积压需要经历 OTLP 推送 + collector 入队 + 下一次
	// scrape 才可见——单次快照会稳定误报 no-backlog。这里对"积压上涨"
	// 做有界轮询（至少等一个 poll interval，直到 deadline），任何一次
	// 快照看到 QueueSize > before 即继续；全程未见则如实按 no-backlog 失败。
	duringPause, err := persistentQueueWaitForBacklog(ctx, deps, request, before, PollMarkerTarget{Marker: identity.Marker, StartedAt: startedAt, Deadline: request.Deadline})
	if err != nil || !persistentQueueComponentMatches(request, duringPause) {
		unpauseErr := deps.Injector.Unpause(ctx, request.BackendService)
		cleanupStatus, residualResources = persistentQueueCleanupAfterRestore(unpauseErr)
		return buildPersistentQueueReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, residualResources, []BackendCheckInput{
			persistentQueueCollectorCheck("failed-query", 0, 0, 0),
			persistentQueueTempoCheck("skipped", 0),
		})
	}
	backlog := duringPause.QueueSize - before.QueueSize
	if backlog <= 0 {
		unpauseErr := deps.Injector.Unpause(ctx, request.BackendService)
		cleanupStatus, residualResources = persistentQueueCleanupAfterRestore(unpauseErr)
		return buildPersistentQueueReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, residualResources, []BackendCheckInput{
			persistentQueueCollectorCheck("no-backlog", 0, 0, 0),
			persistentQueueTempoCheck("skipped", 0),
		})
	}

	// 5. 跨重启：restart Collector（持久队列由 file_storage 保存）。
	if err := deps.Injector.RestartCollector(ctx); err != nil {
		unpauseErr := deps.Injector.Unpause(ctx, request.BackendService)
		cleanupStatus, residualResources = persistentQueueCleanupAfterRestore(unpauseErr)
		return buildPersistentQueueReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, residualResources, []BackendCheckInput{
			persistentQueueCollectorCheck("failed-restart", 0, 0, 0),
			persistentQueueTempoCheck("skipped", 0),
		})
	}

	// 6. 恢复 backend。
	restoreErr := deps.Injector.Unpause(ctx, request.BackendService)
	cleanupStatus, residualResources = persistentQueueCleanupAfterRestore(restoreErr)

	// 7. 有界 drain 轮询：窗口 = resume + drainWindow，以 request.Deadline 为上界。
	// marker 的搜索窗口从 run 起点开始（trigger 流量产生于 pause 期间），但
	// 查询窗口必须保持有界（marker target 安全校验的上限）：下界取
	// max(run startedAt, drainEnd-150s)，保证覆盖 trigger 时刻且不超过安全上限。
	drainStart := clock.Now()
	drainEnd := request.Deadline
	if windowEnd := drainStart.Add(drainWindow); windowEnd.Before(drainEnd) {
		drainEnd = windowEnd
	}
	searchStart := startedAt
	if floor := drainEnd.Add(-150 * time.Second); searchStart.Before(floor) {
		searchStart = floor
	}
	target := PollMarkerTarget{Marker: identity.Marker, StartedAt: searchStart, Deadline: drainEnd}
	matchedSpans, queryErr := persistentQueuePollMarker(ctx, deps, target)
	tempoCheck := persistentQueueTempoCheckFromResult(matchedSpans, queryErr)

	// 8. drain 后快照与会计：sent delta vs 积压量。
	// reset-aware：本场景在暂停与 drain 之间重启 Collector（这正是被验证的
	// 恢复能力），重启后 sent counter 从 0 重新计数，跨进程差值恒为负。
	// counter 回退即 reset 的机器可判证据；此时 sent 会计跨重启不可比——
	// 积压已处理的判定以 marker 命中（tempo check）为一等证据，duplicate
	// 计数记 0 且不在 sent 不可比时宣称（不伪造，也不宣称 exactly-once）。
	// marker 未命中而 sent 也不可比时，由 tempo check 的 marker_missing
	// 承载失败事实，drain 会计如实呈现 reset。
	after, snapshotErr := deps.Backend.SnapshotCollectorQueue(ctx)
	if snapshotErr != nil || !persistentQueueComponentMatches(request, after) {
		checks = append(checks, persistentQueueCollectorCheck("failed-query", 0, 0, 0), tempoCheck)
		return buildPersistentQueueReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, residualResources, checks)
	}
	counterReset := after.Sent < duringPause.Sent
	var drainDelta, duplicates int64
	switch {
	case counterReset:
		// 重启后新进程的 sent 就是从 0 重新累计的发送量；delta 表达为
		// 新进程绝对值，duplicate 跨重启不可推导，记 0 不宣称。
		drainDelta, duplicates = after.Sent, 0
	case after.Sent == duringPause.Sent && tempoCheck.Status == "passed":
		// sent 持平且 marker 已命中：restart 使 Prometheus 里旧序列 stale、
		// 新序列尚未被 scrape（或仍为 0）——sent 会计此刻不可比。marker
		// 命中是积压已处理的一等证据；duplicate 不可推导，如实记 0。
		drainDelta, duplicates = 0, 0
	default:
		drainDelta = after.Sent - duringPause.Sent
		duplicates = drainDelta - backlog
		if duplicates < 0 && tempoCheck.Status != "passed" {
			// sent delta 小于积压量且 marker 未命中：积压未被处理的证据
			// 相互印证，不得判为通过。
			checks = append(checks, persistentQueueCollectorCheck("drain-incomplete", backlog, drainDelta, 0), tempoCheck)
			return buildPersistentQueueReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, residualResources, checks)
		}
		if duplicates < 0 {
			// marker 已命中但 sent 会计数不足：scrape 相位下 after 快照可能
			// 仍停在旧值；把会计事实如实呈现，duplicate 记 0 不宣称。
			duplicates = 0
		}
	}
	if tempoCheck.Status != "passed" {
		// marker 未命中：无论 sent 会计如何，恢复证据缺失，drain 检查失败
		// 与 tempo 失败同源；保持 collector 检查呈现会计事实。
		checks = append(checks, persistentQueueCollectorCheck("no-marker", backlog, drainDelta, duplicates), tempoCheck)
		return buildPersistentQueueReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, residualResources, checks)
	}
	checks = append(checks, persistentQueueCollectorCheck("passed", backlog, drainDelta, duplicates), tempoCheck)
	return buildPersistentQueueReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, residualResources, checks)
}

func persistentQueueComponentMatches(request PersistentQueueSmokeRequest, snapshot PersistentQueueSnapshot) bool {
	return snapshot.ComponentID == request.ComponentID
}

// persistentQueueWaitForBacklog 有界轮询"积压上涨"：真实装配的快照来自
// Prometheus scrape，pause→trigger 后需要等到下一次 scrape 才能看到入队。
// 立即看到（fake 后端/已积压）时零等待返回；否则每个 poll interval 复查
// 一次直到 deadline。查询错误原样上抛（由调用方映射 failed-query）。
func persistentQueueWaitForBacklog(ctx context.Context, deps PersistentQueueSmokeDependencies, request PersistentQueueSmokeRequest, before PersistentQueueSnapshot, window PollMarkerTarget) (PersistentQueueSnapshot, error) {
	var lastErr error
	for {
		snapshot, err := deps.Backend.SnapshotCollectorQueue(ctx)
		if err != nil {
			return snapshot, err
		}
		if snapshot.QueueSize > before.QueueSize {
			return snapshot, nil
		}
		if !deps.Clock.Now().Add(deps.PollInterval).Before(window.Deadline) {
			return snapshot, nil
		}
		if err := deps.Clock.Wait(ctx, deps.PollInterval); err != nil {
			lastErr = err
			return snapshot, lastErr
		}
	}
}

// persistentQueuePollMarker 在受限窗口内轮询 marker。返回命中的观察次数
// 与查询错误；迟到观察（ObservedAt 越过窗口）被隔离，不计入命中。
// 查询错误在窗口内被视为"暂时不可见"而不是终止条件：真实场景里 backend
// 刚从 pause 恢复，Tempo 查询面短暂 5xx 是预期路径——持续到窗口耗尽的
// 查询失败才作为最终错误返回，让报告如实呈现 backend_timeout/query_failed。
func persistentQueuePollMarker(ctx context.Context, deps PersistentQueueSmokeDependencies, target PollMarkerTarget) (int64, error) {
	matched := int64(0)
	var lastQueryErr error
	for deps.Clock.Now().Before(target.Deadline) {
		observations, err := deps.Backend.QueryBackendMarker(ctx, target)
		if err != nil {
			// ctx 取消立刻终止；普通查询错误记录后继续轮询。
			if ctx.Err() != nil {
				return matched, err
			}
			lastQueryErr = err
		} else {
			lastQueryErr = nil
			for _, observation := range observations {
				if observation.Marker != target.Marker {
					continue
				}
				if observation.ObservedAt.Before(target.StartedAt) || observation.ObservedAt.After(target.Deadline) {
					continue
				}
				matched++
			}
			if matched > 0 {
				return matched, nil
			}
		}
		if err := deps.Clock.Wait(ctx, deps.PollInterval); err != nil {
			return matched, err
		}
	}
	// 窗口耗尽：命中数为 0；若期间持续查询失败，返回最后一次错误（报告
	// 区分 backend 查询故障与 marker 未到），否则返回 nil（marker_missing）。
	return matched, lastQueryErr
}

func persistentQueueCleanupAfterRestore(unpauseErr error) (string, []string) {
	if unpauseErr != nil {
		return "failed", []string{"paused-service"}
	}
	return "completed", nil
}

func persistentQueueCollectorCheck(mode string, backlog, drainDelta, duplicates int64) BackendCheckInput {
	switch mode {
	case "failed-query":
		return BackendCheckInput{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "query_failed"}
	case "failed-injection", "failed-restart":
		return BackendCheckInput{Backend: "collector", Status: "failed", FailureStage: "export", ErrorClass: "export_failed"}
	case "no-backlog", "drain-incomplete", "no-marker":
		return BackendCheckInput{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "unexpected_evidence"}
	}
	return BackendCheckInput{
		Backend:      "collector",
		Status:       "passed",
		FailureStage: "none",
		Evidence: map[string]any{
			"queue_depth":         backlog,
			"exporter_sent":       drainDelta,
			"duplicate_delivered": duplicates,
		},
	}
}

func persistentQueueTempoCheck(mode string, matchedSpans int64) BackendCheckInput {
	if mode == "skipped" {
		return BackendCheckInput{Backend: "tempo", Status: "skipped", FailureStage: "none"}
	}
	return BackendCheckInput{Backend: "tempo", Status: "passed", FailureStage: "none", Evidence: map[string]any{"matched_spans": matchedSpans}}
}

func persistentQueueTempoCheckFromResult(matchedSpans int64, queryErr error) BackendCheckInput {
	if queryErr != nil {
		return BackendCheckInput{Backend: "tempo", Status: "failed", FailureStage: "query", ErrorClass: "query_failed"}
	}
	if matchedSpans == 0 {
		return BackendCheckInput{Backend: "tempo", Status: "failed", FailureStage: "query", ErrorClass: "marker_missing"}
	}
	return persistentQueueTempoCheck("passed", matchedSpans)
}

func validatePersistentQueueRun(ctx context.Context, request PersistentQueueSmokeRequest, deps PersistentQueueSmokeDependencies) error {
	if ctx == nil {
		return errPersistentQueueSmokeFailed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if deps.Backend == nil || deps.Injector == nil || deps.Trigger == nil || deps.Clock == nil || deps.PollInterval <= 0 {
		return errPersistentQueueSmokeFailed
	}
	if !contains(allowedProfiles, request.Profile) {
		return errPersistentQueueSmokeFailed
	}
	if request.Deadline.IsZero() || !request.Deadline.After(deps.Clock.Now()) {
		return errPersistentQueueSmokeFailed
	}
	if !safeExporterScopePattern.MatchString(request.BackendService) ||
		!safeExporterComponentPattern.MatchString(request.ComponentID) {
		return errPersistentQueueSmokeFailed
	}
	if request.DrainWindow < 0 {
		return errPersistentQueueSmokeFailed
	}
	return nil
}

func buildPersistentQueueReport(request PersistentQueueSmokeRequest, identity PersistentQueueSmokeIdentity, startedAt, finishedAt time.Time, cleanupStatus string, residualResources []string, checks []BackendCheckInput) (*SmokeReport, error) {
	return BuildSmokeReport(SmokeReportInput{
		RunID:      identity.RunID,
		Marker:     identity.Marker,
		Profile:    request.Profile,
		Scenario:   "persistent_queue",
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

// NewPersistentQueueSmokeIdentity 生成一次 persistent-queue 场景身份。
// live composition 需要在构造 trigger 之前预生成 identity（trigger 请求的
// smoke run_id 必须与报告 marker 一致），因此身份生成器是显式公开契约；
// runner 内部在未注入 IdentityFactory 时同样使用它。
func NewPersistentQueueSmokeIdentity(context.Context) (PersistentQueueSmokeIdentity, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return PersistentQueueSmokeIdentity{}, err
	}
	encoded := hex.EncodeToString(bytes)
	return PersistentQueueSmokeIdentity{RunID: "run-" + encoded, Marker: "marker-" + encoded}, nil
}
