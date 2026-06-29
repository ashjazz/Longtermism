package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// DatasetFactory 为契约测试创建一个全新的 Dataset。
//
// 每个子测试都拿独立 dataset，避免平台同步、本地文件或内存 fake 的内部缓存互相影响。
// 未来 LangFuse、对象存储或评估平台 adapter 只需要提供同样的 factory，就能复用这套契约。
type DatasetFactory func(t *testing.T) Dataset

func TestJSONDatasetContract(t *testing.T) {
	RunDatasetContract(t, func(t *testing.T) Dataset {
		t.Helper()

		return NewJSONDataset(writeContractDatasetJSON(t, `{
			"samples": [
				{
					"id": "contract-001",
					"query": "如何证明 AI 能力没有退化？",
					"groundTruth": "运行 golden dataset 并比较受保护指标",
					"relevantCtx": ["评估报告包含 dataset version、sample count 和 metric scores"],
					"difficulty": "moderate",
					"category": "eval",
					"meta": {
						"source": "contract",
						"tags": ["eval", "gate"],
						"nested": {"stage": "US5"}
					}
				},
				{
					"id": "contract-002",
					"query": "为什么普通 trace 不记录原始 prompt？",
					"groundTruth": "避免敏感内容进入普通观测链路",
					"relevantCtx": ["普通 trace 只记录 hash、长度、模型和状态"],
					"difficulty": "simple",
					"category": "obs",
					"meta": {"source": "contract"}
				}
			]
		}`))
	})
}

// RunDatasetContract 验证所有 Dataset 实现必须保持的核心语义。
//
// Dataset 是 eval runner 的输入边界。无论样例来自本地 JSON、对象存储还是未来评估平台，
// runner 都需要看到稳定 ID、完整字段、可重复加载和不会被调用方污染的样例副本。
func RunDatasetContract(t *testing.T, newDataset DatasetFactory) {
	t.Helper()

	t.Run("loads stable samples with full evaluation fields", func(t *testing.T) {
		dataset := newDataset(t)

		samples, err := dataset.Load(context.Background())
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if len(samples) != 2 {
			t.Fatalf("sample count = %d, want 2", len(samples))
		}

		first := samples[0]
		if first.ID != "contract-001" {
			t.Fatalf("first ID = %q, want contract-001", first.ID)
		}
		if first.Query != "如何证明 AI 能力没有退化？" {
			t.Fatalf("first Query = %q", first.Query)
		}
		if first.GroundTruth != "运行 golden dataset 并比较受保护指标" {
			t.Fatalf("first GroundTruth = %q", first.GroundTruth)
		}
		if len(first.RelevantCtx) != 1 || first.RelevantCtx[0] != "评估报告包含 dataset version、sample count 和 metric scores" {
			t.Fatalf("first RelevantCtx = %#v", first.RelevantCtx)
		}
		if first.Difficulty != "moderate" || first.Category != "eval" {
			t.Fatalf("first Difficulty/Category = %q/%q, want moderate/eval", first.Difficulty, first.Category)
		}
		if first.Meta["source"] != "contract" {
			t.Fatalf("first Meta[source] = %#v, want contract", first.Meta["source"])
		}

		second := samples[1]
		if second.ID != "contract-002" || second.Category != "obs" {
			t.Fatalf("second sample = %#v, want contract-002 obs sample", second)
		}
	})

	t.Run("returns defensive copies on repeated loads", func(t *testing.T) {
		dataset := newDataset(t)

		firstLoad, err := dataset.Load(context.Background())
		if err != nil {
			t.Fatalf("first Load() error = %v", err)
		}
		firstLoad[0].ID = "mutated"
		firstLoad[0].RelevantCtx[0] = "mutated context"
		firstLoad[0].Meta["source"] = "mutated"
		firstLoad[0].Meta["tags"].([]any)[0] = "mutated"
		firstLoad[0].Meta["nested"].(map[string]any)["stage"] = "mutated"

		secondLoad, err := dataset.Load(context.Background())
		if err != nil {
			t.Fatalf("second Load() error = %v", err)
		}
		if secondLoad[0].ID != "contract-001" {
			t.Fatalf("ID = %q, want contract-001", secondLoad[0].ID)
		}
		if secondLoad[0].RelevantCtx[0] != "评估报告包含 dataset version、sample count 和 metric scores" {
			t.Fatalf("RelevantCtx[0] = %q, want original context", secondLoad[0].RelevantCtx[0])
		}
		if secondLoad[0].Meta["source"] != "contract" {
			t.Fatalf("Meta[source] = %#v, want contract", secondLoad[0].Meta["source"])
		}
		if secondLoad[0].Meta["tags"].([]any)[0] != "eval" {
			t.Fatalf("Meta[tags][0] = %#v, want eval", secondLoad[0].Meta["tags"].([]any)[0])
		}
		if secondLoad[0].Meta["nested"].(map[string]any)["stage"] != "US5" {
			t.Fatalf("Meta[nested][stage] = %#v, want US5", secondLoad[0].Meta["nested"].(map[string]any)["stage"])
		}
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		dataset := newDataset(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		samples, err := dataset.Load(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Load() error = %v, want context.Canceled", err)
		}
		if samples != nil {
			t.Fatalf("Load() samples = %#v, want nil on canceled context", samples)
		}
	})
}

func writeContractDatasetJSON(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "dataset_contract.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write dataset contract fixture: %v", err)
	}
	return path
}
