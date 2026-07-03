package obs

import "testing"

func TestTracerContractRecordIncludesDualPlaneCorrelationFields(t *testing.T) {
	record := TracerContractRecord{
		RequestID:       "req-contract",
		ServiceTraceID:  "svc-trace-contract",
		SpanID:          "span-contract",
		ObservationType: ObservationTypeGeneration,
		FailureStatus:   "telemetry_export_failed",
	}

	for field, gotWant := range map[string][2]string{
		"RequestID":       {record.RequestID, "req-contract"},
		"ServiceTraceID":  {record.ServiceTraceID, "svc-trace-contract"},
		"SpanID":          {record.SpanID, "span-contract"},
		"ObservationType": {record.ObservationType.String(), "generation"},
		"FailureStatus":   {record.FailureStatus, "telemetry_export_failed"},
	} {
		if gotWant[0] != gotWant[1] {
			t.Fatalf("%s = %q, want %q", field, gotWant[0], gotWant[1])
		}
	}
}

func TestTracerContractRecordIncludesSafeSummaries(t *testing.T) {
	record := TracerContractRecord{
		QuerySummary: NewSafeSummary(
			WithSummaryHash("sha256:query"),
			WithSummaryLength(42),
			WithSummaryCategory("zh-CN"),
		),
		PromptSummary: NewSafeSummary(
			WithSummaryHash("sha256:prompt"),
			WithSummaryLength(120),
			WithSummaryStatus("rendered"),
		),
		RetrievalSummary: NewSafeSummary(
			WithSummaryCount(3),
			WithSummaryScore(0.92),
			WithSummaryStatus("success"),
		),
		ToolSummary: NewSafeSummary(
			WithSummaryCategory("weather.lookup"),
			WithSummaryStatus("success"),
			WithSummaryErrorClass(""),
		),
	}

	if record.QuerySummary.Hash != "sha256:query" || record.QuerySummary.Length != 42 {
		t.Fatalf("QuerySummary = %#v, want query hash and length", record.QuerySummary)
	}
	if record.PromptSummary.Hash != "sha256:prompt" || record.PromptSummary.Status != "rendered" {
		t.Fatalf("PromptSummary = %#v, want prompt hash and rendered status", record.PromptSummary)
	}
	if record.RetrievalSummary.Count != 3 {
		t.Fatalf("RetrievalSummary.Count = %d, want 3", record.RetrievalSummary.Count)
	}
	if record.RetrievalSummary.Score == nil || *record.RetrievalSummary.Score != 0.92 {
		t.Fatalf("RetrievalSummary.Score = %#v, want 0.92", record.RetrievalSummary.Score)
	}
	if record.ToolSummary.Category != "weather.lookup" || record.ToolSummary.Status != "success" {
		t.Fatalf("ToolSummary = %#v, want weather.lookup success", record.ToolSummary)
	}
}
