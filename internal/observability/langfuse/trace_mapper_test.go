// Package langfuse fixes the platform-only projection boundary for Langfuse OTLP attributes.
package langfuse

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

const (
	t077OTelTraceID = "0123456789abcdef0123456789abcdef"
	t077OTelSpanID  = "0123456789abcdef"
	t077AITraceID   = "ai-trace-t077-domain"
)

func TestMapTraceToProjectionProjectsOnlyExplicitAllowlist(t *testing.T) {
	span := validT077SpanSnapshot()
	span.Attributes = map[string]string{
		"ai.feature":                               "chat",
		"ai.outcome":                               "success",
		"longtermism.ai.trace_id":                  t077AITraceID,
		"gen_ai.provider.name":                     "openai-compatible",
		"gen_ai.request.model":                     "server-requested-model",
		"gen_ai.response.model":                    "provider-actual-model",
		"gen_ai.usage.input_tokens":                "11",
		"gen_ai.usage.output_tokens":               "17",
		"gen_ai.usage.reasoning.output_tokens":     "5",
		"ai.prompt.template_version":               "chat-v1",
		"ai.prompt.hash":                           "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"longtermism.payload.mode":                 string(obs.PayloadModeMetadataOnly),
		"longtermism.payload.redacted":             "false",
		"request.id":                               "req-t077-mapped",
		"longtermism.smoke.run_id":                 "run-t177-mapper",
		"ai.tenant_id":                             "tenant-t077-private",
		"ai.user_id":                               "user-t077-private",
		"ai.session_id":                            "session-t077-private",
		"ai.query.hash":                            "query-hash-t077-private",
		"ai.agent.tool_name":                       "unapproved-tool-t077-private",
		"http.route":                               "/api/v1/chat",
		"ai.unexpected":                            "unknown-attribute-t077",
		"langfuse.observation.metadata.caller_key": "caller-controlled-t077",
		"authorization":                            "Bearer private-t077-token",
		"gen_ai.prompt.0.content":                  "raw prompt t077",
	}
	input := TraceMapperInput{
		Span:        newT077OTLPSpanSnapshot(span.Attributes),
		PayloadMode: obs.PayloadModeMetadataOnly,
	}
	attributesBefore := cloneT077Attributes(input.Span.Attributes)

	projection, err := MapTraceToProjection(input)
	if err != nil {
		t.Fatalf("MapTraceToProjection() error = %v", err)
	}
	if projection.PlatformTraceID() != t077OTelTraceID || projection.PlatformObservationID() != t077OTelSpanID {
		t.Fatalf("platform identity = %#v, want native OTel identities", projection)
	}
	projectionAttributes := projection.AttributesSnapshot()
	wantAttributes := map[string]string{
		"langfuse.observation.type":                              "generation",
		"langfuse.observation.name":                              "ai.generation",
		"langfuse.observation.metadata.ai_feature":               "chat",
		"langfuse.observation.metadata.outcome":                  "success",
		"langfuse.observation.metadata.ai_trace_id":              t077AITraceID,
		"langfuse.observation.metadata.request_id":               "req-t077-mapped",
		"langfuse.observation.metadata.longtermism.smoke.run_id": "run-t177-mapper",
		"langfuse.observation.model.name":                        "provider-actual-model",
		"langfuse.observation.metadata.requested_model":          "server-requested-model",
		"langfuse.observation.usage_details.input":               "11",
		"langfuse.observation.usage_details.output":              "17",
		"langfuse.observation.usage_details.reasoning_output":    "5",
		"langfuse.observation.metadata.prompt_template_version":  "chat-v1",
		"langfuse.observation.metadata.prompt_hash":              "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"langfuse.observation.metadata.payload_mode":             string(obs.PayloadModeMetadataOnly),
		"langfuse.observation.metadata.payload_redacted":         "false",
	}
	for key, want := range wantAttributes {
		if got := projectionAttributes[key]; got != want {
			t.Fatalf("mapped attribute %q = %q, want %q", key, got, want)
		}
	}
	assertExactT077AttributeKeys(t, projectionAttributes, wantAttributes)
	for _, forbidden := range []string{
		"ai.tenant_id", "ai.user_id", "ai.session_id", "ai.query.hash", "ai.agent.tool_name",
		"http.route", "langfuse.observation.metadata.caller_key", "authorization", "gen_ai.prompt.0.content",
	} {
		if _, exists := projectionAttributes[forbidden]; exists {
			t.Fatalf("mapper must not copy source attribute %q into a platform projection", forbidden)
		}
	}
	if !reflect.DeepEqual(input.Span.Attributes, attributesBefore) {
		t.Fatalf("mapper must not mutate source attributes: got %#v, want %#v", input.Span.Attributes, attributesBefore)
	}
	assertT077ProjectionContainsNoSecrets(t, projection)
}

func TestMapTraceToProjectionCreatesLangfusePropertiesOnlyAtAdapterBoundary(t *testing.T) {
	span := validT077SpanSnapshot()
	for key := range span.Attributes {
		if strings.HasPrefix(key, "langfuse.") {
			t.Fatalf("core trace snapshot must not own a platform attribute %q", key)
		}
	}

	projection, err := MapTraceToProjection(TraceMapperInput{
		Span:        newT077OTLPSpanSnapshot(span.Attributes),
		PayloadMode: obs.PayloadModeMetadataOnly,
	})
	if err != nil {
		t.Fatalf("MapTraceToProjection() error = %v", err)
	}
	if !hasT077AttributePrefix(projection.AttributesSnapshot(), "langfuse.") {
		t.Fatal("Langfuse properties must be created only by the platform adapter")
	}
	if got := span.Attributes["langfuse.observation.type"]; got != "" {
		t.Fatalf("mapper must not write platform properties back into the core snapshot, got %q", got)
	}
}

func TestMapTraceToProjectionKeepsNativeOTelAndAIDomainIdentitiesSeparate(t *testing.T) {
	tests := []struct {
		name      string
		traceID   string
		spanID    string
		wantError bool
	}{
		{name: "native OTel identities are required", traceID: t077OTelTraceID, spanID: t077OTelSpanID},
		{name: "missing native trace ID cannot fall back to AI identity", spanID: t077OTelSpanID, wantError: true},
		{name: "missing native span ID cannot fall back to AI identity", traceID: t077OTelTraceID, wantError: true},
		{name: "malformed native trace ID is rejected", traceID: "not-an-otel-trace", spanID: t077OTelSpanID, wantError: true},
		{name: "malformed native span ID is rejected", traceID: t077OTelTraceID, spanID: "not-an-otel-span", wantError: true},
		{name: "zero native trace ID is rejected", traceID: strings.Repeat("0", 32), spanID: t077OTelSpanID, wantError: true},
		{name: "zero native span ID is rejected", traceID: t077OTelTraceID, spanID: strings.Repeat("0", 16), wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := TraceMapperInput{
				Span:        newT077OTLPSpanSnapshotWithIdentity(tt.traceID, tt.spanID, validT077SpanSnapshot().Attributes),
				PayloadMode: obs.PayloadModeMetadataOnly,
			}
			projection, err := MapTraceToProjection(input)
			if tt.wantError {
				if err == nil || !isZeroT077Projection(projection) {
					t.Fatalf("MapTraceToProjection() = (%#v, %v), want fail-fast empty projection", projection, err)
				}
				for _, forbidden := range []string{t077OTelTraceID, t077OTelSpanID, t077AITraceID} {
					if strings.Contains(err.Error(), forbidden) {
						t.Fatalf("identity validation error must not echo %q", forbidden)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("MapTraceToProjection() error = %v", err)
			}
			if projection.PlatformTraceID() != t077OTelTraceID || projection.PlatformObservationID() != t077OTelSpanID || projection.PlatformTraceID() == t077AITraceID || projection.PlatformObservationID() == t077AITraceID {
				t.Fatalf("platform and AI identities were conflated: %#v", projection)
			}
			attributes := projection.AttributesSnapshot()
			if attributes["langfuse.observation.metadata.ai_trace_id"] != t077AITraceID {
				t.Fatalf("AI identity must remain an explicit metadata fact, attributes=%#v", attributes)
			}
		})
	}
}

func TestMapTraceToProjectionRejectsUnsafeObservationNames(t *testing.T) {
	tests := []struct {
		name            string
		observationName string
	}{
		{name: "empty", observationName: ""},
		{name: "natural language", observationName: "show me the private customer request"},
		{name: "line break", observationName: "ai.generation\nraw content"},
		{name: "too long", observationName: strings.Repeat("a", 257)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := newT077OTLPSpanSnapshot(validT077SpanSnapshot().Attributes)
			span.Name = tt.observationName
			projection, err := MapTraceToProjection(TraceMapperInput{
				Span:        span,
				PayloadMode: obs.PayloadModeMetadataOnly,
			})
			if err == nil || !isZeroT077Projection(projection) {
				t.Fatalf("MapTraceToProjection() = (%#v, %v), want unsafe name rejected", projection, err)
			}
			if tt.observationName != "" && strings.Contains(err.Error(), tt.observationName) {
				t.Fatal("observation validation error must not echo the rejected name")
			}
		})
	}
}

func TestMapTraceToProjectionAppliesPayloadModeBoundary(t *testing.T) {
	redactedPolicy, err := obs.ResolvePayloadPolicy(obs.PayloadPolicyInput{Mode: obs.PayloadModeContentRedacted, Environment: "local"})
	if err != nil {
		t.Fatalf("ResolvePayloadPolicy(redacted) error = %v", err)
	}
	rawPolicy, err := obs.ResolvePayloadPolicy(obs.PayloadPolicyInput{Mode: obs.PayloadModeContentRaw, Environment: "test", RawContentEnabled: true})
	if err != nil {
		t.Fatalf("ResolvePayloadPolicy(raw) error = %v", err)
	}
	const rawInput = "raw-input-t077 Bearer never-export-this"
	const rawOutput = "raw-output-t077 private@example.test"
	rawPayload, err := rawPolicy.LocalRawPayload(obs.PayloadContent{Input: rawInput, Output: rawOutput})
	if err != nil {
		t.Fatalf("LocalRawPayload() error = %v", err)
	}

	tests := []struct {
		name          string
		mode          obs.PayloadMode
		payload       obs.PayloadSnapshot
		wantInput     string
		wantOutput    string
		wantError     bool
		wantContent   bool
		forbiddenText []string
	}{
		{
			name: "metadata only omits content",
			mode: obs.PayloadModeMetadataOnly,
		},
		{
			name:        "redacted content may be projected",
			mode:        obs.PayloadModeContentRedacted,
			payload:     redactedPolicy.Sanitize(obs.PayloadContent{Input: "safe input t077", Output: "safe output t077"}),
			wantInput:   "safe input t077",
			wantOutput:  "safe output t077",
			wantContent: true,
		},
		{
			name:          "redacted content removes credentials and PII before projection",
			mode:          obs.PayloadModeContentRedacted,
			payload:       redactedPolicy.Sanitize(obs.PayloadContent{Input: "safe input t077 Bearer secret-t077", Output: "safe output t077 private-t077@example.test"}),
			wantContent:   true,
			forbiddenText: []string{"Bearer secret-t077", "private-t077@example.test"},
		},
		{
			name:          "raw content is never a Langfuse export mode",
			mode:          obs.PayloadModeContentRaw,
			wantError:     true,
			forbiddenText: []string{rawPayload.Input(), rawPayload.Output(), rawInput, rawOutput},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projection, err := MapTraceToProjection(TraceMapperInput{
				Span:        newT077OTLPSpanSnapshot(validT077SpanSnapshot().Attributes),
				PayloadMode: tt.mode,
				Payload:     tt.payload,
			})
			if tt.wantError {
				if err == nil || !isZeroT077Projection(projection) {
					t.Fatalf("MapTraceToProjection() = (%#v, %v), want raw export rejected", projection, err)
				}
				for _, forbidden := range tt.forbiddenText {
					if strings.Contains(err.Error(), forbidden) {
						t.Fatalf("raw payload error must not expose %q", forbidden)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("MapTraceToProjection() error = %v", err)
			}
			for key, want := range map[string]string{"langfuse.observation.input": tt.wantInput, "langfuse.observation.output": tt.wantOutput} {
				got, exists := projection.AttributesSnapshot()[key]
				if !tt.wantContent && exists {
					t.Fatalf("metadata-only mapper must omit %q, got %q", key, got)
				}
				if tt.wantContent && (!exists || (want != "" && got != want)) {
					t.Fatalf("mapped attribute %q = %q present=%t, want %q", key, got, exists, want)
				}
			}
			serialized, marshalErr := json.Marshal(projection)
			if marshalErr != nil {
				t.Fatalf("marshal projection: %v", marshalErr)
			}
			for _, forbidden := range tt.forbiddenText {
				if strings.Contains(string(serialized), forbidden) {
					t.Fatalf("payload projection must not contain forbidden content")
				}
			}
			assertT077ProjectionContainsNoSecrets(t, projection)
		})
	}
}

func TestTraceMapperTypesDoNotExposeRawPayloadOrCallerMetadata(t *testing.T) {
	allowedStringMaps := map[string]map[string]struct{}{
		"OTLPSpanSnapshot": {"Attributes": {}},
	}
	for _, typeOfValue := range []reflect.Type{
		reflect.TypeFor[TraceMapperInput](),
		reflect.TypeFor[OTLPSpanSnapshot](),
		reflect.TypeFor[TraceProjection](),
	} {
		for index := range typeOfValue.NumField() {
			field := typeOfValue.Field(index)
			name := strings.ToLower(field.Name)
			if field.Type == reflect.TypeFor[obs.LocalRawPayload]() || strings.Contains(name, "raw") || strings.Contains(name, "secret") || strings.Contains(name, "credential") || strings.Contains(name, "authorization") {
				t.Fatalf("%s must not expose raw content or a secret-bearing field %q", typeOfValue.Name(), field.Name)
			}
			if field.Type.Kind() == reflect.Map && field.IsExported() {
				if field.Type.Key().Kind() != reflect.String || field.Type.Elem().Kind() != reflect.String {
					t.Fatalf("%s.%s must not expose arbitrary metadata values", typeOfValue.Name(), field.Name)
				}
				if _, allowed := allowedStringMaps[typeOfValue.Name()][field.Name]; !allowed {
					t.Fatalf("%s.%s must not add a caller-controlled metadata map", typeOfValue.Name(), field.Name)
				}
			}
		}
	}
}

func TestTraceProjectionAttributesSnapshotIsDefensive(t *testing.T) {
	projection, err := MapTraceToProjection(TraceMapperInput{
		Span:        newT077OTLPSpanSnapshot(validT077SpanSnapshot().Attributes),
		PayloadMode: obs.PayloadModeMetadataOnly,
	})
	if err != nil {
		t.Fatalf("MapTraceToProjection() error = %v", err)
	}

	callerCopy := projection.AttributesSnapshot()
	callerCopy["authorization"] = "Bearer caller-injected-t077"
	callerCopy["langfuse.observation.name"] = "mutated"

	stableSnapshot := projection.AttributesSnapshot()
	if _, exists := stableSnapshot["authorization"]; exists {
		t.Fatal("caller mutation must not add attributes after the privacy scan")
	}
	if got := stableSnapshot["langfuse.observation.name"]; got != "ai.generation" {
		t.Fatalf("caller mutation changed stored observation name to %q", got)
	}
}

func validT077SpanSnapshot() obs.TraceSpanSnapshot {
	return obs.TraceSpanSnapshot{
		Name:            "ai.generation",
		RequestID:       "req-t077",
		AITraceID:       t077AITraceID,
		ObservationType: obs.ObservationTypeGeneration,
		Attributes: map[string]string{
			"ai.feature":                   "chat",
			"ai.outcome":                   "success",
			"longtermism.ai.trace_id":      t077AITraceID,
			"gen_ai.provider.name":         "openai-compatible",
			"gen_ai.request.model":         "server-requested-model",
			"gen_ai.response.model":        "provider-actual-model",
			"gen_ai.usage.input_tokens":    "11",
			"gen_ai.usage.output_tokens":   "17",
			"longtermism.payload.mode":     string(obs.PayloadModeMetadataOnly),
			"longtermism.payload.redacted": "false",
		},
	}
}

func cloneT077Attributes(input map[string]string) map[string]string {
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func newT077OTLPSpanSnapshot(attributes map[string]string) OTLPSpanSnapshot {
	return newT077OTLPSpanSnapshotWithIdentity(t077OTelTraceID, t077OTelSpanID, attributes)
}

func newT077OTLPSpanSnapshotWithIdentity(traceID, spanID string, attributes map[string]string) OTLPSpanSnapshot {
	return OTLPSpanSnapshot{
		TraceID:         traceID,
		SpanID:          spanID,
		Name:            "ai.generation",
		ObservationType: obs.ObservationTypeGeneration,
		Attributes:      attributes,
	}
}

func isZeroT077Projection(projection TraceProjection) bool {
	return projection.PlatformTraceID() == "" && projection.PlatformObservationID() == "" && len(projection.AttributesSnapshot()) == 0
}

func assertT077ProjectionContainsNoSecrets(t *testing.T, projection TraceProjection) {
	t.Helper()
	serialized, err := json.Marshal(projection)
	if err != nil {
		t.Fatalf("marshal projection: %v", err)
	}
	for _, forbidden := range []string{
		"Bearer private-t077-token", "raw prompt t077", "tenant-t077-private", "user-t077-private",
		"req-t077-private", "session-t077-private", "query-hash-t077-private", "unapproved-tool-t077-private", "caller-controlled-t077", "unknown-attribute-t077",
	} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("Langfuse projection leaked forbidden content")
		}
	}
	if findings := obs.ScanForbiddenPayloadFields(projection.AttributesSnapshot()); len(findings) != 0 {
		t.Fatalf("Langfuse platform attributes contain forbidden payload fields: %#v", findings)
	}
}

func assertExactT077AttributeKeys(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("mapped attribute count = %d, want explicit allowlist count %d: %#v", len(got), len(want), got)
	}
	for key := range got {
		if _, allowed := want[key]; !allowed {
			t.Fatalf("mapped attribute %q is outside the explicit platform allowlist", key)
		}
	}
}

func hasT077AttributePrefix(attributes map[string]string, prefix string) bool {
	for key := range attributes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
