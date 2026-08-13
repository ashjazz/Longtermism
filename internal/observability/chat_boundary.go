package observability

import (
	"context"
	"errors"
	"sync"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	traceapi "go.opentelemetry.io/otel/trace"
)

const (
	chatAIExecutionSpanName = "ai.chat"
	chatAIExecutionFeature  = "chat"
)

type chatSmokeRunIDContextKey struct{}

// ContextWithChatSmokeRunID stores only a marker that has already crossed the protected
// transport boundary. Authentication material is deliberately never placed in context.
func ContextWithChatSmokeRunID(ctx context.Context, marker string) context.Context {
	if ctx == nil || !isSafeObservationIdentifier(marker) {
		return ctx
	}
	return context.WithValue(ctx, chatSmokeRunIDContextKey{}, marker)
}

func ChatSmokeRunIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	marker, _ := ctx.Value(chatSmokeRunIDContextKey{}).(string)
	if !isSafeObservationIdentifier(marker) {
		return ""
	}
	return marker
}

// ChatAIExecutionOutcome 是 root/bridge 结束时唯一允许写入的低敏业务结果。
type ChatAIExecutionOutcome struct {
	Outcome       string
	FailureStatus obs.FailureStatus
}

// EndChatAIExecution 结束 adapter 自己创建的 bridge，或只补充现有 HTTP root 的结果属性。
type EndChatAIExecution func(ChatAIExecutionOutcome) error

// ChatAIExecutionBoundary 把一次 chat 用例挂到真实 OTel root/bridge。
//
// adapter 始终创建自己拥有的 ai.chat bridge，避免根据“当前 span 正在记录”猜测
// 它就是 HTTP root（当前 span 也可能是普通 client/DB child）。bridge 即使因 head
// sampling 不记录 attributes，也仍可提供有效 native SpanContext 供本地 evidence
// 关联；平台投影是否可用由 generation adapter 的结果单独决定。
type ChatAIExecutionBoundary struct {
	tracer traceapi.Tracer
}

func NewChatAIExecutionBoundary(tracer traceapi.Tracer) *ChatAIExecutionBoundary {
	return &ChatAIExecutionBoundary{tracer: tracer}
}

func (boundary *ChatAIExecutionBoundary) Start(
	ctx context.Context,
	identity obs.CorrelationIdentity,
) (context.Context, obs.CorrelationIdentity, EndChatAIExecution, error) {
	if ctx == nil {
		return nil, identity, nil, errors.New("chat AI execution context is required")
	}
	if boundary == nil || boundary.tracer == nil {
		return ctx, identity, nil, errors.New("chat AI execution tracer is required")
	}

	attributes, err := chatBoundaryRoutingAttributes(identity, obs.SpanRoutingRoleAIChatBridge)
	if err != nil {
		return ctx, identity, nil, err
	}
	if marker := ChatSmokeRunIDFromContext(ctx); marker != "" {
		attributes = append(attributes, attribute.String("longtermism.smoke.run_id", marker))
	}
	bridgeContext, bridge := boundary.tracer.Start(
		ctx,
		chatAIExecutionSpanName,
		traceapi.WithAttributes(attributes...),
	)
	if !bridge.SpanContext().IsValid() {
		bridge.End()
		return ctx, identity, nil, errors.New("chat AI bridge did not produce a native identity")
	}
	derived := identityFromNativeSpan(identity, bridge.SpanContext())
	derivedContext := obs.ContextWithCorrelationIdentity(bridgeContext, derived)
	return derivedContext, derived, chatBoundaryEnd(bridge), nil
}

func chatBoundaryRoutingAttributes(identity obs.CorrelationIdentity, role obs.SpanRoutingRole) ([]attribute.KeyValue, error) {
	routing, err := obs.MapSpanRoutingAttributes(obs.SpanRoutingInput{
		Role:     role,
		Identity: identity,
		Feature:  chatAIExecutionFeature,
	})
	if err != nil {
		return nil, errors.New("chat AI execution routing facts are invalid")
	}
	return routingOTelAttributes(routing), nil
}

func identityFromNativeSpan(identity obs.CorrelationIdentity, spanContext traceapi.SpanContext) obs.CorrelationIdentity {
	return obs.ApplyCorrelationOptions(
		identity,
		obs.WithServiceSpan(spanContext.TraceID().String(), spanContext.SpanID().String()),
	)
}

func chatBoundaryEnd(span traceapi.Span) EndChatAIExecution {
	var once sync.Once
	var resultErr error
	return func(outcome ChatAIExecutionOutcome) error {
		once.Do(func() {
			resultErr = applyChatBoundaryOutcome(span, outcome)
			span.End()
		})
		return resultErr
	}
}

func applyChatBoundaryOutcome(span traceapi.Span, outcome ChatAIExecutionOutcome) error {
	switch outcome.Outcome {
	case "success":
		if outcome.FailureStatus != "" {
			return errors.New("successful chat AI execution cannot carry a failure status")
		}
		span.SetAttributes(attribute.String("ai.outcome", outcome.Outcome))
		return nil
	case "failed":
		if !isSafeObservationIdentifier(string(outcome.FailureStatus)) {
			return errors.New("failed chat AI execution requires a stable failure status")
		}
		span.SetAttributes(
			attribute.String("ai.outcome", outcome.Outcome),
			attribute.String("ai.failure_status", string(outcome.FailureStatus)),
		)
		span.SetStatus(codes.Error, "chat AI execution failed")
		return nil
	default:
		return errors.New("chat AI execution outcome is invalid")
	}
}
