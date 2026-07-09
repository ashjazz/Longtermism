package smoke

import (
	"context"
	"path/filepath"
	"testing"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
)

func TestAgentSmokeGoldenDatasetLoads(t *testing.T) {
	t.Parallel()

	// 这个测试加载真实的 internal/eval/golden/agent_smoke.json，而不是临时 fixture。
	// 它的价值是把“评估资产文件仍然符合 JSONDataset 契约”纳入 Go 测试门禁：
	// 文件改坏、样例 ID 漂移、关键 meta 字段缺失时，都能在本地测试里尽早暴露。
	dataset := aieval.NewJSONDataset(filepath.Join("..", "golden", "agent_smoke.json"))

	samples, err := dataset.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(samples) != 4 {
		t.Fatalf("samples length = %d, want 4", len(samples))
	}

	byID := make(map[string]aieval.Sample, len(samples))
	for _, sample := range samples {
		byID[sample.ID] = sample
	}

	assertAgentGoldenSample(t, byID, "agent-smoke-success-tool-loop", "finished", 1)
	assertAgentGoldenSample(t, byID, "agent-smoke-tool-error-visible", "finished", 1)
	assertAgentGoldenSample(t, byID, "agent-smoke-self-correction-after-tool-error", "finished", 2)
	assertAgentGoldenSample(t, byID, "agent-smoke-max-steps-limit", "max_steps", 3)
}

func TestAgentSmokeGoldenEvalTraceLinksAreDiagnosable(t *testing.T) {
	t.Parallel()

	dataset := aieval.NewJSONDataset(filepath.Join("..", "golden", "agent_smoke.json"))
	samples, err := dataset.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	traceLinkSamples := makeAgentGoldenTraceLinkSamples(t, samples)
	result, err := RunEvalTraceLinkSmoke(context.Background(), EvalTraceLinkSmokeConfig{
		Dataset:   aieval.DatasetIdentity{Name: "agent-smoke", Version: "agent-smoke-v1"},
		EvalRunID: "eval-run-agent-smoke-golden-001",
		Samples:   traceLinkSamples,
	})
	if err != nil {
		t.Fatalf("RunEvalTraceLinkSmoke() error = %v", err)
	}

	if result.SampleCount != 4 {
		t.Fatalf("SampleCount = %d, want 4 agent golden samples", result.SampleCount)
	}
	if result.LinkedCount != result.SampleCount {
		t.Fatalf("LinkedCount = %d, want all %d samples linked; missing = %#v", result.LinkedCount, result.SampleCount, result.MissingLinks)
	}
	assertAgentGoldenTraceLinkCoverage(t, traceLinkSamples, map[string]bool{
		"successful_tool_call_loop": false,
		"tool_error":                false,
		"self_correction":           false,
		"max_steps":                 false,
	})
}

func assertAgentGoldenSample(t *testing.T, samples map[string]aieval.Sample, id string, wantTerminatedBy string, wantSteps float64) {
	t.Helper()

	sample, ok := samples[id]
	if !ok {
		t.Fatalf("sample %q missing", id)
	}
	if sample.Query == "" {
		t.Fatalf("sample %q query is empty", id)
	}
	if sample.GroundTruth == "" {
		t.Fatalf("sample %q ground truth is empty", id)
	}
	if len(sample.RelevantCtx) == 0 {
		t.Fatalf("sample %q relevant context is empty", id)
	}

	if got := sample.Meta["expected_terminated_by"]; got != wantTerminatedBy {
		t.Fatalf("sample %q expected_terminated_by = %#v, want %q", id, got, wantTerminatedBy)
	}
	if got := sample.Meta["expected_steps_taken"]; got != wantSteps {
		t.Fatalf("sample %q expected_steps_taken = %#v, want %v", id, got, wantSteps)
	}

	toolCalls, ok := sample.Meta["expected_tool_calls"].([]any)
	if !ok || len(toolCalls) == 0 {
		t.Fatalf("sample %q expected_tool_calls = %#v, want non-empty array", id, sample.Meta["expected_tool_calls"])
	}
	for index, rawCall := range toolCalls {
		call, ok := rawCall.(map[string]any)
		if !ok {
			t.Fatalf("sample %q tool call %d = %#v, want object", id, index, rawCall)
		}
		if call["name"] == "" {
			t.Fatalf("sample %q tool call %d name is empty", id, index)
		}
		if _, ok := call["arguments"].(map[string]any); !ok {
			t.Fatalf("sample %q tool call %d arguments = %#v, want object", id, index, call["arguments"])
		}
		if _, ok := call["result_is_error"].(bool); !ok {
			t.Fatalf("sample %q tool call %d result_is_error = %#v, want bool", id, index, call["result_is_error"])
		}
	}
}

func makeAgentGoldenTraceLinkSamples(t *testing.T, samples []aieval.Sample) []EvalTraceLinkSmokeSample {
	t.Helper()

	traceLinkSamples := make([]EvalTraceLinkSmokeSample, 0, len(samples))
	for _, sample := range samples {
		raw, ok := sample.Meta["expected_eval_trace_link"].(map[string]any)
		if !ok {
			t.Fatalf("sample %q expected_eval_trace_link = %#v, want object", sample.ID, sample.Meta["expected_eval_trace_link"])
		}

		traceLinkSamples = append(traceLinkSamples, EvalTraceLinkSmokeSample{
			SampleID:       sample.ID,
			RequestID:      stringMetaField(t, sample.ID, raw, "request_id"),
			AITraceID:      stringMetaField(t, sample.ID, raw, "ai_trace_id"),
			ServiceTraceID: stringMetaField(t, sample.ID, raw, "service_trace_id"),
			SpanID:         stringMetaField(t, sample.ID, raw, "span_id"),
			MetricName:     stringMetaField(t, sample.ID, raw, "metric_name"),
			Score:          floatMetaField(t, sample.ID, raw, "score"),
			Threshold:      floatMetaField(t, sample.ID, raw, "threshold"),
		})
	}
	return traceLinkSamples
}

func assertAgentGoldenTraceLinkCoverage(t *testing.T, samples []EvalTraceLinkSmokeSample, coverage map[string]bool) {
	t.Helper()

	for _, sample := range samples {
		switch sample.SampleID {
		case "agent-smoke-success-tool-loop":
			coverage["successful_tool_call_loop"] = true
		case "agent-smoke-tool-error-visible":
			coverage["tool_error"] = true
		case "agent-smoke-self-correction-after-tool-error":
			coverage["self_correction"] = true
		case "agent-smoke-max-steps-limit":
			coverage["max_steps"] = true
		}
	}

	for name, seen := range coverage {
		if !seen {
			t.Fatalf("agent golden eval trace link coverage %q = false, want true", name)
		}
	}
}

func stringMetaField(t *testing.T, sampleID string, values map[string]any, key string) string {
	t.Helper()

	value, ok := values[key].(string)
	if !ok || value == "" {
		t.Fatalf("sample %q expected_eval_trace_link.%s = %#v, want non-empty string", sampleID, key, values[key])
	}
	return value
}

func floatMetaField(t *testing.T, sampleID string, values map[string]any, key string) float64 {
	t.Helper()

	value, ok := values[key].(float64)
	if !ok {
		t.Fatalf("sample %q expected_eval_trace_link.%s = %#v, want number", sampleID, key, values[key])
	}
	return value
}
