package smoke

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestEvalTraceLinkSmokeMeetsNinetyPercentLinkRate(t *testing.T) {
	samples := makeEvalTraceLinkSamples(10)

	result, err := RunEvalTraceLinkSmoke(context.Background(), EvalTraceLinkSmokeConfig{
		DatasetName:    "agent-smoke",
		DatasetVersion: "agent-smoke-v1",
		EvalRunID:      "eval-run-link-rate-001",
		Samples:        samples,
	})
	if err != nil {
		t.Fatalf("RunEvalTraceLinkSmoke() error = %v", err)
	}

	if result.DatasetName != "agent-smoke" {
		t.Fatalf("DatasetName = %q, want agent-smoke", result.DatasetName)
	}
	if result.DatasetVersion != "agent-smoke-v1" {
		t.Fatalf("DatasetVersion = %q, want agent-smoke-v1", result.DatasetVersion)
	}
	if result.EvalRunID != "eval-run-link-rate-001" {
		t.Fatalf("EvalRunID = %q, want eval-run-link-rate-001", result.EvalRunID)
	}
	if result.SampleCount != 10 {
		t.Fatalf("SampleCount = %d, want 10", result.SampleCount)
	}
	if result.LinkedCount != 9 {
		t.Fatalf("LinkedCount = %d, want 9", result.LinkedCount)
	}
	if result.LinkRate != 0.9 {
		t.Fatalf("LinkRate = %v, want 0.9", result.LinkRate)
	}
	if len(result.MissingLinks) != 1 || result.MissingLinks[0].SampleID != "sample-10" {
		t.Fatalf("MissingLinks = %#v, want sample-10 only", result.MissingLinks)
	}
}

func TestEvalTraceLinkSmokeFailsBelowNinetyPercentAndListsSamples(t *testing.T) {
	samples := makeEvalTraceLinkSamples(10)
	samples[7].RequestID = ""
	samples[8].AITraceID = ""
	samples[9].AITraceID = "ai-trace-eval-link-10"
	samples[9].ServiceTraceID = ""

	result, err := RunEvalTraceLinkSmoke(context.Background(), EvalTraceLinkSmokeConfig{
		DatasetName:    "agent-smoke",
		DatasetVersion: "agent-smoke-v1",
		EvalRunID:      "eval-run-link-rate-002",
		Samples:        samples,
	})
	if err == nil {
		t.Fatal("RunEvalTraceLinkSmoke() error = nil, want link-rate failure")
	}
	if !strings.Contains(err.Error(), "eval trace link rate") {
		t.Fatalf("error = %q, want eval trace link rate diagnostic", err.Error())
	}
	if result.LinkRate != 0.7 {
		t.Fatalf("LinkRate = %v, want 0.7", result.LinkRate)
	}
	if len(result.MissingLinks) != 3 {
		t.Fatalf("MissingLinks length = %d, want 3: %#v", len(result.MissingLinks), result.MissingLinks)
	}
	assertMissingEvalTraceLink(t, result.MissingLinks, "sample-08", "request_id")
	assertMissingEvalTraceLink(t, result.MissingLinks, "sample-09", "ai_trace_id")
	assertMissingEvalTraceLink(t, result.MissingLinks, "sample-10", "service_trace_id")
}

func TestEvalTraceLinkSmokeLocatesFailedEvidenceSample(t *testing.T) {
	samples := makeEvalTraceLinkSamples(3)
	samples[1].MetricName = "answer_relevance"
	samples[1].Score = 0.42
	samples[1].Threshold = 0.8

	result, err := RunEvalTraceLinkSmoke(context.Background(), EvalTraceLinkSmokeConfig{
		DatasetName:    "agent-smoke",
		DatasetVersion: "agent-smoke-v1",
		EvalRunID:      "eval-run-link-rate-003",
		Samples:        samples,
	})
	if err != nil {
		t.Fatalf("RunEvalTraceLinkSmoke() error = %v", err)
	}

	if len(result.FailedSamples) != 1 {
		t.Fatalf("FailedSamples length = %d, want 1: %#v", len(result.FailedSamples), result.FailedSamples)
	}

	failed := result.FailedSamples[0]
	if failed.SampleID != "sample-02" {
		t.Fatalf("failed SampleID = %q, want sample-02", failed.SampleID)
	}
	if failed.MetricName != "answer_relevance" {
		t.Fatalf("failed MetricName = %q, want answer_relevance", failed.MetricName)
	}
	if failed.RequestID != "req-eval-link-02" {
		t.Fatalf("failed RequestID = %q, want req-eval-link-02", failed.RequestID)
	}
	if failed.AITraceID != "ai-trace-eval-link-02" {
		t.Fatalf("failed AITraceID = %q, want ai-trace-eval-link-02", failed.AITraceID)
	}
	if failed.FailureSummary == "" {
		t.Fatalf("FailureSummary is empty, want diagnostic summary")
	}
}

func makeEvalTraceLinkSamples(count int) []EvalTraceLinkSmokeSample {
	samples := make([]EvalTraceLinkSmokeSample, 0, count)
	for index := 1; index <= count; index++ {
		sampleID := evalTraceLinkSampleID(index)
		sample := EvalTraceLinkSmokeSample{
			SampleID:       sampleID,
			RequestID:      "req-eval-link-" + sampleID[len("sample-"):],
			AITraceID:      "ai-trace-eval-link-" + sampleID[len("sample-"):],
			ServiceTraceID: "svc-trace-eval-link-" + sampleID[len("sample-"):],
			SpanID:         "span-eval-link-" + sampleID[len("sample-"):],
			MetricName:     "exact_match",
			Score:          1,
			Threshold:      0.8,
		}
		if index == count {
			sample.AITraceID = ""
		}
		samples = append(samples, sample)
	}
	return samples
}

func evalTraceLinkSampleID(index int) string {
	return fmt.Sprintf("sample-%02d", index)
}

func assertMissingEvalTraceLink(t *testing.T, missing []EvalTraceLinkMissingSample, sampleID, field string) {
	t.Helper()

	for _, item := range missing {
		if item.SampleID == sampleID {
			if item.MissingField != field {
				t.Fatalf("missing field for %s = %q, want %q", sampleID, item.MissingField, field)
			}
			return
		}
	}
	t.Fatalf("missing sample %q not found in %#v", sampleID, missing)
}
