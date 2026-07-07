package eval

import (
	"fmt"
	"strings"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/obs"
)

// RegressionStatus 表示单条 eval evidence 对回归门禁的判定结果。
//
// evidence 是评估体系里的“可追溯证据”：它把 dataset/sample/metric/score 与一次
// 真实请求的 request_id、AI trace、service trace 串起来。后续 CI 门禁、Langfuse
// score、离线报告和问题复盘都应依赖这份稳定证据，而不是只看汇总平均分。
type RegressionStatus string

const (
	RegressionStatusPassed  RegressionStatus = "passed"
	RegressionStatusWarning RegressionStatus = "warning"
	RegressionStatusFailed  RegressionStatus = "failed"
)

// EvaluationEvidenceInput 是构建单条评估证据的输入 DTO。
type EvaluationEvidenceInput struct {
	Identity   obs.CorrelationIdentity
	Dataset    DatasetIdentity
	SampleID   string
	MetricName string
	Score      float64
	Threshold  *float64
}

// EvaluationEvidence 是单个 sample + metric 的可回溯评估证据。
//
// v1 只保存低敏身份和分数，不保存 query、answer、context 原文；原文样本属于 dataset
// 或加密审计存储，普通 eval evidence 只负责把“哪个样本在哪次 trace 上得了多少分”
// 讲清楚。
type EvaluationEvidence struct {
	EvalRunID        string
	RequestID        string
	AITraceID        string
	ServiceTraceID   string
	SpanID           string
	Dataset          DatasetIdentity
	SampleID         string
	MetricName       string
	Score            float64
	Threshold        *float64
	RegressionStatus RegressionStatus
	FailureSummary   string
}

// NewEvaluationEvidence 校验并构造单条 eval evidence。
func NewEvaluationEvidence(input EvaluationEvidenceInput) (EvaluationEvidence, error) {
	normalized := normalizeEvaluationEvidenceInput(input)
	if err := validateEvaluationEvidenceInput(normalized); err != nil {
		return EvaluationEvidence{}, err
	}

	status, summary := classifyRegressionStatus(normalized.Score, normalized.Threshold)
	return EvaluationEvidence{
		EvalRunID:        normalized.Identity.EvalRunID,
		RequestID:        normalized.Identity.RequestID,
		AITraceID:        normalized.Identity.AITraceID,
		ServiceTraceID:   normalized.Identity.ServiceTraceID,
		SpanID:           normalized.Identity.SpanID,
		Dataset:          normalized.Dataset,
		SampleID:         normalized.SampleID,
		MetricName:       normalized.MetricName,
		Score:            normalized.Score,
		Threshold:        cloneEvidenceFloat64Pointer(normalized.Threshold),
		RegressionStatus: status,
		FailureSummary:   summary,
	}, nil
}

func normalizeEvaluationEvidenceInput(input EvaluationEvidenceInput) EvaluationEvidenceInput {
	normalized := input
	normalized.Dataset = normalizeDatasetIdentity(input.Dataset)
	normalized.SampleID = strings.TrimSpace(input.SampleID)
	normalized.MetricName = strings.TrimSpace(input.MetricName)
	normalized.Threshold = cloneEvidenceFloat64Pointer(input.Threshold)
	return normalized
}

func validateEvaluationEvidenceInput(input EvaluationEvidenceInput) error {
	if strings.TrimSpace(input.Identity.EvalRunID) == "" {
		return fmt.Errorf("evaluation evidence eval_run_id is required")
	}
	if strings.TrimSpace(input.Identity.RequestID) == "" {
		return fmt.Errorf("evaluation evidence request_id is required")
	}
	if strings.TrimSpace(input.Identity.AITraceID) == "" {
		return fmt.Errorf("evaluation evidence ai_trace_id is required")
	}
	if strings.TrimSpace(input.Identity.ServiceTraceID) == "" {
		return fmt.Errorf("evaluation evidence service_trace_id is required")
	}
	if strings.TrimSpace(input.Identity.SpanID) == "" {
		return fmt.Errorf("evaluation evidence span_id is required")
	}
	if err := validateDatasetIdentity(input.Dataset); err != nil {
		return fmt.Errorf("evaluation evidence dataset identity: %w", err)
	}
	if input.SampleID == "" {
		return fmt.Errorf("evaluation evidence sample_id is required")
	}
	if input.MetricName == "" {
		return fmt.Errorf("evaluation evidence metric_name is required")
	}
	if !isUnitInterval(input.Score) {
		return fmt.Errorf("evaluation evidence score must be within [0,1]")
	}
	if input.Threshold != nil && !isUnitInterval(*input.Threshold) {
		return fmt.Errorf("evaluation evidence threshold must be within [0,1]")
	}
	return nil
}

func classifyRegressionStatus(score float64, threshold *float64) (RegressionStatus, string) {
	if threshold == nil {
		return RegressionStatusWarning, ""
	}
	if score >= *threshold {
		return RegressionStatusPassed, ""
	}
	return RegressionStatusFailed, fmt.Sprintf("score %.2f is below threshold %.2f", score, *threshold)
}

func isUnitInterval(value float64) bool {
	return value >= 0 && value <= 1
}

func cloneEvidenceFloat64Pointer(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
