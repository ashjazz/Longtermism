// Package llm 是模型抽象层（准备清单 §2、全局 rules/ai/integration.md）。
//
// 职责：在业务代码与具体 LLM provider（OpenAI / Anthropic / DeepSeek / Ollama …）之间
// 提供统一接口，屏蔽差异，支持运行时切换、故障切换（与 resilience/ 协作）。
//
// 本包只定义「契约」。具体 provider 适配放在子包（如 llm/openai、llm/anthropic），
// 业务代码只依赖 llm.Provider 接口。
package llm

import (
	"context"
	"errors"
)

// Role 标识消息角色。用类型而非裸 string，避免拼写错误。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message 是发送给模型的单条消息。
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content,omitempty"`
	// Name 工具/函数调用相关时可选。
	Name string `json:"name,omitempty"`
	// ToolCallID 关联工具结果与模型上一轮返回的 tool call。
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ChatRequest 是一次聊天补全请求。
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
	// Temperature 使用指针区分“调用方未设置”和“显式设置为 0”。
	//
	// 0 是合法且常用的确定性采样参数，若使用 float64 零值配合 omitempty，
	// adapter 会错误地把显式 0 当成缺省值并丢弃。
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	// StructuredOutput 约束最终输出的 JSON Schema；strict=true 时 provider 必须强校验。
	StructuredOutput *StructuredOutput `json:"structured_output,omitempty"`
	// ReasoningEffort 调节推理模型的 test-time compute，如 none/low/medium/high/xhigh。
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// Stream 是否流式。流式走 ChatStream。
	Stream bool `json:"-"`
}

// Usage 记录 token 消耗，用于成本控制（§11）与可观测性（§8）。
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens"`
}

// Tool 是暴露给模型的可调用工具声明。Parameters 应为 JSON Schema object。
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict,omitempty"`
}

// ToolCall 是 provider 返回的结构化工具调用请求。
type ToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ToolResult 是工具执行后回传给模型的结构化结果。
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name,omitempty"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error,omitempty"`
}

// StructuredOutput 描述 strict JSON schema 输出约束。
type StructuredOutput struct {
	Name   string         `json:"name"`
	Schema map[string]any `json:"schema"`
	Strict bool           `json:"strict"`
}

// ProviderCapabilities 描述 provider/model 支持的能力，供 failover 和 agent 路由使用。
type ProviderCapabilities struct {
	ToolCalling         bool `json:"tool_calling"`
	StrictStructuredOut bool `json:"strict_structured_out"`
	Streaming           bool `json:"streaming"`
	StreamingToolCall   bool `json:"streaming_tool_call"`
	ReasoningEffort     bool `json:"reasoning_effort"`
	PromptCaching       bool `json:"prompt_caching"`
	Vision              bool `json:"vision"`
}

// FinishReason 标识生成结束原因，便于上层判断是否被截断。
type FinishReason string

const (
	FinishStop          FinishReason = "stop"
	FinishLength        FinishReason = "length"     // 命中 max_tokens 被截断
	FinishToolCall      FinishReason = "tool_calls" // 需要调用工具（见 agent/）
	FinishContentFilter FinishReason = "content_filter"
)

// ChatResponse 是一次（非流式）聊天补全的响应。
type ChatResponse struct {
	Content      string       `json:"content"`
	Model        string       `json:"model"` // 实际服务的模型名，可能与请求不同（降级时）
	Usage        Usage        `json:"usage"`
	FinishReason FinishReason `json:"finish_reason"`
	ToolCalls    []ToolCall   `json:"tool_calls,omitempty"`
}

// ErrUpstream 表示上游 provider 不可用。resilience/ 据此决定重试/熔断/降级。
var ErrUpstream = errors.New("llm: upstream provider unavailable")

// ErrRateLimit 表示 provider 或上游网关返回限流。
//
// 它通常也是一种上游不可用原因，因此 adapter 可以同时包装 ErrRateLimit 与
// ErrUpstream：resilience 层用 ErrUpstream 决定是否计入断路器，用 ErrRateLimit
// 保留更精确的可观测分类和后续限流策略信号。
var ErrRateLimit = errors.New("llm: provider rate limited")

// Provider 是所有 LLM provider 必须实现的契约。
// 实现应做到：
//   - 受 ctx 超时控制（默认 60s，见 rules/ai/integration.md）；
//   - 对 429/5xx/timeout 返回 ErrUpstream，交由 resilience/ 处理；
//   - 对 4xx（参数错误）返回原始错误，不重试。
type Provider interface {
	// Name 返回 provider 标识，用于 trace、限流 key、健康检查。
	Name() string

	// Capabilities 返回当前 provider/model 的能力，用于 failover 过滤候选模型。
	Capabilities(model string) ProviderCapabilities

	// Chat 同步补全。
	Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error)

	// ChatStream 流式补全。每个 chunk 经 ch 发送；结束时关闭 ch 并返回最终 err/usage。
	// TTFT（首 token 延迟）等指标由 obs/ 在消费端记录（§8）。
	ChatStream(ctx context.Context, req *ChatRequest) (<-chan ChatChunk, error)
}

// ChatChunk 是流式响应的增量单元。
type ChatChunk struct {
	DeltaContent  string       `json:"delta_content,omitempty"`
	DeltaToolCall *ToolCall    `json:"delta_tool_call,omitempty"`
	FinishReason  FinishReason `json:"finish_reason,omitempty"`
	Usage         *Usage       `json:"usage,omitempty"` // 仅末尾 chunk 携带
	Err           error        `json:"-"`               // 流中错误
}

// TokenCounter 估算 token 数，用于预算管理（§11）与上下文截断。
// 不同 provider 计数规则不同，应由各 provider 提供。
type TokenCounter interface {
	CountMessages(messages []Message) int
}
