// Package chat contains the HTTP adapter for the non-streaming chat usecase.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	v1 "github.com/ashjazz/Longtermism/api/v1/chat"
	logicchat "github.com/ashjazz/Longtermism/internal/logic/chat"
	"github.com/ashjazz/Longtermism/pkg/ai/llm"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

const (
	chatInvalidRequestMessage      = "invalid chat request"
	chatRateLimitedMessage         = "chat rate limited"
	chatUpstreamUnavailableMessage = "chat upstream unavailable"
	chatUpstreamTimeoutMessage     = "chat upstream timeout"
	chatInternalErrorMessage       = "internal server error"

	// JSON escaping can expand one input byte to six bytes. Keep the transport limit bounded
	// without rejecting an otherwise valid 32 KiB decoded message that uses escaped characters.
	chatHTTPBodyMaxBytes = chatMessageMaxBytes*6 + 1024
)

// ChatUsecase is defined at the consuming edge so the controller cannot reach provider,
// telemetry, persistence, or platform-specific dependencies.
type ChatUsecase interface {
	Execute(context.Context, logicchat.ChatCommand) (logicchat.ChatResult, error)
}

type ChatControllerDependencies struct {
	Usecase               ChatUsecase
	RequestIDFromContext  func(context.Context) string
	SmokeRunIDFromContext func(context.Context) string
	DebugEnabled          bool
}

// ControllerV1 maps between the public DTO and application command/result only.
type ControllerV1 struct {
	usecase               ChatUsecase
	requestIDFromContext  func(context.Context) string
	smokeRunIDFromContext func(context.Context) string
	debugEnabled          bool
}

func NewV1(dependencies ChatControllerDependencies) *ControllerV1 {
	requestIDFromContext := dependencies.RequestIDFromContext
	if requestIDFromContext == nil {
		requestIDFromContext = func(context.Context) string { return "" }
	}
	smokeRunIDFromContext := dependencies.SmokeRunIDFromContext
	if smokeRunIDFromContext == nil {
		smokeRunIDFromContext = func(context.Context) string { return "" }
	}
	return &ControllerV1{
		usecase:               dependencies.Usecase,
		requestIDFromContext:  requestIDFromContext,
		smokeRunIDFromContext: smokeRunIDFromContext,
		debugEnabled:          dependencies.DebugEnabled,
	}
}

// Chat validates the passive API DTO before entering the AI usecase. Once the usecase starts,
// failures retain only its generated AI identity and a stable public classification.
func (controller *ControllerV1) Chat(ctx context.Context, request *v1.ChatReq) (*v1.ChatSuccessEnvelope, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if controller == nil {
		return nil, newChatControllerError(http.StatusInternalServerError, chatInternalErrorMessage, "", "")
	}
	requestID := controller.requestIDFromContext(ctx)
	if request == nil || ValidateChatRequest(ctx, *request) != nil {
		return nil, newChatControllerError(http.StatusBadRequest, chatInvalidRequestMessage, requestID, "")
	}
	if controller.usecase == nil {
		return nil, newChatControllerError(http.StatusInternalServerError, chatInternalErrorMessage, requestID, "")
	}

	result, err := controller.usecase.Execute(ctx, logicchat.ChatCommand{
		Message:    request.Message,
		SmokeRunID: controller.smokeRunIDFromContext(ctx),
	})
	if err != nil {
		return nil, mapChatUsecaseFailure(result, requestID, err)
	}
	return controller.mapChatSuccess(result, requestID)
}

func (controller *ControllerV1) mapChatSuccess(result logicchat.ChatResult, requestID string) (*v1.ChatSuccessEnvelope, error) {
	aiTraceID, validIdentity := boundAITraceID(result, requestID)
	if !validIdentity {
		return nil, newChatControllerError(http.StatusInternalServerError, chatInternalErrorMessage, requestID, "")
	}
	envelope, err := NewChatSuccessEnvelope(
		requestID,
		aiTraceID,
		mapChatData(result),
		mapEvalSummary(result.EvalSummary),
		controller.debugEnabled,
	)
	if err != nil {
		return nil, newChatControllerError(http.StatusInternalServerError, chatInternalErrorMessage, requestID, aiTraceID)
	}
	return &envelope, nil
}

func mapChatUsecaseFailure(result logicchat.ChatResult, requestID string, cause error) error {
	aiTraceID, validIdentity := boundAITraceID(result, requestID)
	if !validIdentity {
		return newChatControllerError(http.StatusInternalServerError, chatInternalErrorMessage, requestID, "")
	}
	statusCode, message := classifyChatControllerError(cause)
	return newChatControllerError(statusCode, message, requestID, aiTraceID)
}

func boundAITraceID(result logicchat.ChatResult, requestID string) (string, bool) {
	if requestID == "" ||
		result.Identity.RequestID != requestID ||
		!isValidAITraceID(result.Identity.AITraceID) {
		return "", false
	}
	return result.Identity.AITraceID, true
}

func isValidAITraceID(value string) bool {
	if len(value) == 0 || len(value) > 128 || !isASCIIAlphanumeric(value[0]) {
		return false
	}
	if obs.ContainsSensitivePayloadValue(value) {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if isASCIIAlphanumeric(character) || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func isASCIIAlphanumeric(character byte) bool {
	return character >= 'A' && character <= 'Z' ||
		character >= 'a' && character <= 'z' ||
		character >= '0' && character <= '9'
}

func classifyChatControllerError(err error) (int, string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, chatUpstreamTimeoutMessage
	case errors.Is(err, llm.ErrRateLimit):
		return http.StatusTooManyRequests, chatRateLimitedMessage
	case errors.Is(err, llm.ErrUpstream),
		errors.Is(err, logicchat.ErrChatProviderFailure),
		errors.Is(err, logicchat.ErrChatInvalidResponse):
		return http.StatusBadGateway, chatUpstreamUnavailableMessage
	default:
		return http.StatusInternalServerError, chatInternalErrorMessage
	}
}

func mapChatData(result logicchat.ChatResult) v1.ChatData {
	return v1.ChatData{
		Content:      result.Content,
		Model:        result.Model,
		FinishReason: v1.FinishReason(result.FinishReason),
		Usage: v1.UsageSummary{
			InputTokens:      int64(result.Usage.InputTokens),
			OutputTokens:     int64(result.Usage.OutputTokens),
			ReasoningTokens:  int64(result.Usage.ReasoningTokens),
			CacheReadTokens:  int64(result.Usage.CacheReadTokens),
			CacheWriteTokens: int64(result.Usage.CacheWriteTokens),
			TotalTokens:      int64(result.Usage.TotalTokens),
		},
	}
}

func mapEvalSummary(summary *logicchat.DebugEvalSummary) *v1.EvalSummary {
	if summary == nil {
		return nil
	}
	mapped := &v1.EvalSummary{
		Status:      v1.EvalStatus(summary.Status),
		Evaluator:   summary.Evaluator,
		ReasonClass: summary.ReasonClass,
	}
	if summary.Score != nil {
		score := *summary.Score
		mapped.Score = &score
	}
	return mapped
}

// ChatControllerError owns an already-sanitized response. It deliberately does not wrap the
// internal error because errors can contain provider bodies, endpoints, credentials, or input.
type ChatControllerError struct {
	statusCode int
	message    string
	envelope   any
}

func (controllerError ChatControllerError) Error() string {
	return controllerError.message
}

func (controllerError ChatControllerError) StatusCode() int {
	return controllerError.statusCode
}

func (controllerError ChatControllerError) Envelope() any {
	return controllerError.envelope
}

func newChatControllerError(statusCode int, message, requestID, aiTraceID string) error {
	if aiTraceID == "" && (statusCode == http.StatusBadRequest || statusCode == http.StatusTooManyRequests) {
		envelope, err := NewPreAIChatErrorEnvelope(statusCode, message, requestID)
		if err == nil {
			return ChatControllerError{statusCode: statusCode, message: message, envelope: envelope}
		}
	}
	if aiTraceID != "" &&
		(statusCode == http.StatusTooManyRequests ||
			statusCode == http.StatusBadGateway ||
			statusCode == http.StatusGatewayTimeout) {
		envelope, err := NewPostAIChatErrorEnvelope(statusCode, message, requestID, aiTraceID)
		if err == nil {
			return ChatControllerError{statusCode: statusCode, message: message, envelope: envelope}
		}
	}

	// A controller/composition failure can occur before or after AI identity creation. The
	// generic envelope preserves only identities that already exist and never invents one.
	envelope := chatInternalErrorEnvelope{
		Code:    statusCode,
		Message: message,
		Data:    nil,
		Meta: chatInternalErrorMeta{
			RequestID: requestID,
			AITraceID: aiTraceID,
		},
	}
	return ChatControllerError{statusCode: statusCode, message: message, envelope: envelope}
}

type chatInternalErrorEnvelope struct {
	Code    int                   `json:"code"`
	Message string                `json:"message"`
	Data    *v1.ErrorData         `json:"data"`
	Meta    chatInternalErrorMeta `json:"meta"`
}

type chatInternalErrorMeta struct {
	RequestID string `json:"request_id"`
	AITraceID string `json:"ai_trace_id,omitempty"`
}

type chatHTTPHandler struct {
	controller *ControllerV1
}

func NewHTTPHandler(controller *ControllerV1) http.Handler {
	return chatHTTPHandler{controller: controller}
}

// ServeHTTP performs strict body decoding before invoking Chat. It never logs or reflects the
// body, so malformed JSON and provider failures share the same low-sensitive response boundary.
func (handler chatHTTPHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	requestID := ""
	if handler.controller != nil {
		requestID = handler.controller.requestIDFromContext(ctx)
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Request-ID", requestID)

	if !hasJSONContentType(request.Header.Get("Content-Type")) {
		handler.writeError(writer, newChatControllerError(http.StatusBadRequest, chatInvalidRequestMessage, requestID, ""))
		return
	}
	body, err := readBoundedChatBody(request.Body)
	if err != nil {
		handler.writeError(writer, newChatControllerError(http.StatusBadRequest, chatInvalidRequestMessage, requestID, ""))
		return
	}
	decoded, err := DecodeAndValidateChatRequest(ctx, body)
	if err != nil {
		handler.writeError(writer, newChatControllerError(http.StatusBadRequest, chatInvalidRequestMessage, requestID, ""))
		return
	}
	if handler.controller == nil {
		handler.writeError(writer, newChatControllerError(http.StatusInternalServerError, chatInternalErrorMessage, requestID, ""))
		return
	}

	response, err := handler.controller.Chat(ctx, &decoded)
	if err != nil {
		handler.writeError(writer, err)
		return
	}
	if !writeJSONResponse(writer, http.StatusOK, response) {
		return
	}
}

func (handler chatHTTPHandler) writeError(writer http.ResponseWriter, err error) {
	var controllerError ChatControllerError
	if !errors.As(err, &controllerError) {
		controllerError = newChatControllerError(
			http.StatusInternalServerError,
			chatInternalErrorMessage,
			writer.Header().Get("X-Request-ID"),
			"",
		).(ChatControllerError)
	}
	if !writeJSONResponse(writer, controllerError.StatusCode(), controllerError.Envelope()) {
		return
	}
}

func hasJSONContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func readBoundedChatBody(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, errors.New("chat request body is required")
	}
	payload, err := io.ReadAll(io.LimitReader(body, chatHTTPBodyMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read chat request body: %w", err)
	}
	if len(payload) > chatHTTPBodyMaxBytes {
		return nil, errors.New("chat request body exceeds limit")
	}
	return payload, nil
}

// writeJSONResponse handles serialization before committing the status code. A network write
// failure cannot be repaired from http.Handler after the client disconnects, so false tells the
// caller to stop immediately without attempting a second, potentially mixed response.
func writeJSONResponse(writer http.ResponseWriter, statusCode int, value any) bool {
	payload, err := json.Marshal(value)
	if err != nil {
		return false
	}
	payload = append(payload, '\n')
	writer.WriteHeader(statusCode)
	if _, err := writer.Write(payload); err != nil {
		return false
	}
	return true
}

var (
	_ error        = ChatControllerError{}
	_ http.Handler = chatHTTPHandler{}
)
