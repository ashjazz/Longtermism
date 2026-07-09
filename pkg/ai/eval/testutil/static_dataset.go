// Package testutil 提供 eval 包的测试工具。
//
// StaticDataset 是最小的内存数据集实现：它不读文件、不访问网络，也不依赖模型服务。
// 后续 runner、metric、smoke 测试可以用它稳定构造 golden case。
package testutil

import (
	"context"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
)

// StaticDataset 把一组样本固定在内存里，并实现 eval.Dataset。
//
// 它的关键价值不是“存数据”，而是帮助测试隔离状态：每次 Load 都返回样本副本。
// 这样某个测试即使修改了返回值，也不会污染下一次评估或并行测试。
type StaticDataset struct {
	samples []aieval.Sample
}

// NewStaticDataset 创建静态数据集。
//
// 构造时立即复制输入样本，是为了防止调用方在创建 dataset 后继续修改原 slice。
// 这和生产评估里的 golden dataset 原则一致：评估输入必须稳定，结果才可回归。
func NewStaticDataset(samples []aieval.Sample) *StaticDataset {
	return &StaticDataset{samples: cloneSamples(samples)}
}

// Load 实现 eval.Dataset。
//
// 虽然 StaticDataset 不做 IO，仍然先检查 ctx.Err()；这样它和未来 JSON/platform dataset
// 在取消语义上保持一致，runner 测试可以复用相同预期。
func (d *StaticDataset) Load(ctx context.Context) ([]aieval.Sample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return cloneSamples(d.samples), nil
}

func cloneSamples(samples []aieval.Sample) []aieval.Sample {
	if samples == nil {
		return nil
	}

	cloned := make([]aieval.Sample, len(samples))
	for i, sample := range samples {
		cloned[i] = cloneSample(sample)
	}
	return cloned
}

func cloneSample(sample aieval.Sample) aieval.Sample {
	cloned := sample
	cloned.RelevantCtx = append([]string(nil), sample.RelevantCtx...)
	cloned.Meta = cloneMeta(sample.Meta)
	return cloned
}

func cloneMeta(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}

	cloned := make(map[string]any, len(meta))
	for key, value := range meta {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

// cloneValue 递归复制 JSON-like 元数据。
//
// golden case 的 Meta 常由 JSON 解码而来，主要形态是 map[string]any、[]any
// 和标量。只复制最外层 map 会让嵌套筛选条件、标签等数据仍被测试间共享。
func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMeta(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
