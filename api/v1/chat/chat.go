// Package chat declares the public GoFrame HTTP contract for non-streaming chat.
//
// It deliberately contains only route metadata and request/response DTOs. The controller owns
// strict HTTP decoding, validation, and mappings; the application usecase owns LLM orchestration.
package chat

import "github.com/gogf/gf/v2/frame/g"

const (
	ChatSmokeRunIDHeader         = "X-Observability-Smoke-Run-ID"
	ChatSmokeAuthorizationHeader = "X-Observability-Smoke-Authorization"
)

type ChatReq struct {
	g.Meta `path:"/chat" method:"post" tags:"Chat" summary:"Run a server-configured non-streaming chat" json:"-"`
	// v tag is declarative DTO metadata. The controller executes it through gvalid;
	// byte-size and strict-JSON rules remain controller concerns.
	Message string `json:"message" v:"required"`
}

type UsageSummary struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
	TotalTokens      int64 `json:"total_tokens"`
}

type FinishReason string

const (
	FinishReasonStop          FinishReason = "stop"
	FinishReasonLength        FinishReason = "length"
	FinishReasonToolCalls     FinishReason = "tool_calls"
	FinishReasonContentFilter FinishReason = "content_filter"
)

type ChatData struct {
	Content      string       `json:"content"`
	Model        string       `json:"model"`
	FinishReason FinishReason `json:"finish_reason"`
	Usage        UsageSummary `json:"usage"`
}

type EvalStatus string

const (
	EvalStatusPassed  EvalStatus = "passed"
	EvalStatusWarning EvalStatus = "warning"
	EvalStatusFailed  EvalStatus = "failed"
	EvalStatusNotRun  EvalStatus = "not_run"
)

type EvalSummary struct {
	Status      EvalStatus `json:"status"`
	Evaluator   string     `json:"evaluator,omitempty"`
	Score       *float64   `json:"score,omitempty"`
	ReasonClass string     `json:"reason_class,omitempty"`
}

type ChatSuccessMeta struct {
	RequestID   string       `json:"request_id"`
	AITraceID   string       `json:"ai_trace_id"`
	EvalSummary *EvalSummary `json:"eval_summary,omitempty"`
}

type ChatPreAIErrorMeta struct {
	RequestID string `json:"request_id"`
}

type ChatPostAIErrorMeta struct {
	RequestID string `json:"request_id"`
	AITraceID string `json:"ai_trace_id"`
}

type ChatSuccessEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    ChatData        `json:"data"`
	Meta    ChatSuccessMeta `json:"meta"`
}

// ErrorData intentionally has no fields. A nil pointer serializes as the OpenAPI-required null;
// unlike any, this DTO cannot carry a provider body, prompt, credential, or request payload.
type ErrorData struct{}

type ChatPreAIErrorEnvelope struct {
	Code    int                `json:"code"`
	Message string             `json:"message"`
	Data    *ErrorData         `json:"data"`
	Meta    ChatPreAIErrorMeta `json:"meta"`
}

type ChatPostAIErrorEnvelope struct {
	Code    int                 `json:"code"`
	Message string              `json:"message"`
	Data    *ErrorData          `json:"data"`
	Meta    ChatPostAIErrorMeta `json:"meta"`
}
