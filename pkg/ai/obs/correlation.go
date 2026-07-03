package obs

import "context"

// CorrelationIdentity 是双平面观测的最小关联身份。
//
// 基础设施平面会产生 service_trace_id/span_id，AI 语义平面会产生 ai_trace_id，
// eval 再通过 eval_run_id 回链。把这些字段集中为一个值对象，可以避免各模块
// 从 context 中临时拼装字段，也能明确哪些身份允许跨层传播。
type CorrelationIdentity struct {
	RequestID      string
	ServiceTraceID string
	SpanID         string
	AITraceID      string
	SessionID      string
	EvalRunID      string
}

// CorrelationOption 描述一次不可变关联身份更新。
type CorrelationOption func(CorrelationIdentity) CorrelationIdentity

// NewCorrelationIdentity 创建带 request_id 的关联身份。
//
// request_id 是全链路查询的入口；其它字段由基础设施平面、AI 平面或 eval 运行器
// 在各自边界补充。函数返回值是独立值，不依赖可变全局状态。
func NewCorrelationIdentity(requestID string, options ...CorrelationOption) CorrelationIdentity {
	return ApplyCorrelationOptions(CorrelationIdentity{
		RequestID: requestID,
	}, options...)
}

// ApplyCorrelationOptions 在已有身份基础上派生新身份。
//
// 这里沿用 Trace helper 的不可变风格：option 返回新的值，调用方传入的 base
// 不会被原地修改。当前字段都是 string，值复制已经足够；后续若加入 slice/map
// 字段，必须同步加入防御性拷贝。
func ApplyCorrelationOptions(base CorrelationIdentity, options ...CorrelationOption) CorrelationIdentity {
	identity := base
	for _, option := range options {
		if option == nil {
			continue
		}
		identity = option(identity)
	}
	return identity
}

// WithServiceSpan 绑定基础设施观测平面的 trace/span 身份。
func WithServiceSpan(serviceTraceID, spanID string) CorrelationOption {
	return func(identity CorrelationIdentity) CorrelationIdentity {
		identity.ServiceTraceID = serviceTraceID
		identity.SpanID = spanID
		return identity
	}
}

// WithAITraceID 绑定 AI 语义观测平面的 trace 身份。
func WithAITraceID(aiTraceID string) CorrelationOption {
	return func(identity CorrelationIdentity) CorrelationIdentity {
		identity.AITraceID = aiTraceID
		return identity
	}
}

// WithSessionID 绑定会话身份，用于后续按 session 聚合 AI 行为和评估证据。
func WithSessionID(sessionID string) CorrelationOption {
	return func(identity CorrelationIdentity) CorrelationIdentity {
		identity.SessionID = sessionID
		return identity
	}
}

// WithEvalRunID 绑定评估运行身份，让 eval sample 可以回链到请求和 AI 阶段。
func WithEvalRunID(evalRunID string) CorrelationOption {
	return func(identity CorrelationIdentity) CorrelationIdentity {
		identity.EvalRunID = evalRunID
		return identity
	}
}

type correlationIdentityContextKey struct{}

// ContextWithCorrelationIdentity 把显式构造的关联身份写入 context。
//
// 该 helper 只存储一个已知类型的值，避免把 context 当作任意字段容器使用。
// 敏感原文、prompt、tool args 或外部响应不应通过这个路径传播。
func ContextWithCorrelationIdentity(ctx context.Context, identity CorrelationIdentity) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, correlationIdentityContextKey{}, identity)
}

// CorrelationIdentityFromContext 从 context 读取显式写入的关联身份。
//
// 函数只读取本包私有 key，不遍历、不序列化、不推断 context 中其它值，避免把
// 上游中间件或测试塞入的敏感内容带入普通观测记录。
func CorrelationIdentityFromContext(ctx context.Context) (CorrelationIdentity, bool) {
	if ctx == nil {
		return CorrelationIdentity{}, false
	}
	identity, ok := ctx.Value(correlationIdentityContextKey{}).(CorrelationIdentity)
	return identity, ok
}
