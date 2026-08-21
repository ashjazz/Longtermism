package smoke

import (
	"fmt"
	"time"

	obssmoke "github.com/ashjazz/Longtermism/internal/observability/smoke"
)

// === US5：local platform_contract smoke report（T157，使 T153 GREEN）===
//
// 本文件把受控运行（T155 runner + T156 扫描）的产物聚合为 schema-valid 的
// 机器可读报告。报告的身份边界是它的核心价值：profile=local +
// scenario=platform_contract 的报告只证明 payload/identity/privacy 契约，
// 所有真实后端 checks 一律 skipped 并说明范围——绝不在 local 报告里伪造
// 真实后端的 passed 证据。

// LocalPlatformContractDeadline 是受控运行的墙钟上限（US5 独立验收标准：
// 清空外部配置后 30 秒内完成）。受控链路是纯内存操作，超时只可能来自实现
// 缺陷（意外阻塞、外部等待），因此超时 fail-fast 而不是放慢通过。
const LocalPlatformContractDeadline = 30 * time.Second

const (
	// v3 与核心 SmokeReport 同步关闭 v2 的开放词表；local producer 不能继续
	// 输出旧版本号，否则同一 wire version 会具有两套安全语义。
	localPlatformContractSchemaVersion = "3"
	localPlatformContractProfile       = "local"
	localPlatformContractScenario      = "platform_contract"

	// localPlatformRealBackendSkipReason 说明每个真实后端 check 的 skipped
	// 范围：local 报告不验证真实后端，该证明只属于 Grafana/SigNoz E2E。
	localPlatformRealBackendSkipReason = "not_verified_local_profile"

	// localPlatformPayloadDisabledClass 说明受控 payload check 被 skip 的原因
	// （默认关闭 / 未显式 opt-in），与真实后端"范围外"的 skip 语义不同。
	localPlatformPayloadDisabledClass = "platform_smoke_disabled"

	// localPlatformPrivacyHitClass 是受控 canary 扫描命中的失败类别。
	localPlatformPrivacyHitClass = "privacy_canary_hits"

	localPlatformPayloadNotSentClass = "payload_not_sent"
)

// localPlatformContractRealBackends 列出 schema backend 枚举中的全部真实后端。
// 每个都在 local 报告中获得一个显式 skipped check——范围声明必须完整到
// "任何一个真实后端都没被验证"，而不是只挑几个代表。
var localPlatformContractRealBackends = []string{
	"collector",
	"tempo",
	"loki",
	"prometheus",
	"grafana",
	"langfuse_trace",
	"langfuse_score",
	"signoz",
	"signoz_traces",
	"signoz_logs",
	"signoz_metrics",
}

// LocalPlatformContractReportInput 聚合一次受控运行的产物。
type LocalPlatformContractReportInput struct {
	RunID      string
	Marker     string
	StartedAt  time.Time
	FinishedAt time.Time
	Payload    LocalPlatformSmokeResult
	Privacy    PlatformPrivacyScanResult
}

// LocalPlatformContractCheck 是报告中一个 check 的低敏快照，字段与
// contracts/smoke-report.schema.json 的 $defs/check 对齐。Evidence 只允许
// 数值事实（计数），结构上没有携带文本的通道。
type LocalPlatformContractCheck struct {
	Backend      string           `json:"backend"`
	Status       string           `json:"status"`
	DurationMS   int64            `json:"duration_ms"`
	FailureStage string           `json:"failure_stage"`
	ErrorClass   string           `json:"error_class,omitempty"`
	Evidence     map[string]int64 `json:"evidence,omitempty"`
}

// LocalPlatformContractCleanup 是 local profile 的清理声明：受控运行不创建
// 任何临时凭据或数据，也就不存在需要撤销/删除的东西——如实记录"未创建"。
type LocalPlatformContractCleanup struct {
	Status               string   `json:"status"`
	ResidualResources    []string `json:"residual_resources"`
	TemporaryCredentials string   `json:"temporary_credentials"`
	TemporaryData        string   `json:"temporary_data"`
}

// LocalPlatformContractReport 是 local platform_contract 的 schema 对齐报告。
type LocalPlatformContractReport struct {
	SchemaVersion string                       `json:"schema_version"`
	RunID         string                       `json:"run_id"`
	Marker        string                       `json:"marker"`
	Profile       string                       `json:"profile"`
	Scenario      string                       `json:"scenario"`
	StartedAt     time.Time                    `json:"started_at"`
	FinishedAt    time.Time                    `json:"finished_at"`
	Status        string                       `json:"status"`
	Checks        []LocalPlatformContractCheck `json:"checks"`
	Cleanup       LocalPlatformContractCleanup `json:"cleanup"`
}

// BuildLocalPlatformContractReport 聚合受控运行产物为 schema-valid 报告。
//
// 顶层状态只聚合两个受控 checks（platform_payload、privacy）：真实后端的
// skipped 是"声明范围外"的证据而非未完成的验证，若参与聚合，任何 local
// 报告都将是 skipped，通过证据与范围声明就无法区分。
func BuildLocalPlatformContractReport(input LocalPlatformContractReportInput) (LocalPlatformContractReport, error) {
	// local producer 与核心 producer 共用 v3 identity 值边界。这里必须在序列化前
	// fail-fast；依赖后续 schema 校验会允许一份自称 v3 的敏感/非法报告先被持久化。
	if !obssmoke.IsSafeSmokeReportIdentity(input.RunID) || !obssmoke.IsSafeSmokeReportIdentity(input.Marker) {
		return LocalPlatformContractReport{}, fmt.Errorf("invalid local platform contract report identity")
	}
	elapsed := input.FinishedAt.Sub(input.StartedAt)
	if elapsed > LocalPlatformContractDeadline {
		return LocalPlatformContractReport{}, fmt.Errorf(
			"controlled local platform contract run exceeded the %s deadline (elapsed %s); a slow controlled run is an implementation defect, not a tolerable variance",
			LocalPlatformContractDeadline, elapsed,
		)
	}

	payloadCheck := buildLocalPlatformPayloadCheck(input.Payload)
	privacyCheck := buildLocalPlatformPrivacyCheck(input.Privacy)

	checks := append(
		[]LocalPlatformContractCheck{payloadCheck, privacyCheck},
		localPlatformContractSkippedRealBackendChecks()...,
	)

	return LocalPlatformContractReport{
		SchemaVersion: localPlatformContractSchemaVersion,
		RunID:         input.RunID,
		Marker:        input.Marker,
		Profile:       localPlatformContractProfile,
		Scenario:      localPlatformContractScenario,
		StartedAt:     input.StartedAt,
		FinishedAt:    input.FinishedAt,
		Status:        aggregateLocalPlatformStatus(payloadCheck.Status, privacyCheck.Status),
		Checks:        checks,
		Cleanup: LocalPlatformContractCleanup{
			Status:               "not_required",
			ResidualResources:    []string{},
			TemporaryCredentials: "not_created",
			TemporaryData:        "not_created",
		},
	}, nil
}

func buildLocalPlatformPayloadCheck(result LocalPlatformSmokeResult) LocalPlatformContractCheck {
	// duration 由 runner 层计量；聚合层没有分项耗时输入，记 0 而不是虚构数值。
	check := LocalPlatformContractCheck{
		Backend:    "platform_payload",
		DurationMS: 0,
		Evidence: map[string]int64{
			"infra_stage_count": int64(len(result.Payload.InfraStages)),
			"ai_span_count":     int64(len(result.Payload.AISpans)),
			// 零外连是受控 runner 的结构契约（RunLocalPlatformSmoke 从不调用
			// transport.Dial，由 T151 的 counting transport 断言守护），不是
			// 本聚合层计量出来的数值。
			"external_attempts": 0,
		},
	}

	switch {
	case result.Skipped:
		check.Status = "skipped"
		check.FailureStage = "none"
		check.ErrorClass = localPlatformPayloadDisabledClass
	case result.Ready && result.PayloadSent:
		check.Status = "passed"
		check.FailureStage = "none"
	default:
		// 未发送也非显式 skip 属于异常态：受控链路在任何输入下都应产出
		// 明确结论，这里按失败归类并给出可检索的类别。
		check.Status = "failed"
		check.FailureStage = "api"
		check.ErrorClass = localPlatformPayloadNotSentClass
	}
	return check
}

func buildLocalPlatformPrivacyCheck(result PlatformPrivacyScanResult) LocalPlatformContractCheck {
	check := LocalPlatformContractCheck{
		Backend:    "privacy",
		DurationMS: 0,
		Evidence: map[string]int64{
			"scanned_surface_count": int64(len(result.ScannedSurfaces)),
			"finding_count":         int64(len(result.Findings)),
		},
	}

	if result.Clean && len(result.Findings) == 0 {
		check.Status = "passed"
		check.FailureStage = "none"
		return check
	}

	// 命中发生在受控 payload 的应用边界内：api 阶段即失败，从未触及
	// export/query——failure_stage 如实反映这一位置。
	check.Status = "failed"
	check.FailureStage = "api"
	check.ErrorClass = localPlatformPrivacyHitClass
	return check
}

func localPlatformContractSkippedRealBackendChecks() []LocalPlatformContractCheck {
	checks := make([]LocalPlatformContractCheck, 0, len(localPlatformContractRealBackends))
	for _, backend := range localPlatformContractRealBackends {
		checks = append(checks, LocalPlatformContractCheck{
			Backend:      backend,
			Status:       "skipped",
			DurationMS:   0,
			FailureStage: "none",
			ErrorClass:   localPlatformRealBackendSkipReason,
		})
	}
	return checks
}

func aggregateLocalPlatformStatus(controlledStatuses ...string) string {
	failed, skipped := false, false
	for _, status := range controlledStatuses {
		switch status {
		case "failed":
			failed = true
		case "skipped":
			skipped = true
		}
	}
	if failed {
		return "failed"
	}
	if skipped {
		return "skipped"
	}
	return "passed"
}
