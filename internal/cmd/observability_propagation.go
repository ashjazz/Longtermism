package cmd

import (
	"context"
	"strings"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// NewObservabilityPropagator 只传播 W3C TraceContext 与经过 allowlist 校验的
// 低敏 baggage。领域 ai_trace_id 只能用于关联，绝不能被解释成 OTel TraceID。
func NewObservabilityPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		observabilityBaggagePropagator{},
	)
}

// ObservabilityIngressTrustPolicy 明确区分已认证的服务间流量和公网入口。默认零值代表
// 不受信：remote sampled/tracestate 都不能影响本地采样或被转发给下一跳。调用方只能在
// mTLS、受信 proxy 或等价认证边界之后设置 TrustedRemote，不能让公网 header 自行声明信任。
type ObservabilityIngressTrustPolicy struct {
	TrustedRemote bool
}

// ObservabilityIngressPropagator 只用于 HTTP ingress；出站传播必须使用同一个对象，
// 才能保证从不受信入口提取的 tracestate 不会在第三方调用时被意外继承。
type ObservabilityIngressPropagator struct {
	trust ObservabilityIngressTrustPolicy
}

func NewObservabilityIngressPropagator(trust ObservabilityIngressTrustPolicy) ObservabilityIngressPropagator {
	return ObservabilityIngressPropagator{trust: trust}
}

func (p ObservabilityIngressPropagator) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	NewObservabilityPropagator().Inject(ctx, carrier)
}

func (p ObservabilityIngressPropagator) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	ctx = observabilityBaggagePropagator{}.Extract(ctx, carrier)
	traceContext := propagation.TraceContext{}.Extract(ctx, carrier)
	spanContext := trace.SpanContextFromContext(traceContext)
	if !spanContext.IsValid() || p.trust.TrustedRemote {
		return traceContext
	}
	// 保留 trace identity 便于排障关联，但清除远端 sample 与 tracestate；本地
	// exporter sampler 对 remote 两个分支都使用本地预算，因此不会被 `-00`/`-01` 绕过。
	trustedByLocalPolicy := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    spanContext.TraceID(),
		SpanID:     spanContext.SpanID(),
		TraceFlags: 0,
		Remote:     true,
	})
	return trace.ContextWithRemoteSpanContext(traceContext, trustedByLocalPolicy)
}

func (p ObservabilityIngressPropagator) Fields() []string {
	return NewObservabilityPropagator().Fields()
}

// observabilityBaggagePropagator 在进程边界重新校验 baggage，而不是直接复用
// OTel 的全量 propagator。baggage 是远端可控输入，未经筛选会把原文或凭据带进
// 后续服务、span 与日志。
type observabilityBaggagePropagator struct{}

const (
	maxObservabilityBaggageHeaderBytes = 8192
	observabilityPlaneBaggageKey       = "longtermism.observability.plane"
)

func (observabilityBaggagePropagator) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	safeBaggage := sanitizeObservabilityBaggage(baggage.FromContext(ctx))
	propagation.Baggage{}.Inject(baggage.ContextWithBaggage(ctx, safeBaggage), carrier)
}

func (observabilityBaggagePropagator) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return baggage.ContextWithBaggage(ctx, extractObservabilityBaggage(ctx, carrier))
}

func (observabilityBaggagePropagator) Fields() []string {
	return propagation.Baggage{}.Fields()
}

func sanitizeObservabilityBaggage(input baggage.Baggage) baggage.Baggage {
	members := make([]baggage.Member, 0, len(input.Members()))
	for _, member := range input.Members() {
		// properties 是远端可控的第二套键值空间，OTel 会在 Inject 时把它们重新
		// 序列化。当前契约不需要 properties，故整项丢弃，不能只校验主 value。
		if !isAllowedObservabilityBaggageMember(member) {
			continue
		}
		// 从 key/value 重建 member，确保即使未来的 baggage 实现增加成员状态，也不
		// 会把入站的未审计数据带到下一个服务。
		safeMember, err := baggage.NewMemberRaw(member.Key(), member.Value())
		if err == nil {
			members = append(members, safeMember)
		}
	}
	safeBaggage, err := baggage.New(members...)
	if err != nil {
		return baggage.Baggage{}
	}
	return safeBaggage
}

func isAllowedObservabilityBaggageMember(member baggage.Member) bool {
	if len(member.Properties()) != 0 {
		return false
	}
	switch member.Key() {
	case obs.BaggageRequestID, obs.BaggageAITraceID, obs.BaggageEvalRunID:
		return obs.ValidateBaggageFieldSafety(member.Key(), member.Value()) == nil
	case observabilityPlaneBaggageKey:
		return member.Value() == "ai"
	default:
		// trace/span context 已由 W3C TraceContext 传播；session_id 需要未来的显式
		// 运行策略才可加入。其它字段必须由产品决策明确启用，不能继承核心包较宽的
		// 内部关联字段集合。
		return false
	}
}

func extractObservabilityBaggage(ctx context.Context, carrier propagation.TextMapCarrier) baggage.Baggage {
	values := []string{carrier.Get("baggage")}
	// 有可能上游传过来的是多个拆分了的，包含多个`相同key`的对象，要能拿同 key 的多个值
	if multiValueCarrier, ok := carrier.(propagation.ValuesGetter); ok {
		values = multiValueCarrier.Values("baggage")
	}
	if len(values) == 0 || strings.Join(values, ",") == "" {
		return sanitizeObservabilityBaggage(baggage.FromContext(ctx))
	}
	// 统一整合为 raw string, 形如 key1=value1,key2=value2的形式
	rawValue := strings.Join(values, ",")
	if len(rawValue) > maxObservabilityBaggageHeaderBytes {
		return sanitizeObservabilityBaggage(baggage.FromContext(ctx))
	}
	parsed, err := baggage.Parse(rawValue)
	if err != nil {
		return sanitizeObservabilityBaggage(baggage.FromContext(ctx))
	}
	return sanitizeObservabilityBaggage(parsed)
}
