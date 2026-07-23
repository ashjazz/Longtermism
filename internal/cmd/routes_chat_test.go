package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"github.com/ashjazz/Longtermism/pkg/ai/ratelimit"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/os/gsession"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
)

const (
	chatRouteTestRequestID = "req-t076-chat"
	chatRouteTestInput     = "private-chat-input-t076"
)

func TestRegisterChatRoutesGatesChatAndPreservesRequestContext(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		wantCode int
		wantCall int
	}{
		{name: "enabled", enabled: true, wantCode: http.StatusOK, wantCall: 1},
		{name: "disabled", enabled: false, wantCode: http.StatusNotFound, wantCall: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newChatRouteTestServer(t)
			handler := &chatRouteHandlerStub{}
			input := newChatRoutesInput(tt.enabled, handler.handle, ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), &chatRoutesState{})
			if err := RegisterChatRoutes(server, input); err != nil {
				t.Fatalf("RegisterChatRoutes() error = %v", err)
			}

			response := serveChatRoute(server)
			if response.Code != tt.wantCode || response.Header().Get(RequestIDHeader) != chatRouteTestRequestID {
				t.Fatalf("chat response = status:%d request_id:%q, want status:%d caller request ID", response.Code, response.Header().Get(RequestIDHeader), tt.wantCode)
			}
			if handler.calls() != tt.wantCall {
				t.Fatalf("chat handler calls = %d, want %d", handler.calls(), tt.wantCall)
			}
			if tt.wantCall == 1 {
				assertChatRouteIngressIdentity(t, handler)
			}
		})
	}
}

func TestRegisterChatRoutesUsesAnIndependentConfiguredRateLimit(t *testing.T) {
	server := newChatRouteTestServer(t)
	limiter := ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{})
	handler := &chatRouteHandlerStub{startAIUsecase: true}
	infraHandler := &infraSmokeHandlerStub{}
	infraInput := newObservabilityRoutesInput(true, infraHandler.handle, limiter, &observabilityRoutesState{})
	infraInput.InfraSmokeLimit = InfraSmokeRateLimitConfig{Rate: 1, Period: time.Minute}
	if err := RegisterObservabilityRoutes(server, infraInput); err != nil {
		t.Fatalf("RegisterObservabilityRoutes() error = %v", err)
	}
	input := newChatRoutesInput(true, handler.handle, limiter, &chatRoutesState{})
	input.Limit = ChatRateLimitConfig{Rate: 1, Period: time.Minute}
	if err := RegisterChatRoutes(server, input); err != nil {
		t.Fatalf("RegisterChatRoutes() error = %v", err)
	}

	// 先耗尽真实 infra_smoke bucket：若 chat 错误复用该 key，首个 chat 请求就会被拒绝。
	firstInfra := serveInfraSmokeRoute(server)
	secondInfra := serveInfraSmokeRoute(server)
	if firstInfra.Code != http.StatusOK || secondInfra.Code != http.StatusTooManyRequests {
		t.Fatalf("infra rate limit statuses = %d,%d, want 200,429", firstInfra.Code, secondInfra.Code)
	}

	first := serveChatRoute(server)
	second := serveChatRoute(server)
	if first.Code != http.StatusOK || second.Code != http.StatusTooManyRequests {
		t.Fatalf("chat rate limit statuses = %d,%d, want 200,429", first.Code, second.Code)
	}
	assertChatRateLimitResponse(t, second)
	if handler.calls() != 1 || handler.aiStarts() != 1 {
		t.Fatalf("rate-limited request must not enter handler/usecase start, calls=%d ai_starts=%d", handler.calls(), handler.aiStarts())
	}

	healthRequest := httptest.NewRequest(http.MethodGet, "/api/v1/health/ping", nil)
	healthResponse := httptest.NewRecorder()
	server.ServeHTTP(healthResponse, healthRequest)
	if healthResponse.Code == http.StatusTooManyRequests {
		t.Fatal("chat limiter must not limit an unrelated health route")
	}
}

func TestRegisterChatRoutesDoesNotCreateAIIdentityBeforeTheUsecaseStarts(t *testing.T) {
	server := newChatRouteTestServer(t)
	handler := &chatRouteHandlerStub{startAIUsecase: true}
	if err := RegisterChatRoutes(server, newChatRoutesInput(true, handler.handle, ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), &chatRoutesState{})); err != nil {
		t.Fatalf("RegisterChatRoutes() error = %v", err)
	}

	response := serveChatRoute(server)
	if response.Code != http.StatusOK || handler.calls() != 1 || handler.aiStarts() != 1 {
		t.Fatalf("chat route = status:%d calls:%d ai_starts:%d, want one actual usecase start", response.Code, handler.calls(), handler.aiStarts())
	}
	if handler.preUsecaseAITraceID() != "" || handler.preUsecaseAIPlaneMarker() != "" || handler.startedAITraceID() != "ai-t076-created-in-usecase" || handler.startedAIPlaneMarker() != "ai" {
		t.Fatalf("AI marker timing = before_trace:%q before_marker:%q after_trace:%q after_marker:%q, route must not manufacture AI facts", handler.preUsecaseAITraceID(), handler.preUsecaseAIPlaneMarker(), handler.startedAITraceID(), handler.startedAIPlaneMarker())
	}
}

// 公网 chat 入口不是可信 AI 调用链的延续。即使通用 propagator 为受信服务链路保留
// plane marker，chat 路由也必须在 controller/usecase 之前清除这个可伪造的语义事实。
func TestRegisterChatRoutesClearsUntrustedInboundAIPlaneMarker(t *testing.T) {
	server := newChatRouteTestServer(t)
	handler := &chatRouteHandlerStub{}
	if err := RegisterChatRoutes(server, newChatRoutesInput(true, handler.handle, ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), &chatRoutesState{})); err != nil {
		t.Fatalf("RegisterChatRoutes() error = %v", err)
	}

	requestContext := NewObservabilityIngressPropagator(ObservabilityIngressTrustPolicy{}).Extract(context.Background(), propagation.MapCarrier{
		"baggage": observabilityPlaneBaggageKey + "=ai," + obs.BaggageAITraceID + "=forged-ai-t076," + obs.BaggageEvalRunID + "=forged-eval-t076",
	})
	response := serveChatRouteWithContext(server, requestContext)
	identity, _ := handler.preUsecaseIdentity()
	if response.Code != http.StatusOK || handler.preUsecaseAIPlaneMarker() != "" || handler.preUsecaseAITraceID() != "" || identity.EvalRunID != "" {
		t.Fatalf("untrusted inbound AI facts = status:%d marker:%q trace:%q eval:%q, want request-only ingress", response.Code, handler.preUsecaseAIPlaneMarker(), handler.preUsecaseAITraceID(), identity.EvalRunID)
	}
}

func TestRegisterChatRoutesIsIdempotentAndValidatesEnabledDependencies(t *testing.T) {
	server := newChatRouteTestServer(t)
	handler := &chatRouteHandlerStub{}
	state := &chatRoutesState{}
	input := newChatRoutesInput(true, handler.handle, ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), state)
	if err := RegisterChatRoutes(server, input); err != nil {
		t.Fatalf("first RegisterChatRoutes() error = %v", err)
	}
	if err := RegisterChatRoutes(server, input); err != nil {
		t.Fatalf("second RegisterChatRoutes() error = %v", err)
	}
	if response := serveChatRoute(server); response.Code != http.StatusOK || handler.calls() != 1 {
		t.Fatalf("idempotent registration response=%d handler_calls=%d, want one handler invocation", response.Code, handler.calls())
	}
	if routes := countChatRoutes(server.GetRoutes()); routes != 1 {
		t.Fatalf("chat route count = %d, want exactly one", routes)
	}

	if err := RegisterChatRoutes(nil, input); err == nil {
		t.Fatal("RegisterChatRoutes(nil) error = nil, want fail-fast")
	}
	if err := RegisterChatRoutes(newChatRouteTestServer(t), ChatRoutesInput{Enabled: true, state: &chatRoutesState{}}); err == nil {
		t.Fatal("enabled chat without handler error = nil, want fail-fast")
	}
}

func assertChatRouteIngressIdentity(t *testing.T, handler *chatRouteHandlerStub) {
	t.Helper()
	if handler.requestID() != chatRouteTestRequestID || handler.routeTemplate() != chatHTTPPath {
		t.Fatalf("chat request context = request:%q route:%q, want request:%q route:%q", handler.requestID(), handler.routeTemplate(), chatRouteTestRequestID, chatHTTPPath)
	}
	identity, ok := handler.preUsecaseIdentity()
	if !ok || identity.RequestID != chatRouteTestRequestID || identity.AITraceID != "" || identity.EvalRunID != "" {
		t.Fatalf("chat ingress correlation identity = %#v present=%t, want request-only identity before usecase", identity, ok)
	}
}

func assertChatRateLimitResponse(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var envelope struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
		Meta    ResponseMeta    `json:"meta"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode chat 429 response: %v", err)
	}
	if envelope.Code != http.StatusTooManyRequests || envelope.Message != "rate limit exceeded" || string(envelope.Data) != "null" || envelope.Meta.RequestID != chatRouteTestRequestID || response.Header().Get(RequestIDHeader) != envelope.Meta.RequestID {
		t.Fatalf("chat 429 response = header:%q body:%s, want stable request-only envelope", response.Header().Get(RequestIDHeader), response.Body.Bytes())
	}
	// 429 是网关层拒绝，不能附带尚未创建的 AI 事实或用户原始输入。
	// 注意 envelope 本身需要 message 字段，因此只检查敏感/AI 语义字段。
	for _, forbidden := range []string{"ai_trace_id", "prompt", "content"} {
		if strings.Contains(strings.ToLower(response.Body.String()), `"`+forbidden+`"`) {
			t.Fatalf("chat 429 must not expose %q: %s", forbidden, response.Body.String())
		}
	}
	if strings.Contains(response.Body.String(), chatRouteTestInput) {
		t.Fatalf("chat 429 must not echo user input: %s", response.Body.String())
	}
}

func newChatRouteTestServer(t *testing.T) *ghttp.Server {
	t.Helper()
	server := ghttp.GetServer("t076-" + strings.ReplaceAll(t.Name(), "/", "-"))
	server.SetDumpRouterMap(false)
	server.SetSessionStorage(gsession.NewStorageMemory())
	server.SetPort(0)
	server.BindHandler("GET:/api/v1/health/ping", func(request *ghttp.Request) { request.Response.WriteStatus(http.StatusOK) })
	if err := server.Start(); err != nil {
		t.Fatalf("start chat route test server: %v", err)
	}
	t.Cleanup(func() { server.Shutdown() })
	return server
}

func newChatRoutesInput(enabled bool, handler ghttp.HandlerFunc, limiter ratelimit.Limiter, state *chatRoutesState) ChatRoutesInput {
	return ChatRoutesInput{
		Enabled: enabled,
		Handler: handler,
		Limiter: limiter,
		Limit:   ChatRateLimitConfig{Rate: 10, Period: time.Minute},
		state:   state,
	}
}

func serveChatRoute(server *ghttp.Server) *httptest.ResponseRecorder {
	return serveChatRouteWithContext(server, context.Background())
}

func serveChatRouteWithContext(server *ghttp.Server, requestContext context.Context) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, chatHTTPPath, strings.NewReader(`{"message":"`+chatRouteTestInput+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(RequestIDHeader, chatRouteTestRequestID)
	request = request.WithContext(requestContext)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func countChatRoutes(routes []ghttp.RouterItem) int {
	count := 0
	for _, route := range routes {
		if route.Method == http.MethodPost && route.Route == chatHTTPPath && route.Handler.Type == ghttp.HandlerTypeHandler {
			count++
		}
	}
	return count
}

type chatRouteHandlerStub struct {
	mu                        sync.Mutex
	count                     int
	requestIDValue            string
	routeTemplateValue        string
	preIdentity               obs.CorrelationIdentity
	preIdentityOK             bool
	preAIPlaneMarker          string
	startedIdentity           obs.CorrelationIdentity
	startedAIPlaneMarkerValue string
	startAIUsecase            bool
}

func (h *chatRouteHandlerStub) handle(request *ghttp.Request) {
	h.mu.Lock()
	h.count++
	h.requestIDValue = RequestIDFromContext(request.GetCtx())
	h.routeTemplateValue = RouteTemplateFromContext(request.GetCtx())
	h.preIdentity, h.preIdentityOK = obs.CorrelationIdentityFromContext(request.GetCtx())
	h.preAIPlaneMarker = baggage.FromContext(request.GetCtx()).Member(observabilityPlaneBaggageKey).Value()
	if h.startAIUsecase {
		// 模拟真实 usecase 开始 AI 调用时才写入的 AI 平面标记；路由层在此之前不能猜测。
		marker, err := baggage.NewMemberRaw(observabilityPlaneBaggageKey, "ai")
		if err != nil {
			h.mu.Unlock()
			request.Response.WriteStatus(http.StatusInternalServerError)
			return
		}
		aiContext := baggage.ContextWithBaggage(request.GetCtx(), mustAppendBaggage(marker, baggage.FromContext(request.GetCtx())))
		request.SetCtx(aiContext)
		h.startedIdentity = obs.NewCorrelationIdentity(h.requestIDValue, obs.WithAITraceID("ai-t076-created-in-usecase"))
		h.startedAIPlaneMarkerValue = baggage.FromContext(request.GetCtx()).Member(observabilityPlaneBaggageKey).Value()
	}
	h.mu.Unlock()
	request.Response.WriteStatus(http.StatusOK)
}

func (h *chatRouteHandlerStub) calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}
func (h *chatRouteHandlerStub) requestID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.requestIDValue
}
func (h *chatRouteHandlerStub) routeTemplate() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.routeTemplateValue
}
func (h *chatRouteHandlerStub) preUsecaseIdentity() (obs.CorrelationIdentity, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.preIdentity, h.preIdentityOK
}
func (h *chatRouteHandlerStub) preUsecaseAITraceID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.preIdentity.AITraceID
}
func (h *chatRouteHandlerStub) preUsecaseAIPlaneMarker() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.preAIPlaneMarker
}
func (h *chatRouteHandlerStub) startedAITraceID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.startedIdentity.AITraceID
}
func (h *chatRouteHandlerStub) startedAIPlaneMarker() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.startedAIPlaneMarkerValue
}
func (h *chatRouteHandlerStub) aiStarts() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.startedIdentity.AITraceID != "" {
		return 1
	}
	return 0
}

func mustAppendBaggage(member baggage.Member, current baggage.Baggage) baggage.Baggage {
	members := append(current.Members(), member)
	updated, err := baggage.New(members...)
	if err != nil {
		return current
	}
	return updated
}
