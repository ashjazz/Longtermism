package smoke

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ashjazz/Longtermism/internal/observability/privacy"
)

const maximumPrivacyArtifactBytes = 1 << 20

var (
	errPrivacyArtifactStore = errors.New("privacy artifact unavailable")
	privacyArtifactDigest   = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	privacyArtifactKinds    = []PrivacyArtifactKind{
		PrivacyArtifactKindAPISummary,
		PrivacyArtifactKindApplicationLogProjection,
		PrivacyArtifactKindCollectorCompositeProof,
		PrivacyArtifactKindChatFixtureReport,
	}
)

type PrivacyArtifactKind string

const (
	PrivacyArtifactKindAPISummary               PrivacyArtifactKind = "api_summary"
	PrivacyArtifactKindApplicationLogProjection PrivacyArtifactKind = "application_log_projection"
	PrivacyArtifactKindCollectorCompositeProof  PrivacyArtifactKind = "collector_composite_proof"
	PrivacyArtifactKindChatFixtureReport        PrivacyArtifactKind = "chat_fixture_report"
)

// PrivacyApplicationLogProjection is the exact low-sensitive OTLP completion record written
// before export. Keeping a stable disk DTO avoids coupling the artifact protocol to an SDK type.
type PrivacyApplicationLogProjection struct {
	Timestamp  time.Time      `json:"timestamp"`
	Severity   string         `json:"severity"`
	Body       string         `json:"body"`
	Attributes map[string]any `json:"attributes"`
}

type PrivacyCollectorComponentTelemetry struct {
	ComponentIdentity string    `json:"component_identity"`
	ObservedAt        time.Time `json:"observed_at"`
	WindowStartedAt   time.Time `json:"window_started_at"`
	WindowDeadline    time.Time `json:"window_deadline"`
	Enqueued          int64     `json:"enqueued"`
	Sent              int64     `json:"sent"`
	Failed            int64     `json:"failed"`
	QueueSize         int64     `json:"queue_size"`
	QueueCapacity     int64     `json:"queue_capacity"`
	OldestAgeMS       int64     `json:"oldest_age_ms"`
}

type PrivacyCollectorCompositeProof struct {
	RuntimeConfigDigest        string                             `json:"runtime_config_digest"`
	PrequeueArtifactSHA256     string                             `json:"prequeue_artifact_sha256"`
	ComponentIdentity          string                             `json:"component_identity"`
	ExportAdmissionCorrelation string                             `json:"export_admission_correlation"`
	ComponentTelemetry         PrivacyCollectorComponentTelemetry `json:"component_telemetry"`
}

type PrivacyArtifactResolveRequest struct {
	ManifestRef                                                 string
	RunID, Marker, RequestID, AITraceID, ServiceTraceID, SpanID string
	StartedAt, Deadline                                         time.Time
}

type PrivacyArtifactReadRequest struct {
	Manifest PrivacyArtifactResolveRequest
	Kind     PrivacyArtifactKind
}

type PrivacyArtifactBinding struct {
	Kind      PrivacyArtifactKind `json:"kind"`
	Ref       string              `json:"ref"`
	SHA256    string              `json:"sha256"`
	SizeBytes int64               `json:"size_bytes"`
}

type PrivacyArtifactManifest struct {
	SchemaVersion  string                   `json:"schema_version"`
	RunID          string                   `json:"run_id"`
	Marker         string                   `json:"marker"`
	RequestID      string                   `json:"request_id"`
	AITraceID      string                   `json:"ai_trace_id"`
	ServiceTraceID string                   `json:"service_trace_id"`
	SpanID         string                   `json:"span_id"`
	StartedAt      time.Time                `json:"started_at"`
	Deadline       time.Time                `json:"deadline"`
	Artifacts      []PrivacyArtifactBinding `json:"artifacts"`
}

type PrivacyArtifactDocument struct {
	Kind    PrivacyArtifactKind
	Content []byte
}

type privacyArtifactEnvelope struct {
	SchemaVersion  string                           `json:"schema_version"`
	Kind           PrivacyArtifactKind              `json:"kind"`
	RunID          string                           `json:"run_id"`
	Marker         string                           `json:"marker"`
	RequestID      string                           `json:"request_id"`
	AITraceID      string                           `json:"ai_trace_id"`
	ServiceTraceID string                           `json:"service_trace_id"`
	SpanID         string                           `json:"span_id"`
	StartedAt      time.Time                        `json:"started_at"`
	Deadline       time.Time                        `json:"deadline"`
	APISummary     map[string]int                   `json:"api_summary,omitempty"`
	ApplicationLog *PrivacyApplicationLogProjection `json:"application_log_projection,omitempty"`
	Collector      *PrivacyCollectorCompositeProof  `json:"collector_composite_proof,omitempty"`
	ChatReport     json.RawMessage                  `json:"chat_fixture_report,omitempty"`
}

type privacyArtifactStoreError struct{}

func (privacyArtifactStoreError) Error() string { return errPrivacyArtifactStore.Error() }
func (privacyArtifactStoreError) Class() string { return "artifact_unavailable" }
func (privacyArtifactStoreError) Unwrap() error { return errPrivacyArtifactStore }

func newPrivacyArtifactStoreError() error { return privacyArtifactStoreError{} }

func validPrivacyArtifactKind(kind PrivacyArtifactKind) bool {
	for _, candidate := range privacyArtifactKinds {
		if kind == candidate {
			return true
		}
	}
	return false
}

func validPrivacyArtifactIdentity(runID, marker, requestID, aiTraceID, traceID, spanID string, startedAt, deadline time.Time) bool {
	return safePrivacyOpaqueID(runID) && safePrivacyOpaqueID(marker) && safePrivacyOpaqueID(requestID) &&
		safePrivacyOpaqueID(aiTraceID) && hexTraceID.MatchString(traceID) && hexSpanID.MatchString(spanID) &&
		!startedAt.IsZero() && !deadline.IsZero() && deadline.After(startedAt) && deadline.Sub(startedAt) <= time.Minute &&
		startedAt.Location() == time.UTC && deadline.Location() == time.UTC
}

func validPrivacyCategoryCounts(counts map[string]int) bool {
	want := []string{"synthetic_canary", "credential", "authorization", "token", "recognized_pii"}
	if len(counts) != len(want) {
		return false
	}
	for _, category := range want {
		if count, ok := counts[category]; !ok || count < 0 {
			return false
		}
	}
	return true
}

func validPrivacyApplicationProjection(value PrivacyApplicationLogProjection, identity privacyArtifactEnvelope) bool {
	if value.Timestamp.Before(identity.StartedAt) || value.Timestamp.After(identity.Deadline) || value.Timestamp.Location() != time.UTC ||
		value.Severity != "INFO" || value.Body != "http request completed" || len(value.Attributes) != 9 {
		return false
	}
	wantStrings := map[string]string{
		"request_id": identity.RequestID, "trace_id": identity.ServiceTraceID, "span_id": identity.SpanID,
		"route": "/api/v1/chat", "method": "POST", "ai_trace_id": identity.AITraceID, "smoke_run_id": identity.Marker,
	}
	for key, want := range wantStrings {
		if value.Attributes[key] != want {
			return false
		}
	}
	status, statusOK := privacyArtifactInteger(value.Attributes["status"])
	duration, durationOK := privacyArtifactInteger(value.Attributes["duration_ms"])
	return statusOK && status == 200 && durationOK && duration >= 0
}

func privacyArtifactInteger(value any) (int64, bool) {
	switch number := value.(type) {
	case int:
		return int64(number), true
	case int64:
		return number, true
	case float64:
		integer := int64(number)
		return integer, float64(integer) == number
	case json.Number:
		integer, err := number.Int64()
		return integer, err == nil
	default:
		return 0, false
	}
}

func validPrivacyCollectorProof(value PrivacyCollectorCompositeProof, identity privacyArtifactEnvelope) bool {
	telemetry := value.ComponentTelemetry
	return privacyArtifactDigest.MatchString(value.RuntimeConfigDigest) && privacyArtifactDigest.MatchString(value.PrequeueArtifactSHA256) &&
		value.ComponentIdentity == "otlphttp/loki" && telemetry.ComponentIdentity == value.ComponentIdentity &&
		safePrivacyOpaqueID(value.ExportAdmissionCorrelation) && telemetry.WindowStartedAt.Equal(identity.StartedAt) &&
		telemetry.WindowDeadline.Equal(identity.Deadline) && !telemetry.ObservedAt.Before(identity.StartedAt) &&
		!telemetry.ObservedAt.After(identity.Deadline) && telemetry.Enqueued >= 0 && telemetry.Sent >= 0 && telemetry.Failed >= 0 &&
		telemetry.QueueSize >= 0 && telemetry.QueueCapacity > 0 && telemetry.QueueSize <= telemetry.QueueCapacity &&
		telemetry.OldestAgeMS >= 0 && telemetry.Sent <= telemetry.Enqueued
}

func validPrivacyChatReport(report *SmokeReport, identity privacyArtifactEnvelope) bool {
	return report != nil && report.scenario == "chat" && report.status == "passed" && report.runID == identity.RunID &&
		report.marker == identity.Marker && report.requestID == identity.RequestID && report.aiTraceID == identity.AITraceID &&
		report.startedAt.Equal(identity.StartedAt) && !report.finishedAt.Before(identity.StartedAt) && !report.finishedAt.After(identity.Deadline)
}

func privacyArtifactSHA256(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func scanPrivacyArtifactPayloads(canary string, payloads ...[]byte) error {
	scanner, err := privacy.NewScanner([]string{canary})
	if err != nil {
		return err
	}
	surfaces := make([]privacy.SurfaceText, 0, len(payloads))
	for _, payload := range payloads {
		if int64(len(payload)) == 0 || int64(len(payload)) > maximumPrivacyArtifactBytes {
			return errPrivacyArtifactStore
		}
		surfaces = append(surfaces, privacy.SurfaceText{Surface: privacy.SurfaceReport, Text: string(payload)})
	}
	result, err := scanner.Scan(surfaces)
	if err != nil || privacyScanHasHits(result) {
		return errPrivacyArtifactStore
	}
	return nil
}

func strictPrivacyArtifactJSON(payload []byte, target any) error {
	if len(payload) == 0 || int64(len(payload)) > maximumPrivacyArtifactBytes || !utf8.Valid(payload) || rejectDuplicatePrivacyFixtureKeys(payload) != nil {
		return errPrivacyArtifactStore
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errPrivacyArtifactStore
	}
	return nil
}

func privacyArtifactLockContext(ctx context.Context) bool { return ctx != nil && ctx.Err() == nil }
