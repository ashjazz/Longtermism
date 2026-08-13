package cmd

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"sync"
	"time"

	v1chat "github.com/ashjazz/Longtermism/api/v1/chat"
	appobservability "github.com/ashjazz/Longtermism/internal/observability"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"github.com/ashjazz/Longtermism/pkg/ai/ratelimit"
	"github.com/gogf/gf/v2/net/ghttp"
	"go.opentelemetry.io/otel/baggage"
)

const (
	chatHTTPPath      = "/api/v1/chat"
	chatRateLimitKey  = "chat"
	defaultChatRate   = 20
	defaultChatPeriod = time.Minute
)

// ChatRateLimitConfig 独立描述产品 chat 流量。它不能复用 infra-smoke 的 bucket，
// 否则运维探针可能挤占业务容量，反之业务突发也会让诊断端点失效。
type ChatRateLimitConfig struct {
	Rate   int
	Period time.Duration
}

// ChatRoutesInput 只消费已经完成的应用装配结果。路由层不读取 provider 配置、
// credential 或 prompt，也不创建 AI identity；这些事实只能由 usecase 在调用前建立。
type ChatRoutesInput struct {
	Enabled                     bool
	Bootstrap                   *ObservabilityBootstrap
	CompletionLoggingMiddleware func(http.Handler) http.Handler
	Handler                     ghttp.HandlerFunc
	Limiter                     ratelimit.Limiter
	Limit                       ChatRateLimitConfig
	SmokeEnabled                bool
	SmokeAdmission              *ChatSmokeAdmission
	state                       *chatRoutesState
}

var chatSmokeMarkerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,127}$`)

var disabledChatSmokeAuthorizationDigest = sha256.Sum256([]byte("disabled-chat-smoke-admission"))

type ChatSmokeAdmissionConfig struct {
	Authorization string
	Capacity      int
	TTL           time.Duration
}

type ChatSmokeAdmission struct {
	authorizationDigest [sha256.Size]byte
	capacity            int
	ttl                 time.Duration
	mu                  sync.Mutex
	consumed            map[string]time.Time
}

func NewChatSmokeAdmission(config ChatSmokeAdmissionConfig) *ChatSmokeAdmission {
	authorization := []byte(config.Authorization)
	return &ChatSmokeAdmission{
		authorizationDigest: sha256.Sum256(authorization),
		capacity:            config.Capacity, ttl: config.TTL, consumed: make(map[string]time.Time),
	}
}

func ChatSmokeRunIDFromContext(ctx context.Context) string {
	return appobservability.ChatSmokeRunIDFromContext(ctx)
}

type chatRoutesState struct {
	mu         sync.Mutex
	registered bool
}

var processChatRoutesState chatRoutesState

// RegisterChatRoutes 注册受 feature gate 和独立限流保护的 chat 入口。注册过程幂等，
// 避免 composition root 被重复调用时安装第二条路由、middleware 或 limiter 配置。
func RegisterChatRoutes(server *ghttp.Server, input ChatRoutesInput) error {
	if server == nil {
		return fmt.Errorf("register chat routes: server is required")
	}
	if input.state == nil {
		return fmt.Errorf("register chat routes: state is required")
	}
	if input.Enabled {
		switch {
		case input.Handler == nil:
			return fmt.Errorf("register chat routes: handler is required when enabled")
		case input.Limiter == nil:
			return fmt.Errorf("register chat routes: limiter is required when enabled")
		case input.Limit.Rate <= 0 || input.Limit.Period <= 0:
			return fmt.Errorf("register chat routes: rate limit must be positive")
		}
	}

	input.state.mu.Lock()
	defer input.state.mu.Unlock()
	if input.state.registered {
		return nil
	}

	// feature 关闭时仍建立 request identity，使 404 也能被基础设施平面关联。
	server.BindMiddlewareDefault(RequestIdentityMiddleware)
	if input.Enabled {
		configureChatLimiter(input.Limiter, input.Limit)
		server.BindMiddleware(
			chatHTTPPath,
			RequestIdentityMiddleware,
			wrapPublicChatHTTPMiddleware(chainObservabilityHTTPMiddleware(
				chatSmokeAdmissionMiddleware(input.SmokeEnabled, input.SmokeAdmission),
				bootstrapHTTPMiddleware(input.Bootstrap),
				input.CompletionLoggingMiddleware,
			)),
			chatRateLimitMiddleware(input.Limiter),
		)
		server.BindHandler("POST:"+chatHTTPPath, input.Handler)
	}
	input.state.registered = true
	return nil
}

func chatSmokeAdmissionMiddleware(enabled bool, admission *ChatSmokeAdmission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			marker := request.Header.Get(v1chat.ChatSmokeRunIDHeader)
			authorization := request.Header.Get(v1chat.ChatSmokeAuthorizationHeader)
			if marker == "" && authorization == "" {
				next.ServeHTTP(writer, request)
				return
			}
			request.Header.Del(v1chat.ChatSmokeAuthorizationHeader)
			request.Header.Del(v1chat.ChatSmokeRunIDHeader)
			authenticated, validLength := authenticateChatSmokeCredential(admission, authorization)
			mayConsume := enabled && admission != nil && isExactChatSmokeLoopback(request.RemoteAddr) && authenticated && validLength
			if !mayConsume || !admission.consume(marker, time.Now().UTC()) {
				http.NotFound(writer, request)
				return
			}
			ctx := appobservability.ContextWithChatSmokeRunID(request.Context(), marker)
			next.ServeHTTP(writer, request.WithContext(ctx))
		})
	}
}

func authenticateChatSmokeCredential(admission *ChatSmokeAdmission, authorization string) (bool, bool) {
	expected := disabledChatSmokeAuthorizationDigest
	if admission != nil {
		expected = admission.authorizationDigest
	}
	digest := sha256.Sum256([]byte(authorization))
	authenticated := subtle.ConstantTimeCompare(digest[:], expected[:]) == 1
	validLength := len(authorization) >= minimumChatSmokeCredentialBytes && len(authorization) <= maximumChatSmokeCredentialBytes
	return authenticated, validLength
}

func (admission *ChatSmokeAdmission) consume(marker string, now time.Time) bool {
	if admission == nil || admission.capacity <= 0 || admission.ttl <= 0 || !chatSmokeMarkerPattern.MatchString(marker) {
		return false
	}
	admission.mu.Lock()
	defer admission.mu.Unlock()
	for consumedMarker, consumedAt := range admission.consumed {
		if !now.Before(consumedAt.Add(admission.ttl)) {
			delete(admission.consumed, consumedMarker)
		}
	}
	if _, exists := admission.consumed[marker]; exists || len(admission.consumed) >= admission.capacity {
		return false
	}
	admission.consumed[marker] = now
	return true
}

func isExactChatSmokeLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	return host == "127.0.0.1" || host == "::1"
}

func bootstrapHTTPMiddleware(bootstrap *ObservabilityBootstrap) func(http.Handler) http.Handler {
	if bootstrap == nil {
		return nil
	}
	return bootstrap.Middleware
}

func configureChatLimiter(limiter ratelimit.Limiter, config ChatRateLimitConfig) {
	limiter.Configure(chatRateLimitKey, config.Rate, config.Period)
}

// wrapPublicChatHTTPMiddleware 在 OTel/日志 middleware 与 GoFrame 链之间建立公网信任边界。
// Baggage header 在提取前移除；middleware 若从其它 propagator/context 引入 baggage，
// 最终 callback 仍会再次清空，避免依赖不同 middleware 的内部执行顺序。
func wrapPublicChatHTTPMiddleware(middleware func(http.Handler) http.Handler) ghttp.HandlerFunc {
	return func(request *ghttp.Request) {
		sanitizedRequest := request.Request.Clone(request.Request.Context())
		sanitizedRequest.Header = request.Request.Header.Clone()
		sanitizedRequest.Header.Del("Baggage")
		request.Header.Del(v1chat.ChatSmokeAuthorizationHeader)
		request.Header.Del(v1chat.ChatSmokeRunIDHeader)

		next := http.HandlerFunc(func(_ http.ResponseWriter, nextRequest *http.Request) {
			ctx := clearPublicChatSemanticIdentity(nextRequest.Context())
			request.Request = nextRequest.WithContext(ctx)
			request.SetCtx(ctx)
			request.Middleware.Next()
		})
		if middleware == nil {
			next.ServeHTTP(request.Response.BufferWriter, sanitizedRequest)
			return
		}
		middleware(next).ServeHTTP(request.Response.BufferWriter, sanitizedRequest)
	}
}

func clearPublicChatSemanticIdentity(ctx context.Context) context.Context {
	// 整个入站 baggage 都是不可信的：除 AI/eval 字段外，伪造 request_id 也会让
	// 出站传播身份与已经校验的 X-Request-ID 分叉。可信身份只从本地 context 重建。
	ctx = baggage.ContextWithBaggage(ctx, baggage.Baggage{})
	return obs.ContextWithCorrelationIdentity(ctx, obs.NewCorrelationIdentity(RequestIDFromContext(ctx)))
}

func chatRateLimitMiddleware(limiter ratelimit.Limiter) ghttp.HandlerFunc {
	return func(request *ghttp.Request) {
		allowed, err := allowChatRequest(request.GetCtx(), limiter)
		if err != nil || !allowed {
			request.Response.Status = http.StatusTooManyRequests
			request.Response.WriteJsonExit(newChatRateLimitResponse(NewResponseMeta(request.GetCtx())))
			return
		}
		request.Middleware.Next()
	}
}

func allowChatRequest(ctx context.Context, limiter ratelimit.Limiter) (bool, error) {
	if limiter == nil {
		return false, fmt.Errorf("chat limiter is required")
	}
	// 固定低基数 key 不含 request_id、用户输入或身份信息，避免 limiter 状态成为
	// 另一条敏感数据存储与 AI 语义泄露通道。
	return limiter.Allow(ctx, chatRateLimitKey)
}

type chatRateLimitResponse struct {
	Code    int          `json:"code"`
	Message string       `json:"message"`
	Data    any          `json:"data"`
	Meta    ResponseMeta `json:"meta"`
}

func newChatRateLimitResponse(meta ResponseMeta) chatRateLimitResponse {
	return chatRateLimitResponse{
		Code:    http.StatusTooManyRequests,
		Message: "rate limit exceeded",
		Data:    nil,
		Meta:    meta,
	}
}
