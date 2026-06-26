package eval

import (
	"context"
	"fmt"
)

// LocalRunner 是 P0 的确定性离线评估 runner。
//
// 它只负责编排 dataset -> predict -> metrics -> report，不直接调用模型、不写文件、
// 不连接外部评估平台。这样 P0 的 eval smoke 可以默认离线运行，后续平台同步也能
// 作为外层 adapter 接入，而不会污染核心评估契约。
type LocalRunner struct {
	datasetVersion string
}

func NewRunner(datasetVersion string) *LocalRunner {
	return &LocalRunner{datasetVersion: datasetVersion}
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

	samples, err := dataset.Load(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("load eval dataset: %w", err)
	}
	if len(samples) == 0 {
		return Report{}, fmt.Errorf("eval dataset samples are required")
	}

	sums := make(map[string]float64, len(metrics))
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
			sums[metricName] += clampScore(score)
		}
	}

	return Report{
		DatasetVersion: r.datasetVersion,
		SampleCount:    len(samples),
		Scores:         averageScores(sums, len(samples)),
	}, nil
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
