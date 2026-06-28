package eval

import (
	"math"
	"strings"
)

// RetrievalScores 汇总一次检索排序的核心离线指标。
//
// 这三个指标分别回答不同问题：
//   - Recall@k：前 k 个结果有没有“找全”相关文档，适合看召回能力。
//   - MRR：第一个相关文档出现得有多早，适合看用户第一眼是否能看到证据。
//   - NDCG@k：相关文档的整体排序质量，适合比较 rerank、混合检索等策略。
//
// 分数字段统一保持 [0,1]，便于后续进入 eval report、CI 门禁和趋势图。
type RetrievalScores struct {
	RecallAtK float64
	MRR       float64
	NDCGAtK   float64
}

// RetrievalMetricsAtK 计算 rankedIDs 相对 relevantIDs 的 Recall@k、MRR 和 NDCG@k。
//
// P2 先采用 binary relevance：只区分“相关/不相关”，不引入 graded relevance。
// 这是为了让最初的 golden case 更容易人工审查；未来如果评估集需要标注强相关/弱相关，
// 可以在不改变 retriever 的前提下新增 graded NDCG 指标。
//
// 空结果、空相关集或非正 k 都返回零值分数而不是错误：
//   - 空结果表示检索没有拿回任何证据，召回和排序质量都是 0。
//   - 空相关集表示样例缺少可验证目标，当前指标无法证明检索有效，因此保守记 0。
//   - 非正 k 表示没有评估窗口，同样保守记 0。
func RetrievalMetricsAtK(rankedIDs []string, relevantIDs []string, k int) RetrievalScores {
	relevant := buildRelevantSet(relevantIDs)
	if len(rankedIDs) == 0 || len(relevant) == 0 || k <= 0 {
		return RetrievalScores{}
	}

	limit := min(k, len(rankedIDs))
	hits := 0
	firstRelevantRank := 0
	dcg := 0.0
	seenRanked := make(map[string]struct{}, limit)

	for index := 0; index < limit; index++ {
		id := normalizeRetrievalID(rankedIDs[index])
		if id == "" {
			continue
		}
		if _, seen := seenRanked[id]; seen {
			continue
		}
		seenRanked[id] = struct{}{}

		if _, ok := relevant[id]; !ok {
			continue
		}

		hits++
		rank := index + 1
		if firstRelevantRank == 0 {
			firstRelevantRank = rank
		}
		dcg += reciprocalLog2Discount(rank)
	}

	return RetrievalScores{
		RecallAtK: clamp01(float64(hits) / float64(len(relevant))),
		MRR:       reciprocalRank(firstRelevantRank),
		NDCGAtK:   normalizedDiscountedGain(dcg, idealRelevantCount(len(relevant), limit)),
	}
}

func buildRelevantSet(relevantIDs []string) map[string]struct{} {
	relevant := make(map[string]struct{}, len(relevantIDs))
	for _, rawID := range relevantIDs {
		id := normalizeRetrievalID(rawID)
		if id == "" {
			continue
		}
		relevant[id] = struct{}{}
	}
	return relevant
}

func normalizeRetrievalID(id string) string {
	return strings.TrimSpace(id)
}

func reciprocalRank(rank int) float64 {
	if rank <= 0 {
		return 0
	}
	return 1 / float64(rank)
}

func normalizedDiscountedGain(dcg float64, idealHits int) float64 {
	if idealHits <= 0 {
		return 0
	}

	idcg := 0.0
	for rank := 1; rank <= idealHits; rank++ {
		idcg += reciprocalLog2Discount(rank)
	}
	if idcg == 0 {
		return 0
	}
	return clamp01(dcg / idcg)
}

func reciprocalLog2Discount(rank int) float64 {
	if rank <= 0 {
		return 0
	}
	return 1 / math.Log2(float64(rank)+1)
}

func idealRelevantCount(relevantCount int, limit int) int {
	if relevantCount < limit {
		return relevantCount
	}
	return limit
}
