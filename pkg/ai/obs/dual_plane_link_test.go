package obs

import (
	"strings"
	"testing"
)

func TestBuildDualPlaneLinksConnectsHTTPAIAndEvalPlanes(t *testing.T) {
	identity := NewCorrelationIdentity(
		"req-link-001",
		WithServiceSpan("svc-trace-link-001", "span-http-root-001"),
		WithAITraceID("ai-trace-link-001"),
		WithEvalRunID("eval-run-link-001"),
	)

	links, err := BuildDualPlaneLinks(DualPlaneLinkInput{
		Identity:        identity,
		AIObservationID: "obs-generation-link-001",
		EvalSampleID:    "sample-link-001",
	})
	if err != nil {
		t.Fatalf("BuildDualPlaneLinks() error = %v", err)
	}

	assertDualPlaneHTTPParentLink(t, links, identity)
	assertDualPlaneAIChildLink(t, links, identity, "obs-generation-link-001")
	assertDualPlaneEvalLink(t, links, identity, "sample-link-001")
}

func TestBuildDualPlaneLinksRejectsMissingIdentity(t *testing.T) {
	tests := []struct {
		name    string
		input   DualPlaneLinkInput
		wantErr string
	}{
		{
			name: "missing request id",
			input: DualPlaneLinkInput{
				Identity: NewCorrelationIdentity(
					"",
					WithServiceSpan("svc-trace-link-001", "span-http-root-001"),
					WithAITraceID("ai-trace-link-001"),
					WithEvalRunID("eval-run-link-001"),
				),
				AIObservationID: "obs-generation-link-001",
				EvalSampleID:    "sample-link-001",
			},
			wantErr: "request_id",
		},
		{
			name: "missing service trace id",
			input: DualPlaneLinkInput{
				Identity: NewCorrelationIdentity(
					"req-link-001",
					WithServiceSpan("", "span-http-root-001"),
					WithAITraceID("ai-trace-link-001"),
					WithEvalRunID("eval-run-link-001"),
				),
				AIObservationID: "obs-generation-link-001",
				EvalSampleID:    "sample-link-001",
			},
			wantErr: "service_trace_id",
		},
		{
			name: "missing service span id",
			input: DualPlaneLinkInput{
				Identity: NewCorrelationIdentity(
					"req-link-001",
					WithServiceSpan("svc-trace-link-001", ""),
					WithAITraceID("ai-trace-link-001"),
					WithEvalRunID("eval-run-link-001"),
				),
				AIObservationID: "obs-generation-link-001",
				EvalSampleID:    "sample-link-001",
			},
			wantErr: "span_id",
		},
		{
			name: "missing ai trace id",
			input: DualPlaneLinkInput{
				Identity: NewCorrelationIdentity(
					"req-link-001",
					WithServiceSpan("svc-trace-link-001", "span-http-root-001"),
					WithAITraceID(""),
					WithEvalRunID("eval-run-link-001"),
				),
				AIObservationID: "obs-generation-link-001",
				EvalSampleID:    "sample-link-001",
			},
			wantErr: "ai_trace_id",
		},
		{
			name: "missing eval identity",
			input: DualPlaneLinkInput{
				Identity: NewCorrelationIdentity(
					"req-link-001",
					WithServiceSpan("svc-trace-link-001", "span-http-root-001"),
					WithAITraceID("ai-trace-link-001"),
				),
				AIObservationID: "obs-generation-link-001",
				EvalSampleID:    "",
			},
			wantErr: "eval",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildDualPlaneLinks(tt.input)
			if err == nil {
				t.Fatal("BuildDualPlaneLinks() error = nil, want missing identity error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("BuildDualPlaneLinks() error = %q, want containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func assertDualPlaneHTTPParentLink(t *testing.T, links DualPlaneLinks, identity CorrelationIdentity) {
	t.Helper()

	parent := links.HTTPParent
	if parent.RequestID != identity.RequestID {
		t.Fatalf("HTTPParent.RequestID = %q, want %q", parent.RequestID, identity.RequestID)
	}
	if parent.ServiceTraceID != identity.ServiceTraceID {
		t.Fatalf("HTTPParent.ServiceTraceID = %q, want %q", parent.ServiceTraceID, identity.ServiceTraceID)
	}
	if parent.SpanID != identity.SpanID {
		t.Fatalf("HTTPParent.SpanID = %q, want %q", parent.SpanID, identity.SpanID)
	}
}

func assertDualPlaneAIChildLink(t *testing.T, links DualPlaneLinks, identity CorrelationIdentity, observationID string) {
	t.Helper()

	child := links.AIChild
	if child.RequestID != identity.RequestID {
		t.Fatalf("AIChild.RequestID = %q, want %q", child.RequestID, identity.RequestID)
	}
	if child.ServiceTraceID != identity.ServiceTraceID {
		t.Fatalf("AIChild.ServiceTraceID = %q, want %q", child.ServiceTraceID, identity.ServiceTraceID)
	}
	if child.ParentSpanID != identity.SpanID {
		t.Fatalf("AIChild.ParentSpanID = %q, want %q", child.ParentSpanID, identity.SpanID)
	}
	if child.AITraceID != identity.AITraceID {
		t.Fatalf("AIChild.AITraceID = %q, want %q", child.AITraceID, identity.AITraceID)
	}
	if child.ObservationID != observationID {
		t.Fatalf("AIChild.ObservationID = %q, want %q", child.ObservationID, observationID)
	}
}

func assertDualPlaneEvalLink(t *testing.T, links DualPlaneLinks, identity CorrelationIdentity, sampleID string) {
	t.Helper()

	eval := links.EvalLink
	if eval.EvalRunID != identity.EvalRunID {
		t.Fatalf("EvalLink.EvalRunID = %q, want %q", eval.EvalRunID, identity.EvalRunID)
	}
	if eval.SampleID != sampleID {
		t.Fatalf("EvalLink.SampleID = %q, want %q", eval.SampleID, sampleID)
	}
	if eval.RequestID != identity.RequestID {
		t.Fatalf("EvalLink.RequestID = %q, want %q", eval.RequestID, identity.RequestID)
	}
	if eval.AITraceID != identity.AITraceID {
		t.Fatalf("EvalLink.AITraceID = %q, want %q", eval.AITraceID, identity.AITraceID)
	}
	if eval.ServiceTraceID != identity.ServiceTraceID {
		t.Fatalf("EvalLink.ServiceTraceID = %q, want %q", eval.ServiceTraceID, identity.ServiceTraceID)
	}
	if eval.ParentSpanID != identity.SpanID {
		t.Fatalf("EvalLink.ParentSpanID = %q, want %q", eval.ParentSpanID, identity.SpanID)
	}
}
