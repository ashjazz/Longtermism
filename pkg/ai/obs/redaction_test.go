package obs

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestScanForbiddenPayloadFieldsRejectsForbiddenKeys(t *testing.T) {
	fields := map[string]string{
		"raw_query":         "用户原文不应该进入普通观测",
		"raw_output":        "模型原始回答不应该进入普通观测",
		"raw_response":      "模型原始响应不应该进入普通观测",
		"response_content":  "模型响应正文不应该进入普通观测",
		"prompt_content":    "完整 prompt 不应该进入普通观测",
		"tool_args":         `{"password":"secret"}`,
		"api_key":           "sk-test-forbidden-key",
		"jwt":               "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.signature",
		"password":          "secret-password",
		"external_response": "上游响应原文",
	}

	findings := ScanForbiddenPayloadFields(fields)

	assertForbiddenPayloadFindingKeys(t, findings, []string{
		"api_key",
		"external_response",
		"jwt",
		"password",
		"prompt_content",
		"raw_output",
		"raw_query",
		"raw_response",
		"response_content",
		"tool_args",
	})
	for _, finding := range findings {
		if finding.Reason != "forbidden_key" {
			t.Fatalf("finding for key %q reason = %q, want forbidden_key", finding.Key, finding.Reason)
		}
	}
	assertForbiddenPayloadFindingsDoNotEchoValues(t, findings, fields)
}

func TestScanForbiddenPayloadFieldsRejectsSensitiveValues(t *testing.T) {
	fields := map[string]string{
		"query_hash":       "safe-query-hash",
		"prompt_hash":      "safe-prompt-hash",
		"user_id":          "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ1c2VyIn0.signature",
		"model":            "sk-test-sensitive-value",
		"provider_name":    "Bearer secret-token",
		"outcome_status":   "password=secret",
		"requested_model":  "external_response: private upstream body",
		"response_summary": "raw output: private model response",
		"tool_call_id":     `{"account_id":"acct-private","password":"secret"}`,
		"service_trace_id": "svc-trace-safe",
	}

	findings := ScanForbiddenPayloadFields(fields)

	assertForbiddenPayloadFindingKeys(t, findings, []string{
		"model",
		"outcome_status",
		"provider_name",
		"requested_model",
		"response_summary",
		"tool_call_id",
		"user_id",
	})
	for _, finding := range findings {
		if finding.Reason != "sensitive_value" {
			t.Fatalf("finding for key %q reason = %q, want sensitive_value", finding.Key, finding.Reason)
		}
	}
	assertForbiddenPayloadFindingsDoNotEchoValues(t, findings, fields)
}

func TestScanForbiddenPayloadFieldsNormalizesKeysAndKeepsInputImmutable(t *testing.T) {
	fields := map[string]string{
		" Raw_Query ": "raw user text",
		"TRACE_ID":    "trace-safe",
	}
	before := cloneRedactionTestFields(fields)

	findings := ScanForbiddenPayloadFields(fields)

	assertForbiddenPayloadFindingKeys(t, findings, []string{" Raw_Query "})
	if !reflect.DeepEqual(fields, before) {
		t.Fatalf("ScanForbiddenPayloadFields mutated input: got %#v, want %#v", fields, before)
	}
}

func assertForbiddenPayloadFindingKeys(t *testing.T, findings []ForbiddenPayloadFinding, want []string) {
	t.Helper()

	got := make([]string, len(findings))
	for index, finding := range findings {
		got[index] = finding.Key
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("finding keys = %#v, want %#v; findings = %#v", got, want, findings)
	}
}

func assertForbiddenPayloadFindingsDoNotEchoValues(t *testing.T, findings []ForbiddenPayloadFinding, fields map[string]string) {
	t.Helper()

	payload, err := json.Marshal(findings)
	if err != nil {
		t.Fatalf("marshal findings: %v", err)
	}
	raw := string(payload)
	for _, value := range fields {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if strings.Contains(raw, value) {
			t.Fatalf("finding payload echoed sensitive value %q: %s", value, raw)
		}
	}
}

func cloneRedactionTestFields(fields map[string]string) map[string]string {
	cloned := make(map[string]string, len(fields))
	for key, value := range fields {
		cloned[key] = value
	}
	return cloned
}
