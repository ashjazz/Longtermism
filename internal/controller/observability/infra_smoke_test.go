// Package observability tests the thin HTTP-facing infra-smoke controller contract.
package observability

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	v1 "github.com/ashjazz/Longtermism/api/v1/observability"
	logicobservability "github.com/ashjazz/Longtermism/internal/logic/observability"
)

func TestInfraSmokeControllerReturnsUniformInfrastructureOnlyEnvelopes(t *testing.T) {
	tests := []infraSmokeControllerTestCase{
		{name: "accepts omitted marker", smokeEnabled: true, wantStatusCode: 0, wantMessage: "OK", wantRunCalls: 1},
		{name: "accepts eight byte marker", smokeEnabled: true, marker: "run-0001", wantStatusCode: 0, wantMessage: "OK", wantRunCalls: 1, wantSmokeMeta: true},
		{name: "accepts 128 byte marker", smokeEnabled: true, marker: "r" + strings.Repeat("a", 127), wantStatusCode: 0, wantMessage: "OK", wantRunCalls: 1, wantSmokeMeta: true},
		{name: "rejects short marker", smokeEnabled: true, marker: "short", wantStatusCode: 400, wantMessage: "invalid infra smoke request"},
		{name: "rejects overlong marker", smokeEnabled: true, marker: "r" + strings.Repeat("a", 128), wantStatusCode: 400, wantMessage: "invalid infra smoke request"},
		{name: "rejects whitespace marker", smokeEnabled: true, marker: "run marker", wantStatusCode: 400, wantMessage: "invalid infra smoke request"},
		{name: "rejects path marker", smokeEnabled: true, marker: "run/marker", wantStatusCode: 400, wantMessage: "invalid infra smoke request"},
		{name: "rejects non ASCII marker", smokeEnabled: true, marker: "run-标记", wantStatusCode: 400, wantMessage: "invalid infra smoke request"},
		{name: "rejects NUL marker", smokeEnabled: true, marker: "run\x00marker", wantStatusCode: 400, wantMessage: "invalid infra smoke request"},
		{name: "returns defensive not found when disabled", smokeEnabled: false, marker: "run-0001", wantStatusCode: 404, wantMessage: "infra smoke disabled"},
		{
			name:           "sanitizes internal runner failure",
			smokeEnabled:   true,
			marker:         "run-0001",
			runError:       errors.New("Authorization: Bearer synthetic-secret prompt=private endpoint=https://private.example.invalid"),
			wantStatusCode: 500,
			wantMessage:    "internal server error",
			wantRunCalls:   1,
			wantSmokeMeta:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runInfraSmokeControllerCase(t, tt) })
	}
}

type infraSmokeControllerTestCase struct {
	name           string
	smokeEnabled   bool
	marker         string
	runError       error
	wantStatusCode int
	wantMessage    string
	wantRunCalls   int
	wantSmokeMeta  bool
}

func runInfraSmokeControllerCase(t *testing.T, tt infraSmokeControllerTestCase) {
	t.Helper()
	runner := &infraSmokeRunnerStub{err: tt.runError}
	controller := NewV1(InfraSmokeControllerDependencies{
		SmokeEnabled:         tt.smokeEnabled,
		Runner:               runner,
		RequestIDFromContext: func(context.Context) string { return "req-t039" },
	})
	success, err := controller.InfraSmoke(context.Background(), &v1.InfraSmokeReq{SmokeRunID: tt.marker})
	if tt.wantStatusCode == 0 {
		if err != nil {
			t.Fatalf("InfraSmoke() error = %v", err)
		}
		assertInfraSmokeControllerSuccess(t, success, tt.marker, tt.wantSmokeMeta)
	} else {
		if success != nil {
			t.Fatalf("InfraSmoke() success = %#v, want nil on HTTP error", success)
		}
		assertInfraSmokeControllerError(t, err, tt.wantStatusCode, tt.wantMessage, tt.marker, tt.wantSmokeMeta, tt.runError)
	}
	if runner.calls != tt.wantRunCalls {
		t.Fatalf("runner calls = %d, want %d", runner.calls, tt.wantRunCalls)
	}
	if tt.wantRunCalls == 1 && (runner.input.RequestID != "req-t039" || runner.input.SmokeRunID != tt.marker) {
		t.Fatalf("runner input = %#v, want request ID and marker forwarded unchanged", runner.input)
	}
}

// Controller errors expose an already-sanitized HTTP envelope. T052 can map this narrow
// contract to GoFrame's response writer without ever exposing a provider/exporter error.
func assertInfraSmokeControllerError(t *testing.T, err error, wantStatusCode int, wantMessage, marker string, wantSmokeMeta bool, internalErr error) {
	t.Helper()
	if err == nil {
		t.Fatal("InfraSmoke() error = nil, want a mapped HTTP error")
	}
	var httpError InfraSmokeControllerError
	if !errors.As(err, &httpError) {
		t.Fatalf("InfraSmoke() error type = %T, want InfraSmokeControllerError", err)
	}
	if httpError.StatusCode() != wantStatusCode {
		t.Fatalf("controller error status = %d, want %d", httpError.StatusCode(), wantStatusCode)
	}
	assertInfraSmokeControllerEnvelope(t, httpError.Envelope(), wantStatusCode, wantMessage, marker, wantSmokeMeta, internalErr)
}

func assertInfraSmokeControllerSuccess(t *testing.T, envelope *v1.InfraSmokeSuccessEnvelope, marker string, wantSmokeMeta bool) {
	t.Helper()
	if envelope == nil {
		t.Fatal("InfraSmoke() success = nil")
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal success envelope: %v", err)
	}
	var decoded struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
		Meta    json.RawMessage `json:"meta"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal success envelope: %v", err)
	}
	if decoded.Code != 0 || decoded.Message != "OK" || string(decoded.Data) != `{"status":"ok"}` {
		t.Fatalf("success envelope = %s, want code=0/message=OK/status=ok", payload)
	}
	assertInfraSmokeControllerMeta(t, decoded.Meta, marker, wantSmokeMeta)
}

func assertInfraSmokeControllerEnvelope(t *testing.T, envelope v1.InfraSmokeErrorEnvelope, wantStatusCode int, wantMessage, marker string, wantSmokeMeta bool, internalErr error) {
	t.Helper()
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal error envelope: %v", err)
	}
	var decoded struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
		Meta    json.RawMessage `json:"meta"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal error envelope: %v", err)
	}
	if decoded.Code != wantStatusCode || decoded.Message != wantMessage || string(decoded.Data) != "null" {
		t.Fatalf("error envelope = %s, want code=%d message=%q data=null", payload, wantStatusCode, wantMessage)
	}
	assertInfraSmokeControllerMeta(t, decoded.Meta, marker, wantSmokeMeta)
	if internalErr != nil && strings.Contains(payload, internalErr.Error()) {
		t.Fatalf("controller error envelope leaked full internal failure detail: %s", payload)
	}
	for _, forbidden := range []string{"Authorization", "synthetic-secret", "prompt=private", "private.example.invalid"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("controller error envelope leaked internal failure detail: %s", payload)
		}
	}
}

func assertInfraSmokeControllerMeta(t *testing.T, raw json.RawMessage, marker string, wantSmokeMeta bool) {
	t.Helper()
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal response metadata: %v", err)
	}
	if string(meta["request_id"]) != `"req-t039"` {
		t.Fatalf("response metadata = %s, want request_id", raw)
	}
	if (meta["smoke_run_id"] != nil) != wantSmokeMeta || (wantSmokeMeta && string(meta["smoke_run_id"]) != `"`+marker+`"`) {
		t.Fatalf("response metadata smoke marker = %s, want present=%v", raw, wantSmokeMeta)
	}
	if len(meta) != 1+boolToInt(wantSmokeMeta) || meta["ai_trace_id"] != nil || meta["eval_summary"] != nil {
		t.Fatalf("infra-only response metadata = %s, want request/smoke identities only", raw)
	}
}

type infraSmokeRunnerStub struct {
	calls int
	input logicobservability.InfraSmokeInput
	err   error
}

func (r *infraSmokeRunnerStub) Run(_ context.Context, input logicobservability.InfraSmokeInput) (logicobservability.InfraSmokeResult, error) {
	r.calls++
	r.input = input
	if r.err != nil {
		return logicobservability.InfraSmokeResult{}, r.err
	}
	return logicobservability.InfraSmokeResult{Status: logicobservability.InfraSmokeStatusOK}, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
