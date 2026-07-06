package smoke

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/obs"
)

func TestInfrastructureExportFailureSmokeDoesNotFailBusinessResult(t *testing.T) {
	result, err := RunInfrastructureExportFailureSmoke(context.Background(), InfrastructureExportFailureSmokeConfig{
		RequestID:      "req-export-failure-001",
		ServiceTraceID: "svc-trace-export-failure-001",
		SpanID:         "span-export-failure-001",
		BusinessAction: func(context.Context) (string, error) {
			return "business-ok", nil
		},
		Exporter: failingInfrastructureExporter{
			err: errors.New("collector unavailable"),
		},
	})
	if err != nil {
		t.Fatalf("RunInfrastructureExportFailureSmoke() error = %v, want protected exporter failure", err)
	}

	if result.BusinessResult != "business-ok" {
		t.Fatalf("BusinessResult = %q, want business-ok", result.BusinessResult)
	}
	if result.RequestID != "req-export-failure-001" {
		t.Fatalf("RequestID = %q, want req-export-failure-001", result.RequestID)
	}
	if result.ServiceTraceID != "svc-trace-export-failure-001" {
		t.Fatalf("ServiceTraceID = %q, want svc-trace-export-failure-001", result.ServiceTraceID)
	}
	if result.SpanID != "span-export-failure-001" {
		t.Fatalf("SpanID = %q, want span-export-failure-001", result.SpanID)
	}
	if result.FailureStatus != string(obs.FailureTelemetryExportFailed) {
		t.Fatalf("FailureStatus = %q, want %q", result.FailureStatus, obs.FailureTelemetryExportFailed)
	}
	if !strings.Contains(result.FailureMessage, "collector unavailable") {
		t.Fatalf("FailureMessage = %q, want collector unavailable", result.FailureMessage)
	}
}

func TestInfrastructureExportFailureSmokeKeepsBusinessErrorVisible(t *testing.T) {
	_, err := RunInfrastructureExportFailureSmoke(context.Background(), InfrastructureExportFailureSmokeConfig{
		RequestID:      "req-business-failure-001",
		ServiceTraceID: "svc-trace-business-failure-001",
		SpanID:         "span-business-failure-001",
		BusinessAction: func(context.Context) (string, error) {
			return "", errors.New("business failed")
		},
		Exporter: failingInfrastructureExporter{
			err: errors.New("collector unavailable"),
		},
	})

	if err == nil {
		t.Fatalf("RunInfrastructureExportFailureSmoke() error = nil, want business error")
	}
	if !strings.Contains(err.Error(), "business failed") {
		t.Fatalf("RunInfrastructureExportFailureSmoke() error = %q, want business failed", err.Error())
	}
}

type failingInfrastructureExporter struct {
	err error
}

func (e failingInfrastructureExporter) ExportInfrastructureSpan(context.Context, InfrastructureSpanRecord) error {
	return e.err
}
