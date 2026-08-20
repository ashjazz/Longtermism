package cmd

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	v1observability "github.com/ashjazz/Longtermism/api/v1/observability"
	logicobservability "github.com/ashjazz/Longtermism/internal/logic/observability"
	"github.com/ashjazz/Longtermism/pkg/ai/ratelimit"
	"github.com/gogf/gf/v2/net/ghttp"
)

const (
	infraSmokeHTTPPath      = "/api/v1/observability/infra-smoke"
	infraSmokeRateLimitKey  = "infra_smoke"
	defaultInfraSmokeRate   = 10
	defaultInfraSmokePeriod = time.Minute

	aiPlaneMarkerCountHTTPPath            = "/api/v1/observability/smoke/marker-count"
	aiPlaneMarkerCountRateLimitKey        = "ai_plane_marker_count"
	minimumAIPlaneMarkerCountCredential   = 16
	defaultAIPlaneMarkerCountRate         = 10
	defaultAIPlaneMarkerCountPeriod       = 10 * time.Second
	aiPlaneMarkerCountInvalidMessage      = "invalid ai plane marker count query"
	aiPlaneMarkerCountUnavailableMessage  = "ai plane fact source unavailable"
	aiPlaneMarkerCountUnauthorizedMessage = "unauthorized"
	aiPlaneMarkerCountInternalMessage     = "internal server error"
	aiPlaneMarkerCountMaximumWindow       = time.Minute
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
	Bootstrap                   *ObservabilityBootstrap
	CompletionLoggingMiddleware func(http.Handler) http.Handler
	InfraSmokeHandler           ghttp.HandlerFunc
	Limiter                     ratelimit.Limiter
	InfraSmokeLimit             InfraSmokeRateLimitConfig
	// AIPlaneMarkerCountHandler 与 AIPlaneCredential 配对启用受保护的 AI-negative
	// marker-count 端口：handler 存在时 credential 必须达到最小长度，否则拒绝注册一个
	// 部分受保护的事实源端点。二者皆零值时端口保持关闭（显式 opt-in）。
	AIPlaneMarkerCountHandler ghttp.HandlerFunc
	AIPlaneCredential         string
	AIPlaneMarkerCountLimit   InfraSmokeRateLimitConfig
	state                     *observabilityRoutesState
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
	// marker-count 的 credential 校验必须先于任何 BindMiddleware/BindHandler 副作用：
	// 注册失败时不允许留下半绑定的 middleware 或可重试时重复绑定。
	if input.Bootstrap.InfraSmokeEnabled && input.AIPlaneMarkerCountHandler != nil &&
		len(strings.TrimSpace(input.AIPlaneCredential)) < minimumAIPlaneMarkerCountCredential {
		return fmt.Errorf("register observability routes: ai plane credential must be at least %d bytes when marker count is registered", minimumAIPlaneMarkerCountCredential)
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
			wrapObservabilityHTTPMiddleware(chainObservabilityHTTPMiddleware(input.Bootstrap.Middleware, input.CompletionLoggingMiddleware)),
			infraSmokeRateLimitMiddleware(input.Limiter),
		)
		server.BindHandler("GET:"+infraSmokeHTTPPath, input.InfraSmokeHandler)
		// marker-count 端口是显式扩展：handler 存在才注册。它拥有独立路由桶与独立
		// loopback+credential admission，缺项已在副作用前 fail-closed。
		if input.AIPlaneMarkerCountHandler != nil {
			configureAIPlaneMarkerCountLimiter(input.Limiter, input.AIPlaneMarkerCountLimit)
			server.BindMiddleware(aiPlaneMarkerCountHTTPPath,
				RequestIdentityMiddleware,
				wrapObservabilityHTTPMiddleware(chainObservabilityHTTPMiddleware(input.Bootstrap.Middleware, input.CompletionLoggingMiddleware)),
				observabilityRateLimitMiddleware(input.Limiter, aiPlaneMarkerCountRateLimitKey),
				aiPlaneMarkerCountAdmissionMiddleware(input.AIPlaneCredential),
			)
			server.BindHandler("GET:"+aiPlaneMarkerCountHTTPPath, input.AIPlaneMarkerCountHandler)
		}
	}
	input.state.registered = true
	return nil
}

func chainObservabilityHTTPMiddleware(middlewares ...func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		for index := len(middlewares) - 1; index >= 0; index-- {
			if middlewares[index] != nil {
				next = middlewares[index](next)
			}
		}
		return next
	}
}

func configureInfraSmokeLimiter(limiter ratelimit.Limiter, config InfraSmokeRateLimitConfig) {
	configureObservabilityRouteLimiter(limiter, infraSmokeRateLimitKey, config, defaultInfraSmokeRate, defaultInfraSmokePeriod)
}

// configureAIPlaneMarkerCountLimiter 给 marker-count 独立的路由桶：它是被 poller
// 按 1s 间隔反复查询的负向检查端口，默认每 10 秒 10 个 token（1/s），既不拖垮
// 真实 smoke 轮询，也限制本机恶意进程的查询洪泛。
func configureAIPlaneMarkerCountLimiter(limiter ratelimit.Limiter, config InfraSmokeRateLimitConfig) {
	configureObservabilityRouteLimiter(limiter, aiPlaneMarkerCountRateLimitKey, config, defaultAIPlaneMarkerCountRate, defaultAIPlaneMarkerCountPeriod)
}

func configureObservabilityRouteLimiter(limiter ratelimit.Limiter, key string, config InfraSmokeRateLimitConfig, defaultRate int, defaultPeriod time.Duration) {
	if limiter == nil {
		return
	}
	if config.Rate <= 0 {
		config.Rate = defaultRate
	}
	if config.Period <= 0 {
		config.Period = defaultPeriod
	}
	limiter.Configure(key, config.Rate, config.Period)
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
	return observabilityRateLimitMiddleware(limiter, infraSmokeRateLimitKey)
}

func observabilityRateLimitMiddleware(limiter ratelimit.Limiter, key string) ghttp.HandlerFunc {
	return func(request *ghttp.Request) {
		allowed, err := allowInfraSmokeRequest(request.GetCtx(), limiter, key)
		if err != nil || !allowed {
			request.Response.Status = http.StatusTooManyRequests
			request.Response.WriteJsonExit(newInfraSmokeRateLimitResponse(NewResponseMeta(request.GetCtx())))
			return
		}
		request.Middleware.Next()
	}
}

func allowInfraSmokeRequest(ctx context.Context, limiter ratelimit.Limiter, key string) (bool, error) {
	if limiter == nil {
		return true, nil
	}
	return limiter.Allow(ctx, key)
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

// ---------------------------------------------------------------------------
// AI-negative marker-count 端口（T200）。
//
// 端点只读、loopback-only、受独立 Basic credential 保护，输出只有 bounded count
// 与稳定低敏错误。所有拒绝路径都发生在 usecase/事实源读取之前。
// ---------------------------------------------------------------------------

// aiPlaneMarkerCountAdmissionMiddleware 在事实读取前统一拒绝：缺失/错误 credential
// 或非 loopback peer 都返回同一个 401 envelope，不区分原因以避免开关/credential
// oracle；比较使用恒定时间摘要，避免时序侧信道。credential 与 smoke CLI 的
// Authorization 值逐字一致（"Basic <credential>"），不做 RFC 7617 base64 解码——
// 两侧都由本仓库契约固定，运维不要擅自对 env 值做 base64 转换。
// loopback 判定复用 chat smoke admission 的同一 helper（isExactChatSmokeLoopback），
// 它刻意拒绝 "::ffff:127.0.0.1" 等非规范形式，fail-closed。
func aiPlaneMarkerCountAdmissionMiddleware(credential string) ghttp.HandlerFunc {
	expected := sha256.Sum256([]byte("Basic " + credential))
	return func(request *ghttp.Request) {
		authorization := request.Header.Get("Authorization")
		digest := sha256.Sum256([]byte(authorization))
		authenticated := subtle.ConstantTimeCompare(digest[:], expected[:]) == 1
		if !authenticated || !isExactChatSmokeLoopback(request.RemoteAddr) {
			request.Response.Status = http.StatusUnauthorized
			request.Response.WriteJsonExit(aiPlaneMarkerCountErrorEnvelope{
				Code: http.StatusUnauthorized, Message: aiPlaneMarkerCountUnauthorizedMessage, Data: nil,
				Meta: ResponseMeta{RequestID: RequestIDFromContext(request.GetCtx())},
			})
			return
		}
		request.Middleware.Next()
	}
}

type aiPlaneMarkerCountData struct {
	Count int `json:"count"`
}

// aiPlaneMarkerCountSuccessEnvelope 与既有 AIPlaneSmokeQueryClient 期望的
// `data.count` 形状一致：code=0 + bounded count + request identity。
type aiPlaneMarkerCountSuccessEnvelope struct {
	Code    int                    `json:"code"`
	Message string                 `json:"message"`
	Data    aiPlaneMarkerCountData `json:"data"`
	Meta    ResponseMeta           `json:"meta"`
}

type aiPlaneMarkerCountErrorEnvelope struct {
	Code    int          `json:"code"`
	Message string       `json:"message"`
	Data    any          `json:"data"`
	Meta    ResponseMeta `json:"meta"`
}

// newAIPlaneMarkerCountHTTPHandler 是 marker-count 端点的 HTTP 边界：先解析并校验
// marker+window（零 usecase 调用），再把 usecase 错误按稳定类别映射为低敏 envelope。
// 响应绝不回显 marker、window 或事实源内部错误。
func newAIPlaneMarkerCountHTTPHandler(runner logicobservability.AIPlaneMarkerCountRunner) ghttp.HandlerFunc {
	return func(request *ghttp.Request) {
		// 配置错误（nil runner）优先于输入校验：组装层缺失必须按 500 呈现，而不是
		// 被畸形查询伪装成 400。
		if runner == nil {
			writeAIPlaneMarkerCountError(request, http.StatusInternalServerError, aiPlaneMarkerCountInternalMessage)
			return
		}
		input, parseErr := parseAIPlaneMarkerCountQuery(request, time.Now())
		if parseErr != nil {
			writeAIPlaneMarkerCountError(request, http.StatusBadRequest, aiPlaneMarkerCountInvalidMessage)
			return
		}
		count, err := runner.Count(request.GetCtx(), input)
		if err != nil {
			class := ""
			var classed interface{ Class() string }
			if errors.As(err, &classed) {
				class = classed.Class()
			}
			switch class {
			case logicobservability.AIPlaneMarkerCountInvalidQueryClass:
				writeAIPlaneMarkerCountError(request, http.StatusBadRequest, aiPlaneMarkerCountInvalidMessage)
			case logicobservability.AIPlaneMarkerCountQueryFailedClass:
				writeAIPlaneMarkerCountError(request, http.StatusServiceUnavailable, aiPlaneMarkerCountUnavailableMessage)
			default:
				writeAIPlaneMarkerCountError(request, http.StatusInternalServerError, aiPlaneMarkerCountInternalMessage)
			}
			return
		}
		request.Response.WriteJsonExit(aiPlaneMarkerCountSuccessEnvelope{
			Code: 0, Message: "OK",
			Data: aiPlaneMarkerCountData{Count: count},
			Meta: ResponseMeta{RequestID: RequestIDFromContext(request.GetCtx())},
		})
	}
}

func writeAIPlaneMarkerCountError(request *ghttp.Request, statusCode int, message string) {
	request.Response.Status = statusCode
	request.Response.WriteJsonExit(aiPlaneMarkerCountErrorEnvelope{
		Code: statusCode, Message: message, Data: nil,
		Meta: ResponseMeta{RequestID: RequestIDFromContext(request.GetCtx())},
	})
}

// parseAIPlaneMarkerCountQuery 在 usecase 之前完成传输边界校验。usecase 会再次校验
// 同样的 domain 不变量（纵深防御）；这里拒绝意味着零事实读取。
func parseAIPlaneMarkerCountQuery(request *ghttp.Request, now time.Time) (logicobservability.AIPlaneMarkerCountInput, error) {
	query := request.Request.URL.Query()
	marker := query.Get("marker")
	if marker == "" || !v1observability.IsValidSmokeRunID(marker) {
		return logicobservability.AIPlaneMarkerCountInput{}, errors.New("invalid ai plane marker")
	}
	startedAt, err := time.Parse(time.RFC3339Nano, query.Get("started_at"))
	if err != nil || startedAt.IsZero() {
		return logicobservability.AIPlaneMarkerCountInput{}, errors.New("invalid ai plane window start")
	}
	deadline, err := time.Parse(time.RFC3339Nano, query.Get("deadline"))
	if err != nil || deadline.IsZero() {
		return logicobservability.AIPlaneMarkerCountInput{}, errors.New("invalid ai plane window deadline")
	}
	if !deadline.After(startedAt) || deadline.Sub(startedAt) > aiPlaneMarkerCountMaximumWindow {
		return logicobservability.AIPlaneMarkerCountInput{}, errors.New("invalid ai plane window span")
	}
	if startedAt.Before(now.Add(-aiPlaneMarkerCountMaximumWindow)) || deadline.After(now.Add(aiPlaneMarkerCountMaximumWindow)) {
		return logicobservability.AIPlaneMarkerCountInput{}, errors.New("invalid ai plane window bounds")
	}
	return logicobservability.AIPlaneMarkerCountInput{Marker: marker, StartedAt: startedAt, Deadline: deadline}, nil
}
