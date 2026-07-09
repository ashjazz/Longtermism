package eval

import (
	"context"
	"fmt"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

// LocalRunner 是 P0 的确定性离线评估 runner。
//
// 它只负责编排 dataset -> predict -> metrics -> report，不直接调用模型、不写文件、
// 不连接外部评估平台。这样 P0 的 eval smoke 可以默认离线运行，后续平台同步也能
// 作为外层 adapter 接入，而不会污染核心评估契约。
type LocalRunner struct {
	dataset    DatasetIdentity
	evalRunID  string
	thresholds map[string]float64
}

// RunnerOption 描述 LocalRunner 的可选评估配置。
type RunnerOption func(*LocalRunner)

func NewRunner(dataset DatasetIdentity, options ...RunnerOption) *LocalRunner {
	runner := &LocalRunner{
		dataset:    normalizeDatasetIdentity(dataset),
		thresholds: make(map[string]float64),
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		option(runner)
	}
	return runner
}

// WithEvalRunID 开启 evidence 生成，并绑定本次评估运行身份。
func WithEvalRunID(evalRunID string) RunnerOption {
	return func(runner *LocalRunner) {
		runner.evalRunID = evalRunID
	}
}

// WithMetricThreshold 设置单个指标的回归阈值。
func WithMetricThreshold(metricName string, threshold float64) RunnerOption {
	return func(runner *LocalRunner) {
		if runner.thresholds == nil {
			runner.thresholds = make(map[string]float64)
		}
		runner.thresholds[metricName] = threshold
	}
}

// Run 执行一次评估并返回汇总报告。
//
// 这里采用 fail fast：单个样本的 predict 或 metric 失败都会立即返回带上下文的错误。
// P0 先选择这种严格模式，是为了让本地门禁能明确告诉我们“哪条样例、哪个指标坏了”，
// 而不是产生一个混入部分失败的平均分。
func (r *LocalRunner) Run(ctx context.Context, dataset Dataset, predict PredictFn, metrics []Metric) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if dataset == nil {
		return Report{}, fmt.Errorf("eval runner dataset is required")
	}
	if predict == nil {
		return Report{}, fmt.Errorf("eval runner predict function is required")
	}
	if len(metrics) == 0 {
		return Report{}, fmt.Errorf("eval runner metrics are required")
	}
	if err := validateDatasetIdentity(r.dataset); err != nil {
		return Report{}, fmt.Errorf("eval runner dataset identity: %w", err)
	}

	samples, err := dataset.Load(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("load eval dataset: %w", err)
	}
	if len(samples) == 0 {
		return Report{}, fmt.Errorf("eval dataset samples are required")
	}

	sums := make(map[string]float64, len(metrics))
	evidence := make([]EvaluationEvidence, 0, len(samples)*len(metrics))
	for _, sample := range samples {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}

		prediction, err := predict(ctx, sample)
		if err != nil {
			return Report{}, fmt.Errorf("predict sample %q: %w", sample.ID, err)
		}

		for _, metric := range metrics {
			if metric == nil {
				return Report{}, fmt.Errorf("metric is required for sample %q", sample.ID)
			}
			metricName := metric.Name()
			if metricName == "" {
				return Report{}, fmt.Errorf("metric name is required for sample %q", sample.ID)
			}

			score, err := metric.Score(ctx, sample, prediction)
			if err != nil {
				return Report{}, fmt.Errorf("score metric %q for sample %q: %w", metricName, sample.ID, err)
			}
			clampedScore := clampScore(score)
			sums[metricName] += clampedScore
			if r.evidenceEnabled() {
				item, err := r.newEvidence(sample, metricName, clampedScore, prediction.TraceIdentity)
				if err != nil {
					return Report{}, fmt.Errorf("build evidence for sample %q metric %q: %w", sample.ID, metricName, err)
				}
				evidence = append(evidence, item)
			}
		}
	}

	return Report{
		Dataset:     r.dataset,
		SampleCount: len(samples),
		Scores:      averageScores(sums, len(samples)),
		Evidence:    evidence,
	}, nil
}

func (r *LocalRunner) evidenceEnabled() bool {
	return r != nil && r.evalRunID != ""
}

func (r *LocalRunner) newEvidence(sample Sample, metricName string, score float64, identity obs.CorrelationIdentity) (EvaluationEvidence, error) {
	linkedIdentity := obs.ApplyCorrelationOptions(identity, obs.WithEvalRunID(r.evalRunID))
	threshold := r.metricThreshold(metricName)
	evidence, err := NewEvaluationEvidence(EvaluationEvidenceInput{
		Identity:   linkedIdentity,
		Dataset:    r.dataset,
		SampleID:   sample.ID,
		MetricName: metricName,
		Score:      score,
		Threshold:  threshold,
	})
	if err != nil {
		return EvaluationEvidence{}, fmt.Errorf("trace link request_id/ai_trace_id/service_trace_id/span_id is required: %w", err)
	}
	return evidence, nil
}

func (r *LocalRunner) metricThreshold(metricName string) *float64 {
	if r == nil || r.thresholds == nil {
		return nil
	}
	threshold, ok := r.thresholds[metricName]
	if !ok {
		return nil
	}
	return cloneEvidenceFloat64Pointer(&threshold)
}

func averageScores(sums map[string]float64, sampleCount int) map[string]float64 {
	averages := make(map[string]float64, len(sums))
	for metricName, sum := range sums {
		averages[metricName] = sum / float64(sampleCount)
	}
	return averages
}

func clampScore(score float64) float64 {
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}
