package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	v1 "github.com/ashjazz/Longtermism/api/v1/chat"
	"github.com/gogf/gf/v2/util/gvalid"
)

const (
	chatMessageMaxBytes = 32 * 1024
	maxEvalSummaryBytes = 1024
)

// DecodeAndValidateChatRequest is the HTTP input boundary. GoFrame DTOs intentionally remain
// passive, so this adapter explicitly rejects unknown JSON fields before a caller can claim to
// select server-owned provider, model, endpoint, credential, or debug settings.
func DecodeAndValidateChatRequest(ctx context.Context, body []byte) (v1.ChatReq, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !utf8.Valid(body) {
		return v1.ChatReq{}, errors.New("chat request must be valid UTF-8 JSON")
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request v1.ChatReq
	if err := decoder.Decode(&request); err != nil {
		return v1.ChatReq{}, fmt.Errorf("decode chat request: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return v1.ChatReq{}, errors.New("chat request must contain one JSON object")
		}
		return v1.ChatReq{}, fmt.Errorf("decode trailing chat request data: %w", err)
	}
	if err := ValidateChatRequest(ctx, request); err != nil {
		return v1.ChatReq{}, err
	}
	return request, nil
}

func ValidateChatRequest(ctx context.Context, request v1.ChatReq) error {
	// GoFrame owns declarative, schema-level rules such as `required`. Keep its error
	// internal: a public handler must return a stable error category rather than details
	// derived from untrusted input.
	if err := gvalid.New().Data(request).Run(ctx); err != nil {
		return errors.New("chat request is missing required fields")
	}
	if !utf8.ValidString(request.Message) {
		return errors.New("chat message must be valid UTF-8")
	}
	if strings.TrimSpace(request.Message) == "" {
		return errors.New("chat message must not be blank")
	}
	if len(request.Message) > chatMessageMaxBytes {
		return fmt.Errorf("chat message exceeds %d bytes", chatMessageMaxBytes)
	}
	return nil
}

// NewChatSuccessEnvelope projects already-safe domain facts into the stable HTTP DTO. It copies
// debug data after validation so callers cannot mutate an evaluator-owned object while encoding.
func NewChatSuccessEnvelope(requestID, aiTraceID string, data v1.ChatData, summary *v1.EvalSummary, debugEnabled bool) (v1.ChatSuccessEnvelope, error) {
	if requestID == "" || aiTraceID == "" {
		return v1.ChatSuccessEnvelope{}, errors.New("chat success requires request and AI trace IDs")
	}
	meta := v1.ChatSuccessMeta{RequestID: requestID, AITraceID: aiTraceID}
	if debugEnabled {
		if summary == nil {
			summary = &v1.EvalSummary{
				Status:      v1.EvalStatusNotRun,
				ReasonClass: "evaluator_not_configured",
			}
		}
		if err := validateEvalSummary(*summary); err != nil {
			return v1.ChatSuccessEnvelope{}, err
		}
		meta.EvalSummary = cloneEvalSummary(summary)
	}
	return v1.ChatSuccessEnvelope{Code: 0, Message: "OK", Data: data, Meta: meta}, nil
}

func NewPreAIChatErrorEnvelope(code int, message, requestID string) (v1.ChatPreAIErrorEnvelope, error) {
	if code != 400 && code != 429 {
		return v1.ChatPreAIErrorEnvelope{}, errors.New("pre-AI chat error code must be 400 or 429")
	}
	if requestID == "" {
		return v1.ChatPreAIErrorEnvelope{}, errors.New("pre-AI chat error requires a request ID")
	}
	return v1.ChatPreAIErrorEnvelope{Code: code, Message: message, Data: nil, Meta: v1.ChatPreAIErrorMeta{RequestID: requestID}}, nil
}

func NewPostAIChatErrorEnvelope(code int, message, requestID, aiTraceID string) (v1.ChatPostAIErrorEnvelope, error) {
	if code != 429 && code != 502 && code != 504 {
		return v1.ChatPostAIErrorEnvelope{}, errors.New("post-AI chat error code must be 429, 502, or 504")
	}
	if requestID == "" || aiTraceID == "" {
		return v1.ChatPostAIErrorEnvelope{}, errors.New("post-AI chat error requires request and AI trace IDs")
	}
	return v1.ChatPostAIErrorEnvelope{Code: code, Message: message, Data: nil, Meta: v1.ChatPostAIErrorMeta{RequestID: requestID, AITraceID: aiTraceID}}, nil
}

func validateEvalSummary(summary v1.EvalSummary) error {
	if summary.Score != nil &&
		(math.IsNaN(*summary.Score) ||
			math.IsInf(*summary.Score, 0) ||
			*summary.Score < 0 ||
			*summary.Score > 1) {
		return errors.New("eval summary score must be between 0 and 1")
	}
	if !isValidCompletionContractSummary(summary) {
		return errors.New("eval summary fields do not form an approved completion contract result")
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

func isValidCompletionContractSummary(summary v1.EvalSummary) bool {
	if summary.Status == v1.EvalStatusNotRun {
		return summary.Evaluator == "" &&
			summary.Score == nil &&
			summary.ReasonClass == "evaluator_not_configured"
	}
	if summary.Evaluator != "completion_contract_v1" || summary.Score == nil {
		return false
	}
	switch {
	case summary.Status == v1.EvalStatusPassed &&
		*summary.Score == 1 &&
		summary.ReasonClass == "within_policy":
		return true
	case summary.Status == v1.EvalStatusWarning &&
		*summary.Score == 1 &&
		summary.ReasonClass == "threshold_not_configured":
		return true
	case summary.Status == v1.EvalStatusWarning &&
		*summary.Score == 0 &&
		isCompletionContractFailureReason(summary.ReasonClass):
		return true
	case summary.Status == v1.EvalStatusFailed &&
		*summary.Score == 0 &&
		isCompletionContractFailureReason(summary.ReasonClass):
		return true
	default:
		return false
	}
}

func isCompletionContractFailureReason(reasonClass string) bool {
	switch reasonClass {
	case "output_missing",
		"actual_model_missing",
		"finish_reason_invalid",
		"usage_inconsistent":
		return true
	default:
		return false
	}
}

func cloneEvalSummary(summary *v1.EvalSummary) *v1.EvalSummary {
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
