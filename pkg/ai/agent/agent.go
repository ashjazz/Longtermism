// Package agent 实现 Agent 循环（准备清单 §4）。
//
// 2026 版实现以原生 tool calling 为地基：模型返回结构化 tool call，
// 执行器调用本地工具后回传 tool result，再继续模型循环直到最终答案。
// ReAct 只作为"推理-行动交替"的历史思想背景，不再解析 Thought/Action 文本。
//
// 安全措施（§4.8 生产陷阱，必须内置，不可后补）：
//   - 最大步数限制（maxSteps）
//   - 循环检测（连续相同 tool call → 终止）
//   - 单步超时 + 总 token 预算上限
//   - 工具分级：敏感操作需人工确认
package agent

import (
	"context"

	"github.com/ashjazz/Longtermism/pkg/ai/llm"
)

// Tool 是 Agent 可调用的工具。schema 设计遵循 §4.7：明确 IO 类型、description 写清何时使用、
// strict JSON Schema、有用错误、读操作幂等。
type Tool interface {
	Name() string
	Description() string
	// Parameters 返回 JSON Schema object，用于原生 tool calling。
	Parameters() map[string]any
	// Invoke 执行工具。失败时返回「对 LLM 有用」的错误信息，而非裸 "Error"（§4.7）。
	Invoke(ctx context.Context, args map[string]any) (string, error)
}

// StepResult 单步执行结果，供 obs/ 记录与循环检测使用。
type StepResult struct {
	ToolCall  llm.ToolCall   `json:"toolCall"`
	Result    llm.ToolResult `json:"result"`
	LatencyMs int64          `json:"latencyMs,omitempty"`
}

// Executor Agent 执行器。实现应消费 provider 返回的原生 tool call，而非解析自然语言 action。
type Executor interface {
	// Run 执行直到得出最终答案、命中步数上限、或出错。
	Run(ctx context.Context, req Request) (Result, error)
}

// Request 是一次 Agent 运行请求。
type Request struct {
	Query string `json:"query"`
	Model string `json:"model,omitempty"`
	Tools []Tool `json:"-"`
	Limit Limit  `json:"limit"`
}

// Result Agent 运行结果，承载任务完成情况与效率指标（§4.9 评估独特挑战）。
type Result struct {
	Answer       string       `json:"answer"`
	Steps        []StepResult `json:"steps,omitempty"`
	StepsTaken   int          `json:"stepsTaken"`
	TokensUsed   int          `json:"tokensUsed"`
	TerminatedBy string       `json:"terminatedBy,omitempty"` // finished | max_steps | loop_detected | budget_exceeded
}

// Limit 强制安全边界，避免无限循环与成本失控（§4.8）。
type Limit struct {
	MaxSteps     int `json:"maxSteps"`     // 默认 10
	StepTimeoutS int `json:"stepTimeoutS"` // 默认 30
	TokenBudget  int `json:"tokenBudget"`  // 0 表示不限
}

// ToLLMTool 将本地 Tool 转成 llm.Tool 声明。
func ToLLMTool(tool Tool) llm.Tool {
	return llm.Tool{
		Name:        tool.Name(),
		Description: tool.Description(),
		Parameters:  tool.Parameters(),
		Strict:      true,
	}
}
