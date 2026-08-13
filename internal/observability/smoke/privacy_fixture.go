package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/privacy"
)

const maximumPrivacyFixtureResponseBytes = 1 << 20

var (
	errPrivacyFixtureFailed = errors.New("privacy fixture failed")
	privacyTraceIDPattern   = regexp.MustCompile(`^[a-f0-9]{32}$`)
	privacySpanIDPattern    = regexp.MustCompile(`^[a-f0-9]{16}$`)
)

type PrivacyFixtureRequest struct {
	RunID, Marker, Profile, ForbiddenCanary string
	StartedAt, Deadline                     time.Time
}

type PrivacyFixtureTriggerRequest struct {
	RunID, Marker, ForbiddenCanary string
}

// PrivacyFixtureTriggerResult keeps the raw response in memory only. It intentionally has no
// JSON tags and is never embedded into the result or artifact input.
type PrivacyFixtureTriggerResult struct {
	Attempted, Protected bool
	StatusCode           int
	Body                 []byte
}

func (PrivacyFixtureTriggerResult) MarshalJSON() ([]byte, error) {
	return nil, errors.New("privacy fixture trigger result cannot be serialized")
}

type PrivacyFixtureTrigger interface {
	Trigger(context.Context, PrivacyFixtureTriggerRequest) (PrivacyFixtureTriggerResult, error)
}

type PrivacyFixtureManifestConsumer interface {
	Consume(context.Context, string) (ChatRunManifestInput, error)
}

type PrivacyFixtureArtifactRefs struct {
	ManifestRef, APISummaryRef, ApplicationLogRef, ChatReportRef, CollectorArtifactRef string
}

type PrivacyFixtureArtifactInput struct {
	RunID, Marker, RequestID, AITraceID, ServiceTraceID, SpanID string
	StartedAt, Deadline                                         time.Time
	APIScanSummary                                              map[string]int
	ChatReport                                                  *SmokeReport
}

type PrivacyFixtureArtifactWriter interface {
	Write(context.Context, PrivacyFixtureArtifactInput) (PrivacyFixtureArtifactRefs, error)
}

type PrivacyFixtureDependencies struct {
	Trigger  PrivacyFixtureTrigger
	Manifest PrivacyFixtureManifestConsumer
	Writer   PrivacyFixtureArtifactWriter
}

type PrivacyFixtureResult struct {
	RunID, Marker, RequestID, AITraceID, ServiceTraceID, SpanID string
	ManifestRef, APISummaryRef, ApplicationLogRef               string
	ChatReportRef, CollectorArtifactRef                         string
	StartedAt, Deadline                                         time.Time
	RequestSent, ChatSucceeded                                  bool
}

func RunPrivacyFixture(ctx context.Context, request PrivacyFixtureRequest, deps PrivacyFixtureDependencies) (PrivacyFixtureResult, error) {
	if !validPrivacyFixtureRequest(ctx, request, deps) {
		return PrivacyFixtureResult{}, errPrivacyFixtureFailed
	}
	triggered, err := deps.Trigger.Trigger(ctx, PrivacyFixtureTriggerRequest{RunID: request.RunID, Marker: request.Marker, ForbiddenCanary: request.ForbiddenCanary})
	if err != nil || !triggered.Attempted || !triggered.Protected || triggered.StatusCode < 200 || triggered.StatusCode >= 300 || len(triggered.Body) > maximumPrivacyFixtureResponseBytes {
		return PrivacyFixtureResult{}, errPrivacyFixtureFailed
	}
	responseIdentity, summary, err := inspectPrivacyFixtureResponse(triggered.Body, request.ForbiddenCanary)
	if err != nil {
		return PrivacyFixtureResult{}, errPrivacyFixtureFailed
	}
	manifest, err := deps.Manifest.Consume(ctx, request.Marker)
	if err != nil || !validPrivacyManifest(request, responseIdentity, manifest) {
		return PrivacyFixtureResult{}, errPrivacyFixtureFailed
	}
	report, err := buildPrivacyFixtureChatReport(request, manifest)
	if err != nil {
		return PrivacyFixtureResult{}, errPrivacyFixtureFailed
	}
	input := PrivacyFixtureArtifactInput{
		RunID: request.RunID, Marker: request.Marker, RequestID: manifest.RequestID, AITraceID: manifest.AITraceID,
		ServiceTraceID: manifest.ServiceTraceID, SpanID: manifest.SpanID, StartedAt: request.StartedAt.UTC(), Deadline: request.Deadline.UTC(),
		APIScanSummary: clonePrivacyCounts(summary.Counts), ChatReport: report,
	}
	refs, err := deps.Writer.Write(ctx, input)
	if err != nil || !safePrivacyArtifactRef(refs.ManifestRef) || !safePrivacyArtifactRef(refs.APISummaryRef) || !safePrivacyArtifactRef(refs.ApplicationLogRef) ||
		!safePrivacyArtifactRef(refs.ChatReportRef) || !safePrivacyArtifactRef(refs.CollectorArtifactRef) {
		return PrivacyFixtureResult{}, errPrivacyFixtureFailed
	}
	return PrivacyFixtureResult{
		RunID: request.RunID, Marker: request.Marker, RequestID: manifest.RequestID, AITraceID: manifest.AITraceID,
		ServiceTraceID: manifest.ServiceTraceID, SpanID: manifest.SpanID, ManifestRef: refs.ManifestRef,
		APISummaryRef: refs.APISummaryRef, ApplicationLogRef: refs.ApplicationLogRef,
		ChatReportRef: refs.ChatReportRef, CollectorArtifactRef: refs.CollectorArtifactRef,
		StartedAt: request.StartedAt.UTC(), Deadline: request.Deadline.UTC(), RequestSent: true, ChatSucceeded: true,
	}, nil
}

type privacyFixtureResponseIdentity struct{ RequestID, AITraceID string }

func inspectPrivacyFixtureResponse(body []byte, canary string) (privacyFixtureResponseIdentity, privacy.ScanResult, error) {
	if len(body) == 0 || len(body) > maximumPrivacyFixtureResponseBytes || !json.Valid(body) {
		return privacyFixtureResponseIdentity{}, privacy.ScanResult{}, errPrivacyFixtureFailed
	}
	scanner, err := privacy.NewScanner([]string{canary})
	if err != nil {
		return privacyFixtureResponseIdentity{}, privacy.ScanResult{}, errPrivacyFixtureFailed
	}
	summary, err := scanner.Scan([]privacy.SurfaceText{{Surface: privacy.SurfaceAPI, Text: string(body)}})
	if err != nil || privacyScanHasHits(summary) {
		return privacyFixtureResponseIdentity{}, privacy.ScanResult{}, errPrivacyFixtureFailed
	}
	var envelope struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
		Meta    struct {
			RequestID string `json:"request_id"`
			AITraceID string `json:"ai_trace_id"`
		} `json:"meta"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || envelope.Code != 0 || !safePrivacyOpaqueID(envelope.Meta.RequestID) || !safePrivacyOpaqueID(envelope.Meta.AITraceID) {
		return privacyFixtureResponseIdentity{}, privacy.ScanResult{}, errPrivacyFixtureFailed
	}
	return privacyFixtureResponseIdentity{RequestID: envelope.Meta.RequestID, AITraceID: envelope.Meta.AITraceID}, summary, nil
}

func privacyScanHasHits(summary privacy.ScanResult) bool {
	for _, count := range summary.Counts {
		if count != 0 {
			return true
		}
	}
	return false
}

func validPrivacyFixtureRequest(ctx context.Context, request PrivacyFixtureRequest, deps PrivacyFixtureDependencies) bool {
	return ctx != nil && ctx.Err() == nil && deps.Trigger != nil && deps.Manifest != nil && deps.Writer != nil &&
		safePrivacyOpaqueID(request.RunID) && safePrivacyOpaqueID(request.Marker) && isSafePollMarker(request.ForbiddenCanary) &&
		contains(allowedProfiles, request.Profile) && !request.StartedAt.IsZero() && request.Deadline.After(request.StartedAt) && request.Deadline.Sub(request.StartedAt) <= time.Minute
}

func validPrivacyManifest(request PrivacyFixtureRequest, response privacyFixtureResponseIdentity, manifest ChatRunManifestInput) bool {
	return manifest.SmokeRunID == request.Marker && manifest.RequestID == response.RequestID && manifest.AITraceID == response.AITraceID &&
		privacyTraceIDPattern.MatchString(manifest.ServiceTraceID) && privacySpanIDPattern.MatchString(manifest.SpanID)
}

func buildPrivacyFixtureChatReport(request PrivacyFixtureRequest, manifest ChatRunManifestInput) (*SmokeReport, error) {
	return BuildSmokeReport(SmokeReportInput{
		RunID: request.RunID, Marker: request.Marker, Profile: request.Profile, Scenario: "chat", RequestID: manifest.RequestID, AITraceID: manifest.AITraceID,
		StartedAt: request.StartedAt, Deadline: request.Deadline, FinishedAt: request.StartedAt,
		Checks:  []BackendCheckInput{{Backend: "api", Status: "passed", FailureStage: "none", Evidence: map[string]any{"response_status": int64(200)}}},
		Cleanup: SmokeCleanupInput{Status: "not_required", ResidualResources: []string{}, TemporaryCredentials: "not_created", TemporaryData: "not_created"},
	})
}

func safePrivacyOpaqueID(value string) bool { return isSafePollMarker(value) }

func safePrivacyArtifactRef(value string) bool {
	return safePrivacyOpaqueID(trimJSONSuffix(value)) && len(value) > len(".json") && len(value) <= 133
}

func trimJSONSuffix(value string) string {
	if len(value) <= 5 || value[len(value)-5:] != ".json" {
		return ""
	}
	return value[:len(value)-5]
}

func clonePrivacyCounts(values map[string]int) map[string]int {
	copyValues := make(map[string]int, len(values))
	for key, value := range values {
		copyValues[key] = value
	}
	return copyValues
}
