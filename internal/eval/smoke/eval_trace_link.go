package smoke

import (
	"context"
	"fmt"
	"strings"
)

const (
	evalTraceLinkRequiredRate = 0.9
	// 小样本 smoke 用于定位失败证据，不用比例门禁盖住诊断目标。
	evalTraceLinkRateGateMinSampleSize = 10
)

// EvalTraceLinkSmokeConfig 描述一次 eval evidence 回链率验证。
type EvalTraceLinkSmokeConfig struct {
	DatasetName    string
	DatasetVersion string
	EvalRunID      string
	Samples        []EvalTraceLinkSmokeSample
}

// EvalTraceLinkSmokeSample 是 smoke 中一条低敏评估证据。
//
// 它只保存 dataset/sample/metric/score 和关联身份，不携带 query、prompt、工具参数
// 或模型响应原文。T045 关注的是“评估证据能否回链”，不是评估内容本身。
type EvalTraceLinkSmokeSample struct {
	SampleID       string
	RequestID      string
	AITraceID      string
	ServiceTraceID string
	SpanID         string
	MetricName     string
	Score          float64
	Threshold      float64
}

// EvalTraceLinkSmokeResult 是 eval evidence 回链率报告。
type EvalTraceLinkSmokeResult struct {
	DatasetName    string
	DatasetVersion string
	EvalRunID      string
	SampleCount    int
	LinkedCount    int
	LinkRate       float64
	MissingLinks   []EvalTraceLinkMissingSample
	FailedSamples  []EvalTraceLinkFailedSample
}

// EvalTraceLinkMissingSample 描述无法回链的样例和首个缺失身份字段。
type EvalTraceLinkMissingSample struct {
	SampleID     string
	MissingField string
}

// EvalTraceLinkFailedSample 描述指标未达阈值但仍可回链定位的样例。
type EvalTraceLinkFailedSample struct {
	SampleID       string
	MetricName     string
	RequestID      string
	AITraceID      string
	Score          float64
	Threshold      float64
	FailureSummary string
}

// RunEvalTraceLinkSmoke 验证评估证据能否按样例回链到请求和 AI trace。
//
// 90% 是 v1 契约里的最低回链率门槛；即使低于门槛，也返回完整 result，方便调用方
// 在 CI 或 quickstart 中直接展示哪些 sample 缺失了哪类关联身份。
func RunEvalTraceLinkSmoke(ctx context.Context, config EvalTraceLinkSmokeConfig) (EvalTraceLinkSmokeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return EvalTraceLinkSmokeResult{}, err
	}

	result := buildEvalTraceLinkSmokeResult(config)
	if result.SampleCount == 0 {
		return result, fmt.Errorf("eval trace link samples are required")
	}
	if shouldFailEvalTraceLinkRate(result) {
		return result, fmt.Errorf("eval trace link rate %.2f is below %.2f", result.LinkRate, evalTraceLinkRequiredRate)
	}
	return result, nil
}

func buildEvalTraceLinkSmokeResult(config EvalTraceLinkSmokeConfig) EvalTraceLinkSmokeResult {
	samples := append([]EvalTraceLinkSmokeSample(nil), config.Samples...)
	result := EvalTraceLinkSmokeResult{
		DatasetName:    strings.TrimSpace(config.DatasetName),
		DatasetVersion: strings.TrimSpace(config.DatasetVersion),
		EvalRunID:      strings.TrimSpace(config.EvalRunID),
		SampleCount:    len(samples),
		MissingLinks:   make([]EvalTraceLinkMissingSample, 0),
		FailedSamples:  make([]EvalTraceLinkFailedSample, 0),
	}

	for _, sample := range samples {
		if missingField := firstMissingEvalTraceLinkField(sample); missingField != "" {
			result.MissingLinks = append(result.MissingLinks, EvalTraceLinkMissingSample{
				SampleID:     sample.SampleID,
				MissingField: missingField,
			})
			continue
		}

		result.LinkedCount++
		if isFailedEvalTraceLinkSample(sample) {
			result.FailedSamples = append(result.FailedSamples, failedEvalTraceLinkSample(sample))
		}
	}
	result.LinkRate = evalTraceLinkRate(result.LinkedCount, result.SampleCount)
	return result
}

func firstMissingEvalTraceLinkField(sample EvalTraceLinkSmokeSample) string {
	switch {
	case strings.TrimSpace(sample.RequestID) == "":
		return "request_id"
	case strings.TrimSpace(sample.AITraceID) == "":
		return "ai_trace_id"
	case strings.TrimSpace(sample.ServiceTraceID) == "":
		return "service_trace_id"
	case strings.TrimSpace(sample.SpanID) == "":
		return "span_id"
	default:
		return ""
	}
}

func evalTraceLinkRate(linkedCount, sampleCount int) float64 {
	if sampleCount == 0 {
		return 0
	}
	return float64(linkedCount) / float64(sampleCount)
}

func shouldFailEvalTraceLinkRate(result EvalTraceLinkSmokeResult) bool {
	if result.SampleCount < evalTraceLinkRateGateMinSampleSize {
		return false
	}
	return result.LinkRate < evalTraceLinkRequiredRate
}

func isFailedEvalTraceLinkSample(sample EvalTraceLinkSmokeSample) bool {
	return sample.Threshold > 0 && sample.Score < sample.Threshold
}

func failedEvalTraceLinkSample(sample EvalTraceLinkSmokeSample) EvalTraceLinkFailedSample {
	return EvalTraceLinkFailedSample{
		SampleID:       sample.SampleID,
		MetricName:     sample.MetricName,
		RequestID:      sample.RequestID,
		AITraceID:      sample.AITraceID,
		Score:          sample.Score,
		Threshold:      sample.Threshold,
		FailureSummary: fmt.Sprintf("metric %s score %.2f is below threshold %.2f", sample.MetricName, sample.Score, sample.Threshold),
	}
}
