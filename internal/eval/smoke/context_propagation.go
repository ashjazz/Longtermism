package smoke

import (
	"context"
	"fmt"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/obs"
)

// CoreBoundaryProbe 模拟从应用层进入 pkg/ai core 的调用边界。
type CoreBoundaryProbe func(ctx context.Context) (obs.CorrelationIdentity, error)

// ContextPropagationSmokeConfig 描述一次 handler-to-core 的上下文传播验证。
type ContextPropagationSmokeConfig struct {
	RequestID      string
	ServiceTraceID string
	SpanID         string
	AITraceID      string
	SessionID      string
	ExtraBaggage   map[string]string
	CoreBoundary   CoreBoundaryProbe
}

// ContextPropagationSmokeResult 是 core 边界实际观察到的关联身份。
type ContextPropagationSmokeResult struct {
	RequestID      string
	ServiceTraceID string
	SpanID         string
	AITraceID      string
	SessionID      string
}

// RunContextPropagationSmoke 验证低敏关联身份能从应用层传入 pkg/ai 边界。
func RunContextPropagationSmoke(ctx context.Context, config ContextPropagationSmokeConfig) (ContextPropagationSmokeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if config.CoreBoundary == nil {
		return ContextPropagationSmokeResult{}, fmt.Errorf("context propagation core boundary is required")
	}

	identity := obs.NewCorrelationIdentity(
		config.RequestID,
		obs.WithServiceSpan(config.ServiceTraceID, config.SpanID),
		obs.WithAITraceID(config.AITraceID),
		obs.WithSessionID(config.SessionID),
	)
	if err := validatePropagationBaggage(identity, config.ExtraBaggage); err != nil {
		return ContextPropagationSmokeResult{}, err
	}

	observed, err := config.CoreBoundary(obs.ContextWithCorrelationIdentity(ctx, identity))
	if err != nil {
		return ContextPropagationSmokeResult{}, fmt.Errorf("context propagation core boundary: %w", err)
	}

	return ContextPropagationSmokeResult{
		RequestID:      observed.RequestID,
		ServiceTraceID: observed.ServiceTraceID,
		SpanID:         observed.SpanID,
		AITraceID:      observed.AITraceID,
		SessionID:      observed.SessionID,
	}, nil
}

func validatePropagationBaggage(identity obs.CorrelationIdentity, extra map[string]string) error {
	if _, err := obs.BaggageFieldsFromCorrelationIdentity(identity); err != nil {
		return fmt.Errorf("validate correlation baggage: %w", err)
	}
	for key, value := range extra {
		if err := obs.ValidateBaggageField(key, value); err != nil {
			return fmt.Errorf("validate extra baggage: %w", err)
		}
	}
	return nil
}
