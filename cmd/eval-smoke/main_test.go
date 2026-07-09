package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ashjazz/Longtermism/internal/eval/smoke"
)

func TestRunUsesDefaultFakePredictWithoutAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	// Arrange/Act：显式传入 golden 文件，避免测试依赖执行者当前所在目录。
	// 这里的重点不是调用真实模型，而是证明本地门禁可以在纯离线环境跑通。
	exitCode := run(context.Background(), []string{
		"-dataset", "../../internal/eval/golden/p0_smoke.json",
		"-dataset-name", "p0-smoke",
		"-dataset-version", "p0-smoke-test",
	}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("run() exit code = %d, want 0, stderr = %s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var output evalSmokeOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("stdout is not eval smoke output json: %v\nstdout = %s", err, stdout.String())
	}
	if output.Report.Dataset.Name != "p0-smoke" {
		t.Fatalf("dataset name = %q, want p0-smoke", output.Report.Dataset.Name)
	}
	if output.Report.Dataset.Version != "p0-smoke-test" {
		t.Fatalf("dataset version = %q, want p0-smoke-test", output.Report.Dataset.Version)
	}
	if output.Report.SampleCount != 3 {
		t.Fatalf("sample count = %d, want 3", output.Report.SampleCount)
	}
	assertScore(t, output.Report.Scores, "exact_match", 1)
	assertScore(t, output.Report.Scores, "context_hit", 1)
	assertEvalSmokeEvidenceSummary(t, output.EvalEvidence, 6)

	rendered := stdout.String()
	for _, forbidden := range []string{
		"P0 最小 AI 工程闭环包含哪些关键环节？",
		"Q=",
		"Context",
		"prompt -> llm -> obs -> eval",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("stdout leaked forbidden raw content %q: %s", forbidden, rendered)
		}
	}
}

func TestRunReturnsNonZeroForMissingDataset(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{
		"-dataset", "../../internal/eval/golden/missing.json",
	}, &stdout, &stderr)

	if exitCode == 0 {
		t.Fatalf("run() exit code = 0, want non-zero")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty on failure", stdout.String())
	}
	if !strings.Contains(stderr.String(), "eval smoke failed") {
		t.Fatalf("stderr = %q, want eval smoke failed", stderr.String())
	}
	if !strings.Contains(stderr.String(), "missing.json") {
		t.Fatalf("stderr = %q, want missing dataset path", stderr.String())
	}
}

func TestRunObservabilityChainSmokeCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{
		"-smoke", "observability-chain",
		"-scenario", "retrieval_miss",
	}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("run() exit code = %d, want 0, stderr = %s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var result smoke.ObservabilityChainSmokeResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not observability chain json: %v\nstdout = %s", err, stdout.String())
	}
	if result.RequestID == "" || result.ServiceTraceID == "" || result.RootSpanID == "" || result.RootAITraceID == "" {
		t.Fatalf("result missing correlation identity: %#v", result)
	}
	if result.OutcomeStatus != "failure" {
		t.Fatalf("OutcomeStatus = %q, want failure", result.OutcomeStatus)
	}
	if result.FailureStatus != "retrieval_miss" {
		t.Fatalf("FailureStatus = %q, want retrieval_miss", result.FailureStatus)
	}
	if len(result.ServiceStages) == 0 || len(result.AIObservations) == 0 || len(result.EvalEvidence) == 0 {
		t.Fatalf("result missing chain evidence: %#v", result)
	}
}

func TestRunObservabilityChainSmokeCommandReturnsNonZeroForUnknownScenario(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run(context.Background(), []string{
		"-smoke", "observability-chain",
		"-scenario", "unknown",
	}, &stdout, &stderr)

	if exitCode == 0 {
		t.Fatalf("run() exit code = 0, want non-zero")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty on failure", stdout.String())
	}
	if !strings.Contains(stderr.String(), "observability chain scenario") {
		t.Fatalf("stderr = %q, want observability chain scenario diagnostic", stderr.String())
	}
}

func assertScore(t *testing.T, scores map[string]float64, metricName string, want float64) {
	t.Helper()

	got, ok := scores[metricName]
	if !ok {
		t.Fatalf("score %q is missing from %#v", metricName, scores)
	}
	if got != want {
		t.Fatalf("score %q = %v, want %v", metricName, got, want)
	}
}

func assertEvalSmokeEvidenceSummary(t *testing.T, evidence []evalSmokeEvidenceSummary, wantLen int) {
	t.Helper()

	if len(evidence) != wantLen {
		t.Fatalf("eval evidence summary length = %d, want %d: %#v", len(evidence), wantLen, evidence)
	}
	for _, item := range evidence {
		if item.Sample == "" {
			t.Fatalf("eval evidence item missing sample: %#v", item)
		}
		if item.Metric == "" {
			t.Fatalf("eval evidence item missing metric: %#v", item)
		}
		if item.Score != 1 {
			t.Fatalf("eval evidence item score = %v, want 1: %#v", item.Score, item)
		}
		if item.TraceIdentity.RequestID == "" ||
			item.TraceIdentity.ServiceTraceID == "" ||
			item.TraceIdentity.SpanID == "" ||
			item.TraceIdentity.AITraceID == "" ||
			item.TraceIdentity.EvalRunID == "" {
			t.Fatalf("eval evidence item missing trace identity: %#v", item)
		}
	}
}
