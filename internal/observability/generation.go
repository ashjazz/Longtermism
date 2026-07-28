package observability

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/llm"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	traceapi "go.opentelemetry.io/otel/trace"
)

const (
	generationSpanName           = "ai.generation"
	generationOutcomeSuccess     = "success"
	generationOutcomeFailed      = "failed"
	maxObservationUsageTokens    = 100_000_000
	sha256HexDigestLength        = 64
	promptHashPrefix             = "sha256:"
	generationFailureDescription = "generation failed"
)

// GenerationSpanInput 只承载一次 generation 已经确认的低敏事实。
//
// raw prompt、用户输入、模型输出和 provider body 不属于该类型，避免它们先进入
// OTel 再寄希望于 Collector 或平台做下游清理。
type GenerationSpanInput struct {
	Feature               string
	StartedAt             time.Time
	CompletedAt           time.Time
	Identity              obs.CorrelationIdentity
	Provider              string
	RequestedModel        string
	ActualModel           string
	FinishReason          llm.FinishReason
	Usage                 llm.Usage
	TotalLatency          time.Duration
	TTFT                  *time.Duration
	Outcome               string
	FailureStatus         string
	PromptTemplateVersion string
	PromptHash            string
	PayloadMode           obs.PayloadMode
	PayloadRedacted       bool
}

// GenerationSpanAdapter 把 generation 领域事实投影到真实 OTel span。
type GenerationSpanAdapter struct {
	tracer traceapi.Tracer
}

// NewGenerationSpanAdapter 创建无共享可变状态的 generation adapter。
func NewGenerationSpanAdapter(tracer traceapi.Tracer) *GenerationSpanAdapter {
	return &GenerationSpanAdapter{tracer: tracer}
}

// RecordGeneration 在活动 parent 下创建并结束一个 generation 子 span。
func (adapter *GenerationSpanAdapter) RecordGeneration(ctx context.Context, input GenerationSpanInput) (PlatformSpanIdentity, error) {
	if err := validateSpanRuntime(ctx, adapterTracer(adapter)); err != nil {
		return PlatformSpanIdentity{}, err
	}
	input = cloneGenerationSpanInput(input)
	if err := validateGenerationSpanInput(input); err != nil {
		return PlatformSpanIdentity{}, err
	}

	routingAttributes, err := obs.MapSpanRoutingAttributes(obs.SpanRoutingInput{
		Role:     obs.SpanRoutingRoleAIGeneration,
		Identity: input.Identity,
		Feature:  input.Feature,
	})
	if err != nil {
		return PlatformSpanIdentity{}, errors.New("generation span routing facts are invalid")
	}

	attributes := generationSpanAttributes(input, routingAttributes)
	status := codes.Unset
	if input.Outcome == generationOutcomeFailed {
		status = codes.Error
	}
	return recordNativeChildSpan(
		ctx,
		adapter.tracer,
		generationSpanName,
		attributes,
		status,
		generationFailureDescription,
		nativeSpanTiming{
			Start: input.StartedAt,
			End:   input.CompletedAt,
		},
	)
}

func adapterTracer(adapter *GenerationSpanAdapter) traceapi.Tracer {
	if adapter == nil {
		return nil
	}
	return adapter.tracer
}

func cloneGenerationSpanInput(input GenerationSpanInput) GenerationSpanInput {
	cloned := input
	if input.TTFT != nil {
		ttft := *input.TTFT
		cloned.TTFT = &ttft
	}
	return cloned
}

func validateGenerationSpanInput(input GenerationSpanInput) error {
	if err := validateGenerationTextFacts(input); err != nil {
		return err
	}
	if err := validateGenerationOutcome(input.Outcome, input.FailureStatus); err != nil {
		return err
	}
	if err := validateGenerationResponseFacts(input); err != nil {
		return err
	}
	if err := validateGenerationUsage(input.Usage); err != nil {
		return err
	}
	if err := validateGenerationLatency(input.StartedAt, input.CompletedAt, input.TotalLatency, input.TTFT); err != nil {
		return err
	}
	if err := validateGenerationPayload(input.PayloadMode, input.PayloadRedacted); err != nil {
		return err
	}
	if err := validatePromptHash(input.PromptHash); err != nil {
		return err
	}
	return nil
}

func validateGenerationTextFacts(input GenerationSpanInput) error {
	required := map[string]string{
		"ai.feature":                 input.Feature,
		"request.id":                 input.Identity.RequestID,
		"longtermism.ai.trace_id":    input.Identity.AITraceID,
		"gen_ai.provider.name":       input.Provider,
		"gen_ai.request.model":       input.RequestedModel,
		"ai.prompt.template_version": input.PromptTemplateVersion,
		"ai.prompt.hash":             input.PromptHash,
	}
	if input.Outcome == generationOutcomeSuccess {
		required["gen_ai.response.model"] = input.ActualModel
	}
	for key, value := range required {
		if !isSafeObservationIdentifier(value) {
			return fmt.Errorf("generation span required fact %s is invalid", key)
		}
	}

	allTextFacts := map[string]string{
		"ai.feature":                    input.Feature,
		"request.id":                    input.Identity.RequestID,
		"longtermism.ai.trace_id":       input.Identity.AITraceID,
		"gen_ai.provider.name":          input.Provider,
		"gen_ai.request.model":          input.RequestedModel,
		"gen_ai.response.model":         input.ActualModel,
		"gen_ai.response.finish_reason": string(input.FinishReason),
		"ai.prompt.template_version":    input.PromptTemplateVersion,
		"ai.prompt.hash":                input.PromptHash,
	}
	for _, value := range allTextFacts {
		if len(value) > maxObservationFactBytes {
			return errors.New("generation span text fact exceeds the export limit")
		}
	}
	if input.ActualModel != "" && !isSafeObservationIdentifier(input.ActualModel) {
		return errors.New("generation actual model is invalid")
	}
	if len(obs.ScanForbiddenPayloadFields(allTextFacts)) > 0 {
		return errors.New("generation span contains unsafe text facts")
	}
	return nil
}

func validateGenerationOutcome(outcome, failureStatus string) error {
	switch outcome {
	case generationOutcomeSuccess:
		if failureStatus != "" {
			return errors.New("successful generation cannot carry a failure status")
		}
	case generationOutcomeFailed:
		if !isKnownFailureStatus(obs.FailureStatus(failureStatus)) {
			return errors.New("failed generation requires a stable failure status")
		}
	default:
		return errors.New("generation outcome is unsupported")
	}
	return nil
}

func validateGenerationResponseFacts(input GenerationSpanInput) error {
	if input.ActualModel != "" && strings.TrimSpace(input.ActualModel) == "" {
		return errors.New("generation actual model is invalid")
	}
	if input.FinishReason == "" {
		if input.Outcome == generationOutcomeSuccess {
			return errors.New("successful generation requires a finish reason")
		}
		return nil
	}
	if !isKnownFinishReason(input.FinishReason) {
		return errors.New("generation finish reason is unsupported")
	}
	return nil
}

func validateGenerationUsage(usage llm.Usage) error {
	values := []int{
		usage.InputTokens,
		usage.OutputTokens,
		usage.ReasoningTokens,
		usage.CacheReadTokens,
		usage.CacheWriteTokens,
		usage.TotalTokens,
	}
	for _, value := range values {
		if value < 0 || value > maxObservationUsageTokens {
			return errors.New("generation token usage is invalid")
		}
	}
	if usage.TotalTokens < usage.InputTokens+usage.OutputTokens {
		return errors.New("generation total token usage is inconsistent")
	}
	return nil
}

func validateGenerationLatency(startedAt, completedAt time.Time, totalLatency time.Duration, ttft *time.Duration) error {
	if startedAt.IsZero() || completedAt.IsZero() || completedAt.Before(startedAt) {
		return errors.New("generation timing boundary is invalid")
	}
	if totalLatency < 0 || totalLatency > maxSemanticSpanDuration {
		return errors.New("generation total latency is outside the allowed boundary")
	}
	if completedAt.Sub(startedAt) != totalLatency {
		return errors.New("generation timestamps do not match total latency")
	}
	if ttft == nil {
		return nil
	}
	if *ttft < 0 || *ttft > totalLatency {
		return errors.New("generation TTFT is outside the total latency")
	}
	return nil
}

func validateGenerationPayload(mode obs.PayloadMode, redacted bool) error {
	switch mode {
	case obs.PayloadModeMetadataOnly:
		if redacted {
			return errors.New("metadata-only generation cannot claim redacted content")
		}
	case obs.PayloadModeContentRedacted:
		return nil
	default:
		return errors.New("generation payload mode is not exportable")
	}
	return nil
}

func validatePromptHash(value string) error {
	digest, found := strings.CutPrefix(value, promptHashPrefix)
	if !found || len(digest) != sha256HexDigestLength {
		return errors.New("generation prompt hash is invalid")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return errors.New("generation prompt hash is invalid")
	}
	return nil
}

func generationSpanAttributes(input GenerationSpanInput, routing map[string]string) []attribute.KeyValue {
	attributes := routingOTelAttributes(routing)
	attributes = append(attributes,
		semconv.GenAIProviderNameKey.String(input.Provider),
		semconv.GenAIRequestModelKey.String(input.RequestedModel),
		attribute.Int64("ai.latency.total_ms", input.TotalLatency.Milliseconds()),
		attribute.String("ai.outcome", input.Outcome),
		attribute.String("ai.prompt.template_version", input.PromptTemplateVersion),
		attribute.String("ai.prompt.hash", input.PromptHash),
		attribute.String("longtermism.payload.mode", string(input.PayloadMode)),
		attribute.Bool("longtermism.payload.redacted", input.PayloadRedacted),
	)
	if input.ActualModel != "" {
		attributes = append(attributes, semconv.GenAIResponseModelKey.String(input.ActualModel))
	}
	if input.FinishReason != "" {
		attributes = append(attributes, semconv.GenAIResponseFinishReasonsKey.StringSlice([]string{string(input.FinishReason)}))
	}
	attributes = appendTokenAttributes(attributes, input.Usage)
	if input.TTFT != nil {
		ttftMilliseconds := input.TTFT.Milliseconds()
		// gen_ai.server.time_to_first_token 是单位为秒的 histogram metric 名，不是
		// span attribute。这里保留明确单位的项目属性，metric 由 metrics adapter 记录。
		attributes = append(attributes, attribute.Int64("ai.latency.ttft_ms", ttftMilliseconds))
	}
	if input.FailureStatus != "" {
		attributes = append(attributes, attribute.String("ai.failure_status", input.FailureStatus))
	}
	return attributes
}

func appendTokenAttributes(attributes []attribute.KeyValue, usage llm.Usage) []attribute.KeyValue {
	if usage.InputTokens != 0 {
		attributes = append(attributes, semconv.GenAIUsageInputTokensKey.Int(usage.InputTokens))
	}
	if usage.OutputTokens != 0 {
		attributes = append(attributes, semconv.GenAIUsageOutputTokensKey.Int(usage.OutputTokens))
	}
	if usage.ReasoningTokens != 0 {
		attributes = append(attributes, attribute.Int("gen_ai.usage.reasoning.output_tokens", usage.ReasoningTokens))
	}
	if usage.CacheReadTokens != 0 {
		attributes = append(attributes, attribute.Int("ai.usage.cache_read_tokens", usage.CacheReadTokens))
	}
	if usage.CacheWriteTokens != 0 {
		attributes = append(attributes, attribute.Int("ai.usage.cache_write_tokens", usage.CacheWriteTokens))
	}
	return attributes
}

func isKnownFinishReason(reason llm.FinishReason) bool {
	switch reason {
	case llm.FinishStop, llm.FinishLength, llm.FinishToolCall, llm.FinishContentFilter:
		return true
	default:
		return false
	}
}

func isKnownFailureStatus(status obs.FailureStatus) bool {
	switch status {
	case obs.FailureTimeout,
		obs.FailureRateLimit,
		obs.FailureUpstream,
		obs.FailureCallerError,
		obs.FailureRetrievalMiss,
		obs.FailureLoopDetected,
		obs.FailureBudgetExceeded,
		obs.FailureTelemetryExportFailed:
		return true
	default:
		return false
	}
}
