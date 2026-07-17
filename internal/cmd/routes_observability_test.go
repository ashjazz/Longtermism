package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/ratelimit"
	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/os/gsession"
)

const infraSmokeHTTPPath = "/api/v1/observability/infra-smoke"
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
