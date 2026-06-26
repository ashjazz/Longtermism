package obs

import (
	"testing"
	"time"
)

func TestNewTraceAppliesCoreFieldsAndOptions(t *testing.T) {
	timestamp := time.Date(2026, time.June, 26, 9, 30, 0, 0, time.UTC)

	trace := NewTrace(
		"trace-helper-001",
		"p0_smoke",
		timestamp,
		WithTenant("tenant-001", "user-001", "session-001"),
		WithQuery("query-hash", "zh-CN", 18),
		WithModel("fake-model"),
		WithPrompt("v1", "prompt-hash"),
		WithUsage(21, 8, 3),
		WithCacheUsage(5, 2),
		WithTemperature(0.2),
		WithLatency(40, 128),
		WithCost(0.001),
		WithOutcome("success"),
	)

	for key, gotWant := range map[string][2]any{
		"TraceID":           {trace.TraceID, "trace-helper-001"},
		"Feature":           {trace.Feature, "p0_smoke"},
		"Timestamp":         {trace.Timestamp, timestamp},
		"TenantID":          {trace.TenantID, "tenant-001"},
		"UserID":            {trace.UserID, "user-001"},
		"SessionID":         {trace.SessionID, "session-001"},
		"QueryHash":         {trace.QueryHash, "query-hash"},
		"QueryLang":         {trace.QueryLang, "zh-CN"},
		"QueryLen":          {trace.QueryLen, 18},
		"Model":             {trace.Model, "fake-model"},
		"PromptTemplateVer": {trace.PromptTemplateVer, "v1"},
		"PromptHash":        {trace.PromptHash, "prompt-hash"},
		"InputTokens":       {trace.InputTokens, 21},
		"OutputTokens":      {trace.OutputTokens, 8},
		"ReasoningTokens":   {trace.ReasoningTokens, 3},
		"CacheReadTokens":   {trace.CacheReadTokens, 5},
		"CacheWriteTokens":  {trace.CacheWriteTokens, 2},
		"Temperature":       {trace.Temperature, 0.2},
		"TTFTMs":            {trace.TTFTMs, int64(40)},
		"TotalLatencyMs":    {trace.TotalLatencyMs, int64(128)},
		"CostUSD":           {trace.CostUSD, 0.001},
		"OutcomeStatus":     {trace.OutcomeStatus, "success"},
	} {
		if gotWant[0] != gotWant[1] {
			t.Errorf("%s = %#v, want %#v", key, gotWant[0], gotWant[1])
		}
	}
}

func TestTraceHelpersDoNotMutateBaseTrace(t *testing.T) {
	userRating := 1
	autoEvalScore := 0.7
	base := Trace{
		TraceID:       "trace-base",
		Feature:       "p0_smoke",
		Timestamp:     time.Unix(1, 0),
		TopScores:     []float64{0.9, 0.8},
		OutcomeStatus: "success",
		UserRating:    &userRating,
		AutoEvalScore: &autoEvalScore,
	}

	derived := ApplyTraceOptions(
		base,
		WithRetrieval(2, "rewritten-query-hash", []float64{0.95, 0.91}, 16),
		WithOutcome("degraded"),
	)

	if base.OutcomeStatus != "success" {
		t.Fatalf("base OutcomeStatus = %q, want unchanged success", base.OutcomeStatus)
	}
	if base.TopScores[0] != 0.9 {
		t.Fatalf("base TopScores[0] = %v, want unchanged 0.9", base.TopScores[0])
	}
	if *base.UserRating != 1 || *base.AutoEvalScore != 0.7 {
		t.Fatalf("base feedback = (%v, %v), want unchanged (1, 0.7)", *base.UserRating, *base.AutoEvalScore)
	}

	derived.TopScores[0] = 0
	*derived.UserRating = -1
	*derived.AutoEvalScore = 0.1

	if base.TopScores[0] != 0.9 {
		t.Fatalf("mutating derived TopScores changed base to %v", base.TopScores[0])
	}
	if *base.UserRating != 1 || *base.AutoEvalScore != 0.7 {
		t.Fatalf("mutating derived feedback changed base to (%v, %v)", *base.UserRating, *base.AutoEvalScore)
	}
}

func TestTraceHelpersCloneMutableOptionInputs(t *testing.T) {
	topScores := []float64{0.88, 0.77}
	userRating := 1
	autoEvalScore := 0.92

	trace := NewTrace(
		"trace-clone-inputs",
		"rag_qa",
		time.Unix(2, 0),
		WithRetrieval(2, "rewritten-query-hash", topScores, 25),
		WithFeedback(&userRating, &autoEvalScore),
	)

	topScores[0] = 0
	userRating = -1
	autoEvalScore = 0.1

	if trace.TopScores[0] != 0.88 {
		t.Fatalf("TopScores[0] = %v, want cloned value 0.88", trace.TopScores[0])
	}
	if *trace.UserRating != 1 {
		t.Fatalf("UserRating = %v, want cloned value 1", *trace.UserRating)
	}
	if *trace.AutoEvalScore != 0.92 {
		t.Fatalf("AutoEvalScore = %v, want cloned value 0.92", *trace.AutoEvalScore)
	}
}

func TestApplyTraceOptionsIgnoresNilOptions(t *testing.T) {
	base := Trace{
		TraceID:   "trace-nil-option",
		Feature:   "p0_smoke",
		Timestamp: time.Unix(3, 0),
	}

	trace := ApplyTraceOptions(base, nil, WithOutcome("success"))

	if trace.OutcomeStatus != "success" {
		t.Fatalf("OutcomeStatus = %q, want success", trace.OutcomeStatus)
	}
}
