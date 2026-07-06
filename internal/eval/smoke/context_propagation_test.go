package smoke

import (
	"context"
	"testing"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/obs"
)

func TestContextPropagationSmokePassesServiceIdentityIntoAICoreBoundary(t *testing.T) {
	result, err := RunContextPropagationSmoke(context.Background(), ContextPropagationSmokeConfig{
		RequestID:      "req-propagation-001",
		ServiceTraceID: "svc-trace-propagation-001",
		SpanID:         "span-propagation-001",
		AITraceID:      "ai-trace-propagation-001",
		SessionID:      "session-propagation-001",
		CoreBoundary: func(ctx context.Context) (obs.CorrelationIdentity, error) {
			identity, ok := obs.CorrelationIdentityFromContext(ctx)
			if !ok {
				t.Fatalf("CorrelationIdentityFromContext() ok = false, want true")
			}
			return identity, nil
		},
	})
	if err != nil {
		t.Fatalf("RunContextPropagationSmoke() error = %v", err)
	}

	if result.RequestID != "req-propagation-001" {
		t.Fatalf("RequestID = %q, want req-propagation-001", result.RequestID)
	}
	if result.ServiceTraceID != "svc-trace-propagation-001" {
		t.Fatalf("ServiceTraceID = %q, want svc-trace-propagation-001", result.ServiceTraceID)
	}
	if result.SpanID != "span-propagation-001" {
		t.Fatalf("SpanID = %q, want span-propagation-001", result.SpanID)
	}
	if result.AITraceID != "ai-trace-propagation-001" {
		t.Fatalf("AITraceID = %q, want ai-trace-propagation-001", result.AITraceID)
	}
	if result.SessionID != "session-propagation-001" {
		t.Fatalf("SessionID = %q, want session-propagation-001", result.SessionID)
	}
}

func TestContextPropagationSmokeRejectsSensitiveBaggage(t *testing.T) {
	_, err := RunContextPropagationSmoke(context.Background(), ContextPropagationSmokeConfig{
		RequestID:      "req-propagation-sensitive",
		ServiceTraceID: "svc-trace-propagation-sensitive",
		SpanID:         "span-propagation-sensitive",
		AITraceID:      "ai-trace-propagation-sensitive",
		ExtraBaggage: map[string]string{
			"prompt": "完整 prompt 不允许跨服务传播",
		},
		CoreBoundary: func(ctx context.Context) (obs.CorrelationIdentity, error) {
			identity, _ := obs.CorrelationIdentityFromContext(ctx)
			return identity, nil
		},
	})

	if err == nil {
		t.Fatalf("RunContextPropagationSmoke() error = nil, want sensitive baggage rejection")
	}
}
