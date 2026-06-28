package eval

import (
	"math"
	"testing"
)

func TestRetrievalMetricsAtK(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		rankedIDs   []string
		relevantIDs []string
		k           int
		want        RetrievalScores
	}{
		{
			name:        "happy path 相关文档全部位于理想排序前缀时三个指标都得满分",
			rankedIDs:   []string{"doc-a", "doc-c", "doc-b"},
			relevantIDs: []string{"doc-a", "doc-c"},
			k:           3,
			want: RetrievalScores{
				RecallAtK: 1,
				MRR:       1,
				NDCGAtK:   1,
			},
		},
		{
			name:        "happy path 只命中一个相关文档时返回可解释的比例和排序分",
			rankedIDs:   []string{"noise", "doc-b", "noise-2"},
			relevantIDs: []string{"doc-a", "doc-b"},
			k:           3,
			want: RetrievalScores{
				RecallAtK: 0.5,
				MRR:       0.5,
				NDCGAtK:   0.38685280723454163,
			},
		},
		{
			name:        "boundary k 会截断排序列表并只评估前 k 个结果",
			rankedIDs:   []string{"doc-a", "doc-b"},
			relevantIDs: []string{"doc-a", "doc-b"},
			k:           1,
			want: RetrievalScores{
				RecallAtK: 0.5,
				MRR:       1,
				NDCGAtK:   1,
			},
		},
		{
			name:        "boundary 空检索结果在存在相关集时三个指标都稳定返回零分",
			rankedIDs:   nil,
			relevantIDs: []string{"doc-a"},
			k:           3,
			want:        RetrievalScores{},
		},
		{
			name:        "boundary 空相关集没有可验证目标时三个指标都稳定返回零分",
			rankedIDs:   []string{"doc-a"},
			relevantIDs: nil,
			k:           3,
			want:        RetrievalScores{},
		},
		{
			name:        "boundary 非正 k 不进入排序计算并稳定返回零分",
			rankedIDs:   []string{"doc-a"},
			relevantIDs: []string{"doc-a"},
			k:           0,
			want:        RetrievalScores{},
		},
		{
			name:        "boundary 重复命中文档不得重复抬高 recall 或 ndcg",
			rankedIDs:   []string{"doc-a", "doc-a", "doc-b"},
			relevantIDs: []string{"doc-a", "doc-b"},
			k:           3,
			want: RetrievalScores{
				RecallAtK: 1,
				MRR:       1,
				NDCGAtK:   0.9197207891481876,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RetrievalMetricsAtK(tt.rankedIDs, tt.relevantIDs, tt.k)

			assertRetrievalScores(t, got, tt.want)
		})
	}
}

func assertRetrievalScores(t *testing.T, got RetrievalScores, want RetrievalScores) {
	t.Helper()

	// 检索指标会进入 CI 门禁和趋势图，所以测试既校验具体数值，也校验分数始终落在 [0,1]。
	assertScoreWithinRange(t, "RecallAtK", got.RecallAtK)
	assertScoreWithinRange(t, "MRR", got.MRR)
	assertScoreWithinRange(t, "NDCGAtK", got.NDCGAtK)
	assertAlmostEqual(t, "RecallAtK", got.RecallAtK, want.RecallAtK)
	assertAlmostEqual(t, "MRR", got.MRR, want.MRR)
	assertAlmostEqual(t, "NDCGAtK", got.NDCGAtK, want.NDCGAtK)
}

func assertScoreWithinRange(t *testing.T, name string, got float64) {
	t.Helper()

	if got < 0 || got > 1 {
		t.Fatalf("%s = %v, want within [0,1]", name, got)
	}
}

func assertAlmostEqual(t *testing.T, name string, got float64, want float64) {
	t.Helper()

	const tolerance = 1e-12
	if math.Abs(got-want) > tolerance {
		t.Fatalf("%s = %.15f, want %.15f", name, got, want)
	}
}
