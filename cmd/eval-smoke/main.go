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
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	// 命令入口保持很薄：真正的 prompt -> fake LLM -> trace -> eval 闭环放在
	// internal/eval/smoke 中，便于单测直接断言每条样例产生的 trace 证据。
	result, err := smoke.RunP0(ctx, smoke.Config{
		DatasetPath:    *datasetPath,
		DatasetVersion: *datasetVersion,
	})
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result.Report); err != nil {
		return fmt.Errorf("write eval smoke report: %w", err)
	}
	return nil
}
