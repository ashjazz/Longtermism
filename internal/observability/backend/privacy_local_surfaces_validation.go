package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	observability "github.com/ashjazz/Longtermism/internal/observability"
	"github.com/ashjazz/Longtermism/internal/observability/privacy"
	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

var privacyLocalCategories = []string{"synthetic_canary", "credential", "authorization", "token", "recognized_pii"}

type privacyLocalEnvelope struct {
	SchemaVersion  string                   `json:"schema_version"`
	Kind           privacyLocalArtifactKind `json:"kind"`
	RunID          string                   `json:"run_id"`
	Marker         string                   `json:"marker"`
	RequestID      string                   `json:"request_id"`
	AITraceID      string                   `json:"ai_trace_id"`
	ServiceTraceID string                   `json:"service_trace_id"`
	SpanID         string                   `json:"span_id"`
	StartedAt      time.Time                `json:"started_at"`
	Deadline       time.Time                `json:"deadline"`
	APISummary     json.RawMessage          `json:"api_summary,omitempty"`
	ApplicationLog json.RawMessage          `json:"application_log_projection,omitempty"`
	Collector      json.RawMessage          `json:"collector_composite_proof,omitempty"`
	ChatReport     json.RawMessage          `json:"chat_fixture_report,omitempty"`
}

type privacyLocalReportWire struct {
	SchemaVersion string               `json:"schema_version"`
	RunID         string               `json:"run_id"`
	Marker        string               `json:"marker"`
	Profile       string               `json:"profile"`
	Scenario      string               `json:"scenario"`
	RequestID     string               `json:"request_id,omitempty"`
	AITraceID     string               `json:"ai_trace_id,omitempty"`
	StartedAt     string               `json:"started_at"`
	FinishedAt    string               `json:"finished_at"`
	Status        string               `json:"status"`
	Versions      map[string]string    `json:"versions,omitempty"`
	Checks        []smoke.BackendCheck `json:"checks"`
	Cleanup       smoke.SmokeCleanup   `json:"cleanup"`
}

func inspectPrivacyLocalDocument(content []byte, kind privacyLocalArtifactKind, request PrivacyLocalSurfaceScanRequest, config PrivacyLocalSurfacesConfig) (map[string]int, error) {
	var envelope privacyLocalEnvelope
	if strictPrivacyLocalJSON(content, &envelope) != nil || envelope.SchemaVersion != "1" || envelope.Kind != kind ||
		envelope.RunID != request.RunID || envelope.Marker != request.Marker || envelope.RequestID != request.RequestID ||
		envelope.AITraceID != request.AITraceID || envelope.ServiceTraceID != request.ServiceTraceID || envelope.SpanID != request.SpanID ||
		!envelope.StartedAt.Equal(request.StartedAt) || !envelope.Deadline.Equal(request.Deadline) {
		return nil, errPrivacyLocalSurface
	}
	payload, ok := privacyLocalPayload(envelope, kind)
	if !ok {
		return nil, errPrivacyLocalSurface
	}
	if kind == privacyLocalArtifactAPISummary {
		var counts map[string]int
		if strictPrivacyLocalJSON(payload, &counts) != nil || !validPrivacyLocalCounts(counts) {
			return nil, errPrivacyLocalSurface
		}
		return clonePrivacyLocalCounts(counts), nil
	}
	counts, err := scanPrivacyLocalSemantics(request.ForbiddenCanary, payload)
	if err != nil {
		return nil, errPrivacyLocalSurface
	}
	// Collector evidence carries four integrity bindings in addition to category counts. A
	// confirmed leak cannot be allowed to bypass those bindings, otherwise later composition
	// could truthfully report the hit while falsely claiming configuration/telemetry proof.
	if kind == privacyLocalArtifactCollectorComposite {
		if validatePrivacyLocalCollector(payload, request, config) != nil {
			return nil, errPrivacyLocalSurface
		}
		return counts, nil
	}
	if privacyLocalCountsHaveHits(counts) {
		return counts, nil
	}
	switch kind {
	case privacyLocalArtifactApplicationLog:
		err = validatePrivacyLocalApplication(payload, request)
	case privacyLocalArtifactChatReport:
		err = validatePrivacyLocalReport(payload, request)
	default:
		err = errPrivacyLocalSurface
	}
	if err != nil {
		return nil, errPrivacyLocalSurface
	}
	return counts, nil
}

func privacyLocalPayload(envelope privacyLocalEnvelope, kind privacyLocalArtifactKind) ([]byte, bool) {
	payloads := []json.RawMessage{envelope.APISummary, envelope.ApplicationLog, envelope.Collector, envelope.ChatReport}
	nonempty := 0
	for _, payload := range payloads {
		if len(payload) != 0 && string(payload) != "null" {
			nonempty++
		}
	}
	if nonempty != 1 {
		return nil, false
	}
	switch kind {
	case privacyLocalArtifactAPISummary:
		return envelope.APISummary, len(envelope.APISummary) != 0
	case privacyLocalArtifactApplicationLog:
		return envelope.ApplicationLog, len(envelope.ApplicationLog) != 0
	case privacyLocalArtifactCollectorComposite:
		return envelope.Collector, len(envelope.Collector) != 0
	case privacyLocalArtifactChatReport:
		return envelope.ChatReport, len(envelope.ChatReport) != 0
	default:
		return nil, false
	}
}

func validatePrivacyLocalApplication(payload []byte, request PrivacyLocalSurfaceScanRequest) error {
	var projection smoke.PrivacyApplicationLogProjection
	if strictPrivacyLocalJSON(payload, &projection) != nil || projection.Timestamp.Location() != time.UTC ||
		projection.Timestamp.Before(request.StartedAt) || projection.Timestamp.After(request.Deadline) || len(projection.Attributes) != 9 {
		return errPrivacyLocalSurface
	}
	status, statusOK := privacyLocalInt64(projection.Attributes["status"])
	duration, durationOK := privacyLocalInt64(projection.Attributes["duration_ms"])
	if !statusOK || !durationOK || status != 200 || duration < 0 {
		return errPrivacyLocalSurface
	}
	entry, err := observability.BuildHTTPCompletionLog(observability.HTTPCompletionLogInput{
		Timestamp: projection.Timestamp, RequestID: request.RequestID, TraceID: request.ServiceTraceID, SpanID: request.SpanID,
		RouteTemplate: "/api/v1/chat", Method: "POST", StatusCode: 200, Duration: time.Duration(duration) * time.Millisecond,
		IsAIRequest: true, IsSmokeRun: true, AITraceID: request.AITraceID, SmokeRunID: request.Marker,
	})
	if err != nil {
		return errPrivacyLocalSurface
	}
	want, err := observability.BuildHTTPCompletionOTLPRecord(entry)
	projection.Attributes["status"] = status
	projection.Attributes["duration_ms"] = duration
	if err != nil || !projection.Timestamp.Equal(want.Timestamp) || projection.Severity != want.Severity ||
		projection.Body != want.Body || !reflect.DeepEqual(projection.Attributes, want.Attributes) {
		return errPrivacyLocalSurface
	}
	return nil
}

func validatePrivacyLocalCollector(payload []byte, request PrivacyLocalSurfaceScanRequest, config PrivacyLocalSurfacesConfig) error {
	var proof smoke.PrivacyCollectorCompositeProof
	if strictPrivacyLocalJSON(payload, &proof) != nil {
		return errPrivacyLocalSurface
	}
	telemetry := proof.ComponentTelemetry
	if proof.RuntimeConfigDigest != config.RuntimeConfigDigest || proof.PrequeueArtifactSHA256 != config.ExpectedPrequeueArtifactSHA256 ||
		proof.ComponentIdentity != config.CollectorComponent || proof.ExportAdmissionCorrelation != config.ExportAdmissionCorrelation ||
		telemetry.ComponentIdentity != config.CollectorComponent || !telemetry.WindowStartedAt.Equal(request.StartedAt) ||
		!telemetry.WindowDeadline.Equal(request.Deadline) || telemetry.ObservedAt.Before(request.StartedAt) ||
		telemetry.ObservedAt.After(request.Deadline) || telemetry.Enqueued < 0 || telemetry.Sent < 0 || telemetry.Failed < 0 ||
		telemetry.QueueSize < 0 || telemetry.QueueCapacity <= 0 || telemetry.QueueSize > telemetry.QueueCapacity ||
		telemetry.OldestAgeMS < 0 || telemetry.Sent > telemetry.Enqueued {
		return errPrivacyLocalSurface
	}
	return nil
}

func validatePrivacyLocalReport(payload []byte, request PrivacyLocalSurfaceScanRequest) error {
	var wire privacyLocalReportWire
	if strictPrivacyLocalJSON(payload, &wire) != nil || wire.SchemaVersion != "2" || wire.Scenario != "chat" || wire.Status != "passed" ||
		wire.RunID != request.RunID || wire.Marker != request.Marker || wire.RequestID != request.RequestID || wire.AITraceID != request.AITraceID {
		return errPrivacyLocalSurface
	}
	startedAt, startErr := time.Parse(time.RFC3339Nano, wire.StartedAt)
	finishedAt, finishErr := time.Parse(time.RFC3339Nano, wire.FinishedAt)
	if startErr != nil || finishErr != nil || !startedAt.Equal(request.StartedAt) || finishedAt.Before(request.StartedAt) || finishedAt.After(request.Deadline) {
		return errPrivacyLocalSurface
	}
	checks := make([]smoke.BackendCheckInput, 0, len(wire.Checks))
	for _, check := range wire.Checks {
		checks = append(checks, smoke.BackendCheckInput{
			Backend: check.Backend, Status: check.Status, Duration: time.Duration(check.DurationMS) * time.Millisecond,
			FailureStage: check.FailureStage, ErrorClass: check.ErrorClass, Evidence: normalizePrivacyLocalEvidence(check.Evidence),
		})
	}
	report, err := smoke.BuildSmokeReport(smoke.SmokeReportInput{
		RunID: wire.RunID, Marker: wire.Marker, Profile: wire.Profile, Scenario: wire.Scenario,
		RequestID: wire.RequestID, AITraceID: wire.AITraceID, StartedAt: startedAt, Deadline: request.Deadline,
		FinishedAt: finishedAt, Versions: wire.Versions, Checks: checks,
		Cleanup: smoke.SmokeCleanupInput{
			Status: wire.Cleanup.Status, ResidualResources: wire.Cleanup.ResidualResources,
			TemporaryCredentials: wire.Cleanup.TemporaryCredentials, TemporaryData: wire.Cleanup.TemporaryData,
		},
	})
	if err != nil || report.Status() != wire.Status {
		return errPrivacyLocalSurface
	}
	return nil
}

func scanPrivacyLocalSemantics(canary string, payload []byte) (map[string]int, error) {
	var semantic any
	if strictPrivacyLocalJSON(payload, &semantic) != nil {
		return nil, errPrivacyLocalSurface
	}
	texts := make([]string, 0, 32)
	collectPrivacyLocalStrings(semantic, &texts)
	sort.Strings(texts)
	scanner, err := newPrivacyLocalScanner(canary)
	if err != nil {
		return nil, errPrivacyLocalSurface
	}
	result, err := scanner.Scan([]privacy.SurfaceText{{Surface: privacy.SurfaceReport, Text: strings.Join(texts, "\n")}})
	if err != nil {
		return nil, errPrivacyLocalSurface
	}
	counts := privacyLocalZeroCounts()
	for category, count := range result.Counts {
		mapped, ok := mapPrivacyLocalCategory(category)
		if !ok || count < 0 {
			return nil, errPrivacyLocalSurface
		}
		counts[mapped] = count
	}
	return counts, nil
}

func newPrivacyLocalScanner(canary string) (privacyScanner, error) {
	scanner, err := privacy.NewScanner([]string{canary})
	return privacyScanner{scan: scanner.Scan}, err
}

type privacyScanner struct {
	scan func([]privacy.SurfaceText) (privacy.ScanResult, error)
}

func (scanner privacyScanner) Scan(input []privacy.SurfaceText) (privacy.ScanResult, error) {
	if scanner.scan == nil {
		return privacy.ScanResult{}, errPrivacyLocalSurface
	}
	return scanner.scan(input)
}

func collectPrivacyLocalStrings(value any, destination *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			*destination = append(*destination, key)
			collectPrivacyLocalStrings(nested, destination)
		}
	case []any:
		for _, nested := range typed {
			collectPrivacyLocalStrings(nested, destination)
		}
	case string:
		*destination = append(*destination, typed)
	}
}

func validPrivacyLocalCounts(counts map[string]int) bool {
	if len(counts) != len(privacyLocalCategories) {
		return false
	}
	for _, category := range privacyLocalCategories {
		if count, ok := counts[category]; !ok || count < 0 {
			return false
		}
	}
	return true
}

func privacyLocalZeroCounts() map[string]int {
	counts := make(map[string]int, len(privacyLocalCategories))
	for _, category := range privacyLocalCategories {
		counts[category] = 0
	}
	return counts
}

func privacyLocalCountsHaveHits(counts map[string]int) bool {
	for _, count := range counts {
		if count > 0 {
			return true
		}
	}
	return false
}

func mapPrivacyLocalCategory(category string) (string, bool) {
	switch category {
	case "api_key":
		return "credential", true
	case "pii":
		return "recognized_pii", true
	case "synthetic_canary", "authorization", "token":
		return category, true
	default:
		return "", false
	}
}

func privacyLocalInt64(value any) (int64, bool) {
	switch number := value.(type) {
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

func normalizePrivacyLocalEvidence(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		if number, ok := value.(json.Number); ok {
			integer, err := number.Int64()
			if err == nil {
				result[key] = integer
				continue
			}
		}
		result[key] = value
	}
	return result
}

func strictPrivacyLocalJSON(payload []byte, target any) error {
	if len(payload) == 0 || len(payload) > maximumPrivacyLocalArtifactBytes || !utf8.Valid(payload) || rejectDuplicatePrivacyLocalKeys(payload) != nil {
		return errPrivacyLocalSurface
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errPrivacyLocalSurface
	}
	return nil
}

func rejectDuplicatePrivacyLocalKeys(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if consumePrivacyLocalJSONValue(decoder) != nil {
		return errPrivacyLocalSurface
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errPrivacyLocalSurface
	}
	return nil
}

func consumePrivacyLocalJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			key, ok := keyToken.(string)
			if keyErr != nil || !ok || key != strings.ToLower(key) {
				return errPrivacyLocalSurface
			}
			folded := strings.ToLower(key)
			if _, duplicate := seen[folded]; duplicate {
				return errPrivacyLocalSurface
			}
			seen[folded] = struct{}{}
			if consumePrivacyLocalJSONValue(decoder) != nil {
				return errPrivacyLocalSurface
			}
		}
	case '[':
		for decoder.More() {
			if consumePrivacyLocalJSONValue(decoder) != nil {
				return errPrivacyLocalSurface
			}
		}
	default:
		return errPrivacyLocalSurface
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(map[json.Delim]json.Delim{'{': '}', '[': ']'}[delimiter]) {
		return errPrivacyLocalSurface
	}
	return nil
}
