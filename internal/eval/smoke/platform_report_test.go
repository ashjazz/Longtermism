package smoke

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	obssmoke "github.com/ashjazz/Longtermism/internal/observability/smoke"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

// === US5：local platform_contract smoke report 契约（T153 RED，T157 实现）===
//
// 这些测试固定 obs-platform-smoke 的证据边界：受控运行必须产出 schema-valid 的
// 机器可读报告，其中真实后端 checks 一律标记 skipped 并说明范围——local 报告
// 只证明 payload/identity/privacy 契约，绝不伪装成真实后端验收。顶层状态的
// 聚合规则也因此只看受控 checks：真实后端的 skipped 是"声明范围外"，不是
// "未完成的验证"，不得把一份通过的报告拖成 skipped。

const (
	localPlatformContractRunID   = "run-local-platform-contract-001"
	localPlatformContractMarker  = "marker-local-platform-contract-001"
	localPlatformContractStarted = "2026-08-20T09:00:00Z"
)

// completeLocalPlatformContractInput 返回一次受控运行成功路径的聚合输入。
// Payload/Privacy 字段的构造与 T151/T152 的 runner 结果同构，保证 report
// 层与受控链路层的类型咬合。
func completeLocalPlatformContractInput() LocalPlatformContractReportInput {
	return LocalPlatformContractReportInput{
		RunID:     localPlatformContractRunID,
		Marker:    localPlatformContractMarker,
		StartedAt: mustLocalPlatformContractTime(localPlatformContractStarted),
		FinishedAt: mustLocalPlatformContractTime(localPlatformContractStarted).Add(2 * time.Second),
		Payload: LocalPlatformSmokeResult{
			Ready:       true,
			PayloadSent: true,
			Payload: LocalPlatformSmokePayload{
				RequestID:      "req-local-platform-report-001",
				ServiceTraceID: "svc-trace-local-platform-report-001",
				RootSpanID:     "span-local-platform-report-001",
				RootAITraceID:  "ai-trace-local-platform-report-001",
				EvalRunID:      "eval-run-local-platform-report-001",
				InfraStages: []LocalPlatformSmokeInfraSpan{
					{Name: "http.server.request"},
				},
				AISpans: []LocalPlatformSmokeAISpan{
					{Name: "ai.generation", ObservationType: obs.ObservationTypeGeneration},
				},
			},
		},
		Privacy: PlatformPrivacyScanResult{
			Clean:           true,
			ScannedSurfaces: []string{"payload_json", "payload_debug", "baggage"},
		},
	}
}

func mustLocalPlatformContractTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestPlatformSmokeReportBuildsSchemaValidLocalPlatformContractReport(t *testing.T) {
	report, err := BuildLocalPlatformContractReport(completeLocalPlatformContractInput())
	if err != nil {
		t.Fatalf("BuildLocalPlatformContractReport() error = %v, want nil for a clean controlled run", err)
	}

	// local profile 与 platform_contract scenario 是报告的身份事实；任何把
	// local 受控报告冒充 grafana/signoz 真实验收的字段值都必须在这里暴露。
	if report.Profile != "local" {
		t.Fatalf("Profile = %q, want %q", report.Profile, "local")
	}
	if report.Scenario != "platform_contract" {
		t.Fatalf("Scenario = %q, want %q", report.Scenario, "platform_contract")
	}
	if report.SchemaVersion != "2" {
		t.Fatalf("SchemaVersion = %q, want %q", report.SchemaVersion, "2")
	}
	if report.Status != "passed" {
		t.Fatalf("Status = %q, want passed for a clean controlled run", report.Status)
	}

	// cleanup 语义：local 受控运行不创建任何临时凭据或数据，也就不存在需要
	// 撤销/删除的东西；报告必须如实记录"未创建"，不能空缺或谎报 completed。
	if report.Cleanup.Status != "not_required" {
		t.Fatalf("Cleanup.Status = %q, want not_required for the local profile", report.Cleanup.Status)
	}
	if report.Cleanup.TemporaryCredentials != "not_created" {
		t.Fatalf("Cleanup.TemporaryCredentials = %q, want not_created", report.Cleanup.TemporaryCredentials)
	}
	if report.Cleanup.TemporaryData != "not_created" {
		t.Fatalf("Cleanup.TemporaryData = %q, want not_created", report.Cleanup.TemporaryData)
	}

	assertLocalPlatformContractReportSchemaValid(t, report)
}

func TestPlatformSmokeReportMarksRealBackendsSkippedWithScopeNote(t *testing.T) {
	report, err := BuildLocalPlatformContractReport(completeLocalPlatformContractInput())
	if err != nil {
		t.Fatalf("BuildLocalPlatformContractReport() error = %v, want nil for a clean controlled run", err)
	}

	realBackends := map[string]bool{
		"collector": true, "tempo": true, "loki": true, "prometheus": true,
		"grafana": true, "langfuse_trace": true, "langfuse_score": true,
		"signoz": true, "signoz_traces": true, "signoz_logs": true, "signoz_metrics": true,
	}
	controlledBackends := map[string]bool{"platform_payload": true, "privacy": true}

	// 真实后端 checks：一律 skipped 且 error_class 说明范围；任何 passed 都
	// 是伪造证据（local 报告不可能验证真实后端）。
	for _, check := range report.Checks {
		if realBackends[check.Backend] {
			if check.Status != "skipped" {
				t.Fatalf("real backend %q has status %q, want skipped; a local report must never fake real backend evidence", check.Backend, check.Status)
			}
			if strings.TrimSpace(check.ErrorClass) == "" {
				t.Fatalf("skipped check for %q has an empty error_class; the skip must state its scope", check.Backend)
			}
		}
	}

	// 受控 checks：必须有明确结论（passed/failed/skipped 三态之一且真实反映输入）。
	foundControlled := map[string]bool{}
	for _, check := range report.Checks {
		if controlledBackends[check.Backend] {
			foundControlled[check.Backend] = true
			if check.Status != "passed" && check.Status != "failed" && check.Status != "skipped" {
				t.Fatalf("controlled check %q has invalid status %q", check.Backend, check.Status)
			}
		}
	}
	for _, required := range []string{"platform_payload", "privacy"} {
		if !foundControlled[required] {
			t.Fatalf("report is missing the controlled %q check; got checks %#v", required, report.Checks)
		}
	}

	// 真实后端代表覆盖：基础设施入口（collector）、infra 存储（tempo）与
	// AI 平面（langfuse_trace）都必须出现在 skipped 证据里，缺一类就是范围
	// 声明不完整。
	for _, representative := range []string{"collector", "tempo", "langfuse_trace"} {
		found := false
		for _, check := range report.Checks {
			if check.Backend == representative {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("report is missing a skipped check for representative real backend %q", representative)
		}
	}

	// 零外连事实必须进入受控 payload check 的 evidence。
	for _, check := range report.Checks {
		if check.Backend == "platform_payload" {
			if attempts, present := check.Evidence["external_attempts"]; !present || attempts != 0 {
				t.Fatalf("platform_payload check evidence must record external_attempts=0, got %#v", check.Evidence)
			}
		}
	}

	assertLocalPlatformContractReportSchemaValid(t, report)
}

func TestPlatformSmokeReportPropagatesControlledOutcomesWithoutInflatingStatus(t *testing.T) {
	t.Run("privacy findings fail the report with a non-none failure stage", func(t *testing.T) {
		input := completeLocalPlatformContractInput()
		input.Privacy = PlatformPrivacyScanResult{
			Clean: false,
			ScannedSurfaces: []string{"payload_json", "payload_debug", "baggage"},
			Findings: []PlatformPrivacyFinding{
				{Surface: "payload_debug", Category: "sensitive_value", Count: 1},
			},
		}

		report, err := BuildLocalPlatformContractReport(input)
		if err != nil {
			t.Fatalf("BuildLocalPlatformContractReport() error = %v, want the failure encoded in the report, not an error", err)
		}
		if report.Status != "failed" {
			t.Fatalf("Status = %q, want failed when the controlled privacy scan has findings", report.Status)
		}
		privacyCheck := findLocalPlatformContractCheck(t, report, "privacy")
		if privacyCheck.Status != "failed" {
			t.Fatalf("privacy check status = %q, want failed", privacyCheck.Status)
		}
		if privacyCheck.FailureStage == "none" {
			t.Fatalf("privacy check failure_stage = none, want a concrete stage for a failed check")
		}
		if strings.TrimSpace(privacyCheck.ErrorClass) == "" {
			t.Fatalf("privacy check has an empty error_class; failures must be classified")
		}

		assertLocalPlatformContractReportSchemaValid(t, report)
	})

	t.Run("disabled payload turns the report into skipped instead of passed", func(t *testing.T) {
		input := completeLocalPlatformContractInput()
		input.Payload = LocalPlatformSmokeResult{Skipped: true, SkipReason: "platform smoke is not enabled"}

		report, err := BuildLocalPlatformContractReport(input)
		if err != nil {
			t.Fatalf("BuildLocalPlatformContractReport() error = %v, want nil for a skipped controlled run", err)
		}
		if report.Status != "skipped" {
			t.Fatalf("Status = %q, want skipped when the controlled payload run was skipped", report.Status)
		}
		payloadCheck := findLocalPlatformContractCheck(t, report, "platform_payload")
		if payloadCheck.Status != "skipped" {
			t.Fatalf("platform_payload check status = %q, want skipped", payloadCheck.Status)
		}
	})
}

func TestPlatformSmokeReportEnforcesLocalContractDeadline(t *testing.T) {
	// US5 的独立验收标准是"清空外部配置后 30 秒内完成"。deadline 是报告
	// 契约的一部分：受控运行必须在 30 秒内结束，超时属于实现缺陷而不是
	// 可以静默放慢的自由度。
	if LocalPlatformContractDeadline != 30*time.Second {
		t.Fatalf("LocalPlatformContractDeadline = %v, want 30s", LocalPlatformContractDeadline)
	}

	input := completeLocalPlatformContractInput()
	input.FinishedAt = input.StartedAt.Add(LocalPlatformContractDeadline + time.Second)
	if _, err := BuildLocalPlatformContractReport(input); err == nil {
		t.Fatalf("BuildLocalPlatformContractReport() error = nil, want fail-fast when the controlled run exceeds the deadline")
	} else if !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("error = %q, want it to name the deadline", err.Error())
	}

	boundary := completeLocalPlatformContractInput()
	boundary.FinishedAt = boundary.StartedAt.Add(LocalPlatformContractDeadline)
	if _, err := BuildLocalPlatformContractReport(boundary); err != nil {
		t.Fatalf("BuildLocalPlatformContractReport() error = %v, want nil at exactly the deadline boundary", err)
	}
}

func findLocalPlatformContractCheck(t *testing.T, report LocalPlatformContractReport, backend string) LocalPlatformContractCheck {
	t.Helper()

	for _, check := range report.Checks {
		if check.Backend == backend {
			return check
		}
	}
	t.Fatalf("report has no check for backend %q: %#v", backend, report.Checks)
	return LocalPlatformContractCheck{}
}

// assertLocalPlatformContractReportSchemaValid 用版本控制的 smoke report schema
// 真校验生成的报告：字段级断言不能替代 schema 约束，任何 additionalProperties
// 或枚举漂移都要在这里暴露。
func assertLocalPlatformContractReportSchemaValid(t *testing.T, report LocalPlatformContractReport) {
	t.Helper()

	schema, err := readVersionControlledSmokeReportSchema()
	if err != nil {
		t.Fatalf("failed to load the version-controlled smoke report schema: %v", err)
	}
	validator, err := obssmoke.NewSmokeReportSchemaValidator(schema)
	if err != nil {
		t.Fatalf("NewSmokeReportSchemaValidator() error = %v", err)
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("failed to encode the local platform contract report: %v", err)
	}
	if err := validator.ValidateJSON(encoded); err != nil {
		t.Fatalf("local platform contract report is not schema-valid: %v\nreport: %s", err, encoded)
	}
}

func readVersionControlledSmokeReportSchema() ([]byte, error) {
	return os.ReadFile(filepath.Join("..", "..", "..", "specs", "003-real-observability-backends", "contracts", "smoke-report.schema.json"))
}
