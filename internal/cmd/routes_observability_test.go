package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	v1observability "github.com/ashjazz/Longtermism/api/v1/observability"
	controllerobservability "github.com/ashjazz/Longtermism/internal/controller/observability"
	logicobservability "github.com/ashjazz/Longtermism/internal/logic/observability"
	"github.com/ashjazz/Longtermism/pkg/ai/ratelimit"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/os/gsession"
)

const infraSmokeRouteTestRequestID = "req-t040-route"
const infraSmokeRouteTestMarker = "run-t040-route"

func TestRegisterObservabilityRoutesGatesInfraSmokeAndPreservesRequestIdentity(t *testing.T) {
	tests := []struct {
		name         string
		smokeEnabled bool
		wantStatus   int
		wantCalls    int
	}{
		{name: "registers the enabled GET endpoint", smokeEnabled: true, wantStatus: http.StatusOK, wantCalls: 1},
		{name: "leaves the disabled endpoint unreachable", smokeEnabled: false, wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newObservabilityRouteTestServer(t)
			handler := &infraSmokeHandlerStub{}
			input := newObservabilityRoutesInput(tt.smokeEnabled, handler.handle, ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), &observabilityRoutesState{})
			if err := RegisterObservabilityRoutes(server, input); err != nil {
				t.Fatalf("RegisterObservabilityRoutes() error = %v", err)
			}

			response := serveInfraSmokeRoute(server)
			if response.Code != tt.wantStatus {
				t.Fatalf("GET infra-smoke status = %d, want %d", response.Code, tt.wantStatus)
			}
			if response.Header().Get(RequestIDHeader) != infraSmokeRouteTestRequestID {
				t.Fatalf("infra-smoke response request ID = %q, want caller identity", response.Header().Get(RequestIDHeader))
			}
			if handler.calls() != tt.wantCalls {
				t.Fatalf("infra-smoke handler calls = %d, want %d", handler.calls(), tt.wantCalls)
			}
			if tt.wantCalls == 1 && handler.requestID() != infraSmokeRouteTestRequestID {
				t.Fatalf("infra-smoke handler request ID = %q, want middleware identity", handler.requestID())
			}
		})
	}
}

func TestRegisterObservabilityRoutesLimitsOnlyInfraSmoke(t *testing.T) {
	server := newObservabilityRouteTestServer(t)
	limiter := ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{})
	handler := &infraSmokeHandlerStub{}
	input := newObservabilityRoutesInput(true, handler.handle, limiter, &observabilityRoutesState{})
	input.InfraSmokeLimit = InfraSmokeRateLimitConfig{Rate: 1, Period: time.Minute}
	if err := RegisterObservabilityRoutes(server, input); err != nil {
		t.Fatalf("RegisterObservabilityRoutes() error = %v", err)
	}

	first := serveInfraSmokeRoute(server)
	second := serveInfraSmokeRoute(server)
	if first.Code != http.StatusOK || second.Code != http.StatusTooManyRequests {
		t.Fatalf("infra-smoke rate limit statuses = %d,%d, want 200,429", first.Code, second.Code)
	}
	if second.Header().Get(RequestIDHeader) != infraSmokeRouteTestRequestID || !strings.Contains(second.Body.String(), `"message":"rate limit exceeded"`) || strings.Contains(second.Body.String(), infraSmokeRouteTestMarker) {
		t.Fatalf("429 response must retain a request ID, use a stable generic envelope, and expose no marker: %s", second.Body.String())
	}
	if handler.calls() != 1 {
		t.Fatalf("infra-smoke handler calls = %d, want only the admitted request", handler.calls())
	}

	healthRequest := httptest.NewRequest(http.MethodGet, "/api/v1/health/ping", nil)
	healthResponse := httptest.NewRecorder()
	server.ServeHTTP(healthResponse, healthRequest)
	if healthResponse.Code == http.StatusTooManyRequests {
		t.Fatal("infra-smoke limiter consumed or limited an unrelated health route")
	}
}

func TestRegisterObservabilityRoutesIsIdempotent(t *testing.T) {
	server := newObservabilityRouteTestServer(t)
	handler := &infraSmokeHandlerStub{}
	middleware := &countingHTTPMiddleware{}
	state := &observabilityRoutesState{}
	input := newObservabilityRoutesInput(true, handler.handle, ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), state)
	input.Bootstrap.Middleware = middleware.wrap

	if err := RegisterObservabilityRoutes(server, input); err != nil {
		t.Fatalf("first RegisterObservabilityRoutes() error = %v", err)
	}
	if err := RegisterObservabilityRoutes(server, input); err != nil {
		t.Fatalf("second RegisterObservabilityRoutes() error = %v", err)
	}

	response := serveInfraSmokeRoute(server)
	if response.Code != http.StatusOK || handler.calls() != 1 || middleware.calls() != 1 {
		t.Fatalf("repeated route assembly caused duplicate behavior: status=%d handler=%d middleware=%d", response.Code, handler.calls(), middleware.calls())
	}
	if routes := countInfraSmokeRoutes(server.GetRoutes()); routes != 1 {
		t.Fatalf("infra-smoke route count = %d, want exactly one", routes)
	}
}

// 该测试覆盖真实 HTTP 边界，而非仅检查 Req tag：query 或 body 中伪造同名字段时，
// smoke marker 仍必须只来自受控 header，避免未验证输入进入 span 与结构化日志。
func TestDefaultInfraSmokeHandlerReadsRunMarkerFromHeaderOnly(t *testing.T) {
	server := newObservabilityRouteTestServer(t)
	usecase := logicobservability.NewInfraSmokeUsecase(logicobservability.InfraSmokeUsecaseDependencies{})
	controller := controllerobservability.NewV1(controllerobservability.InfraSmokeControllerDependencies{
		SmokeEnabled:         true,
		Runner:               usecase,
		RequestIDFromContext: RequestIDFromContext,
	})
	input := newObservabilityRoutesInput(true, newInfraSmokeHTTPHandler(controller), ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), &observabilityRoutesState{})
	if err := RegisterObservabilityRoutes(server, input); err != nil {
		t.Fatalf("RegisterObservabilityRoutes() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, infraSmokeHTTPPath+"?"+v1observability.SmokeRunIDHeader+"=query-marker", strings.NewReader(`{"`+v1observability.SmokeRunIDHeader+`":"body-marker"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(RequestIDHeader, infraSmokeRouteTestRequestID)
	request.Header.Set(v1observability.SmokeRunIDHeader, infraSmokeRouteTestMarker)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"smoke_run_id":"`+infraSmokeRouteTestMarker+`"`) || strings.Contains(response.Body.String(), "query-marker") || strings.Contains(response.Body.String(), "body-marker") {
		t.Fatalf("header-only infra smoke response = status:%d body:%s", response.Code, response.Body.String())
	}
}

func TestRegisterObservabilityRoutesPreservesGeneratedRequestID(t *testing.T) {
	server := newObservabilityRouteTestServer(t)
	handler := &infraSmokeHandlerStub{}
	input := newObservabilityRoutesInput(true, handler.handle, ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), &observabilityRoutesState{})
	if err := RegisterObservabilityRoutes(server, input); err != nil {
		t.Fatalf("RegisterObservabilityRoutes() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, infraSmokeHTTPPath, nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || handler.requestID() == "" || response.Header().Get(RequestIDHeader) != handler.requestID() {
		t.Fatalf("generated request identity diverged: status=%d handler=%q response=%q", response.Code, handler.requestID(), response.Header().Get(RequestIDHeader))
	}
}

// 退出路径必须能安全消费 noop bootstrap：默认离线配置没有 exporter/provider，但仍要
// 经过同一 deadline-bound flush/shutdown 流程，避免生产与本地拥有两套生命周期语义。
func TestShutdownObservabilityBootstrapAllowsNoopBootstrap(t *testing.T) {
	shutdownObservabilityBootstrap(&ObservabilityBootstrap{})
}

func newObservabilityRouteTestServer(t *testing.T) *ghttp.Server {
	t.Helper()
	server := ghttp.GetServer("t040-" + strings.ReplaceAll(t.Name(), "/", "-"))
	server.SetDumpRouterMap(false)
	server.SetSessionStorage(gsession.NewStorageMemory())
	server.SetPort(0)
	server.BindHandler("GET:/api/v1/health/ping", func(request *ghttp.Request) {
		request.Response.WriteStatus(http.StatusOK)
	})
	if err := server.Start(); err != nil {
		t.Fatalf("start route test server: %v", err)
	}
	t.Cleanup(func() { server.Shutdown() })
	return server
}

func newObservabilityRoutesInput(enabled bool, handler ghttp.HandlerFunc, limiter ratelimit.Limiter, state *observabilityRoutesState) ObservabilityRoutesInput {
	return ObservabilityRoutesInput{
		Bootstrap:         &ObservabilityBootstrap{InfraSmokeEnabled: enabled},
		InfraSmokeHandler: handler,
		Limiter:           limiter,
		InfraSmokeLimit:   InfraSmokeRateLimitConfig{Rate: 10, Period: time.Minute},
		state:             state,
	}
}

func serveInfraSmokeRoute(server *ghttp.Server) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, infraSmokeHTTPPath, nil)
	request.Header.Set(RequestIDHeader, infraSmokeRouteTestRequestID)
	request.Header.Set("X-Observability-Smoke-Run-ID", infraSmokeRouteTestMarker)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func countInfraSmokeRoutes(routes []ghttp.RouterItem) int {
	count := 0
	for _, route := range routes {
		if route.Method == http.MethodGet && route.Route == infraSmokeHTTPPath && route.Handler.Type == ghttp.HandlerTypeHandler {
			count++
		}
	}
	return count
}

type infraSmokeHandlerStub struct {
	mu             sync.Mutex
	count          int
	requestIDValue string
}

func (h *infraSmokeHandlerStub) handle(request *ghttp.Request) {
	h.mu.Lock()
	h.count++
	h.requestIDValue = RequestIDFromContext(request.GetCtx())
	h.mu.Unlock()
	request.Response.WriteStatus(http.StatusOK)
}

func (h *infraSmokeHandlerStub) requestID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.requestIDValue
}

func (h *infraSmokeHandlerStub) calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

type countingHTTPMiddleware struct {
	mu    sync.Mutex
	count int
}

func (m *countingHTTPMiddleware) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		m.mu.Lock()
		m.count++
		m.mu.Unlock()
		next.ServeHTTP(writer, request)
	})
}

func (m *countingHTTPMiddleware) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}

// ---------------------------------------------------------------------------
// T199（RED）：`/api/v1/observability/smoke/marker-count` 是应用自有的 AI-negative
// 事实源端口。传输边界必须先于事实读取完成统一拒绝：smoke gate 关闭时 404；
// 非 loopback peer、缺失/错误 credential 时 401；畸形 marker/window 时 400——
// 所有拒绝路径的 handler/usecase 调用次数都必须为 0。
// ---------------------------------------------------------------------------

const aiPlaneMarkerCountRouteTestCredential = "ai-plane-read-credential"

func newObservabilityRoutesInputWithAIPlane(enabled bool, infraHandler, aiPlaneHandler ghttp.HandlerFunc, credential string, limiter ratelimit.Limiter, state *observabilityRoutesState) ObservabilityRoutesInput {
	input := newObservabilityRoutesInput(enabled, infraHandler, limiter, state)
	input.AIPlaneMarkerCountHandler = aiPlaneHandler
	input.AIPlaneCredential = credential
	return input
}

func serveAIPlaneMarkerCountRoute(server *ghttp.Server, authorization, remoteAddr, rawQuery string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, aiPlaneMarkerCountHTTPPath+"?"+rawQuery, nil)
	request.RemoteAddr = remoteAddr
	if authorization != "" {
		request.Header.Set("Authorization", "Basic "+authorization)
	}
	request.Header.Set(RequestIDHeader, infraSmokeRouteTestRequestID)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func TestRegisterObservabilityRoutesGatesAIPlaneMarkerCount(t *testing.T) {
	t.Run("enabled endpoint requires the shared read credential", func(t *testing.T) {
		server := newObservabilityRouteTestServer(t)
		handler := &infraSmokeHandlerStub{}
		input := newObservabilityRoutesInputWithAIPlane(true, handler.handle, handler.handle, "", ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), &observabilityRoutesState{})
		if err := RegisterObservabilityRoutes(server, input); err == nil {
			t.Fatal("RegisterObservabilityRoutes() error = nil, want a partially protected marker-count endpoint to fail closed")
		}
	})

	t.Run("enabled endpoint rejects a trivially short credential", func(t *testing.T) {
		server := newObservabilityRouteTestServer(t)
		handler := &infraSmokeHandlerStub{}
		input := newObservabilityRoutesInputWithAIPlane(true, handler.handle, handler.handle, "short", ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), &observabilityRoutesState{})
		if err := RegisterObservabilityRoutes(server, input); err == nil {
			t.Fatal("RegisterObservabilityRoutes() error = nil, want a short ai plane credential to fail closed")
		}
	})

	t.Run("enabled endpoint is reachable with the credential", func(t *testing.T) {
		server := newObservabilityRouteTestServer(t)
		handler := &infraSmokeHandlerStub{}
		input := newObservabilityRoutesInputWithAIPlane(true, handler.handle, handler.handle, aiPlaneMarkerCountRouteTestCredential, ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), &observabilityRoutesState{})
		if err := RegisterObservabilityRoutes(server, input); err != nil {
			t.Fatalf("RegisterObservabilityRoutes() error = %v", err)
		}
		response := serveAIPlaneMarkerCountRoute(server, aiPlaneMarkerCountRouteTestCredential, "127.0.0.1:51234", "")
		if response.Code != http.StatusOK || handler.calls() != 1 {
			t.Fatalf("marker-count status = %d calls = %d, want an admitted loopback query", response.Code, handler.calls())
		}
	})

	t.Run("disabled smoke gate leaves the endpoint unreachable", func(t *testing.T) {
		server := newObservabilityRouteTestServer(t)
		handler := &infraSmokeHandlerStub{}
		input := newObservabilityRoutesInputWithAIPlane(false, handler.handle, handler.handle, aiPlaneMarkerCountRouteTestCredential, ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), &observabilityRoutesState{})
		if err := RegisterObservabilityRoutes(server, input); err != nil {
			t.Fatalf("RegisterObservabilityRoutes() error = %v", err)
		}
		response := serveAIPlaneMarkerCountRoute(server, aiPlaneMarkerCountRouteTestCredential, "127.0.0.1:51234", "")
		if response.Code != http.StatusNotFound || handler.calls() != 0 {
			t.Fatalf("disabled marker-count status = %d calls = %d, want 404 with zero reads", response.Code, handler.calls())
		}
	})

	t.Run("marker-count protection never affects the infra-smoke route", func(t *testing.T) {
		server := newObservabilityRouteTestServer(t)
		handler := &infraSmokeHandlerStub{}
		input := newObservabilityRoutesInputWithAIPlane(true, handler.handle, handler.handle, aiPlaneMarkerCountRouteTestCredential, ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), &observabilityRoutesState{})
		if err := RegisterObservabilityRoutes(server, input); err != nil {
			t.Fatalf("RegisterObservabilityRoutes() error = %v", err)
		}
		response := serveInfraSmokeRoute(server)
		if response.Code != http.StatusOK || handler.calls() != 1 {
			t.Fatalf("infra-smoke status = %d calls = %d, want unchanged open route", response.Code, handler.calls())
		}
	})
}

// marker-count 拥有独立路由桶：负向检查端口仍受有界限流保护，429 响应 marker-free，
// 且不会消耗或影响 infra-smoke 自己的桶。
func TestRegisterObservabilityRoutesLimitsAIPlaneMarkerCount(t *testing.T) {
	server := newObservabilityRouteTestServer(t)
	limiter := ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{})
	handler := &infraSmokeHandlerStub{}
	input := newObservabilityRoutesInputWithAIPlane(true, handler.handle, handler.handle, aiPlaneMarkerCountRouteTestCredential, limiter, &observabilityRoutesState{})
	input.AIPlaneMarkerCountLimit = InfraSmokeRateLimitConfig{Rate: 1, Period: time.Minute}
	if err := RegisterObservabilityRoutes(server, input); err != nil {
		t.Fatalf("RegisterObservabilityRoutes() error = %v", err)
	}

	first := serveAIPlaneMarkerCountRoute(server, aiPlaneMarkerCountRouteTestCredential, "127.0.0.1:51234", "")
	second := serveAIPlaneMarkerCountRoute(server, aiPlaneMarkerCountRouteTestCredential, "127.0.0.1:51234", "")
	if first.Code != http.StatusOK || second.Code != http.StatusTooManyRequests {
		t.Fatalf("marker-count rate limit statuses = %d,%d, want 200,429", first.Code, second.Code)
	}
	if strings.Contains(second.Body.String(), aiPlaneMarkerCountRouteTestCredential) {
		t.Fatalf("429 response leaked the credential: %s", second.Body.String())
	}
	if handler.calls() != 1 {
		t.Fatalf("marker-count handler calls = %d, want only the admitted request", handler.calls())
	}

	infraResponse := serveInfraSmokeRoute(server)
	if infraResponse.Code != http.StatusOK {
		t.Fatalf("infra-smoke status = %d, want its own rate bucket untouched by marker-count traffic", infraResponse.Code)
	}
}

func TestAIPlaneMarkerCountAdmissionRejectsUniformlyBeforeHandler(t *testing.T) {
	server := newObservabilityRouteTestServer(t)
	handler := &infraSmokeHandlerStub{}
	input := newObservabilityRoutesInputWithAIPlane(true, handler.handle, handler.handle, aiPlaneMarkerCountRouteTestCredential, ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), &observabilityRoutesState{})
	if err := RegisterObservabilityRoutes(server, input); err != nil {
		t.Fatalf("RegisterObservabilityRoutes() error = %v", err)
	}

	tests := []struct {
		name          string
		authorization string
		remoteAddr    string
	}{
		{name: "missing credential", authorization: "", remoteAddr: "127.0.0.1:51234"},
		{name: "wrong credential", authorization: "wrong-credential", remoteAddr: "127.0.0.1:51234"},
		{name: "remote peer", authorization: aiPlaneMarkerCountRouteTestCredential, remoteAddr: "203.0.113.9:4321"},
		{name: "malformed peer address", authorization: aiPlaneMarkerCountRouteTestCredential, remoteAddr: "not-an-address"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := serveAIPlaneMarkerCountRoute(server, tt.authorization, tt.remoteAddr, "marker=run-t199-admission")
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("admission rejection status = %d, want uniform 401", response.Code)
			}
			body := response.Body.String()
			leakedCredential := (tt.authorization != "" && strings.Contains(body, tt.authorization)) || strings.Contains(body, aiPlaneMarkerCountRouteTestCredential)
			if strings.Contains(body, "run-t199-admission") || leakedCredential {
				t.Fatalf("admission rejection leaked marker or credential: %s", body)
			}
			if response.Header().Get(RequestIDHeader) != infraSmokeRouteTestRequestID {
				t.Fatalf("admission rejection lost the request identity: %q", response.Header().Get(RequestIDHeader))
			}
		})
	}
	if handler.calls() != 0 {
		t.Fatalf("handler calls = %d, want zero fact reads for every rejection", handler.calls())
	}
}

func aiPlaneMarkerCountQueryValues(t *testing.T, marker, startedAt, deadline string) string {
	t.Helper()
	values := url.Values{}
	if marker != "" {
		values.Set("marker", marker)
	}
	if startedAt != "" {
		values.Set("started_at", startedAt)
	}
	if deadline != "" {
		values.Set("deadline", deadline)
	}
	return values.Encode()
}

func decodeAIPlaneMarkerCountBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("marker-count response is not JSON: %v", err)
	}
	return decoded
}

func TestAIPlaneMarkerCountHandlerRejectsInvalidQueriesBeforeUsecase(t *testing.T) {
	runner := &aiPlaneMarkerCountRunnerStub{count: 2}
	server := newObservabilityRouteTestServer(t)
	infraStub := &infraSmokeHandlerStub{}
	input := newObservabilityRoutesInputWithAIPlane(true, infraStub.handle, newAIPlaneMarkerCountHTTPHandler(runner), aiPlaneMarkerCountRouteTestCredential, ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), &observabilityRoutesState{})
	if err := RegisterObservabilityRoutes(server, input); err != nil {
		t.Fatalf("RegisterObservabilityRoutes() error = %v", err)
	}

	now := time.Now()
	validStarted := now.Add(-30 * time.Second).UTC().Format(time.RFC3339Nano)
	validDeadline := now.Add(30 * time.Second).UTC().Format(time.RFC3339Nano)

	t.Run("valid bounded query reaches the usecase", func(t *testing.T) {
		response := serveAIPlaneMarkerCountRoute(server, aiPlaneMarkerCountRouteTestCredential, "127.0.0.1:51234", aiPlaneMarkerCountQueryValues(t, "run-t199-ok", validStarted, validDeadline))
		if response.Code != http.StatusOK {
			t.Fatalf("valid query status = %d, want 200", response.Code)
		}
		body := decodeAIPlaneMarkerCountBody(t, response.Body.String())
		data, ok := body["data"].(map[string]any)
		if !ok || data["count"] != float64(2) {
			t.Fatalf("marker-count data = %#v, want the usecase count", body["data"])
		}
		meta, ok := body["meta"].(map[string]any)
		if !ok || meta["request_id"] != infraSmokeRouteTestRequestID {
			t.Fatalf("marker-count meta = %#v, want the request identity", body["meta"])
		}
		if runner.calls != 1 || runner.marker != "run-t199-ok" {
			t.Fatalf("usecase received calls=%d marker=%q, want one exact marker", runner.calls, runner.marker)
		}
		wantStarted, _ := time.Parse(time.RFC3339Nano, validStarted)
		wantDeadline, _ := time.Parse(time.RFC3339Nano, validDeadline)
		if !runner.startedAt.Equal(wantStarted) || !runner.deadline.Equal(wantDeadline) {
			t.Fatalf("usecase window = [%s, %s], want the bounded query window", runner.startedAt, runner.deadline)
		}
	})

	tests := []struct {
		name      string
		marker    string
		startedAt string
		deadline  string
	}{
		{name: "missing marker", startedAt: validStarted, deadline: validDeadline},
		{name: "invalid marker", marker: "bad marker!", startedAt: validStarted, deadline: validDeadline},
		{name: "missing started at", marker: "run-t199-window", deadline: validDeadline},
		{name: "malformed started at", marker: "run-t199-window", startedAt: "not-a-time", deadline: validDeadline},
		{name: "missing deadline", marker: "run-t199-window", startedAt: validStarted},
		{name: "started at after deadline", marker: "run-t199-window", startedAt: validDeadline, deadline: validStarted},
		{name: "window exceeds one minute", marker: "run-t199-window", startedAt: now.Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano), deadline: now.Add(time.Second).UTC().Format(time.RFC3339Nano)},
		{name: "stale window start", marker: "run-t199-window", startedAt: now.Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano), deadline: now.Add(-time.Minute).UTC().Format(time.RFC3339Nano)},
		{name: "future window deadline", marker: "run-t199-window", startedAt: now.Add(time.Minute).UTC().Format(time.RFC3339Nano), deadline: now.Add(2 * time.Minute).UTC().Format(time.RFC3339Nano)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := runner.calls
			response := serveAIPlaneMarkerCountRoute(server, aiPlaneMarkerCountRouteTestCredential, "127.0.0.1:51234", aiPlaneMarkerCountQueryValues(t, tt.marker, tt.startedAt, tt.deadline))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid query status = %d, want uniform 400", response.Code)
			}
			if runner.calls != before {
				t.Fatalf("usecase calls = %d, want zero fact reads for an invalid query", runner.calls)
			}
			if strings.Contains(response.Body.String(), tt.marker) && tt.marker != "" {
				t.Fatalf("400 response echoed the marker: %s", response.Body.String())
			}
		})
	}
}

func TestAIPlaneMarkerCountHandlerMapsUseCaseErrorsToStableEnvelopes(t *testing.T) {
	now := time.Now()
	query := aiPlaneMarkerCountQueryValues(t, "run-t199-error", now.Add(-time.Second).UTC().Format(time.RFC3339Nano), now.Add(time.Second).UTC().Format(time.RFC3339Nano))
	tests := []struct {
		name        string
		runnerErr   error
		wantStatus  int
		wantMessage string
	}{
		{name: "invalid query class maps to 400", runnerErr: classedAIPlaneTestError{class: "invalid_query"}, wantStatus: http.StatusBadRequest, wantMessage: "invalid ai plane marker count query"},
		{name: "fact source failure maps to 503", runnerErr: classedAIPlaneTestError{class: "query_failed"}, wantStatus: http.StatusServiceUnavailable, wantMessage: "ai plane fact source unavailable"},
		{name: "unclassified failure maps to 500", runnerErr: errors.New("raw fact source internals"), wantStatus: http.StatusInternalServerError, wantMessage: "internal server error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &aiPlaneMarkerCountRunnerStub{err: tt.runnerErr}
			server := newObservabilityRouteTestServer(t)
			infraStub := &infraSmokeHandlerStub{}
			input := newObservabilityRoutesInputWithAIPlane(true, infraStub.handle, newAIPlaneMarkerCountHTTPHandler(runner), aiPlaneMarkerCountRouteTestCredential, ratelimit.NewMemoryLimiter(ratelimit.MemoryLimiterConfig{}), &observabilityRoutesState{})
			if err := RegisterObservabilityRoutes(server, input); err != nil {
				t.Fatalf("RegisterObservabilityRoutes() error = %v", err)
			}
			response := serveAIPlaneMarkerCountRoute(server, aiPlaneMarkerCountRouteTestCredential, "127.0.0.1:51234", query)
			if response.Code != tt.wantStatus {
				t.Fatalf("error status = %d, want %d", response.Code, tt.wantStatus)
			}
			body := decodeAIPlaneMarkerCountBody(t, response.Body.String())
			if body["message"] != tt.wantMessage {
				t.Fatalf("error message = %#v, want stable %q", body["message"], tt.wantMessage)
			}
			if strings.Contains(response.Body.String(), "run-t199-error") || strings.Contains(response.Body.String(), "raw fact source internals") {
				t.Fatalf("error envelope leaked marker or source internals: %s", response.Body.String())
			}
			if body["data"] != nil {
				t.Fatalf("error data = %#v, want null", body["data"])
			}
		})
	}
}

type aiPlaneMarkerCountRunnerStub struct {
	calls     int
	marker    string
	startedAt time.Time
	deadline  time.Time
	count     int
	err       error
}

func (s *aiPlaneMarkerCountRunnerStub) Count(_ context.Context, input logicobservability.AIPlaneMarkerCountInput) (int, error) {
	s.calls++
	s.marker, s.startedAt, s.deadline = input.Marker, input.StartedAt, input.Deadline
	return s.count, s.err
}

type classedAIPlaneTestError struct{ class string }

func (e classedAIPlaneTestError) Error() string { return "stable test failure" }
func (e classedAIPlaneTestError) Class() string { return e.class }
