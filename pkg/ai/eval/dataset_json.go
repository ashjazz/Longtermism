package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// JSONDataset 从本地 JSON 文件加载 golden samples。
//
// P0 阶段先使用本地文件，是为了让 eval smoke 和 CI 门禁默认不依赖外部平台；
// 后续如果接入 LangFuse、对象存储或评估平台，只需要实现同一个 Dataset 接口。
type JSONDataset struct {
	path string
}

// NewJSONDataset 创建本地 JSON dataset。
//
// 构造函数只保存路径，不立刻读文件；真正读取发生在 Load(ctx) 中，便于 runner
// 在请求级 ctx 取消时尽早停止，也便于测试显式控制文件内容。
func NewJSONDataset(path string) *JSONDataset {
	return &JSONDataset{path: path}
}

// Load 读取、解析并校验 JSON golden dataset。
//
// 普通评估数据可能来自人工整理或自动生成，属于外部输入：这里必须 fail fast，
// 否则缺 ID、坏 JSON 或空文件会让后续回归报告变得不可追踪。
func (d *JSONDataset) Load(ctx context.Context) ([]Sample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d == nil || strings.TrimSpace(d.path) == "" {
		return nil, fmt.Errorf("json dataset path is required")
	}

	content, err := os.ReadFile(d.path)
	if err != nil {
		return nil, fmt.Errorf("read json dataset %q: %w", d.path, err)
	}
	if strings.TrimSpace(string(content)) == "" {
		return nil, fmt.Errorf("json dataset %q is empty", d.path)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var payload jsonDatasetPayload
	if err := json.Unmarshal(content, &payload); err != nil {
		return nil, fmt.Errorf("parse json dataset %q: %w", d.path, err)
	}
	if err := validateSamples(payload.Samples); err != nil {
		return nil, err
	}
	return cloneSamples(payload.Samples), nil
}

type jsonDatasetPayload struct {
	Samples []Sample `json:"samples"`
}

func validateSamples(samples []Sample) error {
	if len(samples) == 0 {
		return fmt.Errorf("json dataset samples are required")
	}
	for index, sample := range samples {
		if strings.TrimSpace(sample.ID) == "" {
			return fmt.Errorf("json dataset sample id is required at index %d", index)
		}
	}
	return nil
}

func cloneSamples(samples []Sample) []Sample {
	if samples == nil {
		return nil
	}

	cloned := make([]Sample, len(samples))
	for index, sample := range samples {
		cloned[index] = cloneSample(sample)
	}
	return cloned
}

func cloneSample(sample Sample) Sample {
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
		cloned[key] = cloneJSONValue(value)
	}
	return cloned
}

// cloneJSONValue 复制 JSON-like 元数据。
//
// JSON 解码后的嵌套结构主要是 map[string]any、[]any 和标量；递归复制这些结构，
// 可以防止调用方修改 Load 返回值后污染下一次评估运行。
func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMeta(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneJSONValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
