package eval

import (
	"context"
	"fmt"
	"strings"
)

const (
	exactMatchMetricName = "exact_match"
	containsAllName      = "contains_all"
	contextHitName       = "context_hit"
)

// ExactMatchMetric 是最严格、也最容易解释的确定性指标。
//
// 它适合 P0 smoke 中那些答案应该完全稳定的样例，例如“框架闭环包含哪些阶段”。
// 对开放问答它会过于苛刻，后续会补 ContainsAll、ContextHit 和 LLM-as-Judge 分层使用。
type ExactMatchMetric struct{}

func NewExactMatchMetric() ExactMatchMetric {
	return ExactMatchMetric{}
}

func (ExactMatchMetric) Name() string {
	return exactMatchMetricName
}

func (ExactMatchMetric) Score(ctx context.Context, sample Sample, prediction Prediction) (float64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	groundTruth := strings.TrimSpace(sample.GroundTruth)
	if groundTruth == "" {
		return 0, fmt.Errorf("exact_match sample %q ground truth is required", sample.ID)
	}
	answer := strings.TrimSpace(prediction.Answer)
	if answer == "" {
		return 0, fmt.Errorf("exact_match sample %q answer is required", sample.ID)
	}
	if answer == groundTruth {
		return 1, nil
	}
	return 0, nil
}

// ContainsAllMetric 检查回答是否覆盖一组关键词。
//
// 这是比 exact match 更宽松的 P0 指标：它不关心措辞顺序，但要求关键事实全部出现。
type ContainsAllMetric struct {
	keywords []string
}

func NewContainsAllMetric(keywords ...string) ContainsAllMetric {
	return ContainsAllMetric{keywords: normalizeKeywords(keywords)}
}

func (ContainsAllMetric) Name() string {
	return containsAllName
}

func (m ContainsAllMetric) Score(ctx context.Context, sample Sample, prediction Prediction) (float64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(m.keywords) == 0 {
		return 0, fmt.Errorf("contains_all keywords are required")
	}

	answer := strings.TrimSpace(prediction.Answer)
	if answer == "" {
		return 0, fmt.Errorf("contains_all sample %q answer is required", sample.ID)
	}
	lowerAnswer := strings.ToLower(answer)
	for _, keyword := range m.keywords {
		if !strings.Contains(lowerAnswer, keyword) {
			return 0, nil
		}
	}
	return 1, nil
}

func normalizeKeywords(keywords []string) []string {
	normalized := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		trimmed := strings.TrimSpace(keyword)
		if trimmed == "" {
			continue
		}
		normalized = append(normalized, strings.ToLower(trimmed))
	}
	return normalized
}

// ContextHitMetric 计算预测检索上下文对 golden relevant context 的命中率。
//
// 分数 = 命中的相关上下文数量 / golden relevant context 数量。它不判断生成答案好坏，
// 只回答“检索是否把应该出现的证据拿回来了”，后续 RAG 评估会大量复用这个思路。
type ContextHitMetric struct{}

func NewContextHitMetric() ContextHitMetric {
	return ContextHitMetric{}
}

func (ContextHitMetric) Name() string {
	return contextHitName
}

func (ContextHitMetric) Score(ctx context.Context, sample Sample, prediction Prediction) (float64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(sample.RelevantCtx) == 0 {
		return 0, fmt.Errorf("context_hit sample %q relevant context is required", sample.ID)
	}
	if len(prediction.Context) == 0 {
		return 0, fmt.Errorf("context_hit sample %q prediction context is required", sample.ID)
	}

	predicted := make(map[string]struct{}, len(prediction.Context))
	for _, item := range prediction.Context {
		normalized := normalizeContext(item)
		if normalized == "" {
			continue
		}
		predicted[normalized] = struct{}{}
	}

	hits := 0
	for _, relevant := range sample.RelevantCtx {
		if _, ok := predicted[normalizeContext(relevant)]; ok {
			hits++
		}
	}
	return clamp01(float64(hits) / float64(len(sample.RelevantCtx))), nil
}

func normalizeContext(value string) string {
	return strings.TrimSpace(value)
}

func clamp01(score float64) float64 {
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}
