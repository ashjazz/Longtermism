package smoke

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

func TestPlatformSmokeSkipsByDefaultWithoutExternalSend(t *testing.T) {
	sender := &recordingPlatformSmokeSender{}

	result, err := RunPlatformSmoke(context.Background(), PlatformSmokeRunConfig{
		Config: PlatformSmokeConfigInput{},
		Sender: sender,
	})
	if err != nil {
		t.Fatalf("RunPlatformSmoke() error = %v, want default skip without failure", err)
	}

	if !result.Skipped {
		t.Fatalf("Skipped = false, want true for default config")
	}
	if result.Ready {
		t.Fatalf("Ready = true, want false for default config")
	}
	if !strings.Contains(result.SkipReason, "not enabled") {
		t.Fatalf("SkipReason = %q, want not enabled diagnostic", result.SkipReason)
	}
	if sender.SendCount() != 0 {
		t.Fatalf("sender SendCount = %d, want 0 for default skip", sender.SendCount())
	}
}

func TestPlatformSmokeSendsMinimalLinkedChainWhenExplicitlyEnabled(t *testing.T) {
	sender := &recordingPlatformSmokeSender{}

	result, err := RunPlatformSmoke(context.Background(), PlatformSmokeRunConfig{
		Config: PlatformSmokeConfigInput{
			Enabled:   true,
			Provider:  "otlp",
			Endpoint:  "https://collector.example.test",
			SecretKey: "sk-platform-smoke-secret",
		},
		Sender:         sender,
		Scenario:       ObservabilityChainScenarioSuccess,
		RequestID:      "req-platform-smoke-001",
		ServiceTraceID: "svc-trace-platform-smoke-001",
		SpanID:         "span-platform-smoke-001",
		AITraceID:      "ai-trace-platform-smoke-001",
		EvalRunID:      "eval-run-platform-smoke-001",
		SampleID:       "sample-platform-smoke-001",
	})
	if err != nil {
		t.Fatalf("RunPlatformSmoke() error = %v", err)
	}

	if result.Skipped {
		t.Fatalf("Skipped = true, want false for complete opt-in config")
	}
	if !result.Ready {
		t.Fatalf("Ready = false, want true for complete opt-in config")
	}
	if !result.PayloadSent {
		t.Fatalf("PayloadSent = false, want true")
	}
	if sender.SendCount() != 1 {
		t.Fatalf("sender SendCount = %d, want 1", sender.SendCount())
	}

	payload := sender.Payloads()[0]
	assertPlatformSmokePayloadConfig(t, payload, "otlp", "https://collector.example.test")
	assertPlatformSmokePayloadIdentity(t, payload, "req-platform-smoke-001", "svc-trace-platform-smoke-001", "span-platform-smoke-001", "ai-trace-platform-smoke-001", "eval-run-platform-smoke-001")
	assertPlatformSmokePayloadContainsAIStage(t, payload, obs.ObservationTypeGeneration)
	assertPlatformSmokePayloadContainsRetrieverOrTool(t, payload)
	assertPlatformSmokePayloadEvalEvidence(t, payload, "sample-platform-smoke-001")
	assertPlatformSmokePayloadDoesNotEchoSecret(t, payload, "sk-platform-smoke-secret")
}

type recordingPlatformSmokeSender struct {
	payloads []PlatformSmokePayload
}

func (s *recordingPlatformSmokeSender) SendPlatformSmoke(ctx context.Context, payload PlatformSmokePayload) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	s.payloads = append(s.payloads, payload)
	return nil
}

func (s *recordingPlatformSmokeSender) SendCount() int {
	return len(s.payloads)
}

func (s *recordingPlatformSmokeSender) Payloads() []PlatformSmokePayload {
	return append([]PlatformSmokePayload(nil), s.payloads...)
}

func assertPlatformSmokePayloadConfig(t *testing.T, payload PlatformSmokePayload, wantProvider, wantEndpoint string) {
	t.Helper()

	if payload.Platform.Provider != wantProvider {
		t.Fatalf("payload Platform.Provider = %q, want %q", payload.Platform.Provider, wantProvider)
	}
	if payload.Platform.Endpoint != wantEndpoint {
		t.Fatalf("payload Platform.Endpoint = %q, want %q", payload.Platform.Endpoint, wantEndpoint)
	}
	if !payload.Platform.CredentialPresent {
		t.Fatalf("payload Platform.CredentialPresent = false, want true")
	}
}

func assertPlatformSmokePayloadIdentity(t *testing.T, payload PlatformSmokePayload, requestID, serviceTraceID, spanID, aiTraceID, evalRunID string) {
	t.Helper()

	if payload.RequestID != requestID {
		t.Fatalf("payload RequestID = %q, want %q", payload.RequestID, requestID)
	}
	if payload.ServiceTraceID != serviceTraceID {
		t.Fatalf("payload ServiceTraceID = %q, want %q", payload.ServiceTraceID, serviceTraceID)
	}
	if payload.RootSpanID != spanID {
		t.Fatalf("payload RootSpanID = %q, want %q", payload.RootSpanID, spanID)
	}
	if payload.RootAITraceID != aiTraceID {
		t.Fatalf("payload RootAITraceID = %q, want %q", payload.RootAITraceID, aiTraceID)
	}
	if payload.EvalRunID != evalRunID {
		t.Fatalf("payload EvalRunID = %q, want %q", payload.EvalRunID, evalRunID)
	}
	if len(payload.ServiceStages) == 0 {
		t.Fatalf("payload ServiceStages is empty, want infrastructure stage")
	}
}

func assertPlatformSmokePayloadContainsAIStage(t *testing.T, payload PlatformSmokePayload, want obs.ObservationType) {
	t.Helper()

	for _, observation := range payload.AIObservations {
		if observation.ObservationType == want {
			return
		}
	}
	t.Fatalf("payload AIObservations missing %q: %#v", want, payload.AIObservations)
}

func assertPlatformSmokePayloadContainsRetrieverOrTool(t *testing.T, payload PlatformSmokePayload) {
	t.Helper()

	for _, observation := range payload.AIObservations {
		if observation.ObservationType == obs.ObservationTypeRetriever || observation.ObservationType == obs.ObservationTypeTool {
			return
		}
	}
	t.Fatalf("payload AIObservations missing retriever or tool stage: %#v", payload.AIObservations)
}

func assertPlatformSmokePayloadEvalEvidence(t *testing.T, payload PlatformSmokePayload, sampleID string) {
	t.Helper()

	if len(payload.EvalEvidence) == 0 {
		t.Fatalf("payload EvalEvidence is empty, want eval summary")
	}
	if payload.EvalEvidence[0].SampleID != sampleID {
		t.Fatalf("payload EvalEvidence[0].SampleID = %q, want %q", payload.EvalEvidence[0].SampleID, sampleID)
	}
}

func assertPlatformSmokePayloadDoesNotEchoSecret(t *testing.T, payload PlatformSmokePayload, secret string) {
	t.Helper()

	rendered := fmt.Sprintf("%#v", payload)
	if strings.Contains(rendered, secret) {
		t.Fatalf("platform smoke payload echoed secret value %q: %#v", secret, payload)
	}
}
