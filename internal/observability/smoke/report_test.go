package smoke

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuildSmokeReportAggregatesChecksAndOwnedCleanupEvidence(t *testing.T) {
	input := validSmokeReportInput()
	report, err := BuildSmokeReport(input)
	if err != nil {
		t.Fatal("BuildSmokeReport() returned an unexpected error")
	}

	// Marker 访问器只读密封的 runner-owned identity：组合根（如 resilience
	// full aggregate）用它强制子报告 marker 唯一性，绝不能从调用方猜测。
	if report.Marker() != "marker-t019-001" || report.Scenario() != "privacy" || report.Status() != "failed" {
		t.Fatalf("sealed accessors = %q/%q/%q, want the frozen low-sensitivity facts", report.Marker(), report.Scenario(), report.Status())
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal("SmokeReport could not be encoded as JSON")
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal("SmokeReport did not encode as valid JSON")
	}

	// 后端检查失败不能被后续 passed 检查覆盖；报告要保留失败域与真实 failure_stage，
	// 这样 E2E 才能区分 API、export 与查询链路，而不是只得到一个笼统的失败结论。
	wantChecks := []any{
		map[string]any{"backend": "api", "status": "passed", "duration_ms": float64(12), "failure_stage": "none", "evidence": map[string]any{"marker_seen": true}},
		map[string]any{"backend": "tempo", "status": "failed", "duration_ms": float64(40), "failure_stage": "query", "error_class": "backend_timeout", "evidence": map[string]any{"matched_spans": float64(0)}},
		map[string]any{"backend": "loki", "status": "passed", "duration_ms": float64(8), "failure_stage": "none", "evidence": map[string]any{"matched_logs": float64(1)}},
	}
	if !reflect.DeepEqual(got["checks"], wantChecks) {
		t.Fatal("SmokeReport checks did not preserve the stable backend evidence")
	}
	if got["schema_version"] != "2" || got["status"] != "failed" || got["run_id"] != "run-t019-001" || got["marker"] != "marker-t019-001" || got["profile"] != "local" || got["scenario"] != "privacy" || got["started_at"] != "2026-07-13T01:02:03Z" || got["finished_at"] != "2026-07-13T01:02:53Z" {
		t.Fatal("SmokeReport did not aggregate run identity and overall status")
	}
	wantCleanup := map[string]any{
		"status":                "completed",
		"residual_resources":    []any{},
		"temporary_credentials": "revoked",
		"temporary_data":        "deleted",
	}
	if !reflect.DeepEqual(got["cleanup"], wantCleanup) {
		t.Fatal("SmokeReport did not retain smoke-owned credential and data cleanup evidence")
	}
	if !reflect.DeepEqual(got["versions"], map[string]any{"collector": "0.127.0", "schema": "2"}) {
		t.Fatal("SmokeReport did not retain the low-sensitivity version matrix")
	}
	assertSmokeReportDoesNotContainSensitiveValues(t, string(encoded))
}

func TestBuildSmokeReportAggregatesPassedAndSkippedResults(t *testing.T) {
	tests := []struct {
		name       string
		checkState string
		wantStatus string
	}{
		{name: "all passed checks produce a passed report", checkState: "passed", wantStatus: "passed"},
		{name: "all skipped checks produce a skipped report", checkState: "skipped", wantStatus: "skipped"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validSmokeReportInput()
			// 纯检查聚合语义只能在没有 sealed proof set 的场景下独立成立：privacy 场景的
			// 八项证明自 T198 起参与状态聚合，检查跳过不再等于整体跳过。
			input.Scenario = "infra"
			input.PrivacyEvidence = nil
			input.Checks = input.Checks[:1]
			input.Checks[0].Status = tt.checkState
			report, err := BuildSmokeReport(input)
			if err != nil {
				t.Fatal("BuildSmokeReport() returned an unexpected error")
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal("SmokeReport could not be encoded as JSON")
			}
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal("SmokeReport did not encode as valid JSON")
			}
			if got["status"] != tt.wantStatus {
				t.Fatal("SmokeReport did not aggregate the check status")
			}
		})
	}
}

func TestBuildSmokeReportProjectsOptionalChatIdentity(t *testing.T) {
	input := validSmokeReportInput()
	input.Scenario = "chat"
	input.PrivacyEvidence = nil
	input.RequestID = "request-t105"
	input.AITraceID = "ai-trace-t105"
	report, err := BuildSmokeReport(input)
	if err != nil {
		t.Fatal("BuildSmokeReport() rejected valid low-sensitivity chat identity")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal("SmokeReport could not be encoded as JSON")
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal("SmokeReport did not encode as valid JSON")
	}
	if got["request_id"] != input.RequestID || got["ai_trace_id"] != input.AITraceID {
		t.Fatal("SmokeReport omitted the response-owned chat correlation identity")
	}

	input.RequestID = "short"
	if _, err := BuildSmokeReport(input); err == nil {
		t.Fatal("BuildSmokeReport() accepted an invalid chat identity")
	}
}

func TestBuildSmokeReportMakesCleanupFailurePartOfOverallResult(t *testing.T) {
	input := validSmokeReportInput()
	input.Checks = input.Checks[:1]
	input.Cleanup.Status = "failed"
	input.Cleanup.ResidualResources = []string{"run-directory"}
	input.Cleanup.TemporaryCredentials = "failed"
	input.Cleanup.TemporaryData = "failed"
	input.Checks = append(input.Checks, BackendCheckInput{
		Backend: "api", Status: "failed", Duration: time.Millisecond, FailureStage: "cleanup", ErrorClass: "temporary_credential_revoke_failed", Evidence: map[string]any{"cleanup_attempted": true},
	})

	report, err := BuildSmokeReport(input)
	if err != nil {
		t.Fatal("BuildSmokeReport() returned an unexpected error")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal("SmokeReport could not be encoded as JSON")
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal("SmokeReport did not encode as valid JSON")
	}
	checks := got["checks"].([]any)
	cleanupCheck := checks[len(checks)-1].(map[string]any)
	if got["status"] != "failed" || cleanupCheck["failure_stage"] != "cleanup" || cleanupCheck["error_class"] != "temporary_credential_revoke_failed" || !reflect.DeepEqual(got["cleanup"], map[string]any{
		"status":                "failed",
		"residual_resources":    []any{"run-directory"},
		"temporary_credentials": "failed",
		"temporary_data":        "failed",
	}) {
		t.Fatal("SmokeReport did not make cleanup failure visible in its stable result")
	}
	assertSmokeReportDoesNotContainSensitiveValues(t, string(encoded))
}

func TestBuildSmokeReportRejectsSensitiveEvidenceAndErrorDetailsWithoutEcho(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SmokeReportInput)
	}{
		{name: "evidence", mutate: func(input *SmokeReportInput) { input.Checks[1].Evidence["debug"] = "smoke-owned-t019-credential" }},
		{name: "unknown evidence key", mutate: func(input *SmokeReportInput) { input.Checks[1].Evidence["user_name"] = "alice" }},
		{name: "unknown version key", mutate: func(input *SmokeReportInput) { input.Versions["x-api-key"] = "secret" }},
		{name: "error class", mutate: func(input *SmokeReportInput) { input.Checks[1].ErrorClass = "synthetic-private-payload-t019" }},
		{name: "residual resource", mutate: func(input *SmokeReportInput) { input.Cleanup.ResidualResources = []string{"/tmp/t019-raw-debug-data"} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validSmokeReportInput()
			tt.mutate(&input)
			_, err := BuildSmokeReport(input)
			if err == nil {
				t.Fatal("BuildSmokeReport() error = nil, want sensitive report input rejected")
			}
			assertSmokeReportDoesNotContainSensitiveValues(t, err.Error())
		})
	}
}

func TestSmokeReportDefensivelyCopiesMutableInputAndOutput(t *testing.T) {
	input := validSmokeReportInput()
	report, err := BuildSmokeReport(input)
	if err != nil {
		t.Fatal("BuildSmokeReport() returned an unexpected error")
	}

	input.Checks[0].Evidence["marker_seen"] = false
	input.Cleanup.ResidualResources = append(input.Cleanup.ResidualResources, "mutated-after-build")
	input.Versions["collector"] = "mutated-after-build"
	checks := report.Checks()
	cleanup := report.Cleanup()
	if checks[0].Evidence["marker_seen"] != true || len(cleanup.ResidualResources) != 0 {
		t.Fatal("SmokeReport retained mutable input references")
	}
	versions := report.Versions()
	if versions["collector"] != "0.127.0" {
		t.Fatal("SmokeReport retained a mutable version matrix")
	}

	checks[0].Evidence["marker_seen"] = false
	cleanup.ResidualResources = append(cleanup.ResidualResources, "mutated-after-read")
	versions["collector"] = "mutated-after-read"
	freshChecks := report.Checks()
	freshCleanup := report.Cleanup()
	if freshChecks[0].Evidence["marker_seen"] != true || len(freshCleanup.ResidualResources) != 0 {
		t.Fatal("SmokeReport exposed mutable output references")
	}
	if report.Versions()["collector"] != "0.127.0" {
		t.Fatal("SmokeReport exposed a mutable version matrix")
	}
}

func TestBuildSmokeReportRejectsExpiredWindowAndDoesNotEchoSensitiveInput(t *testing.T) {
	input := validSmokeReportInput()
	input.FinishedAt = input.Deadline.Add(time.Nanosecond)

	_, err := BuildSmokeReport(input)
	if err == nil {
		t.Fatal("BuildSmokeReport() error = nil, want expired smoke window rejected")
	}
	assertSmokeReportDoesNotContainSensitiveValues(t, err.Error())
}

func TestBuildSmokeReportRejectsSchemaAndCleanupContradictions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SmokeReportInput)
	}{
		{name: "short run ID", mutate: func(input *SmokeReportInput) { input.RunID = "short" }},
		{name: "short marker", mutate: func(input *SmokeReportInput) { input.Marker = "short" }},
		{name: "non-finite evidence", mutate: func(input *SmokeReportInput) { input.Checks[1].Evidence["matched_spans"] = math.NaN() }},
		{name: "completed cleanup with failed credentials", mutate: func(input *SmokeReportInput) { input.Cleanup.TemporaryCredentials = "failed" }},
		{name: "completed cleanup with residual resource", mutate: func(input *SmokeReportInput) { input.Cleanup.ResidualResources = []string{"run-directory"} }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validSmokeReportInput()
			test.mutate(&input)
			if _, err := BuildSmokeReport(input); err == nil {
				t.Fatal("BuildSmokeReport() error = nil, want contradictory input rejected")
			}
		})
	}
}

// validSmokeReportInput 使用 T186 锁定 schema 之后的完整 privacy 场景：八项密封 surface
// 证明与三组检查共同构成 schema-valid 报告（T198 组合契约使 proof set 成为 privacy 场景
// 的强制语义，旧的单检查 fixture 已不再合法）。
func validSmokeReportInput() SmokeReportInput {
	startedAt := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
	return SmokeReportInput{
		RunID:      "run-t019-001",
		Marker:     "marker-t019-001",
		Profile:    "local",
		Scenario:   "privacy",
		StartedAt:  startedAt,
		Deadline:   startedAt.Add(time.Minute),
		FinishedAt: startedAt.Add(50 * time.Second),
		Versions:   map[string]string{"collector": "0.127.0", "schema": "2"},
		Checks: []BackendCheckInput{
			{Backend: "api", Status: "passed", Duration: 12 * time.Millisecond, FailureStage: "none", Evidence: map[string]any{"marker_seen": true}},
			{Backend: "tempo", Status: "failed", Duration: 40 * time.Millisecond, FailureStage: "query", ErrorClass: "backend_timeout", Evidence: map[string]any{"matched_spans": 0}},
			{Backend: "loki", Status: "passed", Duration: 8 * time.Millisecond, FailureStage: "none", Evidence: map[string]any{"matched_logs": 1}},
		},
		PrivacyEvidence: t019PrivacyEvidence(),
		Cleanup: SmokeCleanupInput{
			Status:               "completed",
			ResidualResources:    []string{},
			TemporaryCredentials: "revoked",
			TemporaryData:        "deleted",
		},
	}
}

func t019PrivacyEvidence() []PrivacySmokeReportEvidenceInput {
	evidence := make([]PrivacySmokeReportEvidenceInput, 0, len(privacyCompositionSchemaOrder))
	for _, surface := range privacyCompositionSchemaOrder {
		evidence = append(evidence, PrivacySmokeReportEvidenceInput{
			Surface: surface, EvidenceMethod: privacyCompositionMethod(surface), Status: "passed",
			ScannerPolicyVersion: "1", Counts: privacyCompositionZeroCounts(),
			CollectorProofVerified: surface == PrivacySmokeSurfaceCollectorQueue,
		})
	}
	return evidence
}

func assertSmokeReportDoesNotContainSensitiveValues(t *testing.T, rendered string) {
	t.Helper()
	for _, forbidden := range []string{
		"smoke-owned-t019-credential",
		"/tmp/t019-raw-debug-data",
		"synthetic-private-payload-t019",
		"temporary_credential_value",
		"temporary_data_path",
		"raw_payload",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatal("SmokeReport reflected a forbidden sensitive input")
		}
	}
}
