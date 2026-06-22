// Package eval 是评估体系（准备清单 §6，整份 JD 对单一技能要求最高者）。
//
// 三层架构（§6.2）：
//  1. 单元测试：schema/格式/确定 case
//  2. 离线评估：golden dataset + RAGAS 指标 + LLM-as-Judge（§6.3/6.4）
//  3. 线上指标：用户反馈、任务完成率、人工抽检
//
// 关键工程要求：
//   - 评估是 CI 门禁（§6.6）：指标恶化超阈值（见 config ai.eval）即拦截合并。
//   - LLM-as-Judge 必须处理 bias（§6.4 表：position/verbosity/self-enhancement…）。
//   - 定期校准 judge 与人工评分相关性（Spearman/Kendall，§6.7 meta-evaluation）。
//
// 本框架约定（见 AGENTS.md 完成定义）：每交付一个 AI 能力，必须同时交付评估它的 golden case。
package eval

import "context"

// Sample golden dataset 中的一条样本（§6.5）。
type Sample struct {
	ID          string         `json:"id"`
	Query       string         `json:"query"`
	GroundTruth string         `json:"groundTruth,omitempty"` // 标准答案（可空）
	RelevantCtx []string       `json:"relevantCtx,omitempty"` // 应被检索到的上下文
	Difficulty  string         `json:"difficulty,omitempty"`  // simple/moderate/hard/edge
	Category    string         `json:"category,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

// Dataset golden 数据集。规模建议：起步 50-100，推荐 200-500（§6.5 步骤5）。
type Dataset interface {
	Load(ctx context.Context) ([]Sample, error)
}

// Metric 离线评估指标（RAGAS 类，§6.3）。
type Metric interface {
	Name() string
	// Score 计算单条样本得分，范围 [0,1]，越大越好。
	Score(ctx context.Context, sample Sample, prediction Prediction) (float64, error)
}

// Prediction 被评估系统的输出。
type Prediction struct {
	Answer     string   `json:"answer"`
	Context    []string `json:"context,omitempty"` // 检索到的上下文
	TokensUsed int      `json:"tokensUsed,omitempty"`
}

// Report 一次评估运行的汇总。指标下降超阈值触发 CI 拦截（§6.6）。
type Report struct {
	DatasetVersion string             `json:"datasetVersion"`
	SampleCount    int                `json:"sampleCount"`
	Scores         map[string]float64 `json:"scores"`              // metricName -> 平均分
	Regressed      []string           `json:"regressed,omitempty"` // 相比 baseline 退步的指标
}

// Runner 评估运行器，串联 dataset → system → metrics → report。
type Runner interface {
	Run(ctx context.Context, dataset Dataset, predict PredictFn, metrics []Metric) (Report, error)
}

// PredictFn 被评估系统的统一调用签名。
type PredictFn func(ctx context.Context, sample Sample) (Prediction, error)
