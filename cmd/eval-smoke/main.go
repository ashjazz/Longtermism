package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jazzash/ashjazz-aiagent/internal/eval/smoke"
)

const (
	defaultDatasetPath    = smoke.DefaultDatasetPath
	defaultDatasetVersion = smoke.DefaultDatasetVersion

	smokeModeP0                 = "p0"
	smokeModeObservabilityChain = "observability-chain"
)

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
	datasetVersion := flags.String("dataset-version", defaultDatasetVersion, "stable dataset version written to the eval report")
	smokeMode := flags.String("smoke", smokeModeP0, "smoke mode: p0 or observability-chain")
	observabilityScenario := flags.String("scenario", string(smoke.ObservabilityChainScenarioSuccess), "observability-chain scenario")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	switch *smokeMode {
	case smokeModeP0:
		return runP0Smoke(ctx, stdout, *datasetPath, *datasetVersion)
	case smokeModeObservabilityChain:
		return runObservabilityChainSmoke(ctx, stdout, smoke.ObservabilityChainScenario(*observabilityScenario))
	default:
		return fmt.Errorf("unsupported smoke mode %q", *smokeMode)
	}
}

func runP0Smoke(ctx context.Context, stdout io.Writer, datasetPath, datasetVersion string) error {
	// 命令入口保持很薄：真正的 prompt -> fake LLM -> trace -> eval 闭环放在
	// internal/eval/smoke 中，便于单测直接断言每条样例产生的 trace 证据。
	result, err := smoke.RunP0(ctx, smoke.Config{
		DatasetPath:    datasetPath,
		DatasetVersion: datasetVersion,
	})
	if err != nil {
		return err
	}

	return writeJSON(stdout, result.Report, "write eval smoke report")
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
