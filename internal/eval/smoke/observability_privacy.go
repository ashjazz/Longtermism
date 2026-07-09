package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

const (
	observabilityPrivacyFeature = "observability_privacy_smoke"
	observabilityPrivacyTime    = "2026-07-08T10:00:00Z"
)

// ObservabilityPrivacySmokeConfig 描述一次端到端隐私边界 smoke 的敏感输入。
//
// 这些字段故意包含 raw query、完整 prompt、工具参数和外部响应原文。smoke 的目标
// 是证明这些输入不会出现在普通观测输出面，而不是把它们作为结果的一部分返回。
type ObservabilityPrivacySmokeConfig struct {
	RequestID      string
	ServiceTraceID string
	SpanID         string
	AITraceID      string
	EvalRunID      string
	SampleID       string
	RawQuery       string
	PromptContent  string
	ToolArguments  string
	APIKey         string
	JWT            string
	Password       string
	ExternalResult string
}

// ObservabilityPrivacySmokeResult 是隐私 smoke 的低敏扫描摘要。
type ObservabilityPrivacySmokeResult struct {
	LeakCount       int
	ScannedSurfaces []string
	Leaks           []ObservabilityPrivacyLeak
}

// ObservabilityPrivacyLeak 描述一次泄露命中。
//
// 字段只记录 surface/field/reason，不记录命中的敏感值本身，避免“检测报告再次泄露”。
type ObservabilityPrivacyLeak struct {
	Surface string
	Field   string
	Reason  string
}

// RunObservabilityPrivacySmoke 扫描默认离线观测输出面是否泄露敏感原文。
func RunObservabilityPrivacySmoke(ctx context.Context, config ObservabilityPrivacySmokeConfig) (ObservabilityPrivacySmokeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ObservabilityPrivacySmokeResult{}, err
	}

	identity := observabilityPrivacyIdentity(config)
	trace := observabilityPrivacyTrace(config, identity)
	sensitiveMarkers := observabilityPrivacySensitiveMarkers(config)

	surfaces, err := buildObservabilityPrivacySurfaces(ctx, config, identity, trace)
	if err != nil {
		return ObservabilityPrivacySmokeResult{}, err
	}

	leaks := make([]ObservabilityPrivacyLeak, 0)
	scanned := make([]string, 0, len(surfaces))
	for _, surface := range surfaces {
		scanned = append(scanned, surface.name)
		leaks = append(leaks, scanObservabilityPrivacySurface(surface, sensitiveMarkers)...)
	}

	return ObservabilityPrivacySmokeResult{
		LeakCount:       len(leaks),
		ScannedSurfaces: scanned,
		Leaks:           leaks,
	}, nil
}

type observabilityPrivacySurface struct {
	name    string
	payload string
}

func buildObservabilityPrivacySurfaces(ctx context.Context, config ObservabilityPrivacySmokeConfig, identity obs.CorrelationIdentity, trace obs.Trace) ([]observabilityPrivacySurface, error) {
	loggerPayload := observabilityPrivacyLoggerPayload(ctx, trace)
	spanPayload, err := observabilityPrivacySpanPayload(ctx, trace)
	if err != nil {
		return nil, err
	}
	mapperPayload, err := observabilityPrivacyMapperPayload(trace)
	if err != nil {
		return nil, err
	}
	baggagePayload, err := observabilityPrivacyBaggagePayload(identity)
	if err != nil {
		return nil, err
	}
	smokePayload, err := observabilityPrivacySmokePayload(config, identity)
	if err != nil {
		return nil, err
	}

	return []observabilityPrivacySurface{
		{name: "logger", payload: loggerPayload},
		{name: "span_sink", payload: spanPayload},
		{name: "otel_mapper", payload: mapperPayload},
		{name: "baggage", payload: baggagePayload},
		{name: "smoke_payload", payload: smokePayload},
	}, nil
}

func observabilityPrivacyLoggerPayload(ctx context.Context, trace obs.Trace) string {
	var output bytes.Buffer
	obs.NewLogger(&output).Record(ctx, trace)
	return output.String()
}

func observabilityPrivacySpanPayload(ctx context.Context, trace obs.Trace) (string, error) {
	sink := &observabilityPrivacySpanSink{}
	obs.NewOTelTracer(sink).Record(ctx, trace)
	return marshalObservabilityPrivacyPayload(sink.snapshots)
}

func observabilityPrivacyMapperPayload(trace obs.Trace) (string, error) {
	snapshot, err := obs.MapTraceToSpanSnapshot(trace)
	if err != nil {
		return "", fmt.Errorf("map privacy trace to span snapshot: %w", err)
	}
	return marshalObservabilityPrivacyPayload(snapshot)
}

func observabilityPrivacyBaggagePayload(identity obs.CorrelationIdentity) (string, error) {
	fields, err := obs.BaggageFieldsFromCorrelationIdentity(identity)
	if err != nil {
		return "", fmt.Errorf("build privacy baggage fields: %w", err)
	}
	return marshalObservabilityPrivacyPayload(fields)
}

func observabilityPrivacySmokePayload(config ObservabilityPrivacySmokeConfig, identity obs.CorrelationIdentity) (string, error) {
	payload := struct {
		RequestID      string `json:"request_id"`
		ServiceTraceID string `json:"service_trace_id"`
		SpanID         string `json:"span_id"`
		AITraceID      string `json:"ai_trace_id"`
		EvalRunID      string `json:"eval_run_id"`
		SampleID       string `json:"sample_id"`
		QueryLength    int    `json:"query_length"`
		PromptLength   int    `json:"prompt_length"`
		ToolArgLength  int    `json:"tool_arg_length"`
	}{
		RequestID:      identity.RequestID,
		ServiceTraceID: identity.ServiceTraceID,
		SpanID:         identity.SpanID,
		AITraceID:      identity.AITraceID,
		EvalRunID:      identity.EvalRunID,
		SampleID:       strings.TrimSpace(config.SampleID),
		QueryLength:    len([]rune(config.RawQuery)),
		PromptLength:   len([]rune(config.PromptContent)),
		ToolArgLength:  len([]rune(config.ToolArguments)),
	}
	return marshalObservabilityPrivacyPayload(payload)
}

func marshalObservabilityPrivacyPayload(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal privacy smoke payload: %w", err)
	}
	return string(payload), nil
}

type observabilityPrivacySpanSink struct {
	snapshots []obs.TraceSpanSnapshot
}

func (s *observabilityPrivacySpanSink) RecordTraceSpan(_ context.Context, snapshot obs.TraceSpanSnapshot) {
	if s == nil {
		return
	}
	s.snapshots = append(s.snapshots, snapshot)
}

func observabilityPrivacyIdentity(config ObservabilityPrivacySmokeConfig) obs.CorrelationIdentity {
	return obs.NewCorrelationIdentity(
		strings.TrimSpace(config.RequestID),
		obs.WithServiceSpan(strings.TrimSpace(config.ServiceTraceID), strings.TrimSpace(config.SpanID)),
		obs.WithAITraceID(strings.TrimSpace(config.AITraceID)),
		obs.WithEvalRunID(strings.TrimSpace(config.EvalRunID)),
	)
}

func observabilityPrivacyTrace(config ObservabilityPrivacySmokeConfig, identity obs.CorrelationIdentity) obs.Trace {
	trace := obs.NewTrace(
		identity.AITraceID,
		observabilityPrivacyFeature,
		mustObservabilityPrivacyTime(observabilityPrivacyTime),
		obs.WithCorrelationIdentity(identity),
		obs.WithObservationType(obs.ObservationTypeAgent),
		obs.WithTenant("tenant-privacy-smoke", config.JWT, "session-privacy-smoke"),
		obs.WithQuery(config.RawQuery, "zh-CN", len([]rune(config.RawQuery))),
		obs.WithModel(config.APIKey),
		obs.WithPrompt("prompt-v1", config.PromptContent),
		obs.WithUsage(17, 9, 0),
		obs.WithSafeSummaries(
			obs.NewSafeSummary(obs.WithSummaryHash("query-hash-safe"), obs.WithSummaryLength(len([]rune(config.RawQuery)))),
			obs.NewSafeSummary(obs.WithSummaryHash("prompt-hash-safe"), obs.WithSummaryLength(len([]rune(config.PromptContent)))),
			obs.NewSafeSummary(obs.WithSummaryCount(1), obs.WithSummaryStatus("success")),
			obs.NewSafeSummary(obs.WithSummaryCategory("tool.search"), obs.WithSummaryStatus("success")),
		),
		obs.WithOutcome("success"),
	)
	trace.AgentStepIndex = 1
	trace.ToolCallID = config.ToolArguments
	trace.ToolName = config.ExternalResult
	trace.TerminationReason = "finished"
	trace.ProviderName = "provider"
	trace.RequestedModel = config.ExternalResult
	trace.CircuitState = "closed"
	return trace
}

func observabilityPrivacySensitiveMarkers(config ObservabilityPrivacySmokeConfig) []string {
	markers := []string{
		config.RawQuery,
		config.PromptContent,
		config.ToolArguments,
		config.APIKey,
		config.JWT,
		config.Password,
		config.ExternalResult,
	}
	filtered := make([]string, 0, len(markers))
	for _, marker := range markers {
		if strings.TrimSpace(marker) != "" {
			filtered = append(filtered, marker)
		}
	}
	return filtered
}

func scanObservabilityPrivacySurface(surface observabilityPrivacySurface, markers []string) []ObservabilityPrivacyLeak {
	leaks := make([]ObservabilityPrivacyLeak, 0)
	for _, marker := range markers {
		if strings.Contains(surface.payload, marker) {
			leaks = append(leaks, ObservabilityPrivacyLeak{
				Surface: surface.name,
				Field:   "payload",
				Reason:  obs.ForbiddenPayloadReasonValue,
			})
		}
	}
	for _, finding := range obs.ScanForbiddenPayloadFields(map[string]string{"payload": surface.payload}) {
		leaks = append(leaks, ObservabilityPrivacyLeak{
			Surface: surface.name,
			Field:   finding.Key,
			Reason:  finding.Reason,
		})
	}
	return leaks
}

func mustObservabilityPrivacyTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed
}
