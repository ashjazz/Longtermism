package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	t180Canary = "T180_SYNTHETIC_CANARY"
	t180Raw    = "raw-t180-provider-response"
	t180Secret = "Bearer t180-secret-must-not-leak"
)

func TestPrivacyFixtureRequiresOneSuccessfulProtectedChatBeforePersistingLowSensitiveArtifacts(t *testing.T) {
	startedAt := time.Now().UTC()
	order := make([]string, 0, 3)
	trigger := &t180PrivacyTrigger{result: PrivacyFixtureTriggerResult{
		Attempted:  true,
		Protected:  true,
		StatusCode: 200,
		Body:       []byte(`{"code":0,"message":"success","data":{"reply":"safe"},"meta":{"request_id":"req-t180","ai_trace_id":"ai-t1800"}}`),
	}, order: &order}
	manifest := &t180ManifestConsumer{manifest: ChatRunManifestInput{
		SmokeRunID: "marker-t180", RequestID: "req-t180", AITraceID: "ai-t1800",
		ServiceTraceID: "1234567890abcdef1234567890abcdef", SpanID: "1234567890abcdef",
	}, order: &order}
	writer := &t180FixtureWriter{refs: PrivacyFixtureArtifactRefs{
		ManifestRef: "manifest-t180.json", APISummaryRef: "api-t180.json", ApplicationLogRef: "application-log-t180.json", ChatReportRef: "chat-report-t180.json",
		CollectorArtifactRef: "collector-t180.json",
	}, order: &order}

	result, err := RunPrivacyFixture(context.Background(), PrivacyFixtureRequest{
		RunID: "run-t180", Marker: "marker-t180", Profile: "grafana",
		ForbiddenCanary: t180Canary, StartedAt: startedAt, Deadline: startedAt.Add(time.Minute),
	}, PrivacyFixtureDependencies{Trigger: trigger, Manifest: manifest, Writer: writer})
	if err != nil {
		t.Fatalf("RunPrivacyFixture() error = %v", err)
	}
	if trigger.calls != 1 || manifest.calls != 1 || writer.calls != 1 {
		t.Fatalf("calls trigger=%d manifest=%d writer=%d, want exactly one ordered fixture", trigger.calls, manifest.calls, writer.calls)
	}
	if strings.Join(order, ",") != "trigger,manifest,writer" {
		t.Fatalf("fixture order = %v, want trigger then manifest consume then artifact write", order)
	}
	if manifest.key != "marker-t180" {
		t.Fatalf("manifest consume key = %q, want authenticated marker", manifest.key)
	}
	if !result.RequestSent || !result.ChatSucceeded || result.ManifestRef != writer.refs.ManifestRef {
		t.Fatalf("fixture result = %#v, want successful request proof and trusted manifest ref", result)
	}
	if result.ChatReportRef != writer.refs.ChatReportRef || result.CollectorArtifactRef != writer.refs.CollectorArtifactRef {
		t.Fatalf("fixture refs = %#v, want all writer-registered artifacts", result)
	}
	if result.APISummaryRef != writer.refs.APISummaryRef || result.ApplicationLogRef != writer.refs.ApplicationLogRef {
		t.Fatalf("local fixture refs = %#v, want API and application-log registrations", result)
	}
	if encoded, marshalErr := json.Marshal(trigger.result); marshalErr == nil || bytes.Contains(encoded, trigger.result.Body) {
		t.Fatalf("raw trigger result marshaled as %s, want serialization forbidden", encoded)
	}
	if result.RequestID != manifest.manifest.RequestID || result.AITraceID != manifest.manifest.AITraceID ||
		result.ServiceTraceID != manifest.manifest.ServiceTraceID || result.SpanID != manifest.manifest.SpanID {
		t.Fatalf("fixture identity = %#v, want consumed native manifest identity", result)
	}
	if writer.input.APIScanSummary["synthetic_canary"] != 0 {
		t.Fatalf("API scan summary = %#v, want zero canary hits", writer.input.APIScanSummary)
	}
	if writer.input.RunID != "run-t180" || writer.input.Marker != "marker-t180" ||
		writer.input.RequestID != "req-t180" || writer.input.AITraceID != "ai-t1800" ||
		writer.input.ServiceTraceID != manifest.manifest.ServiceTraceID || writer.input.SpanID != manifest.manifest.SpanID ||
		!writer.input.StartedAt.Equal(startedAt) || !writer.input.Deadline.Equal(startedAt.Add(time.Minute)) ||
		writer.input.ChatReport == nil {
		t.Fatalf("artifact input = %#v, want complete same-run identity/window and chat report", writer.input)
	}
	chatReportJSON, err := json.Marshal(writer.input.ChatReport)
	if err != nil || !strings.Contains(string(chatReportJSON), `"scenario":"chat"`) || !strings.Contains(string(chatReportJSON), `"status":"passed"`) {
		t.Fatalf("chat fixture report = %s error=%v, want schema-valid passed chat report", chatReportJSON, err)
	}
	assertT180LowSensitiveJSON(t, result, writer.input)
}

func TestPrivacyFixtureArtifactFailureDoesNotExposeRawFacts(t *testing.T) {
	startedAt := time.Now().UTC()
	trigger := &t180PrivacyTrigger{result: PrivacyFixtureTriggerResult{
		Attempted:  true,
		Protected:  true,
		StatusCode: 200,
		Body:       []byte(`{"code":0,"message":"success","data":{},"meta":{"request_id":"req-t180","ai_trace_id":"ai-t1800"}}`),
	}}
	consumer := &t180ManifestConsumer{manifest: ChatRunManifestInput{
		SmokeRunID: "marker-t180", RequestID: "req-t180", AITraceID: "ai-t1800",
		ServiceTraceID: "1234567890abcdef1234567890abcdef", SpanID: "1234567890abcdef",
	}}
	writer := &t180FixtureWriter{err: errors.New(t180Raw)}
	_, err := RunPrivacyFixture(context.Background(), PrivacyFixtureRequest{
		RunID: "run-t180", Marker: "marker-t180", Profile: "grafana", ForbiddenCanary: t180Canary,
		StartedAt: startedAt, Deadline: startedAt.Add(time.Minute),
	}, PrivacyFixtureDependencies{Trigger: trigger, Manifest: consumer, Writer: writer})
	if err == nil || writer.calls != 1 || strings.Contains(err.Error(), t180Raw) {
		t.Fatalf("artifact failure error=%v writes=%d, want one attempted write and sanitized failure", err, writer.calls)
	}
}

func TestPrivacyFixtureFailsClosedBeforeArtifactsOrBackendProof(t *testing.T) {
	startedAt := time.Now().UTC()
	tests := []struct {
		name         string
		trigger      PrivacyFixtureTriggerResult
		manifest     ChatRunManifestInput
		wantConsumes int
	}{
		{name: "request not attempted", trigger: PrivacyFixtureTriggerResult{StatusCode: 200}, wantConsumes: 0},
		{name: "request not protected", trigger: PrivacyFixtureTriggerResult{Attempted: true, StatusCode: 200}, wantConsumes: 0},
		{name: "request not successful", trigger: PrivacyFixtureTriggerResult{Attempted: true, Protected: true, StatusCode: 503, Body: []byte(t180Raw)}, wantConsumes: 0},
		{name: "oversized response", trigger: PrivacyFixtureTriggerResult{Attempted: true, Protected: true, StatusCode: 200, Body: make([]byte, (1<<20)+1)}, wantConsumes: 0},
		{name: "malformed response", trigger: PrivacyFixtureTriggerResult{Attempted: true, Protected: true, StatusCode: 200, Body: []byte(`{"code":0`)}, wantConsumes: 0},
		{name: "business failure", trigger: PrivacyFixtureTriggerResult{Attempted: true, Protected: true, StatusCode: 200, Body: []byte(`{"code":10001,"message":"failed","data":null,"meta":{"request_id":"req-t180","ai_trace_id":"ai-t1800"}}`)}, wantConsumes: 0},
		{name: "missing response identity", trigger: PrivacyFixtureTriggerResult{Attempted: true, Protected: true, StatusCode: 200, Body: []byte(`{"code":0,"message":"success","data":{}}`)}, wantConsumes: 0},
		{name: "canary leaked in API response", trigger: PrivacyFixtureTriggerResult{Attempted: true, Protected: true, StatusCode: 200, Body: []byte(`{"code":0,"message":"success","data":{"reply":"` + t180Canary + `"},"meta":{"request_id":"req-t180","ai_trace_id":"ai-t1800"}}`)}, wantConsumes: 0},
		{name: "foreign manifest", trigger: PrivacyFixtureTriggerResult{Attempted: true, Protected: true, StatusCode: 200, Body: []byte(`{"code":0,"message":"success","data":{},"meta":{"request_id":"req-t180","ai_trace_id":"ai-t1800"}}`)}, manifest: ChatRunManifestInput{SmokeRunID: "foreign-marker", RequestID: "req-t180", AITraceID: "ai-t1800", ServiceTraceID: "1234567890abcdef1234567890abcdef", SpanID: "1234567890abcdef"}, wantConsumes: 1},
		{name: "request identity conflict", trigger: PrivacyFixtureTriggerResult{Attempted: true, Protected: true, StatusCode: 200, Body: []byte(`{"code":0,"message":"success","data":{},"meta":{"request_id":"req-t180","ai_trace_id":"ai-t1800"}}`)}, manifest: ChatRunManifestInput{SmokeRunID: "marker-t180", RequestID: "req-foreign", AITraceID: "ai-t1800", ServiceTraceID: "1234567890abcdef1234567890abcdef", SpanID: "1234567890abcdef"}, wantConsumes: 1},
		{name: "AI identity conflict", trigger: PrivacyFixtureTriggerResult{Attempted: true, Protected: true, StatusCode: 200, Body: []byte(`{"code":0,"message":"success","data":{},"meta":{"request_id":"req-t180","ai_trace_id":"ai-t1800"}}`)}, manifest: ChatRunManifestInput{SmokeRunID: "marker-t180", RequestID: "req-t180", AITraceID: "ai-foreign", ServiceTraceID: "1234567890abcdef1234567890abcdef", SpanID: "1234567890abcdef"}, wantConsumes: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trigger := &t180PrivacyTrigger{result: tt.trigger}
			consumer := &t180ManifestConsumer{manifest: tt.manifest}
			writer := &t180FixtureWriter{}
			_, err := RunPrivacyFixture(context.Background(), PrivacyFixtureRequest{
				RunID: "run-t180", Marker: "marker-t180", Profile: "grafana", ForbiddenCanary: t180Canary,
				StartedAt: startedAt, Deadline: startedAt.Add(time.Minute),
			}, PrivacyFixtureDependencies{Trigger: trigger, Manifest: consumer, Writer: writer})
			if err == nil || strings.Contains(err.Error(), t180Canary) || strings.Contains(err.Error(), t180Raw) {
				t.Fatalf("RunPrivacyFixture() error = %v, want low-sensitive failure", err)
			}
			if writer.calls != 0 || consumer.calls != tt.wantConsumes {
				t.Fatalf("artifact writes=%d manifest consumes=%d, want 0/%d", writer.calls, consumer.calls, tt.wantConsumes)
			}
		})
	}
}

func TestPrivacyFixtureRejectsNoRequestAllZeroProof(t *testing.T) {
	startedAt := time.Now().UTC()
	trigger := &t180PrivacyTrigger{err: errors.New(t180Secret)}
	writer := &t180FixtureWriter{}
	consumer := &t180ManifestConsumer{}
	_, err := RunPrivacyFixture(context.Background(), PrivacyFixtureRequest{
		RunID: "run-t180", Marker: "marker-t180", Profile: "grafana", ForbiddenCanary: t180Canary,
		StartedAt: startedAt, Deadline: startedAt.Add(time.Minute),
	}, PrivacyFixtureDependencies{Trigger: trigger, Manifest: consumer, Writer: writer})
	if err == nil || writer.calls != 0 || consumer.calls != 0 || strings.Contains(err.Error(), t180Secret) {
		t.Fatalf("no-request result error=%v writes=%d consumes=%d, want fail closed before evidence consumption", err, writer.calls, consumer.calls)
	}
}

func assertT180LowSensitiveJSON(t *testing.T, values ...any) {
	t.Helper()
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal low-sensitive artifact: %v", err)
		}
		for _, forbidden := range []string{t180Canary, t180Raw, t180Secret, "reply", "authorization", "endpoint"} {
			if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(forbidden)) {
				t.Fatalf("serialized artifact leaked %q: %s", forbidden, encoded)
			}
		}
	}
}

type t180PrivacyTrigger struct {
	calls  int
	result PrivacyFixtureTriggerResult
	err    error
	order  *[]string
}

func (trigger *t180PrivacyTrigger) Trigger(_ context.Context, request PrivacyFixtureTriggerRequest) (PrivacyFixtureTriggerResult, error) {
	trigger.calls++
	if trigger.order != nil {
		*trigger.order = append(*trigger.order, "trigger")
	}
	if request.RunID == "" || request.Marker == "" || request.ForbiddenCanary != t180Canary {
		return PrivacyFixtureTriggerResult{}, errors.New("invalid protected trigger")
	}
	return trigger.result, trigger.err
}

type t180ManifestConsumer struct {
	calls    int
	key      string
	manifest ChatRunManifestInput
	err      error
	order    *[]string
}

func (consumer *t180ManifestConsumer) Consume(_ context.Context, key string) (ChatRunManifestInput, error) {
	consumer.calls++
	consumer.key = key
	if consumer.order != nil {
		*consumer.order = append(*consumer.order, "manifest")
	}
	return consumer.manifest, consumer.err
}

type t180FixtureWriter struct {
	calls int
	input PrivacyFixtureArtifactInput
	refs  PrivacyFixtureArtifactRefs
	err   error
	order *[]string
}

func (writer *t180FixtureWriter) Write(_ context.Context, input PrivacyFixtureArtifactInput) (PrivacyFixtureArtifactRefs, error) {
	writer.calls++
	if writer.order != nil {
		*writer.order = append(*writer.order, "writer")
	}
	writer.input = input
	return writer.refs, writer.err
}
