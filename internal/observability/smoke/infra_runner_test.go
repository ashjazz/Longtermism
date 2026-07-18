package smoke

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestInfrastructureSmokeRunnerContract 用 fake backend 固定纯基础设施验收边界：
// 本次 run 的 marker 必须在 60 秒窗口内同时出现在 Tempo/Loki，指标只能按
// 低基数 route/status 计算增量，且 Langfuse/AI 平面必须保持为零。
func TestInfrastructureSmokeRunnerContract(t *testing.T) {
	startedAt := time.Date(2026, time.July, 18, 10, 0, 0, 0, time.UTC)
	deadline := startedAt.Add(60 * time.Second)
	backend := fakeInfrastructureBackend{
		marker:     "infra-t048-marker",
		observedAt: startedAt.Add(time.Second),
		before:     41,
		after:      42,
	}

	report, err := RunInfrastructureSmoke(context.Background(), InfrastructureSmokeRequest{
		RunID:     "infra-t048-run",
		Marker:    backend.marker,
		StartedAt: startedAt,
		Deadline:  deadline,
		Profile:   "grafana",
	}, backend)
	if err != nil {
		t.Fatalf("RunInfrastructureSmoke() error = %v", err)
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("MarshalJSON() error = %v", err)
	}
	validator, err := NewSmokeReportSchemaValidator(loadSmokeSchema(t))
	if err != nil {
		t.Fatalf("NewSmokeReportSchemaValidator() error = %v", err)
	}
	if err := validator.ValidateJSON(encoded); err != nil {
		t.Fatalf("schema validation error = %v", err)
	}
	if backend.langfuseQueries != 1 || backend.aiPlaneMatches != 0 {
		t.Fatalf("infra smoke negative queries = langfuse:%d ai-plane:%d, want 1/0", backend.langfuseQueries, backend.aiPlaneMatches)
	}
}

type fakeInfrastructureBackend struct {
	marker          string
	observedAt      time.Time
	before          int64
	after           int64
	langfuseQueries int
	aiPlaneMatches  int
}

func (f fakeInfrastructureBackend) QueryTempo(context.Context, PollMarkerTarget) ([]MarkerObservation, error) {
	return []MarkerObservation{{Marker: f.marker, ObservedAt: f.observedAt}}, nil
}

func (f fakeInfrastructureBackend) QueryLoki(context.Context, PollMarkerTarget) ([]MarkerObservation, error) {
	return []MarkerObservation{{Marker: f.marker, ObservedAt: f.observedAt}}, nil
}

func (f fakeInfrastructureBackend) HTTPRequestCount(context.Context) (int64, error) {
	return f.after, nil
}
func (f fakeInfrastructureBackend) BaselineHTTPRequestCount(context.Context) (int64, error) {
	return f.before, nil
}
func (f fakeInfrastructureBackend) QueryLangfuse(context.Context, PollMarkerTarget) (int, error) {
	return 0, nil
}
func (f fakeInfrastructureBackend) QueryAIPlane(context.Context, PollMarkerTarget) (int, error) {
	return 0, nil
}

func loadSmokeSchema(t *testing.T) []byte {
	t.Helper()
	return []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`)
}
