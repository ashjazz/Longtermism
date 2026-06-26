package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestLoggerTracerRecordsStableTraceFields(t *testing.T) {
	tests := []struct {
		name  string
		trace Trace
		want  map[string]any
	}{
		{
			name: "记录成功模型调用的核心可观测字段",
			trace: Trace{
				TraceID:           "trace-p0-success",
				Feature:           "p0_smoke",
				Timestamp:         time.Date(2026, time.June, 25, 8, 30, 0, 0, time.UTC),
				Model:             "fake-model",
				PromptTemplateVer: "v1",
				PromptHash:        "2c26b46b68ffc68ff99b453c1d304134",
				InputTokens:       18,
				OutputTokens:      9,
				ReasoningTokens:   3,
				TTFTMs:            42,
				TotalLatencyMs:    137,
				OutcomeStatus:     "success",
			},
			want: map[string]any{
				"trace_id":                "trace-p0-success",
				"feature":                 "p0_smoke",
				"model":                   "fake-model",
				"prompt_template_version": "v1",
				"prompt_hash":             "2c26b46b68ffc68ff99b453c1d304134",
				"input_tokens":            float64(18),
				"output_tokens":           float64(9),
				"reasoning_tokens":        float64(3),
				"ttft_ms":                 float64(42),
				"total_latency_ms":        float64(137),
				"outcome_status":          "success",
			},
		},
		{
			name: "记录失败调用状态且保留零 token",
			trace: Trace{
				TraceID:        "trace-p0-failed",
				Feature:        "p0_smoke",
				Timestamp:      time.Date(2026, time.June, 25, 8, 31, 0, 0, time.UTC),
				Model:          "fake-model",
				PromptHash:     "fcde2b2edba56bf408601fb0f1fca5da",
				InputTokens:    0,
				OutputTokens:   0,
				TotalLatencyMs: 15,
				OutcomeStatus:  "failed",
			},
			want: map[string]any{
				"trace_id":         "trace-p0-failed",
				"feature":          "p0_smoke",
				"model":            "fake-model",
				"prompt_hash":      "fcde2b2edba56bf408601fb0f1fca5da",
				"input_tokens":     float64(0),
				"output_tokens":    float64(0),
				"total_latency_ms": float64(15),
				"outcome_status":   "failed",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			tracer := NewLogger(&output)

			tracer.Record(context.Background(), tt.trace)

			line := strings.TrimSpace(output.String())
			if line == "" {
				t.Fatal("Record() wrote no log entry")
			}
			if strings.Count(line, "\n") != 0 {
				t.Fatalf("Record() output = %q, want exactly one JSON line", output.String())
			}

			var payload map[string]any
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				t.Fatalf("Record() output is not valid JSON: %v; output = %q", err, line)
			}
			for key, want := range tt.want {
				if got := payload[key]; got != want {
					t.Errorf("log field %q = %#v, want %#v", key, got, want)
				}
			}

			// 时间戳是串联日志、指标与上游请求的基础字段，即使任务门控未逐字列出，
			// 日志型 tracer 也必须输出它，避免得到一条无法放回时间线的孤立记录。
			if got := payload["timestamp"]; got != tt.trace.Timestamp.Format(time.RFC3339Nano) {
				t.Errorf("log field timestamp = %#v, want %q", got, tt.trace.Timestamp.Format(time.RFC3339Nano))
			}
		})
	}
}

func TestLoggerTracerToleratesUnavailableLogSink(t *testing.T) {
	tests := []struct {
		name   string
		tracer Tracer
	}{
		{
			name:   "nil writer 降级为 discard",
			tracer: NewLogger(nil),
		},
		{
			name:   "writer 写入失败不影响调用方",
			tracer: NewLogger(failingWriter{}),
		},
		{
			name:   "nil logger receiver 直接返回",
			tracer: (*Logger)(nil),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("Record() panic = %#v, want no panic", recovered)
				}
			}()

			tt.tracer.Record(context.Background(), Trace{
				TraceID:       "trace-sink-unavailable",
				Feature:       "p0_smoke",
				Timestamp:     time.Date(2026, time.June, 25, 10, 0, 0, 0, time.UTC),
				OutcomeStatus: "success",
			})
		})
	}
}

func TestLoggerTracerRecordsOptionalDiagnosticFields(t *testing.T) {
	userRating := 1
	autoEvalScore := 0.87
	var output bytes.Buffer

	tracer := NewLogger(&output)
	tracer.Record(context.Background(), Trace{
		TraceID:            "trace-with-optional-fields",
		TenantID:           "tenant-learning",
		UserID:             "user-learning",
		SessionID:          "session-learning",
		Feature:            "rag_qa",
		QueryLang:          "zh-CN",
		QueryLen:           12,
		CacheReadTokens:    8,
		CacheWriteTokens:   5,
		Temperature:        0.2,
		ChunksRetrieved:    3,
		QueryRewrittenHash: "rewritten-query-hash",
		TopScores:          []float64{0.92, 0.81},
		RetrievalLatencyMs: 18,
		CostUSD:            0.0012,
		OutcomeStatus:      "success",
		UserRating:         &userRating,
		AutoEvalScore:      &autoEvalScore,
	})

	payload := decodeSingleLogLine(t, output.String())
	for key, want := range map[string]any{
		"tenant_id":               "tenant-learning",
		"user_id":                 "user-learning",
		"session_id":              "session-learning",
		"query_lang":              "zh-CN",
		"query_len":               float64(12),
		"cache_read_tokens":       float64(8),
		"cache_write_tokens":      float64(5),
		"temperature":             0.2,
		"chunks_retrieved":        float64(3),
		"query_rewritten_hash":    "rewritten-query-hash",
		"retrieval_latency_ms":    float64(18),
		"cost_usd":                0.0012,
		"user_rating":             float64(1),
		"auto_eval_score":         0.87,
		"timestamp":               time.Time{}.UTC().Format(time.RFC3339Nano),
		"input_tokens":            float64(0),
		"output_tokens":           float64(0),
		"reasoning_tokens":        float64(0),
		"outcome_status":          "success",
		"prompt_template_version": nil,
	} {
		got, exists := payload[key]
		if want == nil {
			if exists {
				t.Errorf("log field %q = %#v, want omitted", key, got)
			}
			continue
		}
		if got != want {
			t.Errorf("log field %q = %#v, want %#v", key, got, want)
		}
	}

	topScores, ok := payload["top_scores"].([]any)
	if !ok {
		t.Fatalf("top_scores = %#v, want array", payload["top_scores"])
	}
	if len(topScores) != 2 || topScores[0] != 0.92 || topScores[1] != 0.81 {
		t.Fatalf("top_scores = %#v, want [0.92 0.81]", topScores)
	}
}

type failingWriter struct{}

func (failingWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("sink unavailable")
}

func decodeSingleLogLine(t *testing.T, raw string) map[string]any {
	t.Helper()

	line := strings.TrimSpace(raw)
	if line == "" {
		t.Fatal("Record() wrote no log entry")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("Record() output is not valid JSON: %v; output = %q", err, line)
	}
	return payload
}

var _ io.Writer = failingWriter{}
