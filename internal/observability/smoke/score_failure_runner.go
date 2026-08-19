package smoke

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

// T115 契约的 GREEN 实现：score worker 故障场景。
//
// 生产约束（FR-015 + data-model §9 状态机）：先确认本地 evidence 完整，
// 再注入平台故障；业务结果与 eval 事实不得被任何故障改写：
// - langfuse_api：平台失败期间 chat 响应不变，恢复后投影在受限窗口内
//   sent，重试幂等（PlatformScoreCount 必须为 1，Attempts 如实记录）；
// - queue_full：drop 是声明的降级事实（dropped_queue_full 必须入报告），
//   业务与 evidence 不变；
// - shutdown：ShutdownScoreWorker 必须返回超时哨兵，重启后投影恢复；
// - 本地 evidence digest 前后一致（local_evidence_intact），任何改写即失败。

var (
	errScoreWorkerFailureSmokeFailed = errors.New("score worker failure smoke verification failed")

	// ErrScoreWorkerShutdownTimeout 表示 worker 停机超过 grace period，
	// 是 shutdown 场景必须观察到的故障事实。
	ErrScoreWorkerShutdownTimeout = errors.New("score worker shutdown timeout")
)

// ScoreWorkerFailureRecoveryWindow 是投影恢复轮询的默认上界。
const ScoreWorkerFailureRecoveryWindow = 120 * time.Second

type ScoreWorkerFailureScenario string

const (
	ScoreWorkerFailureLangfuseAPI ScoreWorkerFailureScenario = "langfuse_api"
	ScoreWorkerFailureQueueFull   ScoreWorkerFailureScenario = "queue_full"
	ScoreWorkerFailureShutdown    ScoreWorkerFailureScenario = "shutdown"
)

type ScoreFailureEvidenceTarget struct {
	EvidenceID string
}

// ScoreFailureEvidenceSnapshot 是本地 eval evidence 的完整性证明。
type ScoreFailureEvidenceSnapshot struct {
	EvidenceID string
	Digest     string
	Complete   bool
}

type ScoreFailureProjectionTarget struct {
	ProjectionID string
	StartedAt    time.Time
	Deadline     time.Time
}

// ScoreFailureProjectionObservation 是平台侧投影状态观察。PlatformScoreCount
// 是幂等断言的关键事实：重试多少次都必须保持 1。
type ScoreFailureProjectionObservation struct {
	ProjectionID       string
	State              string
	Attempts           int
	PlatformScoreCount int
	ObservedAt         time.Time
}

type ScoreWorkerFailureSmokeBackend interface {
	LocalEvidenceSnapshot(context.Context, ScoreFailureEvidenceTarget) (ScoreFailureEvidenceSnapshot, error)
	ScoreProjectionStates(context.Context, ScoreFailureProjectionTarget) ([]ScoreFailureProjectionObservation, error)
}

type ScoreWorkerFailureInjector interface {
	FailLangfuseAPI(context.Context) error
	RestoreLangfuseAPI(context.Context) error
	FillScoreWorkerQueue(context.Context) error
	DrainScoreWorkerQueue(context.Context) error
	ShutdownScoreWorker(context.Context) error
	RestartScoreWorker(context.Context) error
}

type ScoreWorkerFailureTrigger func(context.Context) (status int, bodyHash string, err error)

type ScoreWorkerFailureSmokeIdentity struct{ RunID, Marker string }

type ScoreWorkerFailureSmokeIdentityFactory func(context.Context) (ScoreWorkerFailureSmokeIdentity, error)

type ScoreWorkerFailureSmokeRequest struct {
	Deadline     time.Time
	Profile      string
	Scenario     ScoreWorkerFailureScenario
	EvidenceID   string
	ProjectionID string
}

type ScoreWorkerFailureSmokeDependencies struct {
	Backend         ScoreWorkerFailureSmokeBackend
	Injector        ScoreWorkerFailureInjector
	Trigger         ScoreWorkerFailureTrigger
	Clock           PollerClock
	IdentityFactory ScoreWorkerFailureSmokeIdentityFactory
	PollInterval    time.Duration
}

type scoreFailureTriggerResult struct {
	status   int
	bodyHash string
}

// RunScoreWorkerFailureSmoke 按 request.Scenario 执行 score worker 故障验证。
func RunScoreWorkerFailureSmoke(ctx context.Context, request ScoreWorkerFailureSmokeRequest, deps ScoreWorkerFailureSmokeDependencies) (*SmokeReport, error) {
	if err := validateScoreWorkerFailureRun(ctx, request, deps); err != nil {
		return nil, err
	}
	clock := deps.Clock
	startedAt := clock.Now().UTC()

	identityFactory := deps.IdentityFactory
	if identityFactory == nil {
		identityFactory = newScoreWorkerFailureSmokeIdentity
	}
	identity, err := identityFactory(ctx)
	if err != nil || !isOpaqueSmokeIdentity(identity.RunID) || !isSafePollMarker(identity.Marker) {
		return nil, errScoreWorkerFailureSmokeFailed
	}

	var baseline scoreFailureTriggerResult
	var baselineErr error
	baseline.status, baseline.bodyHash, baselineErr = deps.Trigger(ctx)
	if baselineErr != nil || baseline.status != 200 {
		return buildScoreWorkerFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, []BackendCheckInput{
			{Backend: "api", Status: "failed", FailureStage: "api", ErrorClass: "backend_unavailable", Evidence: map[string]any{"response_status": int64(baseline.status)}},
			{Backend: "langfuse_score", Status: "skipped", FailureStage: "none"},
		})
	}

	// 先确认本地 evidence：不完整或缺失时绝不注入平台失败（FR-015 事实源优先）。
	evidenceBefore, evidenceErr := deps.Backend.LocalEvidenceSnapshot(ctx, ScoreFailureEvidenceTarget{EvidenceID: request.EvidenceID})
	if evidenceErr != nil {
		return buildScoreWorkerFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, []BackendCheckInput{
			{Backend: "api", Status: "passed", FailureStage: "none", Evidence: map[string]any{"response_status": int64(baseline.status)}},
			{Backend: "langfuse_score", Status: "failed", FailureStage: "query", ErrorClass: "query_failed"},
		})
	}
	if !evidenceBefore.Complete {
		return buildScoreWorkerFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, []BackendCheckInput{
			{Backend: "api", Status: "passed", FailureStage: "none", Evidence: map[string]any{"response_status": int64(baseline.status)}},
			{Backend: "langfuse_score", Status: "failed", FailureStage: "query", ErrorClass: "unexpected_evidence"},
		})
	}

	switch request.Scenario {
	case ScoreWorkerFailureLangfuseAPI:
		return runScoreWorkerFailureLangfuseAPI(ctx, request, deps, identity, startedAt, baseline, evidenceBefore)
	case ScoreWorkerFailureQueueFull:
		return runScoreWorkerFailureQueueFull(ctx, request, deps, identity, startedAt, baseline, evidenceBefore)
	case ScoreWorkerFailureShutdown:
		return runScoreWorkerFailureShutdown(ctx, request, deps, identity, startedAt, baseline, evidenceBefore)
	}
	return nil, errScoreWorkerFailureSmokeFailed
}

func runScoreWorkerFailureLangfuseAPI(ctx context.Context, request ScoreWorkerFailureSmokeRequest, deps ScoreWorkerFailureSmokeDependencies, identity ScoreWorkerFailureSmokeIdentity, startedAt time.Time, baseline scoreFailureTriggerResult, evidenceBefore ScoreFailureEvidenceSnapshot) (*SmokeReport, error) {
	clock := deps.Clock
	if err := deps.Injector.FailLangfuseAPI(ctx); err != nil {
		return buildScoreWorkerFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, []BackendCheckInput{
			{Backend: "api", Status: "passed", FailureStage: "none", Evidence: map[string]any{"response_status": int64(baseline.status)}},
			{Backend: "langfuse_score", Status: "failed", FailureStage: "export", ErrorClass: "export_failed"},
		})
	}

	var during scoreFailureTriggerResult
	var duringErr error
	during.status, during.bodyHash, duringErr = deps.Trigger(ctx)

	cleanupStatus := "completed"
	var residualResources []string
	if restoreErr := deps.Injector.RestoreLangfuseAPI(ctx); restoreErr != nil {
		cleanupStatus = "failed"
		residualResources = []string{"langfuse-api-unavailable"}
	}

	apiCheck := scoreFailureAPICheck(baseline, during, duringErr)
	sent, queryErr := scoreFailurePollProjection(ctx, deps, request, "sent")
	evidenceAfter, evidenceErr := deps.Backend.LocalEvidenceSnapshot(ctx, ScoreFailureEvidenceTarget{EvidenceID: request.EvidenceID})
	scoreCheck := scoreFailureLangfuseCheck(sent, queryErr, evidenceBefore, evidenceAfter, evidenceErr)
	return buildScoreWorkerFailureReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, residualResources, []BackendCheckInput{apiCheck, scoreCheck})
}

func runScoreWorkerFailureQueueFull(ctx context.Context, request ScoreWorkerFailureSmokeRequest, deps ScoreWorkerFailureSmokeDependencies, identity ScoreWorkerFailureSmokeIdentity, startedAt time.Time, baseline scoreFailureTriggerResult, evidenceBefore ScoreFailureEvidenceSnapshot) (*SmokeReport, error) {
	clock := deps.Clock
	if err := deps.Injector.FillScoreWorkerQueue(ctx); err != nil {
		return buildScoreWorkerFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, []BackendCheckInput{
			{Backend: "api", Status: "passed", FailureStage: "none", Evidence: map[string]any{"response_status": int64(baseline.status)}},
			{Backend: "langfuse_score", Status: "failed", FailureStage: "export", ErrorClass: "export_failed"},
		})
	}

	var during scoreFailureTriggerResult
	var duringErr error
	during.status, during.bodyHash, duringErr = deps.Trigger(ctx)

	dropped, queryErr := scoreFailurePollProjection(ctx, deps, request, "dropped_queue_full")

	cleanupStatus := "completed"
	var residualResources []string
	if drainErr := deps.Injector.DrainScoreWorkerQueue(ctx); drainErr != nil {
		cleanupStatus = "failed"
		residualResources = []string{"score-worker-queue-full"}
	}

	evidenceAfter, evidenceErr := deps.Backend.LocalEvidenceSnapshot(ctx, ScoreFailureEvidenceTarget{EvidenceID: request.EvidenceID})
	apiCheck := scoreFailureAPICheck(baseline, during, duringErr)
	scoreCheck := scoreFailureQueueFullCheck(dropped, queryErr, evidenceBefore, evidenceAfter, evidenceErr)
	return buildScoreWorkerFailureReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, residualResources, []BackendCheckInput{apiCheck, scoreCheck})
}

func runScoreWorkerFailureShutdown(ctx context.Context, request ScoreWorkerFailureSmokeRequest, deps ScoreWorkerFailureSmokeDependencies, identity ScoreWorkerFailureSmokeIdentity, startedAt time.Time, baseline scoreFailureTriggerResult, evidenceBefore ScoreFailureEvidenceSnapshot) (*SmokeReport, error) {
	clock := deps.Clock
	var during scoreFailureTriggerResult
	var duringErr error
	during.status, during.bodyHash, duringErr = deps.Trigger(ctx)

	stopErr := deps.Injector.ShutdownScoreWorker(ctx)
	timeoutObserved := errors.Is(stopErr, ErrScoreWorkerShutdownTimeout)
	stopFailed := stopErr != nil && !timeoutObserved

	restartErr := deps.Injector.RestartScoreWorker(ctx)
	cleanupStatus := "completed"
	if restartErr != nil {
		cleanupStatus = "failed"
	}

	sent, queryErr := scoreFailurePollProjection(ctx, deps, request, "sent")
	evidenceAfter, evidenceErr := deps.Backend.LocalEvidenceSnapshot(ctx, ScoreFailureEvidenceTarget{EvidenceID: request.EvidenceID})
	apiCheck := scoreFailureAPICheck(baseline, during, duringErr)
	scoreCheck := scoreFailureShutdownCheck(sent, queryErr, timeoutObserved, stopFailed, restartErr, evidenceBefore, evidenceAfter, evidenceErr)
	return buildScoreWorkerFailureReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, nil, []BackendCheckInput{apiCheck, scoreCheck})
}

// scoreFailurePollProjection 在受限窗口内轮询投影状态，返回命中的观察
// （nil 表示未命中）与查询错误。
func scoreFailurePollProjection(ctx context.Context, deps ScoreWorkerFailureSmokeDependencies, request ScoreWorkerFailureSmokeRequest, wantState string) (*ScoreFailureProjectionObservation, error) {
	pollStart := deps.Clock.Now()
	pollEnd := request.Deadline
	if windowEnd := pollStart.Add(ScoreWorkerFailureRecoveryWindow); windowEnd.Before(pollEnd) {
		pollEnd = windowEnd
	}
	target := ScoreFailureProjectionTarget{ProjectionID: request.ProjectionID, StartedAt: pollStart, Deadline: pollEnd}
	for deps.Clock.Now().Before(pollEnd) {
		observations, err := deps.Backend.ScoreProjectionStates(ctx, target)
		if err != nil {
			return nil, err
		}
		for _, observation := range observations {
			if observation.ProjectionID != request.ProjectionID || observation.State != wantState {
				continue
			}
			if observation.ObservedAt.Before(pollStart) || observation.ObservedAt.After(pollEnd) {
				continue
			}
			copy := observation
			return &copy, nil
		}
		if err := deps.Clock.Wait(ctx, deps.PollInterval); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func scoreFailureAPICheck(baseline, during scoreFailureTriggerResult, duringErr error) BackendCheckInput {
	status, stage, class := "passed", "none", ""
	if duringErr != nil || during.status != baseline.status || during.bodyHash != baseline.bodyHash {
		status, stage, class = "failed", "api", "backend_unavailable"
	}
	return BackendCheckInput{
		Backend:      "api",
		Status:       status,
		FailureStage: stage,
		ErrorClass:   class,
		Evidence:     map[string]any{"response_status": int64(during.status)},
	}
}

func scoreFailureEvidenceIntact(before, after ScoreFailureEvidenceSnapshot, afterErr error) bool {
	return afterErr == nil && after.Complete && after.EvidenceID == before.EvidenceID && after.Digest == before.Digest
}

func scoreFailureLangfuseCheck(sent *ScoreFailureProjectionObservation, queryErr error, evidenceBefore, evidenceAfter ScoreFailureEvidenceSnapshot, evidenceErr error) BackendCheckInput {
	check := BackendCheckInput{Backend: "langfuse_score", FailureStage: "none", Status: "passed", Evidence: map[string]any{}}
	switch {
	case queryErr != nil:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "query_failed"
		return check
	case sent == nil:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "marker_missing"
		return check
	case sent.PlatformScoreCount > 1:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "unexpected_evidence"
		return check
	case !scoreFailureEvidenceIntact(evidenceBefore, evidenceAfter, evidenceErr):
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "unexpected_evidence"
		return check
	}
	check.Evidence = map[string]any{
		"matched_scores":        int64(sent.PlatformScoreCount),
		"score_attempts":        int64(sent.Attempts),
		"local_evidence_intact": true,
	}
	return check
}

func scoreFailureQueueFullCheck(dropped *ScoreFailureProjectionObservation, queryErr error, evidenceBefore, evidenceAfter ScoreFailureEvidenceSnapshot, evidenceErr error) BackendCheckInput {
	check := BackendCheckInput{Backend: "langfuse_score", FailureStage: "none", Status: "passed", Evidence: map[string]any{}}
	switch {
	case queryErr != nil:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "query_failed"
		return check
	case dropped == nil:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "unexpected_evidence"
		return check
	case !scoreFailureEvidenceIntact(evidenceBefore, evidenceAfter, evidenceErr):
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "unexpected_evidence"
		return check
	}
	check.Evidence = map[string]any{
		"dropped_projections":   int64(1),
		"matched_scores":        int64(0),
		"local_evidence_intact": true,
	}
	return check
}

func scoreFailureShutdownCheck(sent *ScoreFailureProjectionObservation, queryErr error, timeoutObserved, stopFailed bool, restartErr error, evidenceBefore, evidenceAfter ScoreFailureEvidenceSnapshot, evidenceErr error) BackendCheckInput {
	check := BackendCheckInput{Backend: "langfuse_score", FailureStage: "none", Status: "passed", Evidence: map[string]any{}}
	switch {
	case stopFailed:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "export", "export_failed"
		return check
	case !timeoutObserved:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "unexpected_evidence"
		return check
	case restartErr != nil:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "export", "export_failed"
		return check
	case queryErr != nil:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "query_failed"
		return check
	case sent == nil:
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "marker_missing"
		return check
	case !scoreFailureEvidenceIntact(evidenceBefore, evidenceAfter, evidenceErr):
		check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "unexpected_evidence"
		return check
	}
	check.Evidence = map[string]any{
		"shutdown_timed_out":    timeoutObserved,
		"matched_scores":        int64(sent.PlatformScoreCount),
		"score_attempts":        int64(sent.Attempts),
		"local_evidence_intact": true,
	}
	return check
}

func validateScoreWorkerFailureRun(ctx context.Context, request ScoreWorkerFailureSmokeRequest, deps ScoreWorkerFailureSmokeDependencies) error {
	if ctx == nil {
		return errScoreWorkerFailureSmokeFailed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if deps.Backend == nil || deps.Injector == nil || deps.Trigger == nil || deps.Clock == nil || deps.PollInterval <= 0 {
		return errScoreWorkerFailureSmokeFailed
	}
	if !contains(allowedProfiles, request.Profile) {
		return errScoreWorkerFailureSmokeFailed
	}
	if request.Deadline.IsZero() || !request.Deadline.After(deps.Clock.Now()) {
		return errScoreWorkerFailureSmokeFailed
	}
	switch request.Scenario {
	case ScoreWorkerFailureLangfuseAPI, ScoreWorkerFailureQueueFull, ScoreWorkerFailureShutdown:
	default:
		return errScoreWorkerFailureSmokeFailed
	}
	if !safeExporterScopePattern.MatchString(request.EvidenceID) ||
		!safeExporterScopePattern.MatchString(request.ProjectionID) {
		return errScoreWorkerFailureSmokeFailed
	}
	return nil
}

func buildScoreWorkerFailureReport(request ScoreWorkerFailureSmokeRequest, identity ScoreWorkerFailureSmokeIdentity, startedAt, finishedAt time.Time, cleanupStatus string, residualResources []string, checks []BackendCheckInput) (*SmokeReport, error) {
	return BuildSmokeReport(SmokeReportInput{
		RunID:      identity.RunID,
		Marker:     identity.Marker,
		Profile:    request.Profile,
		Scenario:   "score_worker_failure",
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

func newScoreWorkerFailureSmokeIdentity(context.Context) (ScoreWorkerFailureSmokeIdentity, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return ScoreWorkerFailureSmokeIdentity{}, err
	}
	encoded := hex.EncodeToString(bytes)
	return ScoreWorkerFailureSmokeIdentity{RunID: "run-" + encoded, Marker: "marker-" + encoded}, nil
}
