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

// === US5：local controlled-sender 双平面 payload 契约（T151 RED，T155 实现）===
//
// 这些测试固定 obs-platform-smoke 的核心承诺：在无 Docker、无凭据、无真实后端
// 的环境中，受控运行仍能证明最小双平面 payload 的 marker 与 identity 语义没有
// 漂移。marker 断言使用 telemetry-contract.md 的公开契约字面量而不是生产私有
// 常量——如果生产 mapper 的键值漂移，这里必须变红，由契约而非实现决定对错。

// 生产 telemetry 契约中的 AI 平面 marker 键值（telemetry-contract.md §AI root/bridge）。
const (
	localPlatformAIPlaneMarkerKey   = "longtermism.observability.plane"
	localPlatformAIPlaneMarkerValue = "ai"
	localPlatformAIDesignatedKey    = "longtermism.ai.designated"
	localPlatformAITraceIDAttrKey   = "longtermism.ai.trace_id"
	localPlatformRequestIDAttrKey   = "request.id"
)

func TestPlatformSmokeLocalRunSkipsByDefaultWithZeroExternalAttempts(t *testing.T) {
	// 生产风险：默认关闭的 local smoke 若在 skip 路径上仍然触发了任何网络
	// 出口（例如错误初始化了 OTLP exporter），CI 会在无感知下产生外连。
	// counting transport 必须在 skip 路径同样保持零计数。
	transport := &countingLocalPlatformTransport{}

	result, err := RunLocalPlatformSmoke(context.Background(), LocalPlatformSmokeRunConfig{
		Config:    PlatformSmokeLocalInput{},
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("RunLocalPlatformSmoke() error = %v, want nil for default disabled config", err)
	}
	if !result.Skipped {
		t.Fatalf("Skipped = false, want true for default disabled config")
	}
	if result.Ready {
		t.Fatalf("Ready = true, want false for default disabled config")
	}
	if result.PayloadSent {
		t.Fatalf("PayloadSent = true, want false for default disabled config")
	}
	if !strings.Contains(result.SkipReason, "not enabled") {
		t.Fatalf("SkipReason = %q, want it to explain the smoke is not enabled", result.SkipReason)
	}
	if attempts := transport.Attempts(); attempts != 0 {
		t.Fatalf("transport Attempts = %d, want 0 for disabled local run", attempts)
	}
}

func TestPlatformSmokeLocalRunRequiresTransport(t *testing.T) {
	// transport 是外连审计的强制通道：缺失时必须 fail-fast，不允许静默
	// 构造一个"看不见外连"的受控运行。
	_, err := RunLocalPlatformSmoke(context.Background(), LocalPlatformSmokeRunConfig{
		Config: completeLocalPlatformSmokeRunConfig(nil),
	})
	if err == nil {
		t.Fatalf("RunLocalPlatformSmoke() error = nil, want fail-fast error when transport is missing")
	}
	if !strings.Contains(err.Error(), "transport") {
		t.Fatalf("error = %q, want it to name the missing transport", err.Error())
	}
}

func TestPlatformSmokeLocalRunFailsFastOnMissingIdentityFacts(t *testing.T) {
	// identity 是调用链必须显式给出的业务事实：request/service/AI/eval 四类
	// 身份与 eval sample 缺一即 fail-fast。用默认常量填充缺失身份会让
	// "identity 分离"退化成"identity 碰巧不冲突"，掩盖装配错误。
	tests := []struct {
		name    string
		mutate  func(config *LocalPlatformSmokeRunConfig)
		wantErr string
	}{
		{
			name:    "missing request id",
			mutate:  func(config *LocalPlatformSmokeRunConfig) { config.RequestID = "" },
			wantErr: "request",
		},
		{
			name:    "missing service trace id",
			mutate:  func(config *LocalPlatformSmokeRunConfig) { config.ServiceTraceID = "" },
			wantErr: "service trace",
		},
		{
			name:    "missing root span id",
			mutate:  func(config *LocalPlatformSmokeRunConfig) { config.SpanID = "" },
			wantErr: "span",
		},
		{
			name:    "missing AI trace id",
			mutate:  func(config *LocalPlatformSmokeRunConfig) { config.AITraceID = "" },
			wantErr: "AI trace",
		},
		{
			name:    "missing eval run id",
			mutate:  func(config *LocalPlatformSmokeRunConfig) { config.EvalRunID = "" },
			wantErr: "eval run",
		},
		{
			name:    "missing eval sample id",
			mutate:  func(config *LocalPlatformSmokeRunConfig) { config.SampleID = "" },
			wantErr: "sample",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := completeLocalPlatformSmokeRunConfig(tt.mutate)

			result, err := RunLocalPlatformSmoke(context.Background(), config)
			if err == nil {
				t.Fatalf("RunLocalPlatformSmoke() error = nil, want fail-fast error (got result %#v)", result)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to name the missing %q identity", err.Error(), tt.wantErr)
			}
			if result.PayloadSent {
				t.Fatalf("PayloadSent = true alongside error, want no payload on failure")
			}
		})
	}
}

func TestPlatformSmokeLocalRunProducesDualPlanePayloadWithProductionMarkers(t *testing.T) {
	// Arrange：显式且互不相同的四类身份，让任何混淆都直接暴露在断言里。
	transport := &countingLocalPlatformTransport{}
	const (
		requestID      = "req-local-platform-001"
		serviceTraceID = "svc-trace-local-platform-001"
		spanID         = "span-local-platform-001"
		aiTraceID      = "ai-trace-local-platform-001"
		evalRunID      = "eval-run-local-platform-001"
		sampleID       = "sample-local-platform-001"
	)

	// Act：完整 local 配置运行受控链路。counting transport 是唯一潜在外连
	// 通道，运行结束后必须仍为零计数。
	result, err := RunLocalPlatformSmoke(context.Background(), LocalPlatformSmokeRunConfig{
		Config:         PlatformSmokeLocalInput{Enabled: true, Provider: "local"},
		Transport:      transport,
		Scenario:       ObservabilityChainScenarioSuccess,
		RequestID:      requestID,
		ServiceTraceID: serviceTraceID,
		SpanID:         spanID,
		AITraceID:      aiTraceID,
		EvalRunID:      evalRunID,
		SampleID:       sampleID,
	})
	if err != nil {
		t.Fatalf("RunLocalPlatformSmoke() error = %v, want nil for complete local config", err)
	}
	if !result.Ready || result.Skipped || !result.PayloadSent {
		t.Fatalf("result = {Ready: %v, Skipped: %v, PayloadSent: %v}, want ready sent payload", result.Ready, result.Skipped, result.PayloadSent)
	}
	if attempts := transport.Attempts(); attempts != 0 {
		t.Fatalf("transport Attempts = %d, want 0: local platform smoke must never open external connections", attempts)
	}

	payload := result.Payload

	// Assert：request/service/AI/eval 四类身份各归其位且互不混用。
	assertLocalPlatformPayloadIdentity(t, payload, requestID, serviceTraceID, spanID, aiTraceID, evalRunID)

	// infra 平面：无 AI marker，不携带 AI 身份。
	if len(payload.InfraStages) == 0 {
		t.Fatalf("payload InfraStages is empty, want the infrastructure plane snapshot")
	}
	for _, stage := range payload.InfraStages {
		assertLocalPlatformInfraStageHasNoAIMarkers(t, stage, aiTraceID)
	}

	// AI 平面：root marker（plane/designated/ai.trace_id）与 semantic marker
	// （span name 前缀）齐全，且通过 parent span 与 infra 平面关联。
	if len(payload.AISpans) == 0 {
		t.Fatalf("payload AISpans is empty, want AI plane snapshots")
	}
	observedTypes := map[obs.ObservationType]bool{}
	for _, span := range payload.AISpans {
		assertLocalPlatformAISpanCarriesProductionMarkers(t, span, requestID, serviceTraceID, spanID, aiTraceID)
		observedTypes[span.ObservationType] = true
	}
	// semantic marker 必须覆盖生产 AI 语义集合：generation 是 root 语义阶段，
	// retriever/tool 证明中间步骤，evaluator 证明评估回链。
	for _, wantType := range []obs.ObservationType{
		obs.ObservationTypeGeneration,
		obs.ObservationTypeRetriever,
		obs.ObservationTypeEvaluator,
	} {
		if !observedTypes[wantType] {
			t.Fatalf("payload AISpans missing semantic marker for observation type %q", wantType)
		}
	}

	// eval evidence：以 eval run 身份回查 request 与 AI 身份，而不是混入任一平面。
	if len(payload.EvalEvidence) == 0 {
		t.Fatalf("payload EvalEvidence is empty, want eval plane evidence")
	}
	evidence := payload.EvalEvidence[0]
	if evidence.EvalRunID != evalRunID {
		t.Fatalf("EvalEvidence EvalRunID = %q, want %q", evidence.EvalRunID, evalRunID)
	}
	if evidence.RequestID != requestID {
		t.Fatalf("EvalEvidence RequestID = %q, want %q", evidence.RequestID, requestID)
	}
	if evidence.AITraceID != aiTraceID {
		t.Fatalf("EvalEvidence AITraceID = %q, want %q", evidence.AITraceID, aiTraceID)
	}
	if evidence.SampleID != sampleID {
		t.Fatalf("EvalEvidence SampleID = %q, want %q", evidence.SampleID, sampleID)
	}
}

func completeLocalPlatformSmokeRunConfig(mutate func(config *LocalPlatformSmokeRunConfig)) LocalPlatformSmokeRunConfig {
	config := LocalPlatformSmokeRunConfig{
		Config:         PlatformSmokeLocalInput{Enabled: true, Provider: "local"},
		Scenario:       ObservabilityChainScenarioSuccess,
		RequestID:      "req-local-platform-complete",
		ServiceTraceID: "svc-trace-local-platform-complete",
		SpanID:         "span-local-platform-complete",
		AITraceID:      "ai-trace-local-platform-complete",
		EvalRunID:      "eval-run-local-platform-complete",
		SampleID:       "sample-local-platform-complete",
	}
	if mutate != nil {
		mutate(&config)
	}
	return config
}

func assertLocalPlatformPayloadIdentity(t *testing.T, payload LocalPlatformSmokePayload, requestID, serviceTraceID, spanID, aiTraceID, evalRunID string) {
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
}

func assertLocalPlatformInfraStageHasNoAIMarkers(t *testing.T, stage LocalPlatformSmokeInfraSpan, aiTraceID string) {
	t.Helper()

	for _, markerKey := range []string{
		localPlatformAIPlaneMarkerKey,
		localPlatformAIDesignatedKey,
		localPlatformAITraceIDAttrKey,
	} {
		if value, present := stage.Attributes[markerKey]; present {
			t.Fatalf("infra stage carries AI marker %q=%q; the infrastructure plane must not be marked as AI", markerKey, value)
		}
	}
	if stage.RequestID == "" || stage.ServiceTraceID == "" || stage.SpanID == "" {
		t.Fatalf("infra stage is missing service-plane identity: %#v", stage)
	}
	// AI 身份不得以任何形式（字段或属性值）混入 infra 平面快照。
	rendered := fmt.Sprintf("%#v", stage)
	if strings.Contains(rendered, aiTraceID) {
		t.Fatalf("infra stage rendered AI trace id %q: %#v", aiTraceID, stage)
	}
}

func assertLocalPlatformAISpanCarriesProductionMarkers(t *testing.T, span LocalPlatformSmokeAISpan, requestID, serviceTraceID, spanID, aiTraceID string) {
	t.Helper()

	if value := span.Attributes[localPlatformAIPlaneMarkerKey]; value != localPlatformAIPlaneMarkerValue {
		t.Fatalf("AI span %q plane marker = %q, want %q (production routing marker)", span.Name, value, localPlatformAIPlaneMarkerValue)
	}
	if value := span.Attributes[localPlatformAIDesignatedKey]; value != "true" {
		t.Fatalf("AI span %q designated marker = %q, want \"true\"", span.Name, value)
	}
	if value := span.Attributes[localPlatformAITraceIDAttrKey]; value != aiTraceID {
		t.Fatalf("AI span %q root marker %s = %q, want root AI trace %q", span.Name, localPlatformAITraceIDAttrKey, value, aiTraceID)
	}
	if value := span.Attributes[localPlatformRequestIDAttrKey]; value != requestID {
		t.Fatalf("AI span %q %s = %q, want %q", span.Name, localPlatformRequestIDAttrKey, value, requestID)
	}
	// semantic marker：AI span 名称由 observation type 派生（ai.generation 等），
	// 与 marker 一起构成平台侧识别 AI 语义阶段的依据。
	if !strings.HasPrefix(span.Name, "ai.") {
		t.Fatalf("AI span name %q lacks the ai. semantic marker prefix", span.Name)
	}
	if span.ObservationType == "" {
		t.Fatalf("AI span %q has empty ObservationType", span.Name)
	}
	// 分离但可关联：AI 平面持有自己的 AITraceID，infra 关联只通过
	// ServiceTraceID + ParentSpanID 表达，不借 AI 身份混入 infra 平面。
	if span.AITraceID != aiTraceID {
		t.Fatalf("AI span %q AITraceID = %q, want %q", span.Name, span.AITraceID, aiTraceID)
	}
	if span.ServiceTraceID != serviceTraceID || span.ParentSpanID != spanID {
		t.Fatalf("AI span %q infra correlation = (%q, %q), want (%q, %q)", span.Name, span.ServiceTraceID, span.ParentSpanID, serviceTraceID, spanID)
	}
}

// countingLocalPlatformTransport 是测试注入的外连审计 transport：它不执行任何
// 网络操作，只记录"实现试图外连"这一事实。Attempts() > 0 即证明 local 路径
// 被错误地接入了真实出口。
type countingLocalPlatformTransport struct {
	attempts int
	targets  []string
}

func (transport *countingLocalPlatformTransport) Dial(network, address string) error {
	transport.attempts++
	transport.targets = append(transport.targets, network+" "+address)
	return nil
}

func (transport *countingLocalPlatformTransport) Attempts() int {
	return transport.attempts
}
