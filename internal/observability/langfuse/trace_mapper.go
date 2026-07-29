// Package langfuse contains the platform adapter that projects Longtermism's
// observability facts into Langfuse-specific attributes.
package langfuse

import (
	"errors"
	"regexp"
	"strings"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	traceapi "go.opentelemetry.io/otel/trace"
)

const (
	langfuseObservationTypeKey = "langfuse.observation.type"
	langfuseObservationNameKey = "langfuse.observation.name"
	langfuseInputKey           = "langfuse.observation.input"
	langfuseOutputKey          = "langfuse.observation.output"
	maxObservationNameBytes    = 256
	platformAttributeCapacity  = 16
)

var (
	errInvalidNativeIdentity = errors.New("langfuse projection requires native OTel identity")
	errInvalidObservation    = errors.New("langfuse projection requires a valid observation")
	errUnsupportedPayload    = errors.New("langfuse projection payload mode is unsupported")
	errUnsafeProjection      = errors.New("langfuse projection contains unsafe observability data")

	safeObservationNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
)

// OTLPSpanSnapshot is the minimal exporter-side view of one native OTel span.
// TraceID and SpanID remain infrastructure identities; the domain ai_trace_id is
// carried only through the explicitly allowlisted Attributes map.
type OTLPSpanSnapshot struct {
	TraceID         string
	SpanID          string
	Name            string
	ObservationType obs.ObservationType
	Attributes      map[string]string
}

// TraceMapperInput contains only facts that are safe to reach a platform
// projection boundary. PayloadSnapshot cannot be constructed with raw content
// outside pkg/ai/obs, while content_raw is rejected explicitly by the mapper.
type TraceMapperInput struct {
	Span        OTLPSpanSnapshot
	PayloadMode obs.PayloadMode
	Payload     obs.PayloadSnapshot
}

// TraceProjection is the result of the platform projection step. attributes stays
// private so callers cannot append sensitive values after the mapper's final scan.
// mapped is deliberately private: later score projection can distinguish a
// mapper-produced native identity from caller-assembled strings.
type TraceProjection struct {
	platformTraceID       string
	platformObservationID string
	attributes            map[string]string
	mapped                bool
}

// PlatformTraceID returns the native OTel trace identity captured by the mapper.
func (projection TraceProjection) PlatformTraceID() string {
	return projection.platformTraceID
}

// PlatformObservationID returns the native OTel span identity captured by the mapper.
func (projection TraceProjection) PlatformObservationID() string {
	return projection.platformObservationID
}

// AttributesSnapshot returns a defensive copy for the eventual OTLP exporter.
// 每次读取都复制，避免 exporter、测试或调用方共享可变 map 后绕过 allowlist。
func (projection TraceProjection) AttributesSnapshot() map[string]string {
	if len(projection.attributes) == 0 {
		return nil
	}

	attributes := make(map[string]string, len(projection.attributes))
	for key, value := range projection.attributes {
		attributes[key] = value
	}
	return attributes
}

// MapTraceToProjection projects normalized OTel facts into Langfuse's attribute
// namespace without changing the source snapshot or the core fact model.
func MapTraceToProjection(input TraceMapperInput) (TraceProjection, error) {
	if !isValidNativeIdentity(input.Span.TraceID, input.Span.SpanID) {
		return TraceProjection{}, errInvalidNativeIdentity
	}
	if !isSafeObservationName(input.Span.Name) || obs.ValidateObservationType(input.Span.ObservationType) != nil {
		return TraceProjection{}, errInvalidObservation
	}

	attributes, err := projectPayloadBoundary(input)
	if err != nil {
		return TraceProjection{}, err
	}
	attributes[langfuseObservationTypeKey] = input.Span.ObservationType.String()
	attributes[langfuseObservationNameKey] = input.Span.Name
	projectAllowlistedAttributes(attributes, input.Span.Attributes)

	// 出口再次 fail closed。只扫描新建的目标 map，既能拒绝 allowlist 值中的凭据，
	// 又不会让应被忽略的 caller/raw source 字段阻断一条安全投影。
	if findings := obs.ScanForbiddenPayloadFields(attributes); len(findings) != 0 {
		return TraceProjection{}, errUnsafeProjection
	}

	return TraceProjection{
		platformTraceID:       input.Span.TraceID,
		platformObservationID: input.Span.SpanID,
		attributes:            attributes,
		mapped:                true,
	}, nil
}

func projectPayloadBoundary(input TraceMapperInput) (map[string]string, error) {
	attributes := make(map[string]string, platformAttributeCapacity)

	switch input.PayloadMode {
	case obs.PayloadModeMetadataOnly:
		attributes["langfuse.observation.metadata.payload_mode"] = string(obs.PayloadModeMetadataOnly)
		attributes["langfuse.observation.metadata.payload_redacted"] = "false"
	case obs.PayloadModeContentRedacted:
		attributes["langfuse.observation.metadata.payload_mode"] = string(obs.PayloadModeContentRedacted)
		attributes["langfuse.observation.metadata.payload_redacted"] = "true"
		attributes[langfuseInputKey] = input.Payload.Input()
		attributes[langfuseOutputKey] = input.Payload.Output()
	case obs.PayloadModeContentRaw:
		return nil, errUnsupportedPayload
	default:
		return nil, errUnsupportedPayload
	}

	return attributes, nil
}

func projectAllowlistedAttributes(destination, source map[string]string) {
	// 平台 adapter 只能投影已经由核心观测层归一化的低敏事实。allowlist 放在
	// 函数内保持只读，且刻意不用前缀复制：新增核心 attribute 不会自动流入
	// Langfuse，必须经过独立的隐私与语义审查。
	allowlist := [...]struct {
		source      string
		destination string
	}{
		{source: "ai.feature", destination: "langfuse.observation.metadata.ai_feature"},
		{source: "ai.outcome", destination: "langfuse.observation.metadata.outcome"},
		{source: "longtermism.ai.trace_id", destination: "langfuse.observation.metadata.ai_trace_id"},
		{source: "gen_ai.response.model", destination: "langfuse.observation.model.name"},
		{source: "gen_ai.request.model", destination: "langfuse.observation.metadata.requested_model"},
		{source: "gen_ai.usage.input_tokens", destination: "langfuse.observation.usage_details.input"},
		{source: "gen_ai.usage.output_tokens", destination: "langfuse.observation.usage_details.output"},
		{source: "gen_ai.usage.reasoning.output_tokens", destination: "langfuse.observation.usage_details.reasoning_output"},
		{source: "ai.prompt.template_version", destination: "langfuse.observation.metadata.prompt_template_version"},
		{source: "ai.prompt.hash", destination: "langfuse.observation.metadata.prompt_hash"},
	}

	for _, mapping := range allowlist {
		value := strings.TrimSpace(source[mapping.source])
		if value == "" {
			continue
		}
		destination[mapping.destination] = value
	}
}

func isValidNativeIdentity(traceIDValue, spanIDValue string) bool {
	traceID, traceErr := traceapi.TraceIDFromHex(traceIDValue)
	spanID, spanErr := traceapi.SpanIDFromHex(spanIDValue)
	return traceErr == nil && spanErr == nil && traceID.IsValid() && spanID.IsValid()
}

func isSafeObservationName(name string) bool {
	return len(name) <= maxObservationNameBytes && safeObservationNamePattern.MatchString(name)
}
