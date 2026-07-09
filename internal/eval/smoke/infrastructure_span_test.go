package smoke

import (
	"context"
	"testing"
	"time"

	obstestutil "github.com/ashjazz/Longtermism/pkg/ai/obs/testutil"
)

func TestInfrastructureSpanSmokeRecordsHTTPServiceSpan(t *testing.T) {
	sink := obstestutil.NewMemorySpanSink()

	result, err := RunInfrastructureSpanSmoke(context.Background(), InfrastructureSpanSmokeConfig{
		Sink:           sink,
		Method:         "POST",
		Path:           "/api/v1/chat",
		StatusCode:     202,
		Duration:       37 * time.Millisecond,
		RequestID:      "req-infra-001",
		ServiceTraceID: "svc-trace-infra-001",
		SpanID:         "span-infra-001",
	})
	if err != nil {
		t.Fatalf("RunInfrastructureSpanSmoke() error = %v", err)
	}

	if result.RequestID != "req-infra-001" {
		t.Fatalf("RequestID = %q, want req-infra-001", result.RequestID)
	}
	if result.ServiceTraceID != "svc-trace-infra-001" {
		t.Fatalf("ServiceTraceID = %q, want svc-trace-infra-001", result.ServiceTraceID)
	}
	if result.SpanID != "span-infra-001" {
		t.Fatalf("SpanID = %q, want span-infra-001", result.SpanID)
	}

	snapshots := sink.Snapshots()
	if len(snapshots) != 1 {
		t.Fatalf("span snapshot count = %d, want 1", len(snapshots))
	}

	// 基础设施平面的 smoke 关注传统服务入口事实：方法、路径、状态码、
	// 耗时和关联身份。后续 AI 语义平面会通过这些身份挂到同一次请求下。
	snapshot := snapshots[0]
	if snapshot.Name != "http.server.request" {
		t.Fatalf("Name = %q, want http.server.request", snapshot.Name)
	}
	if snapshot.RequestID != "req-infra-001" {
		t.Fatalf("snapshot RequestID = %q, want req-infra-001", snapshot.RequestID)
	}
	if snapshot.ServiceTraceID != "svc-trace-infra-001" {
		t.Fatalf("snapshot ServiceTraceID = %q, want svc-trace-infra-001", snapshot.ServiceTraceID)
	}
	if snapshot.SpanID != "span-infra-001" {
		t.Fatalf("snapshot SpanID = %q, want span-infra-001", snapshot.SpanID)
	}

	assertSpanAttribute(t, snapshot, "http.request.method", "POST")
	assertSpanAttribute(t, snapshot, "url.path", "/api/v1/chat")
	assertSpanAttribute(t, snapshot, "http.response.status_code", "202")
	assertSpanAttribute(t, snapshot, "duration_ms", "37")
	assertSpanAttribute(t, snapshot, "outcome", "success")
}

func assertSpanAttribute(t *testing.T, snapshot obstestutil.SpanSnapshot, key, want string) {
	t.Helper()

	if snapshot.Attributes[key] != want {
		t.Fatalf("Attributes[%q] = %q, want %q", key, snapshot.Attributes[key], want)
	}
}
