package obs

import (
	"context"
	"testing"
	"time"
)

func TestRecordWithExportFailureProtectionReturnsStableFailureStatus(t *testing.T) {
	trace := NewTrace(
		"trace-export-failure",
		"observability_smoke",
		time.Unix(1, 0),
		WithOutcome("success"),
	)

	status := RecordWithExportFailureProtection(context.Background(), panicTracer{}, trace)

	if status != FailureTelemetryExportFailed {
		t.Fatalf("failure status = %q, want %q", status, FailureTelemetryExportFailed)
	}
	if trace.OutcomeStatus != "success" {
		t.Fatalf("business OutcomeStatus = %q, want unchanged success", trace.OutcomeStatus)
	}
}

func TestRecordWithExportFailureProtectionDoesNotPanic(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("RecordWithExportFailureProtection() panic = %#v, want no panic", recovered)
		}
	}()

	_ = RecordWithExportFailureProtection(context.Background(), panicTracer{}, Trace{
		TraceID:       "trace-no-panic",
		Feature:       "observability_smoke",
		Timestamp:     time.Unix(2, 0),
		OutcomeStatus: "success",
	})
}

func TestRecordWithExportFailureProtectionReturnsEmptyStatusOnSuccess(t *testing.T) {
	tracer := &recordCountingTracer{}

	status := RecordWithExportFailureProtection(context.Background(), tracer, Trace{
		TraceID:       "trace-export-success",
		Feature:       "observability_smoke",
		Timestamp:     time.Unix(3, 0),
		OutcomeStatus: "success",
	})

	if status != "" {
		t.Fatalf("failure status = %q, want empty status on successful export", status)
	}
	if tracer.count != 1 {
		t.Fatalf("record count = %d, want 1", tracer.count)
	}
}

type panicTracer struct{}

func (panicTracer) Record(context.Context, Trace) {
	panic("exporter unavailable")
}

type recordCountingTracer struct {
	count int
}

func (t *recordCountingTracer) Record(context.Context, Trace) {
	t.count++
}
