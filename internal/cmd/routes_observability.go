package cmd

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/ratelimit"
	"github.com/gogf/gf/v2/net/ghttp"
)

const (
	infraSmokeHTTPPath      = "/api/v1/observability/infra-smoke"
	infraSmokeRateLimitKey  = "infra_smoke"
	defaultInfraSmokeRate   = 10
	defaultInfraSmokePeriod = time.Minute
)

// InfraSmokeRateLimitConfig is intentionally route-scoped. A smoke probe has a different
// abuse profile from product traffic, so sharing its bucket with health or future chat routes
// would make an observability diagnostic endpoint affect business availability.
type InfraSmokeRateLimitConfig struct {
	Rate   int
	Period time.Duration
}

// ObservabilityRoutesInput contains only the already-built bootstrap and HTTP edge adapters.
// It deliberately has no backend endpoint or AI identity field: route assembly cannot decide
// which observability plane receives a fact.
type ObservabilityRoutesInput struct {
	Bootstrap         *ObservabilityBootstrap
	InfraSmokeHandler ghttp.HandlerFunc
	Limiter           ratelimit.Limiter
	InfraSmokeLimit   InfraSmokeRateLimitConfig
	state             *observabilityRoutesState
}

type observabilityRoutesState struct {
	mu         sync.Mutex
	registered bool
}

// processObservabilityRoutesState owns registration for the default application server. Tests
// inject an isolated state, while repeated composition-root calls share this guard and cannot
// add a second route or middleware chain to the same process server.
var processObservabilityRoutesState observabilityRoutesState

// RegisterObservabilityRoutes installs process-local request identity once and conditionally
// exposes the infra-smoke route. It is idempotent because repeated application assembly must
// never create duplicate middleware, routes, token buckets, or provider-facing HTTP work.
func RegisterObservabilityRoutes(server *ghttp.Server, input ObservabilityRoutesInput) error {
	if server == nil {
		return fmt.Errorf("register observability routes: server is required")
	}
	if input.Bootstrap == nil {
		return fmt.Errorf("register observability routes: bootstrap is required")
	}
	if input.state == nil {
		return fmt.Errorf("register observability routes: state is required")
	}

	input.state.mu.Lock()
	defer input.state.mu.Unlock()
	if input.state.registered {
		return nil
	}

	// Identity belongs outside the feature gate: even an intentionally unavailable smoke route
	// must return the same caller/generated request ID for an operator to correlate its 404.
	// GoFrame applies default middleware after route-specific middleware, so enabled smoke routes
	// also bind this idempotent middleware before rate limiting; see RequestIdentityMiddleware.
	server.BindMiddlewareDefault(RequestIdentityMiddleware)
	if input.Bootstrap.InfraSmokeEnabled {
		if input.InfraSmokeHandler == nil {
			return fmt.Errorf("register observability routes: infra smoke handler is required when enabled")
		}
		configureInfraSmokeLimiter(input.Limiter, input.InfraSmokeLimit)
		server.BindMiddleware(infraSmokeHTTPPath,
			RequestIdentityMiddleware,
			wrapObservabilityHTTPMiddleware(input.Bootstrap.Middleware),
			infraSmokeRateLimitMiddleware(input.Limiter),
		)
		server.BindHandler("GET:"+infraSmokeHTTPPath, input.InfraSmokeHandler)
	}
	input.state.registered = true
	return nil
}

func configureInfraSmokeLimiter(limiter ratelimit.Limiter, config InfraSmokeRateLimitConfig) {
	if limiter == nil {
		return
	}
	if config.Rate <= 0 {
		config.Rate = defaultInfraSmokeRate
	}
	if config.Period <= 0 {
		config.Period = defaultInfraSmokePeriod
	}
	limiter.Configure(infraSmokeRateLimitKey, config.Rate, config.Period)
}

// wrapObservabilityHTTPMiddleware adapts the bootstrap's standard net/http middleware to
// GoFrame's middleware chain. The adapter invokes Next exactly once; the bootstrap owns only
// infrastructure tracing concerns, while request identity and rate limiting remain explicit.
func wrapObservabilityHTTPMiddleware(middleware func(http.Handler) http.Handler) ghttp.HandlerFunc {
	if middleware == nil {
		return func(request *ghttp.Request) { request.Middleware.Next() }
	}
	return func(request *ghttp.Request) {
		next := middleware(http.HandlerFunc(func(_ http.ResponseWriter, nextRequest *http.Request) {
			// Standard middleware (notably OTel) may replace Request.Context. Persist that
			// derived request before continuing GoFrame's chain so controller/usecase spans
			// stay children of the server span instead of starting a disconnected trace.
			request.Request = nextRequest
			request.SetCtx(nextRequest.Context())
			request.Middleware.Next()
		}))
		next.ServeHTTP(request.Response.BufferWriter, request.Request)
	}
}

func infraSmokeRateLimitMiddleware(limiter ratelimit.Limiter) ghttp.HandlerFunc {
	return func(request *ghttp.Request) {
		allowed, err := allowInfraSmokeRequest(request.GetCtx(), limiter)
		if err != nil || !allowed {
			request.Response.Status = http.StatusTooManyRequests
			request.Response.WriteJsonExit(newInfraSmokeRateLimitResponse(NewResponseMeta(request.GetCtx())))
			return
		}
		request.Middleware.Next()
	}
}

func allowInfraSmokeRequest(ctx context.Context, limiter ratelimit.Limiter) (bool, error) {
	if limiter == nil {
		return true, nil
	}
	return limiter.Allow(ctx, infraSmokeRateLimitKey)
}

// infraSmokeRateLimitResponse is intentionally marker-free. Rate-limit denials prove only
// that this route's bucket was exhausted; echoing a marker would create an unnecessary log/API
// disclosure path before the request reaches validated controller code.
type infraSmokeRateLimitResponse struct {
	Code    int          `json:"code"`
	Message string       `json:"message"`
	Data    any          `json:"data"`
	Meta    ResponseMeta `json:"meta"`
}

func newInfraSmokeRateLimitResponse(meta ResponseMeta) infraSmokeRateLimitResponse {
	return infraSmokeRateLimitResponse{
		Code:    http.StatusTooManyRequests,
		Message: "rate limit exceeded",
		Data:    nil,
		Meta:    meta,
	}
}
