package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TracerContractFactory 为契约测试创建 tracer 和可观测的测试 sink。
//
// Tracer 接口本身只有 Record 方法，不暴露“读取已记录 trace”的能力；这是对生产代码的
// 简化。但契约测试需要验证写出了什么，所以每个 adapter 测试可以提供自己的 sink：
// Logger 可以解析 JSON Lines，未来 LangFuse/OTEL adapter 可以把平台 span/run 映射为
// TracerContractRecord，再复用同一套断言。
type TracerContractFactory func(t *testing.T) (Tracer, TracerContractSink)

// TracerContractSink 是契约测试读取 tracer 输出的观察面。
type TracerContractSink interface {
	Records(t *testing.T) []TracerContractRecord
	RawPayload(t *testing.T) string
}

// TracerContractRecord 是各观测后端映射回来的稳定测试快照。
type TracerContractRecord struct {
	TraceID               string
	TenantID              string
	UserID                string
	SessionID             string
	Feature               string
	Timestamp             string
	RequestID             string
	ServiceTraceID        string
	SpanID                string
	ObservationType       ObservationType
	FailureStatus         string
	QueryHash             string
	QueryLang             string
	QueryLen              int
	Model                 string
	PromptTemplateVersion string
	PromptHash            string
	InputTokens           int
	OutputTokens          int
	ReasoningTokens       int
	CacheReadTokens       int
	CacheWriteTokens      int
	Temperature           float64
	TTFTMs                int64
	TotalLatencyMs        int64
	ChunksRetrieved       int
	QueryRewrittenHash    string
	TopScores             []float64
	RetrievalLatencyMs    int64
	QuerySummary          SafeSummary
	PromptSummary         SafeSummary
	RetrievalSummary      SafeSummary
	ToolSummary           SafeSummary
	CostUSD               float64
	OutcomeStatus         string
	UserRating            *int
	AutoEvalScore         *float64
}

func TestLoggerTracerContract(t *testing.T) {
	RunTracerContract(t, func(t *testing.T) (Tracer, TracerContractSink) {
		t.Helper()

		var output bytes.Buffer
		return NewLogger(&output), loggerContractSink{output: &output}
	})
}

// RunTracerContract 验证所有 Tracer adapter 必须保持的核心观测语义。
//
// 这套测试不关心底层写到 stdout、LangFuse run、OTEL span 还是别的平台；它只关心
// 业务侧依赖的稳定事实：关键字段能落下、普通 trace 不泄露敏感原文、可变字段在
// Record 后不会被调用方二次修改污染。
func RunTracerContract(t *testing.T, newTracer TracerContractFactory) {
	t.Helper()

	t.Run("records stable AI trace fields", func(t *testing.T) {
		tracer, sink := newTracer(t)
		timestamp := time.Date(2026, time.June, 29, 16, 0, 0, 0, time.UTC)
		topScores := []float64{0.93, 0.81}
		userRating := 1
		autoEvalScore := 0.88

		trace := NewTrace(
			"trace-contract-001",
			"rag_qa",
			timestamp,
			WithCorrelationIdentity(NewCorrelationIdentity(
				"req-contract-001",
				WithServiceSpan("svc-trace-contract-001", "span-contract-001"),
				WithAITraceID("trace-contract-001"),
				WithSessionID("session-001"),
			)),
			WithObservationType(ObservationTypeGeneration),
			WithTenant("tenant-a", "user-001", "session-001"),
			WithQuery("safe-query-hash", "zh-CN", 42),
			WithModel("gpt-contract"),
			WithPrompt("prompt-v1", "prompt-hash"),
			WithUsage(101, 32, 7),
			WithCacheUsage(11, 5),
			WithTemperature(0.2),
			WithLatency(55, 320),
			WithRetrieval(2, "rewritten-query-hash", topScores, 18),
			WithSafeSummaries(
				NewSafeSummary(WithSummaryHash("safe-query-hash"), WithSummaryLength(42), WithSummaryCategory("zh-CN")),
				NewSafeSummary(WithSummaryHash("prompt-hash"), WithSummaryLength(128), WithSummaryStatus("rendered")),
				NewSafeSummary(WithSummaryCount(2), WithSummaryScore(0.93), WithSummaryStatus("success")),
				NewSafeSummary(WithSummaryCategory("tool.none"), WithSummaryStatus("skipped")),
			),
			WithCost(0.0123),
			WithOutcome("success"),
			WithFeedback(&userRating, &autoEvalScore),
		)
		trace.FailureStatus = string(FailureTelemetryExportFailed)

		ctx := context.WithValue(context.Background(), contractSensitiveContextKey{}, "secret-token-should-not-leak")
		tracer.Record(ctx, trace)

		topScores[0] = 0
		trace.TopScores[1] = 0
		userRating = -1
		autoEvalScore = 0.1

		records := sink.Records(t)
		if len(records) != 1 {
			t.Fatalf("record count = %d, want 1", len(records))
		}
		assertTracerContractRecord(t, records[0])
		assertTracerContractPrivacy(t, sink.RawPayload(t))
	})

	t.Run("records multiple traces in order", func(t *testing.T) {
		tracer, sink := newTracer(t)

		tracer.Record(context.Background(), NewTrace(
			"trace-contract-first",
			"p0_smoke",
			time.Date(2026, time.June, 29, 16, 30, 0, 0, time.UTC),
			WithOutcome("success"),
		))
		tracer.Record(context.Background(), NewFailureTrace(
			"trace-contract-second",
			"agent_run",
			time.Date(2026, time.June, 29, 16, 31, 0, 0, time.UTC),
			FailureLoopDetected,
		))

		records := sink.Records(t)
		if len(records) != 2 {
			t.Fatalf("record count = %d, want 2", len(records))
		}
		if records[0].TraceID != "trace-contract-first" || records[0].OutcomeStatus != "success" {
			t.Fatalf("first record = %#v, want success trace", records[0])
		}
		if records[1].TraceID != "trace-contract-second" || records[1].OutcomeStatus != string(FailureLoopDetected) {
			t.Fatalf("second record = %#v, want loop_detected failure trace", records[1])
		}
	})
}

func assertTracerContractRecord(t *testing.T, record TracerContractRecord) {
	t.Helper()

	wantStrings := map[string][2]string{
		"TraceID":               {record.TraceID, "trace-contract-001"},
		"TenantID":              {record.TenantID, "tenant-a"},
		"UserID":                {record.UserID, "user-001"},
		"SessionID":             {record.SessionID, "session-001"},
		"Feature":               {record.Feature, "rag_qa"},
		"Timestamp":             {record.Timestamp, "2026-06-29T16:00:00Z"},
		"RequestID":             {record.RequestID, "req-contract-001"},
		"ServiceTraceID":        {record.ServiceTraceID, "svc-trace-contract-001"},
		"SpanID":                {record.SpanID, "span-contract-001"},
		"ObservationType":       {record.ObservationType.String(), "generation"},
		"FailureStatus":         {record.FailureStatus, "telemetry_export_failed"},
		"QueryHash":             {record.QueryHash, "safe-query-hash"},
		"QueryLang":             {record.QueryLang, "zh-CN"},
		"Model":                 {record.Model, "gpt-contract"},
		"PromptTemplateVersion": {record.PromptTemplateVersion, "prompt-v1"},
		"PromptHash":            {record.PromptHash, "prompt-hash"},
		"QueryRewrittenHash":    {record.QueryRewrittenHash, "rewritten-query-hash"},
		"OutcomeStatus":         {record.OutcomeStatus, "success"},
	}
	for field, gotWant := range wantStrings {
		if gotWant[0] != gotWant[1] {
			t.Fatalf("%s = %q, want %q", field, gotWant[0], gotWant[1])
		}
	}

	wantInts := map[string][2]int{
		"QueryLen":         {record.QueryLen, 42},
		"InputTokens":      {record.InputTokens, 101},
		"OutputTokens":     {record.OutputTokens, 32},
		"ReasoningTokens":  {record.ReasoningTokens, 7},
		"CacheReadTokens":  {record.CacheReadTokens, 11},
		"CacheWriteTokens": {record.CacheWriteTokens, 5},
		"ChunksRetrieved":  {record.ChunksRetrieved, 2},
	}
	for field, gotWant := range wantInts {
		if gotWant[0] != gotWant[1] {
			t.Fatalf("%s = %d, want %d", field, gotWant[0], gotWant[1])
		}
	}

	if record.TTFTMs != 55 || record.TotalLatencyMs != 320 || record.RetrievalLatencyMs != 18 {
		t.Fatalf("latencies = (%d, %d, %d), want (55, 320, 18)", record.TTFTMs, record.TotalLatencyMs, record.RetrievalLatencyMs)
	}
	if record.Temperature != 0.2 || record.CostUSD != 0.0123 {
		t.Fatalf("float fields = (%v, %v), want (0.2, 0.0123)", record.Temperature, record.CostUSD)
	}
	if len(record.TopScores) != 2 || record.TopScores[0] != 0.93 || record.TopScores[1] != 0.81 {
		t.Fatalf("TopScores = %#v, want [0.93 0.81]", record.TopScores)
	}
	if record.QuerySummary.Hash != "safe-query-hash" || record.QuerySummary.Length != 42 {
		t.Fatalf("QuerySummary = %#v, want safe query summary", record.QuerySummary)
	}
	if record.PromptSummary.Hash != "prompt-hash" || record.PromptSummary.Status != "rendered" {
		t.Fatalf("PromptSummary = %#v, want rendered prompt summary", record.PromptSummary)
	}
	if record.RetrievalSummary.Count != 2 || record.RetrievalSummary.Score == nil || *record.RetrievalSummary.Score != 0.93 {
		t.Fatalf("RetrievalSummary = %#v, want retrieval count and score", record.RetrievalSummary)
	}
	if record.ToolSummary.Category != "tool.none" || record.ToolSummary.Status != "skipped" {
		t.Fatalf("ToolSummary = %#v, want skipped tool summary", record.ToolSummary)
	}
	if record.UserRating == nil || *record.UserRating != 1 {
		t.Fatalf("UserRating = %#v, want 1", record.UserRating)
	}
	if record.AutoEvalScore == nil || *record.AutoEvalScore != 0.88 {
		t.Fatalf("AutoEvalScore = %#v, want 0.88", record.AutoEvalScore)
	}
}

func assertTracerContractPrivacy(t *testing.T, raw string) {
	t.Helper()

	for _, forbidden := range []string{
		"secret-token-should-not-leak",
		"raw_query",
		"prompt_content",
		"tool_args",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("ordinary trace payload leaked forbidden content %q: %s", forbidden, raw)
		}
	}
}

type contractSensitiveContextKey struct{}

type loggerContractSink struct {
	output *bytes.Buffer
}

func (s loggerContractSink) RawPayload(t *testing.T) string {
	t.Helper()

	return s.output.String()
}

func (s loggerContractSink) Records(t *testing.T) []TracerContractRecord {
	t.Helper()

	raw := strings.TrimSpace(s.output.String())
	if raw == "" {
		return nil
	}

	lines := strings.Split(raw, "\n")
	records := make([]TracerContractRecord, 0, len(lines))
	for _, line := range lines {
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			t.Fatalf("decode logger contract payload: %v; line = %q", err, line)
		}
		records = append(records, loggerPayloadToContractRecord(t, payload))
	}
	return records
}

func loggerPayloadToContractRecord(t *testing.T, payload map[string]any) TracerContractRecord {
	t.Helper()

	return TracerContractRecord{
		TraceID:               stringField(payload, "trace_id"),
		TenantID:              stringField(payload, "tenant_id"),
		UserID:                stringField(payload, "user_id"),
		SessionID:             stringField(payload, "session_id"),
		Feature:               stringField(payload, "feature"),
		Timestamp:             stringField(payload, "timestamp"),
		RequestID:             stringField(payload, "request_id"),
		ServiceTraceID:        stringField(payload, "service_trace_id"),
		SpanID:                stringField(payload, "span_id"),
		ObservationType:       ObservationType(stringField(payload, "observation_type")),
		FailureStatus:         stringField(payload, "failure_status"),
		QueryHash:             stringField(payload, "query_hash"),
		QueryLang:             stringField(payload, "query_lang"),
		QueryLen:              intField(payload, "query_len"),
		Model:                 stringField(payload, "model"),
		PromptTemplateVersion: stringField(payload, "prompt_template_version"),
		PromptHash:            stringField(payload, "prompt_hash"),
		InputTokens:           intField(payload, "input_tokens"),
		OutputTokens:          intField(payload, "output_tokens"),
		ReasoningTokens:       intField(payload, "reasoning_tokens"),
		CacheReadTokens:       intField(payload, "cache_read_tokens"),
		CacheWriteTokens:      intField(payload, "cache_write_tokens"),
		Temperature:           floatField(payload, "temperature"),
		TTFTMs:                int64Field(payload, "ttft_ms"),
		TotalLatencyMs:        int64Field(payload, "total_latency_ms"),
		ChunksRetrieved:       intField(payload, "chunks_retrieved"),
		QueryRewrittenHash:    stringField(payload, "query_rewritten_hash"),
		TopScores:             floatSliceField(t, payload, "top_scores"),
		RetrievalLatencyMs:    int64Field(payload, "retrieval_latency_ms"),
		QuerySummary:          safeSummaryField(t, payload, "query_summary"),
		PromptSummary:         safeSummaryField(t, payload, "prompt_summary"),
		RetrievalSummary:      safeSummaryField(t, payload, "retrieval_summary"),
		ToolSummary:           safeSummaryField(t, payload, "tool_summary"),
		CostUSD:               floatField(payload, "cost_usd"),
		OutcomeStatus:         stringField(payload, "outcome_status"),
		UserRating:            intPointerField(payload, "user_rating"),
		AutoEvalScore:         floatPointerField(payload, "auto_eval_score"),
	}
}

func stringField(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func intField(payload map[string]any, key string) int {
	value, _ := payload[key].(float64)
	return int(value)
}

func int64Field(payload map[string]any, key string) int64 {
	value, _ := payload[key].(float64)
	return int64(value)
}

func floatField(payload map[string]any, key string) float64 {
	value, _ := payload[key].(float64)
	return value
}

func intPointerField(payload map[string]any, key string) *int {
	value, ok := payload[key].(float64)
	if !ok {
		return nil
	}
	converted := int(value)
	return &converted
}

func floatPointerField(payload map[string]any, key string) *float64 {
	value, ok := payload[key].(float64)
	if !ok {
		return nil
	}
	return &value
}

func floatSliceField(t *testing.T, payload map[string]any, key string) []float64 {
	t.Helper()

	raw, ok := payload[key].([]any)
	if !ok {
		return nil
	}
	values := make([]float64, len(raw))
	for index, item := range raw {
		value, ok := item.(float64)
		if !ok {
			t.Fatalf("%s[%d] = %#v, want float64", key, index, item)
		}
		values[index] = value
	}
	return values
}

func safeSummaryField(t *testing.T, payload map[string]any, key string) SafeSummary {
	t.Helper()

	raw, ok := payload[key].(map[string]any)
	if !ok {
		return SafeSummary{}
	}

	return SafeSummary{
		Hash:       stringField(raw, "hash"),
		Length:     intField(raw, "length"),
		Category:   stringField(raw, "category"),
		Count:      intField(raw, "count"),
		Score:      floatPointerField(raw, "score"),
		Status:     stringField(raw, "status"),
		ErrorClass: stringField(raw, "error_class"),
	}
}
