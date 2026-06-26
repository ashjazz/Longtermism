package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	aieval "github.com/jazzash/ashjazz-aiagent/pkg/ai/eval"
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
		"-dataset-version", "p0-smoke-test",
	}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("run() exit code = %d, want 0, stderr = %s", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var report aieval.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not eval report json: %v\nstdout = %s", err, stdout.String())
	}
	if report.DatasetVersion != "p0-smoke-test" {
		t.Fatalf("dataset version = %q, want p0-smoke-test", report.DatasetVersion)
	}
	if report.SampleCount != 3 {
		t.Fatalf("sample count = %d, want 3", report.SampleCount)
	}
	assertScore(t, report.Scores, "exact_match", 1)
	assertScore(t, report.Scores, "context_hit", 1)
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
