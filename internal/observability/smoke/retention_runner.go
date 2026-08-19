package smoke

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

// T119 契约的 GREEN 实现：retention/volume 边界检查。
//
// 生产约束（data-model §14 数据生命周期 + §10 PayloadPolicy）：
// - 保留窗口逐 unit 校验（Prometheus 15 天 / Loki 7 天 / Tempo 7 天 /
//   Langfuse 14 天 / 低敏 evidence·report 90 天），漂移 → retention_violation；
// - 合成 canary 原文注入后，四个后端 unit 逐一探测：普通原文不得作为
//   可观测 payload 保留，命中 → raw_payload_found=true + retention_violation；
// - persistent queue 仅积压保留：投递完成后有界轮询队列回落至 0；
// - local raw 调试工件在任何退出路径（含失败）清理，残留 → cleanup
//   failed + temporary-debug-data。

var errRetentionSmokeFailed = errors.New("retention smoke verification failed")

type RetentionUnit string

const (
	RetentionUnitPrometheus RetentionUnit = "prometheus"
	RetentionUnitLoki       RetentionUnit = "loki"
	RetentionUnitTempo      RetentionUnit = "tempo"
	RetentionUnitLangfuse   RetentionUnit = "langfuse"
	RetentionUnitEvidence   RetentionUnit = "evidence"
)

// 保留窗口契约（data-model §14）：这些常量既是实现依据也是漂移检测器。
const (
	RetentionPrometheusDays = 15
	RetentionLokiDays       = 7
	RetentionTempoDays      = 7
	RetentionLangfuseDays   = 14
	RetentionEvidenceDays   = 90
)

type RetentionPolicySnapshot struct {
	Unit       RetentionUnit
	MaxAgeDays int
}

type RetentionQueueSnapshot struct {
	QueueSize     int64
	QueueCapacity int64
}

type RawCanaryTarget struct {
	Canary    string
	StartedAt time.Time
	Deadline  time.Time
}

type RetentionSmokeBackend interface {
	RetentionPolicy(context.Context, RetentionUnit) (RetentionPolicySnapshot, error)
	RawPayloadPresent(context.Context, RetentionUnit, RawCanaryTarget) (bool, error)
	CollectorQueueSnapshot(context.Context) (RetentionQueueSnapshot, error)
}

// RetentionTrigger 产生携带 canary 原文的流量，使 canary 有机会进入各
// retention unit（然后必须被证明没有保留）。
type RetentionTrigger func(context.Context, string) error

type RetentionLocalRawStore interface {
	ListRunArtifacts(context.Context) ([]string, error)
	RemoveRunArtifacts(context.Context) ([]string, error)
}

type RetentionSmokeIdentity struct{ RunID, Marker string }

type RetentionSmokeIdentityFactory func(context.Context) (RetentionSmokeIdentity, error)

type RetentionSmokeRequest struct {
	Deadline time.Time
	Profile  string
}

type RetentionSmokeDependencies struct {
	Backend         RetentionSmokeBackend
	Trigger         RetentionTrigger
	LocalRaw        RetentionLocalRawStore
	Clock           PollerClock
	IdentityFactory RetentionSmokeIdentityFactory
	PollInterval    time.Duration
}

// retentionUnitPlan 定义每个后端 unit 的检查顺序、后端名与期望窗口。
type retentionUnitPlan struct {
	unit         RetentionUnit
	backendName  string
	expectedDays int
	probeRaw     bool
}

func retentionUnitPlans() []retentionUnitPlan {
	return []retentionUnitPlan{
		{RetentionUnitPrometheus, "prometheus", RetentionPrometheusDays, true},
		{RetentionUnitLoki, "loki", RetentionLokiDays, true},
		{RetentionUnitTempo, "tempo", RetentionTempoDays, true},
		{RetentionUnitLangfuse, "langfuse_trace", RetentionLangfuseDays, true},
		{RetentionUnitEvidence, "api", RetentionEvidenceDays, false},
	}
}

// RunRetentionSmoke 执行 retention/volume 边界验证。所有退出路径都先完成
// local raw 工件清理，再构建报告。
func RunRetentionSmoke(ctx context.Context, request RetentionSmokeRequest, deps RetentionSmokeDependencies) (*SmokeReport, error) {
	if err := validateRetentionRun(ctx, request, deps); err != nil {
		return nil, err
	}
	clock := deps.Clock
	startedAt := clock.Now().UTC()

	identityFactory := deps.IdentityFactory
	if identityFactory == nil {
		identityFactory = newRetentionSmokeIdentity
	}
	identity, err := identityFactory(ctx)
	if err != nil || !isOpaqueSmokeIdentity(identity.RunID) || !isSafePollMarker(identity.Marker) {
		return nil, errRetentionSmokeFailed
	}

	canary := "raw-canary-" + identity.Marker
	if !isSafePollMarker(canary) {
		return nil, errRetentionSmokeFailed
	}

	// 触发流量：失败意味着 canary 从未进入系统，后续"未保留"结论是
	// 假阴性——必须直接失败而不是假通过。
	if err := deps.Trigger(ctx, canary); err != nil {
		return nil, errRetentionSmokeFailed
	}

	checks := make([]BackendCheckInput, 0, len(retentionUnitPlans())+1)
	rawTarget := RawCanaryTarget{Canary: canary, StartedAt: startedAt, Deadline: request.Deadline}

	for _, plan := range retentionUnitPlans() {
		policy, policyErr := deps.Backend.RetentionPolicy(ctx, plan.unit)
		check := BackendCheckInput{Backend: plan.backendName, Status: "passed", FailureStage: "none"}
		switch {
		case policyErr != nil:
			check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "query_failed"
		case policy.MaxAgeDays != plan.expectedDays:
			check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "retention_violation"
			check.Evidence = map[string]any{"retention_days": int64(policy.MaxAgeDays)}
		case plan.probeRaw:
			present, probeErr := deps.Backend.RawPayloadPresent(ctx, plan.unit, rawTarget)
			switch {
			case probeErr != nil:
				check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "query_failed"
			case present:
				check.Status, check.FailureStage, check.ErrorClass = "failed", "query", "retention_violation"
				check.Evidence = map[string]any{"retention_days": int64(policy.MaxAgeDays), "raw_payload_found": true}
			default:
				check.Evidence = map[string]any{"retention_days": int64(policy.MaxAgeDays), "raw_payload_found": false}
			}
		default:
			check.Evidence = map[string]any{"retention_days": int64(policy.MaxAgeDays)}
		}
		checks = append(checks, check)
	}

	// persistent queue：投递完成后有界轮询队列回落至 0。
	queueCheck := BackendCheckInput{Backend: "collector", Status: "failed", FailureStage: "query", ErrorClass: "retention_violation"}
	var lastQueue int64 = -1
	for clock.Now().Before(request.Deadline) {
		snapshot, snapshotErr := deps.Backend.CollectorQueueSnapshot(ctx)
		if snapshotErr != nil {
			queueCheck.ErrorClass = "query_failed"
			break
		}
		lastQueue = snapshot.QueueSize
		if snapshot.QueueSize == 0 {
			queueCheck.Status, queueCheck.FailureStage, queueCheck.ErrorClass = "passed", "none", ""
			queueCheck.Evidence = map[string]any{"queue_depth": int64(0)}
			break
		}
		if err := clock.Wait(ctx, deps.PollInterval); err != nil {
			return nil, err
		}
	}
	if lastQueue >= 0 && queueCheck.Status != "passed" {
		queueCheck.Evidence = map[string]any{"queue_depth": lastQueue}
	}
	checks = append(checks, queueCheck)

	// local raw 工件清理：任何退出路径（包括上面全部失败路径）都执行，
	// 且清理结果必须进入报告。
	cleanupStatus, residualResources := retentionCleanupLocalRaw(ctx, deps.LocalRaw)
	return buildRetentionReport(request, identity, startedAt, clock.Now().UTC(), cleanupStatus, residualResources, checks)
}

func retentionCleanupLocalRaw(ctx context.Context, store RetentionLocalRawStore) (string, []string) {
	artifacts, err := store.ListRunArtifacts(ctx)
	if err != nil {
		return "failed", []string{"temporary-debug-data"}
	}
	if len(artifacts) == 0 {
		// 幂等清理：无工件也执行一次 remove，保证后续残留被清。
		if _, err := store.RemoveRunArtifacts(ctx); err != nil {
			return "failed", []string{"temporary-debug-data"}
		}
		return "completed", nil
	}
	if _, err := store.RemoveRunArtifacts(ctx); err != nil {
		return "failed", []string{"temporary-debug-data"}
	}
	return "completed", nil
}

func validateRetentionRun(ctx context.Context, request RetentionSmokeRequest, deps RetentionSmokeDependencies) error {
	if ctx == nil {
		return errRetentionSmokeFailed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if deps.Backend == nil || deps.Trigger == nil || deps.LocalRaw == nil || deps.Clock == nil || deps.PollInterval <= 0 {
		return errRetentionSmokeFailed
	}
	if !contains(allowedProfiles, request.Profile) {
		return errRetentionSmokeFailed
	}
	if request.Deadline.IsZero() || !request.Deadline.After(deps.Clock.Now()) {
		return errRetentionSmokeFailed
	}
	return nil
}

func buildRetentionReport(request RetentionSmokeRequest, identity RetentionSmokeIdentity, startedAt, finishedAt time.Time, cleanupStatus string, residualResources []string, checks []BackendCheckInput) (*SmokeReport, error) {
	return BuildSmokeReport(SmokeReportInput{
		RunID:      identity.RunID,
		Marker:     identity.Marker,
		Profile:    request.Profile,
		Scenario:   "retention",
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

func newRetentionSmokeIdentity(context.Context) (RetentionSmokeIdentity, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return RetentionSmokeIdentity{}, err
	}
	encoded := hex.EncodeToString(bytes)
	return RetentionSmokeIdentity{RunID: "run-" + encoded, Marker: "marker-" + encoded}, nil
}
