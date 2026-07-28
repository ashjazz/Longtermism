package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/llm"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

const (
	completionContractEvaluatorName = "completion_contract_v1"
	maxDebugEvalSummaryBytes        = 1024
	maxEvaluationFactBytes          = 256
)

var (
	safeEvaluationFactPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

	// ErrEvaluatorInvalidContext 保持 context 契约错误为稳定低敏分类，不把输入事实写入错误。
	ErrEvaluatorInvalidContext = errors.New("chat evaluator: context is required")

	errEvaluatorInvalidConfig = errors.New("chat evaluator: invalid configuration")
	errEvaluatorInvalidInput  = errors.New("chat evaluator: invalid low-sensitivity input")
)

// EvalStatus 是应用层的评估结果分类。它独立于 HTTP DTO 和平台 schema，避免 transport
// 或 Langfuse 反向决定本地评估事实。
type EvalStatus string

const (
	EvalStatusPassed  EvalStatus = "passed"
	EvalStatusWarning EvalStatus = "warning"
	EvalStatusFailed  EvalStatus = "failed"
	EvalStatusNotRun  EvalStatus = "not_run"
)

// CompletionContractEvaluationInput 只包含完成后可安全评估的低敏事实。原始 message、
// prompt、模型输出和 provider body 不属于本地 contract metric 的输入，从类型边界
// 阻止 debug/evidence 泄漏。
type CompletionContractEvaluationInput struct {
	Identity      obs.CorrelationIdentity
	ActualModel   string
	FinishReason  llm.FinishReason
	Usage         llm.Usage
	OutputPresent bool
}

// DebugEvalSummary 是可选响应诊断。字段均为固定枚举或有界分数；debug 只决定是否
// 暴露这份摘要，不改变 evaluator 的执行、evidence 或 payload policy。
type DebugEvalSummary struct {
	Status      EvalStatus `json:"status"`
	Evaluator   string     `json:"evaluator,omitempty"`
	Score       *float64   `json:"score,omitempty"`
	ReasonClass string     `json:"reason_class,omitempty"`
}

// CompletionContractEvaluationResult 同时携带 contract evidence 与可选诊断来源。
// 两个字段都明确禁止 JSON 序列化，防止调用方绕过显式 debug gate。
type CompletionContractEvaluationResult struct {
	Summary  DebugEvalSummary           `json:"-"`
	Evidence *aieval.EvaluationEvidence `json:"-"`
}

// Evaluator 是评估实现共享的泛型端口。每类 evaluator 同时拥有自己的输入和结果
// 类型，避免语义评估把 raw answer/context 混入 completion contract 的低敏对象，
// 也避免它的连续分数和诊断策略被当前 binary contract 规则错误过滤。
type Evaluator[Input, Result any] interface {
	Evaluate(context.Context, Input) (Result, error)
}

// CompletionContractEvaluatorConfig 固定首阶段 completion contract 的 evidence 身份。
type CompletionContractEvaluatorConfig struct {
	Dataset    aieval.DatasetIdentity
	SampleID   string
	MetricName string
	Threshold  *float64
}

// CompletionContractEvaluator 评估模型调用是否产出了结构完整、自洽的完成事实。
//
// 它不是回答正确性、相关性或忠实度 evaluator；这些语义指标应由后续 dataset 或
// LLM-as-Judge evaluator 实现同一个 Evaluator 端口，不能混进当前低敏 contract check。
type CompletionContractEvaluator struct {
	config CompletionContractEvaluatorConfig
}

var _ Evaluator[CompletionContractEvaluationInput, CompletionContractEvaluationResult] = (*CompletionContractEvaluator)(nil)

// NewCompletionContractEvaluator 校验并复制配置。binary metric 的阈值必须大于零，否则失败
// 分数 0 会被通用回归规则错误判为 passed；nil 则显式表达“尚未配置门槛”。
func NewCompletionContractEvaluator(config CompletionContractEvaluatorConfig) (*CompletionContractEvaluator, error) {
	normalized := normalizeCompletionContractEvaluatorConfig(config)
	if err := validateCompletionContractEvaluatorConfig(normalized); err != nil {
		return nil, err
	}
	return &CompletionContractEvaluator{config: normalized}, nil
}

// Evaluate 对已完成的低敏事实执行确定性 contract check，并始终通过核心 eval 包生成
// 本地 evidence。身份缺失会 fail fast，绝不猜测 eval run 或 trace identity。
func (evaluator *CompletionContractEvaluator) Evaluate(ctx context.Context, input CompletionContractEvaluationInput) (CompletionContractEvaluationResult, error) {
	if ctx == nil {
		return CompletionContractEvaluationResult{}, ErrEvaluatorInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return CompletionContractEvaluationResult{}, err
	}
	if evaluator == nil {
		return CompletionContractEvaluationResult{}, errEvaluatorInvalidConfig
	}
	if !isSafeEvaluationIdentity(input.Identity) {
		return CompletionContractEvaluationResult{}, errEvaluatorInvalidInput
	}

	score, reasonClass := classifyCompletionFacts(input)
	evidence, err := aieval.NewEvaluationEvidence(aieval.EvaluationEvidenceInput{
		Identity:   input.Identity,
		Dataset:    evaluator.config.Dataset,
		SampleID:   evaluator.config.SampleID,
		MetricName: evaluator.config.MetricName,
		Score:      score,
		Threshold:  cloneEvalScore(evaluator.config.Threshold),
	})
	if err != nil {
		return CompletionContractEvaluationResult{}, fmt.Errorf("chat evaluator: build evidence: %w", err)
	}

	status := mapRegressionStatus(evidence.RegressionStatus)
	if score == 1 {
		if evidence.Threshold == nil {
			reasonClass = "threshold_not_configured"
		} else {
			reasonClass = "within_policy"
		}
	}
	return CompletionContractEvaluationResult{
		Summary: DebugEvalSummary{
			Status:      status,
			Evaluator:   completionContractEvaluatorName,
			Score:       cloneEvalScore(&score),
			ReasonClass: reasonClass,
		},
		Evidence: &evidence,
	}, nil
}

// CompletionContractNotRunEvaluator 明确表示 completion contract evaluator 未装配。
// 它不创建 synthetic score 或 evidence；其它 evaluator 应定义自己的 not-run 结果。
type CompletionContractNotRunEvaluator struct{}

var _ Evaluator[CompletionContractEvaluationInput, CompletionContractEvaluationResult] = (*CompletionContractNotRunEvaluator)(nil)

func NewCompletionContractNotRunEvaluator() *CompletionContractNotRunEvaluator {
	return &CompletionContractNotRunEvaluator{}
}

func (*CompletionContractNotRunEvaluator) Evaluate(ctx context.Context, _ CompletionContractEvaluationInput) (CompletionContractEvaluationResult, error) {
	if ctx == nil {
		return CompletionContractEvaluationResult{}, ErrEvaluatorInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return CompletionContractEvaluationResult{}, err
	}
	return CompletionContractEvaluationResult{Summary: DebugEvalSummary{
		Status:      EvalStatusNotRun,
		ReasonClass: "evaluator_not_configured",
	}}, nil
}

// ExposeDebugEvalSummary 只执行响应暴露决策。非法或超限摘要 fail closed；不截断字段，
// 因为截断可能把固定原因类改写成不存在的业务语义。
func ExposeDebugEvalSummary(result CompletionContractEvaluationResult, debug bool) *DebugEvalSummary {
	if !debug {
		return nil
	}
	summary := DebugEvalSummary{
		Status:      result.Summary.Status,
		Evaluator:   result.Summary.Evaluator,
		Score:       cloneEvalScore(result.Summary.Score),
		ReasonClass: result.Summary.ReasonClass,
	}
	if !isAllowedDebugEvalSummary(summary) {
		return nil
	}
	serialized, err := json.Marshal(summary)
	if err != nil || len(serialized) > maxDebugEvalSummaryBytes {
		return nil
	}
	return &summary
}

func normalizeCompletionContractEvaluatorConfig(config CompletionContractEvaluatorConfig) CompletionContractEvaluatorConfig {
	return CompletionContractEvaluatorConfig{
		Dataset: aieval.DatasetIdentity{
			Name:    strings.TrimSpace(config.Dataset.Name),
			Version: strings.TrimSpace(config.Dataset.Version),
		},
		SampleID:   strings.TrimSpace(config.SampleID),
		MetricName: strings.TrimSpace(config.MetricName),
		Threshold:  cloneEvalScore(config.Threshold),
	}
}

func validateCompletionContractEvaluatorConfig(config CompletionContractEvaluatorConfig) error {
	textFacts := map[string]string{
		"dataset_name":    config.Dataset.Name,
		"dataset_version": config.Dataset.Version,
		"sample_id":       config.SampleID,
		"metric_name":     config.MetricName,
	}
	if !areSafeEvaluationDomainFacts(textFacts) {
		return errEvaluatorInvalidConfig
	}
	if config.Threshold != nil &&
		(math.IsNaN(*config.Threshold) ||
			math.IsInf(*config.Threshold, 0) ||
			*config.Threshold <= 0 ||
			*config.Threshold > 1) {
		return errEvaluatorInvalidConfig
	}
	return nil
}

func isSafeEvaluationIdentity(identity obs.CorrelationIdentity) bool {
	return areSafeEvaluationIdentityFacts(map[string]string{
		"eval_run_id":      identity.EvalRunID,
		"request_id":       identity.RequestID,
		"ai_trace_id":      identity.AITraceID,
		"service_trace_id": identity.ServiceTraceID,
		"span_id":          identity.SpanID,
	})
}

func areSafeEvaluationIdentityFacts(facts map[string]string) bool {
	for _, value := range facts {
		if len(value) == 0 ||
			len(value) > maxEvaluationFactBytes ||
			!safeEvaluationFactPattern.MatchString(value) {
			return false
		}
	}
	return len(obs.ScanForbiddenPayloadFields(facts)) == 0
}

func areSafeEvaluationDomainFacts(facts map[string]string) bool {
	for _, value := range facts {
		// Dataset、sample 与 metric 是可读的领域身份，合法值可能含 Unicode、空格和
		// SemVer 的 “+”。字符集不是敏感性信号；这里与核心 evidence/store 共享
		// “规范化非空 + 有界 + 敏感扫描”契约，避免 adapter 擅自缩窄领域模型。
		if strings.TrimSpace(value) == "" || len(value) > maxEvaluationFactBytes {
			return false
		}
	}
	return len(obs.ScanForbiddenPayloadFields(facts)) == 0
}

func classifyCompletionFacts(input CompletionContractEvaluationInput) (float64, string) {
	switch {
	case !input.OutputPresent:
		return 0, "output_missing"
	case !isSafeModelIdentifier(input.ActualModel) ||
		len(obs.ScanForbiddenPayloadFields(map[string]string{"actual_model": input.ActualModel})) > 0:
		return 0, "actual_model_missing"
	case !isSafeFinishReason(input.FinishReason):
		return 0, "finish_reason_invalid"
	case !isConsistentEvaluationUsage(input.Usage):
		return 0, "usage_inconsistent"
	default:
		return 1, "within_policy"
	}
}

func isConsistentEvaluationUsage(usage llm.Usage) bool {
	return usage.InputTokens >= 0 &&
		usage.OutputTokens >= 0 &&
		usage.ReasoningTokens >= 0 &&
		usage.CacheReadTokens >= 0 &&
		usage.CacheWriteTokens >= 0 &&
		usage.TotalTokens >= 0 &&
		usage.InputTokens <= maxUsageTokens &&
		usage.OutputTokens <= maxUsageTokens &&
		usage.ReasoningTokens <= maxUsageTokens &&
		usage.CacheReadTokens <= maxUsageTokens &&
		usage.CacheWriteTokens <= maxUsageTokens &&
		usage.TotalTokens <= maxUsageTokens &&
		usage.TotalTokens >= usage.InputTokens+usage.OutputTokens
}

func mapRegressionStatus(status aieval.RegressionStatus) EvalStatus {
	switch status {
	case aieval.RegressionStatusPassed:
		return EvalStatusPassed
	case aieval.RegressionStatusWarning:
		return EvalStatusWarning
	default:
		return EvalStatusFailed
	}
}

func isAllowedDebugEvalSummary(summary DebugEvalSummary) bool {
	if summary.Status == EvalStatusNotRun {
		return summary.Evaluator == "" &&
			summary.Score == nil &&
			summary.ReasonClass == "evaluator_not_configured"
	}
	if summary.Evaluator != completionContractEvaluatorName ||
		summary.Score == nil ||
		math.IsNaN(*summary.Score) ||
		math.IsInf(*summary.Score, 0) ||
		*summary.Score < 0 ||
		*summary.Score > 1 {
		return false
	}
	switch {
	case summary.Status == EvalStatusPassed &&
		*summary.Score == 1 &&
		summary.ReasonClass == "within_policy":
		return true
	case summary.Status == EvalStatusWarning &&
		*summary.Score == 1 &&
		summary.ReasonClass == "threshold_not_configured":
		return true
	case summary.Status == EvalStatusWarning &&
		*summary.Score == 0 &&
		isEvaluationFailureReason(summary.ReasonClass):
		return true
	case summary.Status == EvalStatusFailed &&
		*summary.Score == 0 &&
		isEvaluationFailureReason(summary.ReasonClass):
		return true
	default:
		return false
	}
}

func isEvaluationFailureReason(reasonClass string) bool {
	switch reasonClass {
	case "output_missing",
		"actual_model_missing",
		"finish_reason_invalid",
		"usage_inconsistent":
		return true
	default:
		return false
	}
}

func cloneEvalScore(score *float64) *float64 {
	if score == nil {
		return nil
	}
	cloned := *score
	return &cloned
}
