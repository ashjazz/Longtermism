package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ashjazz/Longtermism/internal/eval/smoke"
	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
)

const (
	defaultDatasetPath    = smoke.DefaultDatasetPath
	defaultDatasetName    = smoke.DefaultDatasetName
	defaultDatasetVersion = smoke.DefaultDatasetVersion
	defaultEvalRunID      = smoke.DefaultEvalRunID

	smokeModeP0                 = "p0"
	smokeModeObservabilityChain = "observability-chain"
)

// evalSmokeOutput 是默认 CLI 输出的低敏报告信封。
//
// report 只保留汇总分数；evalEvidence 只保留 sample、metric、score 和 trace identity。
// 原始 query、prompt、answer、context 和外部响应都不属于命令输出面，避免本地日志或
// CI 产物把敏感内容带出去。
type evalSmokeOutput struct {
	Report       evalSmokeReportSummary     `json:"report"`
	EvalEvidence []evalSmokeEvidenceSummary `json:"evalEvidence,omitempty"`
}

type evalSmokeReportSummary struct {
	Dataset     aieval.DatasetIdentity `json:"dataset"`
	SampleCount int                    `json:"sampleCount"`
	Scores      map[string]float64     `json:"scores"`
}

type evalSmokeEvidenceSummary struct {
	Sample        string                 `json:"sample"`
	Metric        string                 `json:"metric"`
	Score         float64                `json:"score"`
	TraceIdentity evalSmokeTraceIdentity `json:"traceIdentity"`
}

type evalSmokeTraceIdentity struct {
	RequestID      string `json:"request_id"`
	ServiceTraceID string `json:"service_trace_id"`
	SpanID         string `json:"span_id"`
	AITraceID      string `json:"ai_trace_id"`
	EvalRunID      string `json:"eval_run_id"`
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	if err := runE(ctx, args, stdout); err != nil {
		fmt.Fprintf(stderr, "eval smoke failed: %v\n", err)
		return 1
	}
	return 0
}

func runE(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("eval-smoke", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	datasetPath := flags.String("dataset", defaultDatasetPath, "local golden dataset json path")
	datasetName := flags.String("dataset-name", defaultDatasetName, "stable dataset name written to the eval report")
	datasetVersion := flags.String("dataset-version", defaultDatasetVersion, "stable dataset version written to the eval report")
	evalRunID := flags.String("eval-run-id", defaultEvalRunID, "stable eval run id used to emit trace-linked evidence")
	smokeMode := flags.String("smoke", smokeModeP0, "smoke mode: p0 or observability-chain")
	observabilityScenario := flags.String("scenario", string(smoke.ObservabilityChainScenarioSuccess), "observability-chain scenario")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	switch *smokeMode {
	case smokeModeP0:
		return runP0Smoke(ctx, stdout, *datasetPath, *datasetName, *datasetVersion, *evalRunID)
	case smokeModeObservabilityChain:
		return runObservabilityChainSmoke(ctx, stdout, smoke.ObservabilityChainScenario(*observabilityScenario))
	default:
		return fmt.Errorf("unsupported smoke mode %q", *smokeMode)
	}
}

func runP0Smoke(ctx context.Context, stdout io.Writer, datasetPath, datasetName, datasetVersion, evalRunID string) error {
	// 命令入口保持很薄：真正的 prompt -> fake LLM -> trace -> eval 闭环放在
	// internal/eval/smoke 中，便于单测直接断言每条样例产生的 trace 证据。
	result, err := smoke.RunP0(ctx, smoke.Config{
		DatasetPath: datasetPath,
		DatasetIdentity: aieval.DatasetIdentity{
			Name:    datasetName,
			Version: datasetVersion,
		},
		EvalRunID: evalRunID,
	})
	if err != nil {
		return err
	}

	return writeJSON(stdout, newEvalSmokeOutput(result.Report), "write eval smoke report")
}

func runObservabilityChainSmoke(ctx context.Context, stdout io.Writer, scenario smoke.ObservabilityChainScenario) error {
	result, err := smoke.RunObservabilityChainSmoke(ctx, smoke.ObservabilityChainSmokeConfig{
		Scenario:       scenario,
		RequestID:      "req-observability-chain-smoke",
		ServiceTraceID: "svc-trace-observability-chain-smoke",
		SpanID:         "span-observability-chain-smoke",
		AITraceID:      "ai-trace-observability-chain-smoke",
		EvalRunID:      "eval-run-observability-chain-smoke",
		SampleID:       "sample-observability-chain-smoke",
	})
	if err != nil {
		return err
	}

	return writeJSON(stdout, result, "write observability chain smoke report")
}

func writeJSON(stdout io.Writer, value any, action string) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return nil
}

func newEvalSmokeOutput(report aieval.Report) evalSmokeOutput {
	return evalSmokeOutput{
		Report: evalSmokeReportSummary{
			Dataset:     report.Dataset,
			SampleCount: report.SampleCount,
			Scores:      cloneScores(report.Scores),
		},
		EvalEvidence: newEvalSmokeEvidenceSummary(report.Evidence),
	}
}

func newEvalSmokeEvidenceSummary(evidence []aieval.EvaluationEvidence) []evalSmokeEvidenceSummary {
	if len(evidence) == 0 {
		return nil
	}

	summary := make([]evalSmokeEvidenceSummary, len(evidence))
	for index, item := range evidence {
		summary[index] = evalSmokeEvidenceSummary{
			Sample: item.SampleID,
			Metric: item.MetricName,
			Score:  item.Score,
			TraceIdentity: evalSmokeTraceIdentity{
				RequestID:      item.RequestID,
				ServiceTraceID: item.ServiceTraceID,
				SpanID:         item.SpanID,
				AITraceID:      item.AITraceID,
				EvalRunID:      item.EvalRunID,
			},
		}
	}
	return summary
}

func cloneScores(scores map[string]float64) map[string]float64 {
	if scores == nil {
		return nil
	}

	cloned := make(map[string]float64, len(scores))
	for key, value := range scores {
		cloned[key] = value
	}
	return cloned
}
