package observability

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	traceapi "go.opentelemetry.io/otel/trace"
)

const (
	maxObservationFactBytes = 256
	maxSemanticSpanDuration = 60 * time.Second
)

var safeObservationIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)

// PlatformSpanIdentity 是平台观测面的原生身份。
//
// 它只能从活动 span 的 SpanContext 构造，不能使用领域 CorrelationIdentity 中由
// 应用传播的字符串代替；后续 score projection 依赖这个边界定位真实 observation。
type PlatformSpanIdentity struct {
	TraceID     string
	SpanID      string
	Projectable bool
}

// IsValid 按 W3C/OTel 的 32-hex TraceID、16-hex SpanID 与非零约束校验平台身份。
// 这只能证明结构有效；身份来源仍必须是 adapter 读取的 native SpanContext。
func (identity PlatformSpanIdentity) IsValid() bool {
	traceID, traceErr := traceapi.TraceIDFromHex(identity.TraceID)
	spanID, spanErr := traceapi.SpanIDFromHex(identity.SpanID)
	return traceErr == nil &&
		spanErr == nil &&
		traceID.IsValid() &&
		spanID.IsValid()
}

// CanProject 只有在 native span 被 sampled 时才允许作为外部 score observation
// target。Drop 和 RecordOnly 都不会形成可查询的平台 observation；它们的合法 ID
// 仍可关联本地 evidence，但必须阻止 score 投影。
func (identity PlatformSpanIdentity) CanProject() bool {
	return identity.Projectable && identity.IsValid()
}

func validateSpanRuntime(ctx context.Context, tracer traceapi.Tracer) error {
	if ctx == nil {
		return errors.New("semantic span context is required")
	}
	if tracer == nil {
		return errors.New("semantic span tracer is required")
	}
	if !traceapi.SpanContextFromContext(ctx).IsValid() {
		return errors.New("semantic span requires an active parent SpanContext")
	}
	return nil
}

func isSafeObservationIdentifier(value string) bool {
	return len(value) <= maxObservationFactBytes && safeObservationIdentifierPattern.MatchString(value)
}

func routingOTelAttributes(routing map[string]string) []attribute.KeyValue {
	keys := make([]string, 0, len(routing))
	for key := range routing {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	attributes := make([]attribute.KeyValue, 0, len(keys))
	for _, key := range keys {
		attributes = append(attributes, attribute.String(key, routing[key]))
	}
	return attributes
}

func recordNativeChildSpan(
	ctx context.Context,
	tracer traceapi.Tracer,
	name string,
	attributes []attribute.KeyValue,
	status codes.Code,
	statusDescription string,
	timing nativeSpanTiming,
) (PlatformSpanIdentity, error) {
	parent := traceapi.SpanContextFromContext(ctx)
	_, span := tracer.Start(
		ctx,
		name,
		traceapi.WithAttributes(attributes...),
		traceapi.WithTimestamp(timing.Start),
	)
	defer span.End(traceapi.WithTimestamp(timing.End))

	spanContext := span.SpanContext()
	if !spanContext.IsValid() || spanContext.TraceID() != parent.TraceID() || spanContext.SpanID() == parent.SpanID() {
		return PlatformSpanIdentity{}, errors.New("semantic span did not produce a valid native child identity")
	}
	if status == codes.Error {
		span.SetStatus(codes.Error, statusDescription)
	}
	return PlatformSpanIdentity{
		TraceID:     spanContext.TraceID().String(),
		SpanID:      spanContext.SpanID().String(),
		Projectable: spanContext.IsSampled(),
	}, nil
}

type nativeSpanTiming struct {
	Start time.Time
	End   time.Time
}
