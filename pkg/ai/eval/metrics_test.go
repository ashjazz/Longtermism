package eval

import (
	"context"
	"strings"
	"testing"
)

func TestExactMatchMetric(t *testing.T) {
	t.Parallel()

	metric := NewExactMatchMetric()
	if metric.Name() != "exact_match" {
		t.Fatalf("Name() = %q, want exact_match", metric.Name())
	}

	tests := []struct {
		name       string
		sample     Sample
		prediction Prediction
		wantScore  float64
		wantErr    string
	}{
		{
			name:       "happy path 标准答案完全一致时得满分",
			sample:     Sample{ID: "exact-001", GroundTruth: "prompt -> llm -> obs -> eval"},
			prediction: Prediction{Answer: "prompt -> llm -> obs -> eval"},
			wantScore:  1,
		},
		{
			name:       "boundary 前后空白不应影响完全匹配",
			sample:     Sample{ID: "exact-002", GroundTruth: "P0 最小闭环"},
			prediction: Prediction{Answer: "  P0 最小闭环\n"},
			wantScore:  1,
		},
		{
			name:       "boundary 内容不一致时得零分",
			sample:     Sample{ID: "exact-003", GroundTruth: "expected"},
			prediction: Prediction{Answer: "actual"},
			wantScore:  0,
		},
		{
			name:       "error 缺少标准答案无法计算精确匹配",
			sample:     Sample{ID: "exact-missing-ground-truth"},
			prediction: Prediction{Answer: "anything"},
			wantErr:    "ground truth",
		},
		{
			name:       "error 缺少模型输出无法计算精确匹配",
			sample:     Sample{ID: "exact-missing-answer", GroundTruth: "expected"},
			prediction: Prediction{},
			wantErr:    "answer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, err := metric.Score(context.Background(), tt.sample, tt.prediction)
			assertMetricResult(t, score, err, tt.wantScore, tt.wantErr)
		})
	}
}

func TestContainsAllMetric(t *testing.T) {
	t.Parallel()

	metric := NewContainsAllMetric("prompt", "llm", "eval")
	if metric.Name() != "contains_all" {
		t.Fatalf("Name() = %q, want contains_all", metric.Name())
	}

	tests := []struct {
		name       string
		prediction Prediction
		wantScore  float64
		wantErr    string
	}{
		{
			name:       "happy path 输出包含全部关键词时得满分",
			prediction: Prediction{Answer: "P0 会串起 prompt、LLM、obs 和 eval。"},
			wantScore:  1,
		},
		{
			name:       "boundary 大小写不影响关键词覆盖",
			prediction: Prediction{Answer: "PROMPT calls LLM, then EVAL checks the result."},
			wantScore:  1,
		},
		{
			name:       "boundary 缺少任一关键词时得零分",
			prediction: Prediction{Answer: "P0 会串起 prompt 和 eval。"},
			wantScore:  0,
		},
		{
			name:       "error 缺少模型输出无法计算覆盖率",
			prediction: Prediction{},
			wantErr:    "answer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, err := metric.Score(context.Background(), Sample{ID: "contains-all"}, tt.prediction)
			assertMetricResult(t, score, err, tt.wantScore, tt.wantErr)
		})
	}
}

func TestContainsAllMetricRejectsEmptyKeywords(t *testing.T) {
	t.Parallel()

	metric := NewContainsAllMetric()

	_, err := metric.Score(context.Background(), Sample{ID: "contains-empty-keywords"}, Prediction{Answer: "anything"})
	if err == nil {
		t.Fatal("Score() error = nil, want empty keyword error")
	}
	if !strings.Contains(err.Error(), "keywords") {
		t.Fatalf("Score() error = %v, want mention keywords", err)
	}
}

func TestContextHitMetric(t *testing.T) {
	t.Parallel()

	metric := NewContextHitMetric()
	if metric.Name() != "context_hit" {
		t.Fatalf("Name() = %q, want context_hit", metric.Name())
	}

	tests := []struct {
		name       string
		sample     Sample
		prediction Prediction
		wantScore  float64
		wantErr    string
	}{
		{
			name: "happy path 命中全部相关上下文时得满分",
			sample: Sample{
				ID:          "context-001",
				RelevantCtx: []string{"P0 目标是最小闭环", "评估必须可回归"},
			},
			prediction: Prediction{Context: []string{"P0 目标是最小闭环", "评估必须可回归"}},
			wantScore:  1,
		},
		{
			name: "boundary 只命中一半相关上下文时返回比例分",
			sample: Sample{
				ID:          "context-002",
				RelevantCtx: []string{"prompt hash", "trace id"},
			},
			prediction: Prediction{Context: []string{"prompt hash"}},
			wantScore:  0.5,
		},
		{
			name: "boundary 未命中相关上下文时得零分",
			sample: Sample{
				ID:          "context-003",
				RelevantCtx: []string{"golden case"},
			},
			prediction: Prediction{Context: []string{"unrelated"}},
			wantScore:  0,
		},
		{
			name:       "error 样例缺少相关上下文无法计算 context hit",
			sample:     Sample{ID: "context-missing-relevant"},
			prediction: Prediction{Context: []string{"anything"}},
			wantErr:    "relevant context",
		},
		{
			name:       "error 预测缺少检索上下文无法计算 context hit",
			sample:     Sample{ID: "context-missing-prediction", RelevantCtx: []string{"expected"}},
			prediction: Prediction{},
			wantErr:    "prediction context",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, err := metric.Score(context.Background(), tt.sample, tt.prediction)
			assertMetricResult(t, score, err, tt.wantScore, tt.wantErr)
		})
	}
}

func assertMetricResult(t *testing.T, gotScore float64, err error, wantScore float64, wantErr string) {
	t.Helper()

	if wantErr != "" {
		if err == nil {
			t.Fatalf("Score() error = nil, want containing %q", wantErr)
		}
		if !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("Score() error = %v, want containing %q", err, wantErr)
		}
		return
	}

	if err != nil {
		t.Fatalf("Score() error = %v", err)
	}
	if gotScore != wantScore {
		t.Fatalf("Score() = %v, want %v", gotScore, wantScore)
	}
	if gotScore < 0 || gotScore > 1 {
		t.Fatalf("Score() = %v, want within [0,1]", gotScore)
	}
}
