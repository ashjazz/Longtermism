package smoke

import (
	"context"
	"crypto/sha256"
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

// PrivacyFixtureTriggerResult is a sealed receipt. Its zero value is invalid and every field is
// private, so callers outside this package cannot report their own transport attempt or attach a
// raw provider body. Production composition additionally accepts only the concrete trigger.
type PrivacyFixtureTriggerResult struct {
	proof *privacyFixtureTriggerProof
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
	ForbiddenCanary                                             string
	APIScanSummary                                              map[string]int
	ApplicationLogProjection                                    PrivacyApplicationLogProjection
	CollectorCompositeProof                                     PrivacyCollectorCompositeProof
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
	if err != nil || !validPrivacyFixtureTriggerProof(triggered.proof, request) {
		return PrivacyFixtureResult{}, errPrivacyFixtureFailed
	}
	manifest, err := deps.Manifest.Consume(ctx, request.Marker)
	if err != nil || !validPrivacyManifest(request, triggered.proof.identity, manifest) {
		return PrivacyFixtureResult{}, errPrivacyFixtureFailed
	}
	report, err := buildPrivacyFixtureChatReport(request, manifest)
	if err != nil {
		return PrivacyFixtureResult{}, errPrivacyFixtureFailed
	}
	input := PrivacyFixtureArtifactInput{
		RunID: request.RunID, Marker: request.Marker, RequestID: manifest.RequestID, AITraceID: manifest.AITraceID,
		ServiceTraceID: manifest.ServiceTraceID, SpanID: manifest.SpanID, StartedAt: request.StartedAt.UTC(), Deadline: request.Deadline.UTC(),
		APIScanSummary: clonePrivacyCounts(triggered.proof.summary.Counts), ChatReport: report,
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

type privacyFixtureTriggerProof struct {
	runID, marker string
	canaryDigest  [sha256.Size]byte
	identity      privacyFixtureResponseIdentity
	summary       privacy.ScanResult
}

func validPrivacyFixtureTriggerProof(proof *privacyFixtureTriggerProof, request PrivacyFixtureRequest) bool {
	return proof != nil && proof.runID == request.RunID && proof.marker == request.Marker &&
		proof.canaryDigest == sha256.Sum256([]byte(request.ForbiddenCanary)) &&
		safePrivacyOpaqueID(proof.identity.RequestID) && safePrivacyOpaqueID(proof.identity.AITraceID) &&
		!privacyScanHasHits(proof.summary)
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
