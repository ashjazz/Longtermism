package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONDatasetLoadReturnsSamples(t *testing.T) {
	t.Parallel()

	path := writeDatasetJSON(t, `{
		"samples": [
			{
				"id": "p0-smoke-001",
				"query": "P0 最小 AI 工程闭环包含哪些环节？",
				"groundTruth": "prompt -> llm -> obs -> eval",
				"relevantCtx": ["P0 默认验证 prompt、模型交互、trace 和 eval runner"],
				"difficulty": "simple",
				"category": "p0",
				"meta": {
					"source": "unit-test",
					"tags": ["smoke", "p0"],
					"nested": {"stage": "P0-D"}
				}
			}
		]
	}`)
	dataset := NewJSONDataset(path)

	samples, err := dataset.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("Load() returned %d samples, want 1", len(samples))
	}

	got := samples[0]
	if got.ID != "p0-smoke-001" {
		t.Fatalf("ID = %q, want p0-smoke-001", got.ID)
	}
	if got.Query != "P0 最小 AI 工程闭环包含哪些环节？" {
		t.Fatalf("Query = %q", got.Query)
	}
	if got.GroundTruth != "prompt -> llm -> obs -> eval" {
		t.Fatalf("GroundTruth = %q", got.GroundTruth)
	}
	if len(got.RelevantCtx) != 1 || got.RelevantCtx[0] != "P0 默认验证 prompt、模型交互、trace 和 eval runner" {
		t.Fatalf("RelevantCtx = %#v", got.RelevantCtx)
	}
	if got.Difficulty != "simple" || got.Category != "p0" {
		t.Fatalf("Difficulty/Category = %q/%q, want simple/p0", got.Difficulty, got.Category)
	}
	if got.Meta["source"] != "unit-test" {
		t.Fatalf("Meta[source] = %#v, want unit-test", got.Meta["source"])
	}
}

func TestJSONDatasetLoadRejectsEmptyFile(t *testing.T) {
	t.Parallel()

	path := writeDatasetJSON(t, "")
	dataset := NewJSONDataset(path)

	_, err := dataset.Load(context.Background())
	if err == nil {
		t.Fatal("Load() error = nil, want empty dataset file error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("Load() error = %v, want mention empty file", err)
	}
}

func TestJSONDatasetLoadRejectsInvalidJSON(t *testing.T) {
	t.Parallel()

	path := writeDatasetJSON(t, `{"samples": [`)
	dataset := NewJSONDataset(path)

	_, err := dataset.Load(context.Background())
	if err == nil {
		t.Fatal("Load() error = nil, want invalid JSON error")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Fatalf("Load() error = %v, want parse context", err)
	}
}

func TestJSONDatasetLoadRejectsMissingSampleID(t *testing.T) {
	t.Parallel()

	path := writeDatasetJSON(t, `{
		"samples": [
			{
				"query": "缺少 ID 的样例不应进入评估",
				"groundTruth": "must fail fast"
			}
		]
	}`)
	dataset := NewJSONDataset(path)

	_, err := dataset.Load(context.Background())
	if err == nil {
		t.Fatal("Load() error = nil, want missing sample id error")
	}
	if !strings.Contains(err.Error(), "sample id") {
		t.Fatalf("Load() error = %v, want mention sample id", err)
	}
}

func TestJSONDatasetLoadReturnsImmutableSampleCopies(t *testing.T) {
	t.Parallel()

	path := writeDatasetJSON(t, `{
		"samples": [
			{
				"id": "copy-001",
				"query": "返回副本可以避免什么问题？",
				"relevantCtx": ["original context"],
				"meta": {
					"source": "original",
					"nested": {"stage": "original"},
					"tags": ["original"]
				}
			}
		]
	}`)
	dataset := NewJSONDataset(path)

	first, err := dataset.Load(context.Background())
	if err != nil {
		t.Fatalf("first Load() error = %v", err)
	}
	first[0].ID = "mutated"
	first[0].RelevantCtx[0] = "mutated context"
	first[0].Meta["source"] = "mutated"
	first[0].Meta["nested"].(map[string]any)["stage"] = "mutated"
	first[0].Meta["tags"].([]any)[0] = "mutated"

	second, err := dataset.Load(context.Background())
	if err != nil {
		t.Fatalf("second Load() error = %v", err)
	}
	if second[0].ID != "copy-001" {
		t.Fatalf("ID = %q, want original copy-001", second[0].ID)
	}
	if second[0].RelevantCtx[0] != "original context" {
		t.Fatalf("RelevantCtx[0] = %q, want original context", second[0].RelevantCtx[0])
	}
	if second[0].Meta["source"] != "original" {
		t.Fatalf("Meta[source] = %#v, want original", second[0].Meta["source"])
	}
	if second[0].Meta["nested"].(map[string]any)["stage"] != "original" {
		t.Fatalf("Meta[nested][stage] = %#v, want original", second[0].Meta["nested"].(map[string]any)["stage"])
	}
	if second[0].Meta["tags"].([]any)[0] != "original" {
		t.Fatalf("Meta[tags][0] = %#v, want original", second[0].Meta["tags"].([]any)[0])
	}
}

func writeDatasetJSON(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "dataset.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write dataset fixture: %v", err)
	}
	return path
}
