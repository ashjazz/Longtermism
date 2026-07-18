package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/gogf/gf/v2/net/ghttp"
)

const (
	RequestIDHeader         = "X-Request-ID"
	generatedRequestIDBytes = 16
)

type requestIDContextKey struct{}
type routeTemplateContextKey struct{}

// ResponseMeta 是所有 HTTP 响应都可携带的低敏关联元数据。AI identity 与 eval
// 摘要由后续 chat 切片按各自语义补充，infra-only 响应不能伪造这些字段。
type ResponseMeta struct {
	RequestID string `json:"request_id"`
}

// RequestIdentityMiddleware 为每个请求建立不含业务含义的关联身份。客户端可传入
// 合法 ID 以便跨系统检索；非法或多值 header 不能进入 handler、日志或响应正文。
func RequestIdentityMiddleware(request *ghttp.Request) {
	// A route-specific middleware may run before the global fallback in GoFrame's matching
	// order. Reusing the already-established identity prevents a second random ID from
	// replacing the value observed by the handler, span, or response envelope.
	if RequestIDFromContext(request.GetCtx()) != "" {
		request.Middleware.Next()
		return
	}
	requestID, valid, err := resolveRequestID(request.Header.Values(RequestIDHeader))
	if err != nil {
		request.Response.Status = http.StatusInternalServerError
		request.Response.WriteJsonExit(requestIdentityFailureResponse{
			Code:    http.StatusInternalServerError,
			Message: "request identity unavailable",
			Data:    nil,
			Meta:    ResponseMeta{},
		})
		return
	}

	// 使用已匹配的路由模板作为观测维度，不能把调用方提供的 raw path 放进
	// context；后者可能带用户标识、搜索词或资源 ID，既泄露隐私又造成高基数。
	ctx := context.WithValue(request.GetCtx(), requestIDContextKey{}, requestID)
	ctx = context.WithValue(ctx, routeTemplateContextKey{}, routeTemplateFromRequest(request))
	request.SetCtx(ctx)
	request.Response.Header().Set(RequestIDHeader, requestID)
	if !valid {
		request.Response.Status = http.StatusBadRequest
		request.Response.WriteJsonExit(requestIdentityFailureResponse{
			Code:    http.StatusBadRequest,
			Message: "invalid request identity",
			Data:    nil,
			Meta:    NewResponseMeta(request.GetCtx()),
		})
		return
	}
	request.Middleware.Next()
}

// NewResponseMeta 从当前 request context 读取唯一的 request identity，保证 header
// 与响应 envelope 使用同一个值，而不是在 controller 中再次生成不相关的 ID。
func NewResponseMeta(ctx context.Context) ResponseMeta {
	return ResponseMeta{RequestID: RequestIDFromContext(ctx)}
}

// RequestIDFromContext 返回由 middleware 写入的关联 ID。正常 HTTP 请求必定存在；
// 在脱离 HTTP 的单元测试或后台任务中返回空值，调用方不得据此伪造请求身份。
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

// RouteTemplateFromContext 返回 GoFrame 在路由匹配后确定的模板（如
// /resources/{id}）。它适合用于日志、span 和指标属性；空值表示请求未匹配路由，
// 调用方不得回退到 raw URL path。
func RouteTemplateFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	routeTemplate, _ := ctx.Value(routeTemplateContextKey{}).(string)
	return routeTemplate
}

type requestIdentityFailureResponse struct {
	Code    int          `json:"code"`
	Message string       `json:"message"`
	Data    any          `json:"data"`
	Meta    ResponseMeta `json:"meta"`
}

func resolveRequestID(values []string) (string, bool, error) {
	if len(values) == 0 {
		requestID, err := newOpaqueRequestID()
		return requestID, true, err
	}
	if len(values) != 1 || !isValidOpaqueRequestID(values[0]) {
		requestID, err := newOpaqueRequestID()
		return requestID, false, err
	}
	return values[0], true, nil
}

// newOpaqueRequestID 使用 crypto/rand，避免把时间、进程或主机指纹编码到会返回给
// 客户端的关联 ID。128 bit 随机性足以让并发请求的碰撞概率可以忽略。
func newOpaqueRequestID() (string, error) {
	bytes := make([]byte, generatedRequestIDBytes)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func routeTemplateFromRequest(request *ghttp.Request) string {
	if request == nil {
		return ""
	}
	handler := request.GetServeHandler()
	if handler == nil || handler.Handler.Router == nil {
		return ""
	}
	return handler.Handler.Router.Uri
}

func isValidOpaqueRequestID(value string) bool {
	if len(value) == 0 || len(value) > 128 || !isRequestIDAlphanumeric(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if isRequestIDAlphanumeric(character) || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func isRequestIDAlphanumeric(character byte) bool {
	return character >= 'A' && character <= 'Z' ||
		character >= 'a' && character <= 'z' ||
		character >= '0' && character <= '9'
}
