// Package observability contains pure application usecases for infrastructure observability.
package observability

import (
	"context"
	"regexp"
	"sync"
	"time"

	appobservability "github.com/ashjazz/Longtermism/internal/observability"
	"go.opentelemetry.io/otel/attribute"
	traceapi "go.opentelemetry.io/otel/trace"
)

const (
	InfraSmokeStatusOK              = "ok"
	infraSmokeRouteTemplate         = "/api/v1/observability/infra-smoke"
	infraSmokeTelemetryFailureClass = "telemetry_export_failed"
)

// ---------------------------------------------------------------------------
// AI-negative marker-count 事实源（T199/T200）。
//
// `/api/v1/observability/smoke/marker-count` 的回答只能来自应用自己拥有的真实
// AI 平面发射事实：进程内 bounded registry 记录“某 marker 已进入 AI 平面”的执行
// 事实，usecase 按 marker+window+limit 只读扫描。禁止固定返回 0、把 Collector
// 冒充可查询存储或从日志字段猜测结果。
// ---------------------------------------------------------------------------

const (
	// AIPlaneMarkerCountInvalidQueryClass 与 AIPlaneMarkerCountQueryFailedClass 是
	// marker-count 端点的稳定错误类别，由 cmd 传输层映射为 400/503 低敏 envelope。
	AIPlaneMarkerCountInvalidQueryClass = "invalid_query"
	AIPlaneMarkerCountQueryFailedClass  = "query_failed"

	// maximumAIPlaneFactSourceLimit 是每次事实源扫描的内置上限：负向检查只需要
	// 知道“是否存在命中”，有界扫描防止诊断查询退化成无界内存遍历。
	maximumAIPlaneFactSourceLimit = 100

	// maximumAIPlaneQueryWindow 与客户端 PollMarkerTarget 的窗口上限一致：
	// smoke 全窗口最多 60 秒，拒绝 stale 或未来窗口。
	maximumAIPlaneQueryWindow = time.Minute

	aiPlaneEmissionFactTTL        = 2 * time.Minute
	aiPlaneEmissionFactsPerMarker = 64
	aiPlaneEmissionMarkerPattern  = `^[A-Za-z0-9._-]{8,128}$`
)

var aiPlaneMarkerPattern = regexp.MustCompile(aiPlaneEmissionMarkerPattern)

// AIPlaneFactSource 是 AI 平面发射事实的只读查询端口。实现必须返回真实计数，
// 任何失败都不能被调用方改写成 0。
type AIPlaneFactSource interface {
	CountAIPlaneFacts(ctx context.Context, marker string, startedAt, deadline time.Time, limit int) (int, error)
}

// AIPlaneMarkerCountInput 只承载经过传输边界解析的 marker+window；limit 由
// usecase 内置，调用方不能放宽扫描。
type AIPlaneMarkerCountInput struct {
	Marker    string
	StartedAt time.Time
	Deadline  time.Time
}

// AIPlaneMarkerCountRunner 让 cmd 传输层依赖窄行为，不接触事实源实现细节。
type AIPlaneMarkerCountRunner interface {
	Count(context.Context, AIPlaneMarkerCountInput) (int, error)
}

// AIPlaneMarkerCountError 是 marker-count 端点的稳定低敏错误：只暴露类别，
// 绝不携带 marker、window 或事实源内部错误文本。
type AIPlaneMarkerCountError struct {
	class string
}

func (e AIPlaneMarkerCountError) Error() string { return "ai plane marker count failed" }
func (e AIPlaneMarkerCountError) Class() string { return e.class }

// AIPlaneMarkerCountUsecaseDependencies 注入事实源与时钟；Source 为 nil 时
// Count fail-closed，绝不退化为固定 0。
type AIPlaneMarkerCountUsecaseDependencies struct {
	Source AIPlaneFactSource
	Now    func() time.Time
}

type AIPlaneMarkerCountUsecase struct {
	source AIPlaneFactSource
	now    func() time.Time
}

func NewAIPlaneMarkerCountUsecase(dependencies AIPlaneMarkerCountUsecaseDependencies) *AIPlaneMarkerCountUsecase {
	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	return &AIPlaneMarkerCountUsecase{source: dependencies.Source, now: now}
}

// Count 在接触事实源之前完成全部 domain 校验，保证 invalid query 零事实读取；
// 事实源失败原样浮出，成功结果必须落在 [0, limit] 内。
func (usecase *AIPlaneMarkerCountUsecase) Count(ctx context.Context, input AIPlaneMarkerCountInput) (int, error) {
	if usecase == nil || usecase.source == nil {
		return 0, AIPlaneMarkerCountError{class: AIPlaneMarkerCountQueryFailedClass}
	}
	if !validAIPlaneMarkerCountQuery(input, usecase.now()) {
		return 0, AIPlaneMarkerCountError{class: AIPlaneMarkerCountInvalidQueryClass}
	}
	count, err := usecase.source.CountAIPlaneFacts(ctx, input.Marker, input.StartedAt, input.Deadline, maximumAIPlaneFactSourceLimit)
	if err != nil {
		return 0, err
	}
	if count < 0 || count > maximumAIPlaneFactSourceLimit {
		return 0, AIPlaneMarkerCountError{class: AIPlaneMarkerCountQueryFailedClass}
	}
	return count, nil
}

func validAIPlaneMarkerCountQuery(input AIPlaneMarkerCountInput, now time.Time) bool {
	if !aiPlaneMarkerPattern.MatchString(input.Marker) {
		return false
	}
	if input.StartedAt.IsZero() || input.Deadline.IsZero() || !input.Deadline.After(input.StartedAt) {
		return false
	}
	if input.Deadline.Sub(input.StartedAt) > maximumAIPlaneQueryWindow {
		return false
	}
	return !input.StartedAt.Before(now.Add(-maximumAIPlaneQueryWindow)) && !input.Deadline.After(now.Add(maximumAIPlaneQueryWindow))
}

var _ AIPlaneMarkerCountRunner = (*AIPlaneMarkerCountUsecase)(nil)

// AIPlaneEmissionRegistry 是应用自有的有界 AI 平面发射事实登记表。Record 由 AI
// 用例在桥接 span 创建后（AI 平面已存在真实事实时）调用；CountAIPlaneFacts 只读
// 扫描。TTL 驱逐 + 每 marker 事实上限保证内存有界；marker 基数本身已由 chat smoke
// admission 的一次性消费与容量约束保证。写路径与读路径的驱逐 cutoff 来自同一个
// 注入时钟，避免时钟不对称导致过期判定分叉。
type AIPlaneEmissionRegistry struct {
	mu             sync.Mutex
	facts          map[string][]time.Time
	ttl            time.Duration
	factsPerMarker int
	now            func() time.Time
}

func NewAIPlaneEmissionRegistry(ttl time.Duration, factsPerMarker int, now func() time.Time) *AIPlaneEmissionRegistry {
	if ttl <= 0 {
		ttl = aiPlaneEmissionFactTTL
	}
	if factsPerMarker <= 0 {
		factsPerMarker = aiPlaneEmissionFactsPerMarker
	}
	if now == nil {
		now = time.Now
	}
	return &AIPlaneEmissionRegistry{facts: make(map[string][]time.Time), ttl: ttl, factsPerMarker: factsPerMarker, now: now}
}

// RecordAIPlaneFact 记录一次已经真实发生的 AI 平面发射事实。畸形 marker、零值时间
// 与超出每 marker 上限的洪水写入被静默拒绝——registry 是诊断事实源，不是业务队列。
func (registry *AIPlaneEmissionRegistry) RecordAIPlaneFact(marker string, at time.Time) {
	if registry == nil || at.IsZero() || !aiPlaneMarkerPattern.MatchString(marker) {
		return
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	cutoff := registry.now().Add(-registry.ttl)
	for existingMarker, timestamps := range registry.facts {
		kept := timestamps[:0]
		for _, recorded := range timestamps {
			if !recorded.Before(cutoff) {
				kept = append(kept, recorded)
			}
		}
		if len(kept) == 0 {
			delete(registry.facts, existingMarker)
			continue
		}
		registry.facts[existingMarker] = kept
	}
	if len(registry.facts[marker]) >= registry.factsPerMarker {
		return
	}
	registry.facts[marker] = append(registry.facts[marker], at)
}

// CountAIPlaneFacts 是 marker-count 端点的真实事实读取：对精确 marker 在闭合窗口
// 内的发射事实做有界计数。返回的 count 只来自登记事实，绝不包含猜测或默认值。
func (registry *AIPlaneEmissionRegistry) CountAIPlaneFacts(_ context.Context, marker string, startedAt, deadline time.Time, limit int) (int, error) {
	if registry == nil {
		return 0, AIPlaneMarkerCountError{class: AIPlaneMarkerCountQueryFailedClass}
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	cutoff := registry.now().Add(-registry.ttl)
	for existingMarker, timestamps := range registry.facts {
		kept := timestamps[:0]
		for _, recorded := range timestamps {
			if !recorded.Before(cutoff) {
				kept = append(kept, recorded)
			}
		}
		if len(kept) == 0 {
			delete(registry.facts, existingMarker)
			continue
		}
		registry.facts[existingMarker] = kept
	}
	count := 0
	for _, recorded := range registry.facts[marker] {
		if count >= limit {
			break
		}
		if !recorded.Before(startedAt) && !recorded.After(deadline) {
			count++
		}
	}
	return count, nil
}

var _ AIPlaneFactSource = (*AIPlaneEmissionRegistry)(nil)

// InfraSmokeInput carries only low-sensitive correlation facts. The controller owns HTTP
// validation; this usecase must not inspect headers, query parameters, or request bodies.
type InfraSmokeInput struct {
	RequestID  string
	SmokeRunID string
}

type InfraSmokeResult struct {
	Status string
}

// InfraSmokeRunner lets the HTTP controller depend on this usecase's narrow behavior rather
// than its concrete observability dependencies. It keeps the controller free of OTel details.
type InfraSmokeRunner interface {
	Run(context.Context, InfraSmokeInput) (InfraSmokeResult, error)
}

// HTTPMetrics is the narrow application port required by the smoke usecase. It intentionally
// omits backend details so the usecase cannot query or configure observability platforms.
type HTTPMetrics interface {
	RecordHTTP(context.Context, appobservability.HTTPMetric) error
}

// HTTPCompletionLogWriter writes an already-redacted completion projection. Production HTTP
// composition intentionally leaves this nil because the outer completion middleware is the sole
// owner of one log per request; tests may inject it to verify usecase-level failure isolation.
type HTTPCompletionLogWriter interface {
	Write(context.Context, appobservability.HTTPCompletionLog) error
}

// InfraSmokeTelemetryFailure is deliberately a low-sensitive diagnostic. Exporter errors can
// contain endpoints or credentials, so diagnostics retain only a stable component and class.
type InfraSmokeTelemetryFailure struct {
	Component  string
	ErrorClass string
}

type InfraSmokeTelemetryDiagnostics interface {
	RecordTelemetryFailure(context.Context, InfraSmokeTelemetryFailure)
}

type InfraSmokeUsecaseDependencies struct {
	Tracer           traceapi.Tracer
	Metrics          HTTPMetrics
	MetricFlusher    interface{ Flush(context.Context) error }
	CompletionLogger HTTPCompletionLogWriter
	Diagnostics      InfraSmokeTelemetryDiagnostics
	Now              func() time.Time
}

type InfraSmokeUsecase struct {
	tracer           traceapi.Tracer
	metrics          HTTPMetrics
	metricFlusher    interface{ Flush(context.Context) error }
	completionLogger HTTPCompletionLogWriter
	diagnostics      InfraSmokeTelemetryDiagnostics
	now              func() time.Time
}

func NewInfraSmokeUsecase(dependencies InfraSmokeUsecaseDependencies) *InfraSmokeUsecase {
	now := dependencies.Now
	if now == nil {
		now = time.Now
	}
	return &InfraSmokeUsecase{
		tracer:           dependencies.Tracer,
		metrics:          dependencies.Metrics,
		metricFlusher:    dependencies.MetricFlusher,
		completionLogger: dependencies.CompletionLogger,
		diagnostics:      dependencies.Diagnostics,
		now:              now,
	}
}

// Run emits exactly infrastructure-plane facts for a successful smoke probe. Observability
// failure must never make the probe's business result ambiguous: callers still receive `ok`,
// while a separate low-sensitive diagnostic preserves the delivery failure for operations.
func (u *InfraSmokeUsecase) Run(ctx context.Context, input InfraSmokeInput) (InfraSmokeResult, error) {
	startedAt := u.now()
	var traceID, spanID string
	if u.tracer != nil {
		tracedContext, span := u.tracer.Start(ctx, "HTTP GET "+infraSmokeRouteTemplate)
		ctx = tracedContext
		span.SetAttributes(infraSmokeSpanAttributes(input)...)
		spanContext := span.SpanContext()
		traceID = spanContext.TraceID().String()
		spanID = spanContext.SpanID().String()
		defer span.End()
	}

	completedAt := u.now()
	duration := completedAt.Sub(startedAt)
	if duration < 0 {
		duration = 0
	}

	u.recordMetric(ctx, input, duration, traceID, spanID)
	u.flushMetrics(ctx)
	u.recordCompletionLog(ctx, input, startedAt, duration, traceID, spanID)
	return InfraSmokeResult{Status: InfraSmokeStatusOK}, nil
}

// flushMetrics bounds the smoke-only export latency. Regular application metrics retain their
// periodic batching; this explicit probe must make its one counter visible before its bounded
// Prometheus query window closes.
func (u *InfraSmokeUsecase) flushMetrics(ctx context.Context) {
	if u.metricFlusher == nil {
		return
	}
	if err := u.metricFlusher.Flush(ctx); err != nil {
		u.recordFailure(ctx, "metrics_flush")
	}
}

func infraSmokeSpanAttributes(input InfraSmokeInput) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("request.id", input.RequestID),
		attribute.String("longtermism.smoke.run_id", input.SmokeRunID),
		attribute.String("http.request.method", "GET"),
		attribute.String("http.route", infraSmokeRouteTemplate),
		attribute.Int("http.response.status_code", 200),
		attribute.String("custom.attr.name", "ashjazz"),
	}
}

func (u *InfraSmokeUsecase) recordMetric(ctx context.Context, input InfraSmokeInput, duration time.Duration, traceID, spanID string) {
	if u.metrics == nil {
		return
	}
	if err := u.metrics.RecordHTTP(ctx, appobservability.HTTPMetric{
		RouteTemplate: infraSmokeRouteTemplate,
		Method:        "GET",
		StatusCode:    200,
		Duration:      duration,
		RequestID:     input.RequestID,
		TraceID:       traceID,
		SpanID:        spanID,
		SmokeRunID:    input.SmokeRunID,
	}); err != nil {
		u.recordFailure(ctx, "metrics")
	}
}

func (u *InfraSmokeUsecase) recordCompletionLog(ctx context.Context, input InfraSmokeInput, timestamp time.Time, duration time.Duration, traceID, spanID string) {
	if u.completionLogger == nil {
		return
	}
	entry, err := appobservability.BuildHTTPCompletionLog(appobservability.HTTPCompletionLogInput{
		Timestamp:     timestamp,
		RequestID:     input.RequestID,
		TraceID:       traceID,
		SpanID:        spanID,
		RouteTemplate: infraSmokeRouteTemplate,
		Method:        "GET",
		StatusCode:    200,
		Duration:      duration,
		IsSmokeRun:    true,
		SmokeRunID:    input.SmokeRunID,
	})
	if err == nil {
		err = u.completionLogger.Write(ctx, entry)
	}
	if err != nil {
		u.recordFailure(ctx, "completion_log")
	}
}

func (u *InfraSmokeUsecase) recordFailure(ctx context.Context, component string) {
	if u.diagnostics == nil {
		return
	}
	u.diagnostics.RecordTelemetryFailure(ctx, InfraSmokeTelemetryFailure{
		Component:  component,
		ErrorClass: infraSmokeTelemetryFailureClass,
	})
}
