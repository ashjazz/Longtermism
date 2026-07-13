package smoke

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSmokeReportSchemaValidatorAcceptsEverySupportedScenario(t *testing.T) {
	validator, err := NewSmokeReportSchemaValidator(loadSmokeReportSchema(t))
	if err != nil {
		t.Fatal("NewSmokeReportSchemaValidator() returned an unexpected error")
	}

	for _, scenario := range []string{
		"infra",
		"chat",
		"score",
		"privacy",
		"exporter_failure",
		"persistent_queue",
		"storage_failure",
		"score_worker_failure",
		"alert",
		"retention",
		"platform_contract",
		"full",
	} {
		t.Run(scenario, func(t *testing.T) {
			document := validSmokeReportDocument(scenario)
			encoded := marshalSmokeReportDocument(t, document)
			if err := validator.ValidateJSON(encoded); err != nil {
				t.Fatal("ValidateJSON() rejected a schema-valid smoke report")
			}
		})
	}
}

func TestSmokeReportSchemaValidatorRejectsInvalidDocumentsWithoutEchoingPayload(t *testing.T) {
	validator, err := NewSmokeReportSchemaValidator(loadSmokeReportSchema(t))
	if err != nil {
		t.Fatal("NewSmokeReportSchemaValidator() returned an unexpected error")
	}

	tests := []struct {
		name     string
		wantPath string
		mutate   func(map[string]any)
	}{
		{
			name:     "missing marker",
			wantPath: "$.marker",
			mutate: func(document map[string]any) {
				delete(document, "marker")
			},
		},
		{
			name:     "invalid backend",
			wantPath: "$.checks[0].backend",
			mutate: func(document map[string]any) {
				document["checks"].([]map[string]any)[0]["backend"] = "unknown_backend"
			},
		},
		{
			name:     "additional check property",
			wantPath: "$.checks[0].raw_payload",
			mutate: func(document map[string]any) {
				document["checks"].([]map[string]any)[0]["raw_payload"] = "synthetic-private-payload-t020"
			},
		},
		{
			name:     "invalid failure stage",
			wantPath: "$.checks[1].failure_stage",
			mutate: func(document map[string]any) {
				document["checks"].([]map[string]any)[1]["failure_stage"] = "unknown_stage"
			},
		},
		{
			name:     "negative duration",
			wantPath: "$.checks[0].duration_ms",
			mutate: func(document map[string]any) {
				document["checks"].([]map[string]any)[0]["duration_ms"] = -1
			},
		},
		{
			name:     "missing check failure stage",
			wantPath: "$.checks[1].failure_stage",
			mutate: func(document map[string]any) {
				delete(document["checks"].([]map[string]any)[1], "failure_stage")
			},
		},
		{
			name:     "missing temporary credentials cleanup",
			wantPath: "$.cleanup.temporary_credentials",
			mutate: func(document map[string]any) {
				delete(document["cleanup"].(map[string]any), "temporary_credentials")
			},
		},
		{
			name:     "missing temporary data cleanup",
			wantPath: "$.cleanup.temporary_data",
			mutate: func(document map[string]any) {
				delete(document["cleanup"].(map[string]any), "temporary_data")
			},
		},
		{
			name:     "passed check with failed stage",
			wantPath: "$.checks[0].failure_stage",
			mutate: func(document map[string]any) {
				document["checks"].([]map[string]any)[0]["failure_stage"] = "query"
			},
		},
		{
			name:     "failed check without a stage",
			wantPath: "$.checks[1].failure_stage",
			mutate: func(document map[string]any) {
				document["checks"].([]map[string]any)[1]["failure_stage"] = "none"
			},
		},
		{
			name:     "additional root property",
			wantPath: "$.authorization",
			mutate: func(document map[string]any) {
				document["authorization"] = "Bearer synthetic-t020-authorization"
			},
		},
		{
			name:     "additional cleanup property",
			wantPath: "$.cleanup.temporary_credential_value",
			mutate: func(document map[string]any) {
				document["cleanup"].(map[string]any)["temporary_credential_value"] = "smoke-owned-t020-credential"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document := validSmokeReportDocument("privacy")
			tt.mutate(document)
			err := validator.ValidateJSON(marshalSmokeReportDocument(t, document))
			if err == nil {
				t.Fatal("ValidateJSON() error = nil, want invalid report rejected")
			}
			if !strings.Contains(err.Error(), tt.wantPath) {
				t.Fatal("ValidateJSON() error did not identify the rejected JSON path")
			}
			assertSchemaValidationErrorDoesNotEchoSensitiveValues(t, err.Error())
		})
	}
}

func TestSmokeReportSchemaValidatorNeverResolvesRemoteReferences(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	// `$schema` 与 `$ref` 都是潜在的外连入口。报告 schema 必须是仓库内的
	// 自包含契约；遇到远端引用时 fail-closed，而不是为了“验证成功”取网络数据。
	remoteReferenceSchema := []byte(`{"$ref": "` + server.URL + `/remote.schema.json"}`)
	_, err := NewSmokeReportSchemaValidator(remoteReferenceSchema)
	if err == nil {
		t.Fatal("NewSmokeReportSchemaValidator() error = nil, want remote reference rejected")
	}
	if requests.Load() != 0 {
		t.Fatal("NewSmokeReportSchemaValidator() attempted a remote schema request")
	}
}

func validSmokeReportDocument(scenario string) map[string]any {
	return map[string]any{
		"schema_version": "2",
		"run_id":         "run-t020-" + scenario,
		"marker":         "marker-t020-" + scenario,
		"profile":        "local",
		"scenario":       scenario,
		"started_at":     "2026-07-13T01:02:03Z",
		"finished_at":    "2026-07-13T01:02:53Z",
		"status":         "failed",
		"checks": []map[string]any{
			{"backend": "api", "status": "passed", "duration_ms": 12, "failure_stage": "none", "evidence": map[string]any{"marker_seen": true}},
			{"backend": "tempo", "status": "failed", "duration_ms": 40, "failure_stage": "query", "error_class": "backend_timeout", "evidence": map[string]any{"matched_spans": 0}},
		},
		"cleanup": map[string]any{
			"status":                "completed",
			"residual_resources":    []string{},
			"temporary_credentials": "revoked",
			"temporary_data":        "deleted",
		},
	}
}

func marshalSmokeReportDocument(t *testing.T, document map[string]any) []byte {
	t.Helper()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal("failed to encode smoke report fixture")
	}
	return encoded
}

func smokeReportSchemaPath() string {
	return filepath.Join("..", "..", "..", "specs", "003-real-observability-backends", "contracts", "smoke-report.schema.json")
}

func loadSmokeReportSchema(t *testing.T) []byte {
	t.Helper()
	schema, err := os.ReadFile(smokeReportSchemaPath())
	if err != nil {
		t.Fatal("failed to load the version-controlled smoke report schema")
	}
	return schema
}

func assertSchemaValidationErrorDoesNotEchoSensitiveValues(t *testing.T, message string) {
	t.Helper()
	for _, forbidden := range []string{
		"synthetic-private-payload-t020",
		"synthetic-t020-authorization",
		"smoke-owned-t020-credential",
	} {
		if strings.Contains(message, forbidden) {
			t.Fatal("ValidateJSON() error echoed a forbidden payload value")
		}
	}
}
