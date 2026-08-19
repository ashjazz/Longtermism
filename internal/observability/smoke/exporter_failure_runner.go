package smoke

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/failure"
)

// T112 契约的 GREEN 实现：独立 exporter 故障场景。
//
// 生产约束（FR-007 / data-model §13）：
// - 单出口故障期间业务结果不得被改写（api check 比较故障前/故障中 HTTP
//   status 与 body hash）；
// - 报告按 Collector 组件分别记录 sent/failed/enqueue/queue delta，被注入
//   组件必须产生 send_failed 证据，其它出口必须继续投递（正面成功证据，
//   不能只证明"没失败"）；
// - 任何退出路径都恢复 backend；恢复失败进入 cleanup + paused-service
//   residual。
// - 组件事实（组件 ID 与队列名）取自 failure 目录，与 T133 dashboard、
//   告警规则共用同一份映射，不在此重复定义。

var (
	errExporterFailureSmokeFailed = errors.New("exporter failure smoke verification failed")

	// safeExporterScopePattern 是 backend service 与 evidence prefix 的安全边界，
	// 与 failure 包的 DockerControl 校验同一类风险（未校验 shell 参数拼接）。
	safeExporterScopePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

	// safeExporterComponentPattern 额外允许斜杠：Collector 组件 ID 形如
	// otlp/tempo、otlphttp/loki，斜杠是组件身份的一部分而非 shell 元字符。
	safeExporterComponentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]{0,63}$`)
)

// ExporterHealthSnapshot 是 data-model §13 的按组件只读证据投影。
type ExporterHealthSnapshot struct {
	ComponentID   string
	Sent          int64
	SendFailed    int64
	EnqueueFailed int64
	QueueSize     int64
	QueueCapacity int64
	Dropped       int64
}

// ExporterFailureSmokeTarget 描述本次注入的目标：暂停哪个 backend 服务、
// 归因到哪个 Collector 组件、报告证据键使用哪个前缀（= 持久队列名）。
type ExporterFailureSmokeTarget struct {
	BackendService string
	ComponentID    string
	EvidencePrefix string
}

type ExporterFailureSmokeRequest struct {
	Deadline time.Time
	Profile  string
	Target   ExporterFailureSmokeTarget
}

// ExporterFailureSmokeBackend 查询 Collector exporter 组件健康快照。
type ExporterFailureSmokeBackend interface {
	SnapshotCollectorHealth(context.Context) ([]ExporterHealthSnapshot, error)
}

// ExporterFailureInjector 注入/恢复单出口故障。
type ExporterFailureInjector interface {
	Pause(context.Context, string) error
	Unpause(context.Context, string) error
}

// ExporterFailureTrigger 产生一次业务请求并返回可比较的响应事实
// （HTTP status + body hash）。
type ExporterFailureTrigger func(context.Context) (status int, bodyHash string, err error)

type ExporterFailureSmokeDependencies struct {
	Backend  ExporterFailureSmokeBackend
	Injector ExporterFailureInjector
	Trigger  ExporterFailureTrigger
	Clock    PollerClock
	// PollInterval 驱动失败证据的有界轮询：真实装配的快照来自 Prometheus
	// scrape，证据可见性受 scrape 相位影响（journal 0013 缺陷 1）。
	PollInterval time.Duration
}

type exporterFailureTriggerResult struct {
	status   int
	bodyHash string
}

// exporterComponentFact 是 failure 目录投影到 smoke 的最小事实。
type exporterComponentFact struct {
	ComponentID string
	Prefix      string // 持久队列名，即报告证据键前缀
}

// exporterComponentFacts 是三个真实出口的静态映射（单一事实源：failure 目录）。
func exporterComponentFacts() []exporterComponentFact {
	domains := []failure.Domain{
		failure.DomainTempoExporter,
		failure.DomainLokiExporter,
		failure.DomainLangfuseExporter,
	}
	facts := make([]exporterComponentFact, 0, len(domains))
	for _, domain := range domains {
		definition, ok := failure.Lookup(domain)
		if !ok {
			continue
		}
		facts = append(facts, exporterComponentFact{ComponentID: definition.CollectorComponentID, Prefix: definition.StorageQueueName})
	}
	return facts
}

// RunExporterFailureSmoke 执行单出口故障验证。参数/依赖非法返回 error；
// 验证失败写入报告（与 infra/chat runner 同一约定），保证同一次运行的全部
// 低敏事实可被 CI 保留。
func RunExporterFailureSmoke(ctx context.Context, request ExporterFailureSmokeRequest, deps ExporterFailureSmokeDependencies) (*SmokeReport, error) {
	if err := validateExporterFailureRun(ctx, request, deps); err != nil {
		return nil, err
	}
	clock := deps.Clock
	startedAt := clock.Now().UTC()
	identity, err := newExporterFailureSmokeIdentity()
	if err != nil {
		return nil, errExporterFailureSmokeFailed
	}

	target := request.Target
	checks := make([]BackendCheckInput, 0, 2)

	var baseline exporterFailureTriggerResult
	var baselineErr error
	baseline.status, baseline.bodyHash, baselineErr = deps.Trigger(ctx)
	if baselineErr != nil || baseline.status != 200 {
		checks = append(checks, exporterFailureAPICheck("failed", "api", "backend_unavailable", baseline.status, clock.Now().UTC().Sub(startedAt)))
		checks = append(checks, exporterFailureCollectorCheck("skipped", nil))
		return buildExporterFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, checks)
	}

	beforeSnapshots, snapshotErr := deps.Backend.SnapshotCollectorHealth(ctx)
	if snapshotErr != nil {
		checks = append(checks, exporterFailureAPICheck("passed", "none", "", baseline.status, clock.Now().UTC().Sub(startedAt)))
		checks = append(checks, exporterFailureCollectorCheck("failed-query", nil))
		return buildExporterFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, checks)
	}
	beforeByComponent := exporterFailureSnapshotIndex(beforeSnapshots)

	if err := deps.Injector.Pause(ctx, target.BackendService); err != nil {
		checks = append(checks, exporterFailureAPICheck("passed", "none", "", baseline.status, clock.Now().UTC().Sub(startedAt)))
		checks = append(checks, exporterFailureCollectorCheck("failed-injection", nil))
		return buildExporterFailureReport(request, identity, startedAt, clock.Now().UTC(), "not_required", nil, checks)
	}

	var during exporterFailureTriggerResult
	var duringErr error
	var afterSnapshots []ExporterHealthSnapshot
	var afterErr error
	cleanupStatus := "completed"
	var residualResources []string

	during.status, during.bodyHash, duringErr = deps.Trigger(ctx)
	if duringErr == nil {
		// 失败证据需要经历 OTLP 推送 → collector 失败计数 → Prometheus 下一次
		// scrape 才可见（journal 0013 缺陷 1 的传播链）；单次快照会稳定误报
		// "无失败证据"。有界轮询直到归因证据完整（或 deadline），随后统一
		// 恢复 backend——保持 pause 窗口只延长到证据可见为止。
		afterSnapshots, afterErr = exporterFailureWaitForEvidence(ctx, deps, request, target, beforeByComponent)
	}
	restoreErr := deps.Injector.Unpause(ctx, target.BackendService)
	if restoreErr != nil {
		cleanupStatus = "failed"
		residualResources = []string{"paused-service"}
	}

	apiPassed := duringErr == nil && during.status == baseline.status && during.bodyHash == baseline.bodyHash
	apiStatus, apiStage, apiClass := "passed", "none", ""
	if !apiPassed {
		apiStatus, apiStage, apiClass = "failed", "api", "backend_unavailable"
	}
	checks = append(checks, exporterFailureAPICheck(apiStatus, apiStage, apiClass, during.status, clock.Now().UTC().Sub(startedAt)))

	collectorCheck := exporterFailureCollectorCheck("evaluate", &exporterFailureAttributionInput{
		target:            target,
		beforeByComponent: beforeByComponent,
		afterSnapshots:    afterSnapshots,
		afterErr:          afterErr,
	})
	checks = append(checks, collectorCheck)

	return buildExporterFailureReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, residualResources, checks)
}

type exporterFailureAttributionInput struct {
	target            ExporterFailureSmokeTarget
	beforeByComponent map[string]ExporterHealthSnapshot
	afterSnapshots    []ExporterHealthSnapshot
	afterErr          error
}

func exporterFailureSnapshotIndex(snapshots []ExporterHealthSnapshot) map[string]ExporterHealthSnapshot {
	index := make(map[string]ExporterHealthSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		index[snapshot.ComponentID] = snapshot
	}
	return index
}

// exporterFailureWaitForEvidence 有界轮询归因证据（真实装配的快照来自
// Prometheus scrape，失败计数要等到下一次 scrape 才可见）。证据完整或
// deadline 耗尽即返回；查询错误立即上抛（Prometheus 本身不受注入影响，
// 错误是真实故障而不是恢复期噪声——与 persistent-queue 的 Tempo 容错不同）。
func exporterFailureWaitForEvidence(ctx context.Context, deps ExporterFailureSmokeDependencies, request ExporterFailureSmokeRequest, target ExporterFailureSmokeTarget, beforeByComponent map[string]ExporterHealthSnapshot) ([]ExporterHealthSnapshot, error) {
	for {
		snapshots, err := deps.Backend.SnapshotCollectorHealth(ctx)
		if err != nil {
			return snapshots, err
		}
		if exporterFailureEvidenceComplete(snapshots, target, beforeByComponent) {
			return snapshots, nil
		}
		if !deps.Clock.Now().Add(deps.PollInterval).Before(request.Deadline) {
			return snapshots, nil
		}
		if err := deps.Clock.Wait(ctx, deps.PollInterval); err != nil {
			return snapshots, err
		}
	}
}

// exporterFailureEvidenceComplete 判定归因证据是否齐全：被注入组件出现
// send_failed 增量、其它出口未失败且至少一个继续投递。与 evaluate 模式
// check 的判定保持同一事实集（此处只驱动轮询终止，报告仍由 check 构建）。
func exporterFailureEvidenceComplete(snapshots []ExporterHealthSnapshot, target ExporterFailureSmokeTarget, beforeByComponent map[string]ExporterHealthSnapshot) bool {
	afterByComponent := exporterFailureSnapshotIndex(snapshots)
	otherSentPositive := false
	for _, fact := range exporterComponentFacts() {
		after, ok := afterByComponent[fact.ComponentID]
		if !ok {
			return false
		}
		before := beforeByComponent[fact.ComponentID]
		failedDelta := after.SendFailed - before.SendFailed
		if fact.ComponentID == target.ComponentID {
			if failedDelta <= 0 {
				return false
			}
			continue
		}
		if failedDelta > 0 {
			return false
		}
		if after.Sent > before.Sent {
			otherSentPositive = true
		}
	}
	return otherSentPositive
}

// exporterFailureCollectorCheck 构造 collector check。mode:
// "skipped"（无证据）、"failed-query"、"failed-injection"、"evaluate"。
func exporterFailureCollectorCheck(mode string, attribution *exporterFailureAttributionInput) BackendCheckInput {
	switch mode {
	case "skipped":
		return BackendCheckInput{Backend: "collector", Status: "skipped", FailureStage: "none"}
	case "failed-query":
		return BackendCheckInput{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "query_failed"}
	case "failed-injection":
		return BackendCheckInput{Backend: "collector", Status: "failed", FailureStage: "export", ErrorClass: "export_failed"}
	}

	evidence := make(map[string]any)
	afterByComponent := exporterFailureSnapshotIndex(attribution.afterSnapshots)
	if attribution.afterErr != nil {
		return BackendCheckInput{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "query_failed", Evidence: evidence}
	}

	target := attribution.target
	if _, targetFound := afterByComponent[target.ComponentID]; !targetFound {
		return BackendCheckInput{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "unexpected_evidence", Evidence: evidence}
	}

	deltas := make(map[string]exporterFailureDeltas)
	for _, fact := range exporterComponentFacts() {
		before := attribution.beforeByComponent[fact.ComponentID]
		after, ok := afterByComponent[fact.ComponentID]
		if !ok {
			return BackendCheckInput{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "unexpected_evidence", Evidence: evidence}
		}
		deltas[fact.ComponentID] = exporterFailureDeltas{
			Sent:    after.Sent - before.Sent,
			Failed:  after.SendFailed - before.SendFailed,
			Enqueue: after.EnqueueFailed - before.EnqueueFailed,
			Queue:   after.QueueSize - before.QueueSize,
		}
	}

	// 被注入组件必须产生 send_failed 证据，否则注入无效或指标映射错误。
	targetDeltas := deltas[target.ComponentID]
	if targetDeltas.Failed <= 0 {
		return BackendCheckInput{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "unexpected_evidence", Evidence: evidence}
	}

	// 其它出口必须继续投递：不得失败，且至少一个出口有正面 sent 证据。
	otherSentPositive := false
	for _, fact := range exporterComponentFacts() {
		if fact.ComponentID == target.ComponentID {
			continue
		}
		if deltas[fact.ComponentID].Failed > 0 {
			return BackendCheckInput{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "unexpected_evidence", Evidence: evidence}
		}
		if deltas[fact.ComponentID].Sent > 0 {
			otherSentPositive = true
		}
	}
	if !otherSentPositive {
		return BackendCheckInput{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "unexpected_evidence", Evidence: evidence}
	}

	// 12 个组件限定证据键：每个前缀（= 持久队列名）四个 delta。
	for _, fact := range exporterComponentFacts() {
		delta := deltas[fact.ComponentID]
		evidence[fact.Prefix+"_sent_delta"] = delta.Sent
		evidence[fact.Prefix+"_failed_delta"] = delta.Failed
		evidence[fact.Prefix+"_enqueue_delta"] = delta.Enqueue
		evidence[fact.Prefix+"_queue_delta"] = delta.Queue
	}
	return BackendCheckInput{Backend: "collector", Status: "passed", FailureStage: "none", Evidence: evidence}
}

func targetPrefixForComponent(componentID string) string {
	for _, fact := range exporterComponentFacts() {
		if fact.ComponentID == componentID {
			return fact.Prefix
		}
	}
	return ""
}

type exporterFailureDeltas struct {
	Sent    int64
	Failed  int64
	Enqueue int64
	Queue   int64
}

func exporterFailureAPICheck(status, failureStage, errorClass string, responseStatus int, duration time.Duration) BackendCheckInput {
	return BackendCheckInput{
		Backend:      "api",
		Status:       status,
		Duration:     duration,
		FailureStage: failureStage,
		ErrorClass:   errorClass,
		Evidence:     map[string]any{"response_status": int64(responseStatus)},
	}
}

func validateExporterFailureRun(ctx context.Context, request ExporterFailureSmokeRequest, deps ExporterFailureSmokeDependencies) error {
	if ctx == nil || ctx.Err() != nil {
		if ctx == nil {
			return errExporterFailureSmokeFailed
		}
		return ctx.Err()
	}
	if deps.Backend == nil || deps.Injector == nil || deps.Trigger == nil || deps.Clock == nil || deps.PollInterval <= 0 {
		return errExporterFailureSmokeFailed
	}
	if !contains(allowedProfiles, request.Profile) {
		return errExporterFailureSmokeFailed
	}
	startedAt := deps.Clock.Now()
	if request.Deadline.IsZero() || !request.Deadline.After(startedAt) {
		return errExporterFailureSmokeFailed
	}
	target := request.Target
	if !safeExporterScopePattern.MatchString(target.BackendService) ||
		!safeExporterComponentPattern.MatchString(target.ComponentID) ||
		!safeExporterScopePattern.MatchString(target.EvidencePrefix) {
		return errExporterFailureSmokeFailed
	}
	if prefix := targetPrefixForComponent(target.ComponentID); prefix != target.EvidencePrefix {
		return errExporterFailureSmokeFailed
	}
	return nil
}

func buildExporterFailureReport(request ExporterFailureSmokeRequest, identity ExporterFailureSmokeIdentity, startedAt, finishedAt time.Time, cleanupStatus string, residualResources []string, checks []BackendCheckInput) (*SmokeReport, error) {
	if len(checks) == 0 {
		checks = append(checks, BackendCheckInput{Backend: "collector", Status: "skipped", FailureStage: "none"})
	}
	return BuildSmokeReport(SmokeReportInput{
		RunID:      identity.RunID,
		Marker:     identity.Marker,
		Profile:    request.Profile,
		Scenario:   "exporter_failure",
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

type ExporterFailureSmokeIdentity struct{ RunID, Marker string }

func newExporterFailureSmokeIdentity() (ExporterFailureSmokeIdentity, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return ExporterFailureSmokeIdentity{}, err
	}
	encoded := hex.EncodeToString(bytes)
	return ExporterFailureSmokeIdentity{RunID: "run-" + encoded, Marker: "marker-" + encoded}, nil
}
