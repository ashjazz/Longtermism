// Package chat fixes the HTTP-facing chat controller boundary.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/ashjazz/Longtermism/api/v1/chat"
	logicchat "github.com/ashjazz/Longtermism/internal/logic/chat"
	"github.com/ashjazz/Longtermism/pkg/ai/llm"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

func TestChatControllerRejectsInvalidRequestsBeforeUsecase(t *testing.T) {
	tests := []struct {
		name    string
		request *v1.ChatReq
	}{
		{name: "nil request", request: nil},
		{name: "empty message", request: &v1.ChatReq{}},
		{name: "whitespace message", request: &v1.ChatReq{Message: " \t\n　"}},
		{name: "overlong message", request: &v1.ChatReq{Message: strings.Repeat("a", 32*1024+1)}},
		{name: "invalid UTF-8", request: &v1.ChatReq{Message: string([]byte{0xff})}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usecase := &chatUsecaseStub{}
			controller := NewV1(ChatControllerDependencies{
				Usecase:              usecase,
				RequestIDFromContext: func(context.Context) string { return "req-t075-invalid" },
			})

			response, err := controller.Chat(context.Background(), tt.request)
			if response != nil {
				t.Fatalf("Chat() response = %#v, want nil on client error", response)
			}
			assertChatControllerError(t, err, 400, "invalid chat request", "req-t075-invalid", "", nil)
			if usecase.calls != 0 {
				t.Fatalf("usecase calls = %d, want 0 before valid JSON DTO reaches application logic", usecase.calls)
			}
		})
	}
}

func TestChatControllerMapsUsecaseResultAndServerDebugPolicy(t *testing.T) {
	identity := obs.NewCorrelationIdentity(
		"req-t075-success",
		obs.WithServiceSpan("service-trace-t075", "span-t075"),
		obs.WithAITraceID("ai-trace-t075-success"),
	)
	score := 1.0
	result := logicchat.ChatResult{
		Content:      "A completed answer.",
		Model:        "provider-actual-model",
		FinishReason: llm.FinishStop,
		Usage:        llm.Usage{InputTokens: 11, OutputTokens: 17, TotalTokens: 28},
		Identity:     identity,
		EvalSummary: &logicchat.DebugEvalSummary{
			Status:      logicchat.EvalStatusPassed,
			Evaluator:   "deterministic_completion_contract_v1",
			Score:       &score,
			ReasonClass: "within_policy",
		},
	}

	for _, debugEnabled := range []bool{false, true} {
		t.Run("debug="+boolString(debugEnabled), func(t *testing.T) {
			usecase := &chatUsecaseStub{result: result}
			controller := NewV1(ChatControllerDependencies{
				Usecase:              usecase,
				RequestIDFromContext: func(context.Context) string { return "req-t075-success" },
				DebugEnabled:         debugEnabled,
			})

			response, err := controller.Chat(context.Background(), &v1.ChatReq{Message: "Explain the observability loop."})
			if err != nil {
				t.Fatalf("Chat() error = %v", err)
			}
			if usecase.calls != 1 || usecase.command != (logicchat.ChatCommand{Message: "Explain the observability loop."}) {
				t.Fatalf("controller must forward only the validated message command, calls=%d command=%#v", usecase.calls, usecase.command)
			}
			assertChatControllerSuccess(t, response, "req-t075-success", "ai-trace-t075-success", debugEnabled)
		})
	}
}

func TestChatControllerMapsStableFailureClassesAndKeepsStartedAIIdentity(t *testing.T) {
	identity := obs.NewCorrelationIdentity("req-t075-failure", obs.WithAITraceID("ai-trace-t075-failure"))
	tests := []struct {
		name        string
		cause       error
		wantCode    int
		wantMessage string
	}{
		{name: "deadline takes precedence over joined upstream errors", cause: errors.Join(context.DeadlineExceeded, llm.ErrUpstream), wantCode: 504, wantMessage: "chat upstream timeout"},
		{name: "deadline takes precedence over rate limit and upstream errors", cause: errors.Join(context.DeadlineExceeded, llm.ErrRateLimit, llm.ErrUpstream), wantCode: 504, wantMessage: "chat upstream timeout"},
		{name: "rate limit takes precedence over joined upstream errors", cause: errors.Join(llm.ErrRateLimit, llm.ErrUpstream), wantCode: 429, wantMessage: "chat rate limited"},
		{name: "upstream unavailable", cause: llm.ErrUpstream, wantCode: 502, wantMessage: "chat upstream unavailable"},
		{name: "unclassified failure", cause: errors.New("unexpected failure"), wantCode: 500, wantMessage: "internal server error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usecase := &chatUsecaseStub{result: logicchat.ChatResult{Identity: identity}, err: tt.cause}
			controller := NewV1(ChatControllerDependencies{
				Usecase:              usecase,
				RequestIDFromContext: func(context.Context) string { return "req-t075-failure" },
			})

			response, err := controller.Chat(context.Background(), &v1.ChatReq{Message: "Can I retain correlation when this fails?"})
			if response != nil {
				t.Fatalf("Chat() response = %#v, want nil on mapped failure", response)
			}
			assertChatControllerError(t, err, tt.wantCode, tt.wantMessage, "req-t075-failure", "ai-trace-t075-failure", tt.cause)
			if usecase.calls != 1 {
				t.Fatalf("usecase calls = %d, want 1", usecase.calls)
			}
		})
	}
}

func TestChatHTTPHandlerKeepsRequestIDHeaderConsistentWithResponseMeta(t *testing.T) {
	tests := []struct {
		name        string
		usecase     *chatUsecaseStub
		wantStatus  int
		wantAITrace bool
	}{
		{
			name: "success",
			usecase: &chatUsecaseStub{result: logicchat.ChatResult{
				Content: "completed", Model: "provider-actual-model", FinishReason: llm.FinishStop,
				Usage:    llm.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
				Identity: obs.NewCorrelationIdentity("req-t075-http", obs.WithAITraceID("ai-trace-t075-http")),
			}},
			wantStatus: http.StatusOK, wantAITrace: true,
		},
		{
			name: "started AI upstream failure",
			usecase: &chatUsecaseStub{
				result: logicchat.ChatResult{Identity: obs.NewCorrelationIdentity("req-t075-http", obs.WithAITraceID("ai-trace-t075-http"))},
				err:    llm.ErrUpstream,
			},
			wantStatus: http.StatusBadGateway, wantAITrace: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := NewV1(ChatControllerDependencies{
				Usecase:              tt.usecase,
				RequestIDFromContext: func(context.Context) string { return "req-t075-http" },
			})
			handler := NewHTTPHandler(controller)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(`{"message":"hello"}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("HTTP status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			var body struct {
				Meta struct {
					RequestID string `json:"request_id"`
					AITraceID string `json:"ai_trace_id"`
				} `json:"meta"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode handler response: %v", err)
			}
			if recorder.Header().Get("X-Request-ID") != body.Meta.RequestID || body.Meta.RequestID != "req-t075-http" {
				t.Fatalf("X-Request-ID=%q response meta request_id=%q, want req-t075-http", recorder.Header().Get("X-Request-ID"), body.Meta.RequestID)
			}
			if (body.Meta.AITraceID != "") != tt.wantAITrace {
				t.Fatalf("response ai_trace_id=%q, want present=%v", body.Meta.AITraceID, tt.wantAITrace)
			}
		})
	}
}

func TestChatHTTPHandlerRejectsMalformedJSONBeforeUsecase(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "malformed object", body: `{"message":`},
		{name: "message has wrong JSON type", body: `{"message":123}`},
		{name: "unknown server-owned field", body: `{"message":"hello","provider":"caller-controlled"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usecase := &chatUsecaseStub{}
			controller := NewV1(ChatControllerDependencies{
				Usecase:              usecase,
				RequestIDFromContext: func(context.Context) string { return "req-t075-json" },
			})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/v1/chat", strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			NewHTTPHandler(controller).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("HTTP status = %d, want 400", recorder.Code)
			}
			var body struct {
				Code    int             `json:"code"`
				Message string          `json:"message"`
				Data    json.RawMessage `json:"data"`
				Meta    struct {
					RequestID string `json:"request_id"`
					AITraceID string `json:"ai_trace_id"`
				} `json:"meta"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode handler error response: %v", err)
			}
			if body.Code != http.StatusBadRequest || body.Message != "invalid chat request" || string(body.Data) != "null" || body.Meta.RequestID != "req-t075-json" || body.Meta.AITraceID != "" {
				t.Fatalf("JSON validation envelope = %s, want stable 400 without ai_trace_id", recorder.Body.Bytes())
			}
			if recorder.Header().Get("X-Request-ID") != body.Meta.RequestID {
				t.Fatalf("X-Request-ID=%q response meta request_id=%q, want equal", recorder.Header().Get("X-Request-ID"), body.Meta.RequestID)
			}
			if usecase.calls != 0 {
				t.Fatalf("usecase calls = %d, want 0 after invalid JSON", usecase.calls)
			}
		})
	}
}

func TestChatControllerNeverReflectsSensitiveUsecaseDetails(t *testing.T) {
	const messageMarker = "user-message-t075-private"
	const providerBodyMarker = "provider-body-t075-private"
	const endpointMarker = "https://private.t075.example.invalid/v1/chat"
	const credentialMarker = "Bearer t075-private-token"
	internalFailure := errors.Join(llm.ErrUpstream, errors.New(providerBodyMarker+" endpoint="+endpointMarker+" authorization="+credentialMarker+" message="+messageMarker))
	usecase := &chatUsecaseStub{
		result: logicchat.ChatResult{Identity: obs.NewCorrelationIdentity("req-t075-private", obs.WithAITraceID("ai-trace-t075-private"))},
		err:    internalFailure,
	}
	controller := NewV1(ChatControllerDependencies{
		Usecase:              usecase,
		RequestIDFromContext: func(context.Context) string { return "req-t075-private" },
	})

	_, err := controller.Chat(context.Background(), &v1.ChatReq{Message: messageMarker})
	assertChatControllerError(t, err, 502, "chat upstream unavailable", "req-t075-private", "ai-trace-t075-private", internalFailure)
	var controllerError ChatControllerError
	if !errors.As(err, &controllerError) {
		t.Fatalf("Chat() error type = %T, want ChatControllerError", err)
	}
	payload, marshalErr := json.Marshal(controllerError.Envelope())
	if marshalErr != nil {
		t.Fatalf("marshal error envelope: %v", marshalErr)
	}
	for _, forbidden := range []string{messageMarker, providerBodyMarker, endpointMarker, credentialMarker, "content", "model", "usage", "eval_summary"} {
		if strings.Contains(string(payload), forbidden) || strings.Contains(err.Error(), forbidden) {
			t.Fatalf("controller exposed forbidden failure detail %q", forbidden)
		}
	}
}

func TestChatControllerDependsOnlyOnUsecaseInterface(t *testing.T) {
	dependenciesType := reflect.TypeFor[ChatControllerDependencies]()
	for index := range dependenciesType.NumField() {
		field := dependenciesType.Field(index)
		name := strings.ToLower(field.Name)
		for _, prohibited := range []string{"provider", "telemetry", "endpoint", "credential", "secret", "apikey"} {
			if strings.Contains(name, prohibited) {
				t.Fatalf("ChatControllerDependencies must not own %q field %q", prohibited, field.Name)
			}
		}
	}
	usecaseField, ok := dependenciesType.FieldByName("Usecase")
	if !ok || usecaseField.Type.Kind() != reflect.Interface || usecaseField.Type.NumMethod() != 1 {
		t.Fatal("ChatControllerDependencies.Usecase must be a narrow consumer-side interface")
	}
}

func assertChatControllerSuccess(t *testing.T, response *v1.ChatSuccessEnvelope, requestID, aiTraceID string, wantDebugSummary bool) {
	t.Helper()
	if response == nil {
		t.Fatal("Chat() response = nil, want success envelope")
	}
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal success envelope: %v", err)
	}
	var decoded struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
		Meta struct {
			RequestID   string          `json:"request_id"`
			AITraceID   string          `json:"ai_trace_id"`
			EvalSummary json.RawMessage `json:"eval_summary"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal success envelope: %v", err)
	}
	if decoded.Code != 0 || decoded.Meta.RequestID != requestID || decoded.Meta.AITraceID != aiTraceID {
		t.Fatalf("success envelope identity = %s, want request_id=%q ai_trace_id=%q", payload, requestID, aiTraceID)
	}
	var data struct {
		Content      string          `json:"content"`
		Model        string          `json:"model"`
		FinishReason string          `json:"finish_reason"`
		Usage        json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(decoded.Data, &data); err != nil {
		t.Fatalf("unmarshal success data: %v", err)
	}
	if data.Content != "A completed answer." || data.Model != "provider-actual-model" || data.FinishReason != string(llm.FinishStop) || data.Usage == nil {
		t.Fatalf("success data = %s, want forwarded provider result", decoded.Data)
	}
	if (decoded.Meta.EvalSummary != nil) != wantDebugSummary {
		t.Fatalf("success eval_summary presence = %v, want %v", decoded.Meta.EvalSummary != nil, wantDebugSummary)
	}
}

func assertChatControllerError(t *testing.T, err error, wantCode int, wantMessage, requestID, aiTraceID string, internalErr error) {
	t.Helper()
	if err == nil {
		t.Fatal("Chat() error = nil, want mapped controller error")
	}
	var controllerError ChatControllerError
	if !errors.As(err, &controllerError) {
		t.Fatalf("Chat() error type = %T, want ChatControllerError", err)
	}
	if controllerError.StatusCode() != wantCode || controllerError.Error() != wantMessage {
		t.Fatalf("controller error = status:%d message:%q, want status:%d message:%q", controllerError.StatusCode(), controllerError.Error(), wantCode, wantMessage)
	}
	payload, marshalErr := json.Marshal(controllerError.Envelope())
	if marshalErr != nil {
		t.Fatalf("marshal controller error envelope: %v", marshalErr)
	}
	var decoded struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
		Meta    struct {
			RequestID string `json:"request_id"`
			AITraceID string `json:"ai_trace_id"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal controller error envelope: %v", err)
	}
	if decoded.Code != wantCode || decoded.Message != wantMessage || string(decoded.Data) != "null" || decoded.Meta.RequestID != requestID || decoded.Meta.AITraceID != aiTraceID {
		t.Fatalf("controller error envelope = %s, want code/message/data/meta identities", payload)
	}
	if internalErr != nil && strings.Contains(string(payload), internalErr.Error()) {
		t.Fatal("controller envelope must not reflect the full internal error")
	}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

type chatUsecaseStub struct {
	calls   int
	command logicchat.ChatCommand
	result  logicchat.ChatResult
	err     error
}

func (u *chatUsecaseStub) Execute(_ context.Context, command logicchat.ChatCommand) (logicchat.ChatResult, error) {
	u.calls++
	u.command = command
	return u.result, u.err
}
