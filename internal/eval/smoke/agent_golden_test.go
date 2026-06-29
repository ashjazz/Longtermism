package smoke

import (
	"context"
	"path/filepath"
	"testing"

	aieval "github.com/jazzash/ashjazz-aiagent/pkg/ai/eval"
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
