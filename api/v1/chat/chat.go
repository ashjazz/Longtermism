// Package chat owns the public, non-streaming chat transport contract.
//
// The API deliberately exposes only a message. Model/provider selection, credentials, payload
// policy, and debug mode belong to server-side assembly so callers cannot bypass safety or
// observability boundaries at the HTTP edge.
package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/util/gvalid"
)

const (
	chatMessageMaxBytes = 32 * 1024
	maxEvalSummaryBytes = 1024
	chatMessageRule     = "longtermism-chat-message"
)

var (
	allowedEvaluators = map[string]struct{}{
		"contract_check":                       {},
		"deterministic_completion_contract_v1": {},
	}
	allowedEvalReasonClasses = map[string]struct{}{
		"within_policy":            {},
		"low_confidence":           {},
		"threshold_not_configured": {},
		"output_missing":           {},
		"actual_model_missing":     {},
		"finish_reason_invalid":    {},
		"usage_inconsistent":       {},
		"evaluator_not_configured": {},
	}
)

// ChatReq has exactly one caller-controlled field. Keeping the validation tag alongside the
// request type means GoFrame controller binding and explicit decoding apply the same boundary.
type ChatReq struct {
	g.Meta  `path:"/chat" method:"post" tags:"Chat" summary:"Run a server-configured non-streaming chat" json:"-"`
	Message string `json:"message" v:"longtermism-chat-message"`
}

// UsageSummary contains only aggregate token counts, never prompt or completion content.
type UsageSummary struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
	TotalTokens      int64 `json:"total_tokens"`
}

// FinishReason is the transport-safe form of the provider completion reason.
type FinishReason string

const (
	FinishReasonStop          FinishReason = "stop"
	FinishReasonLength        FinishReason = "length"
	FinishReasonToolCalls     FinishReason = "tool_calls"
	FinishReasonContentFilter FinishReason = "content_filter"
)

// ChatData is the successful model result. Model records the actual provider result rather than
// the configured model name so later evidence can distinguish routing from observed behavior.
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

// EvalSummary is an intentionally bounded, low-sensitivity diagnostic. Detailed evaluator input
// and evidence remain in the local evidence store rather than being sent back to the caller.
type EvalSummary struct {
	Status      EvalStatus `json:"status"`
	Evaluator   string     `json:"evaluator,omitempty"`
	Score       *float64   `json:"score,omitempty"`
	ReasonClass string     `json:"reason_class,omitempty"`
}

// ChatSuccessMeta has no exported mutable fields. It can only be made through
// NewChatSuccessMeta, which validates and snapshots the optional debug summary before it crosses
// the HTTP boundary.
type ChatSuccessMeta struct {
	requestID   string
	aiTraceID   string
	evalSummary *EvalSummary
}

// ChatPreAIErrorMeta cannot contain an AI identity because validation and local limiting happen
// before the chat usecase creates one.
type ChatPreAIErrorMeta struct{ requestID string }

// ChatPostAIErrorMeta requires an AI identity because model-side failures happen after the
// usecase starts and must remain correlatable to its trace.
type ChatPostAIErrorMeta struct {
	requestID string
	aiTraceID string
}

type ChatSuccessEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    ChatData        `json:"data"`
	Meta    ChatSuccessMeta `json:"meta"`
}

// NullData is deliberately the only error data representation. Its marshaler always emits null,
// preventing provider response bodies, credentials, prompts, or user text from entering errors.
type NullData struct{}

func (NullData) MarshalJSON() ([]byte, error) { return []byte("null"), nil }

type ChatPreAIErrorEnvelope struct {
	Code    int                `json:"code"`
	Message string             `json:"message"`
	Data    NullData           `json:"data"`
	Meta    ChatPreAIErrorMeta `json:"meta"`
}

type ChatPostAIErrorEnvelope struct {
	Code    int                 `json:"code"`
	Message string              `json:"message"`
	Data    NullData            `json:"data"`
	Meta    ChatPostAIErrorMeta `json:"meta"`
}

func (envelope ChatSuccessEnvelope) MarshalJSON() ([]byte, error) {
	if envelope.Code != 0 {
		return nil, errors.New("chat success envelope code must be zero")
	}
	return json.Marshal(struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    ChatData        `json:"data"`
		Meta    ChatSuccessMeta `json:"meta"`
	}{Code: envelope.Code, Message: envelope.Message, Data: envelope.Data, Meta: envelope.Meta})
}

func (envelope ChatPreAIErrorEnvelope) MarshalJSON() ([]byte, error) {
	if envelope.Code != 400 && envelope.Code != 429 {
		return nil, errors.New("pre-AI chat error envelope code must be 400 or 429")
	}
	return json.Marshal(struct {
		Code    int                `json:"code"`
		Message string             `json:"message"`
		Data    NullData           `json:"data"`
		Meta    ChatPreAIErrorMeta `json:"meta"`
	}{Code: envelope.Code, Message: envelope.Message, Data: envelope.Data, Meta: envelope.Meta})
}

func (envelope ChatPostAIErrorEnvelope) MarshalJSON() ([]byte, error) {
	if envelope.Code != 502 && envelope.Code != 504 {
		return nil, errors.New("post-AI chat error envelope code must be 502 or 504")
	}
	return json.Marshal(struct {
		Code    int                 `json:"code"`
		Message string              `json:"message"`
		Data    NullData            `json:"data"`
		Meta    ChatPostAIErrorMeta `json:"meta"`
	}{Code: envelope.Code, Message: envelope.Message, Data: envelope.Data, Meta: envelope.Meta})
}

func init() {
	// GoFrame applies struct tags during controller binding. The registered rule calls the same
	// pure validator used by tests and strict JSON decoding, avoiding three drifting definitions of
	// the 32 KiB/UTF-8/whitespace boundary.
	gvalid.RegisterRule(chatMessageRule, func(_ context.Context, input gvalid.RuleFuncInput) error {
		return ValidateChatMessage(input.Value.String())
	})
}

// ValidateChatMessage enforces the byte budget promised by OpenAPI. Byte length matters because
// requests are transported and logged as UTF-8 bytes; rune counting would permit an oversized
// multibyte payload through this boundary.
func ValidateChatMessage(message string) error {
	if !utf8.ValidString(message) {
		return errors.New("chat message must be valid UTF-8")
	}
	if strings.TrimSpace(message) == "" {
		return errors.New("chat message must not be blank")
	}
	if len(message) > chatMessageMaxBytes {
		return fmt.Errorf("chat message exceeds %d bytes", chatMessageMaxBytes)
	}
	return nil
}

// DecodeChatRequest rejects unknown fields instead of silently discarding them. In particular,
// this prevents a client from believing it selected a provider, model, endpoint, credential, or
// debug mode that is owned by the server configuration.
func DecodeChatRequest(body []byte) (ChatReq, error) {
	if !utf8.Valid(body) {
		return ChatReq{}, errors.New("chat request must be valid UTF-8 JSON")
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()

	var request ChatReq
	if err := decoder.Decode(&request); err != nil {
		return ChatReq{}, fmt.Errorf("decode chat request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return ChatReq{}, errors.New("chat request must contain one JSON object")
		}
		return ChatReq{}, fmt.Errorf("decode trailing chat request data: %w", err)
	}
	if err := ValidateChatMessage(request.Message); err != nil {
		return ChatReq{}, err
	}
	return request, nil
}

// NewChatSuccessMeta applies the server-controlled debug boundary. A missing evaluator is
// represented as an explicit low-sensitivity not_run result only when diagnostics are enabled.
func NewChatSuccessMeta(requestID, aiTraceID string, summary *EvalSummary, debugEnabled bool) (ChatSuccessMeta, error) {
	if requestID == "" {
		return ChatSuccessMeta{}, errors.New("request ID is required")
	}
	if aiTraceID == "" {
		return ChatSuccessMeta{}, errors.New("AI trace ID is required")
	}

	meta := ChatSuccessMeta{requestID: requestID, aiTraceID: aiTraceID}
	if !debugEnabled {
		return meta, nil
	}
	if summary == nil {
		summary = &EvalSummary{Status: EvalStatusNotRun}
	}
	if err := ValidateEvalSummary(*summary); err != nil {
		return ChatSuccessMeta{}, err
	}
	return ChatSuccessMeta{requestID: requestID, aiTraceID: aiTraceID, evalSummary: cloneEvalSummary(summary)}, nil
}

func NewChatPreAIErrorMeta(requestID string) (ChatPreAIErrorMeta, error) {
	if requestID == "" {
		return ChatPreAIErrorMeta{}, errors.New("request ID is required")
	}
	return ChatPreAIErrorMeta{requestID: requestID}, nil
}

func NewChatPostAIErrorMeta(requestID, aiTraceID string) (ChatPostAIErrorMeta, error) {
	if requestID == "" {
		return ChatPostAIErrorMeta{}, errors.New("request ID is required")
	}
	if aiTraceID == "" {
		return ChatPostAIErrorMeta{}, errors.New("AI trace ID is required")
	}
	return ChatPostAIErrorMeta{requestID: requestID, aiTraceID: aiTraceID}, nil
}

func (meta ChatSuccessMeta) MarshalJSON() ([]byte, error) {
	if meta.requestID == "" || meta.aiTraceID == "" {
		return nil, errors.New("chat success metadata requires request and AI trace IDs")
	}
	if meta.evalSummary != nil {
		if err := ValidateEvalSummary(*meta.evalSummary); err != nil {
			return nil, err
		}
	}
	return json.Marshal(struct {
		RequestID   string       `json:"request_id"`
		AITraceID   string       `json:"ai_trace_id"`
		EvalSummary *EvalSummary `json:"eval_summary,omitempty"`
	}{RequestID: meta.requestID, AITraceID: meta.aiTraceID, EvalSummary: cloneEvalSummary(meta.evalSummary)})
}

func (meta ChatPreAIErrorMeta) MarshalJSON() ([]byte, error) {
	if meta.requestID == "" {
		return nil, errors.New("pre-AI error metadata requires a request ID")
	}
	return json.Marshal(struct {
		RequestID string `json:"request_id"`
	}{RequestID: meta.requestID})
}

func (meta ChatPostAIErrorMeta) MarshalJSON() ([]byte, error) {
	if meta.requestID == "" || meta.aiTraceID == "" {
		return nil, errors.New("post-AI error metadata requires request and AI trace IDs")
	}
	return json.Marshal(struct {
		RequestID string `json:"request_id"`
		AITraceID string `json:"ai_trace_id"`
	}{RequestID: meta.requestID, AITraceID: meta.aiTraceID})
}

// ValidateEvalSummary preserves the one-KiB response budget and validates the finite enum/range
// contract before a summary crosses the API boundary.
func ValidateEvalSummary(summary EvalSummary) error {
	switch summary.Status {
	case EvalStatusPassed, EvalStatusWarning, EvalStatusFailed, EvalStatusNotRun:
	default:
		return errors.New("eval summary has an invalid status")
	}
	if summary.Score != nil && (*summary.Score < 0 || *summary.Score > 1) {
		return errors.New("eval summary score must be between 0 and 1")
	}
	if err := validateEvalClass("evaluator", summary.Evaluator, allowedEvaluators); err != nil {
		return err
	}
	if err := validateEvalClass("reason class", summary.ReasonClass, allowedEvalReasonClasses); err != nil {
		return err
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal eval summary: %w", err)
	}
	if len(encoded) > maxEvalSummaryBytes {
		return fmt.Errorf("eval summary exceeds %d bytes", maxEvalSummaryBytes)
	}
	return nil
}

func validateEvalClass(field, value string, allowed map[string]struct{}) error {
	if value == "" {
		return nil
	}
	if _, ok := allowed[value]; !ok {
		return fmt.Errorf("eval summary %s is not an approved low-sensitivity classification", field)
	}
	return nil
}

func cloneEvalSummary(summary *EvalSummary) *EvalSummary {
	if summary == nil {
		return nil
	}
	copy := *summary
	if summary.Score != nil {
		score := *summary.Score
		copy.Score = &score
	}
	return &copy
}
