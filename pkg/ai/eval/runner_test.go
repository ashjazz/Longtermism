package eval

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRunnerBuildsReportWithMetricAverages(t *testing.T) {
	t.Parallel()

	runner := NewRunner("p0-smoke-v1")
	dataset := runnerTestDataset{samples: []Sample{
		{ID: "sample-001", Query: "什么是 P0？"},
		{ID: "sample-002", Query: "如何验证 eval？"},
	}}
	predict := func(_ context.Context, sample Sample) (Prediction, error) {
		return Prediction{Answer: "answer for " + sample.ID}, nil
	}
	metrics := []Metric{
		runnerTestMetric{
			name: "exact_match",
			scores: map[string]float64{
				"sample-001": 1,
				"sample-002": 0,
			},
		},
		runnerTestMetric{
			name: "contains_all",
			scores: map[string]float64{
				"sample-001": 0.5,
				"sample-002": 1,
			},
		},
	}

	report, err := runner.Run(context.Background(), dataset, predict, metrics)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if report.DatasetVersion != "p0-smoke-v1" {
		t.Fatalf("DatasetVersion = %q, want p0-smoke-v1", report.DatasetVersion)
	}
	if report.SampleCount != 2 {
		t.Fatalf("SampleCount = %d, want 2", report.SampleCount)
	}
	assertReportScore(t, report, "exact_match", 0.5)
	assertReportScore(t, report, "contains_all", 0.75)
}

func TestRunnerReturnsPredictionErrorWithSampleContext(t *testing.T) {
	t.Parallel()

	runner := NewRunner("p0-smoke-v1")
	dataset := runnerTestDataset{samples: []Sample{
		{ID: "sample-ok", Query: "first"},
		{ID: "sample-failed", Query: "second"},
	}}
	predict := func(_ context.Context, sample Sample) (Prediction, error) {
		if sample.ID == "sample-failed" {
			return Prediction{}, errors.New("model unavailable")
		}
		return Prediction{Answer: "ok"}, nil
	}

	_, err := runner.Run(context.Background(), dataset, predict, []Metric{
		runnerTestMetric{name: "exact_match", scores: map[string]float64{"sample-ok": 1}},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want prediction error")
	}
	if !strings.Contains(err.Error(), "sample-failed") {
		t.Fatalf("Run() error = %v, want sample id context", err)
	}
	if !strings.Contains(err.Error(), "predict") {
		t.Fatalf("Run() error = %v, want predict context", err)
	}
}

func TestRunnerReturnsMetricErrorWithSampleAndMetricContext(t *testing.T) {
	t.Parallel()

	runner := NewRunner("p0-smoke-v1")
	dataset := runnerTestDataset{samples: []Sample{{ID: "sample-metric-error", Query: "metric error"}}}
	predict := func(_ context.Context, _ Sample) (Prediction, error) {
		return Prediction{Answer: "ok"}, nil
	}

	_, err := runner.Run(context.Background(), dataset, predict, []Metric{
		runnerTestMetric{name: "exact_match", err: errors.New("bad metric input")},
	})
	if err == nil {
		t.Fatal("Run() error = nil, want metric error")
	}
	if !strings.Contains(err.Error(), "sample-metric-error") {
		t.Fatalf("Run() error = %v, want sample id context", err)
	}
	if !strings.Contains(err.Error(), "exact_match") {
		t.Fatalf("Run() error = %v, want metric name context", err)
	}
}

type runnerTestDataset struct {
	samples []Sample
	err     error
}

func (d runnerTestDataset) Load(ctx context.Context) ([]Sample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.err != nil {
		return nil, d.err
	}
	return append([]Sample(nil), d.samples...), nil
}

type runnerTestMetric struct {
	name   string
	scores map[string]float64
	err    error
}

func (m runnerTestMetric) Name() string {
	return m.name
}

func (m runnerTestMetric) Score(_ context.Context, sample Sample, _ Prediction) (float64, error) {
	if m.err != nil {
		return 0, m.err
	}
	score, ok := m.scores[sample.ID]
	if !ok {
		return 0, fmt.Errorf("missing score for sample %s", sample.ID)
	}
	return score, nil
}

func assertReportScore(t *testing.T, report Report, metricName string, want float64) {
	t.Helper()

	got, ok := report.Scores[metricName]
	if !ok {
		t.Fatalf("report score %q missing; scores = %#v", metricName, report.Scores)
	}
	if got != want {
		t.Fatalf("report score %q = %v, want %v", metricName, got, want)
	}
}
