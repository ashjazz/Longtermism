package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

const maximumPrivacyLocalArtifactBytes = 1 << 20

var (
	errPrivacyLocalSurface = errors.New("privacy local surface unavailable")
	privacyLocalDigest     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	privacyLocalTraceID    = regexp.MustCompile(`^[a-f0-9]{32}$`)
	privacyLocalSpanID     = regexp.MustCompile(`^[a-f0-9]{16}$`)
)

type PrivacyLocalSurfacesConfig struct {
	RuntimeConfigDigest            string
	ExpectedPrequeueArtifactSHA256 string
	CollectorComponent             string
	ExportAdmissionCorrelation     string
}

type PrivacyLocalSurfaceScanRequest struct {
	RunID, Marker, ForbiddenCanary               string
	RequestID, AITraceID, ServiceTraceID, SpanID string
	ManifestRef                                  string
	Surface                                      smoke.PrivacySmokeSurface
	StartedAt, Deadline                          time.Time
}

// PrivacyLocalSurfaceEvidence is a sealed proof: only a successful contained read, strict
// decode and scan can construct its private state. Accessors expose only the low-sensitive
// facts needed by the later eight-surface composition.
type PrivacyLocalSurfaceEvidence struct {
	surface              smoke.PrivacySmokeSurface
	localProofKind       string
	scannerPolicyVersion string
	counts               map[string]int
}

func (evidence *PrivacyLocalSurfaceEvidence) Surface() smoke.PrivacySmokeSurface {
	if evidence == nil {
		return ""
	}
	return evidence.surface
}

func (evidence *PrivacyLocalSurfaceEvidence) LocalProofKind() string {
	if evidence == nil {
		return ""
	}
	return evidence.localProofKind
}

func (evidence *PrivacyLocalSurfaceEvidence) ScannerPolicyVersion() string {
	if evidence == nil {
		return ""
	}
	return evidence.scannerPolicyVersion
}

func (evidence *PrivacyLocalSurfaceEvidence) Counts() map[string]int {
	if evidence == nil {
		return nil
	}
	return clonePrivacyLocalCounts(evidence.counts)
}

type PrivacyLocalSurfaces struct {
	config PrivacyLocalSurfacesConfig
	store  *smoke.PrivacyArtifactStore
	reader privacyLocalArtifactCapability
}

type privacyLocalArtifactKind string

const (
	privacyLocalArtifactAPISummary         privacyLocalArtifactKind = "api_summary"
	privacyLocalArtifactApplicationLog     privacyLocalArtifactKind = "application_log_projection"
	privacyLocalArtifactCollectorComposite privacyLocalArtifactKind = "collector_composite_proof"
	privacyLocalArtifactChatReport         privacyLocalArtifactKind = "chat_fixture_report"
)

type privacyLocalArtifactReadRequest struct {
	Kind                                             privacyLocalArtifactKind
	ManifestRef, RunID, Marker, RequestID, AITraceID string
	ServiceTraceID, SpanID                           string
	StartedAt, Deadline                              time.Time
}

type privacyLocalArtifactDocument struct {
	kind           privacyLocalArtifactKind
	content        []byte
	artifactSHA256 string
}

type privacyLocalArtifactCapability interface {
	Read(context.Context, privacyLocalArtifactReadRequest) (privacyLocalArtifactDocument, error)
}

type privacyLocalStoreReader struct {
	store *smoke.PrivacyArtifactStore
}

func NewPrivacyLocalSurfaces(config PrivacyLocalSurfacesConfig, store *smoke.PrivacyArtifactStore) (*PrivacyLocalSurfaces, error) {
	if store == nil {
		return nil, newPrivacyLocalSurfaceError()
	}
	return newPrivacyLocalSurfaces(config, store, privacyLocalStoreReader{store: store})
}

func newPrivacyLocalSurfacesForTest(config PrivacyLocalSurfacesConfig, reader privacyLocalArtifactCapability) (*PrivacyLocalSurfaces, error) {
	return newPrivacyLocalSurfaces(config, nil, reader)
}

func newPrivacyLocalSurfaces(config PrivacyLocalSurfacesConfig, store *smoke.PrivacyArtifactStore, reader privacyLocalArtifactCapability) (*PrivacyLocalSurfaces, error) {
	if reader == nil || !validPrivacyLocalConfig(config) {
		return nil, newPrivacyLocalSurfaceError()
	}
	return &PrivacyLocalSurfaces{config: config, store: store, reader: reader}, nil
}

func (reader privacyLocalStoreReader) Read(ctx context.Context, request privacyLocalArtifactReadRequest) (privacyLocalArtifactDocument, error) {
	if reader.store == nil {
		return privacyLocalArtifactDocument{}, errPrivacyLocalSurface
	}
	kind, ok := smokePrivacyArtifactKind(request.Kind)
	if !ok {
		return privacyLocalArtifactDocument{}, errPrivacyLocalSurface
	}
	document, err := reader.store.Read(ctx, smoke.PrivacyArtifactReadRequest{
		Manifest: smoke.PrivacyArtifactResolveRequest{
			ManifestRef: request.ManifestRef, RunID: request.RunID, Marker: request.Marker,
			RequestID: request.RequestID, AITraceID: request.AITraceID, ServiceTraceID: request.ServiceTraceID,
			SpanID: request.SpanID, StartedAt: request.StartedAt, Deadline: request.Deadline,
		},
		Kind: kind,
	})
	if err != nil || string(document.Kind) != string(request.Kind) || len(document.Content) == 0 {
		return privacyLocalArtifactDocument{}, errPrivacyLocalSurface
	}
	digest := sha256.Sum256(document.Content)
	return privacyLocalArtifactDocument{
		kind: request.Kind, content: append([]byte(nil), document.Content...),
		artifactSHA256: "sha256:" + hex.EncodeToString(digest[:]),
	}, nil
}

func (surfaces *PrivacyLocalSurfaces) Scan(ctx context.Context, request PrivacyLocalSurfaceScanRequest) (PrivacyLocalSurfaceEvidence, error) {
	if surfaces == nil || surfaces.reader == nil || ctx == nil || ctx.Err() != nil || !validPrivacyLocalRequest(request) {
		return PrivacyLocalSurfaceEvidence{}, newPrivacyLocalSurfaceError()
	}
	kind, proofKind, ok := privacyLocalRoute(request.Surface)
	if !ok {
		return PrivacyLocalSurfaceEvidence{}, newPrivacyLocalSurfaceError()
	}
	document, err := surfaces.reader.Read(ctx, privacyLocalArtifactReadRequest{
		Kind: kind, ManifestRef: request.ManifestRef, RunID: request.RunID, Marker: request.Marker,
		RequestID: request.RequestID, AITraceID: request.AITraceID, ServiceTraceID: request.ServiceTraceID,
		SpanID: request.SpanID, StartedAt: request.StartedAt, Deadline: request.Deadline,
	})
	if err != nil || ctx.Err() != nil || document.kind != kind || len(document.content) == 0 ||
		len(document.content) > maximumPrivacyLocalArtifactBytes || !privacyLocalDigest.MatchString(document.artifactSHA256) {
		return PrivacyLocalSurfaceEvidence{}, newPrivacyLocalSurfaceError()
	}
	counts, err := inspectPrivacyLocalDocument(document.content, kind, request, surfaces.config)
	if err != nil || ctx.Err() != nil {
		return PrivacyLocalSurfaceEvidence{}, newPrivacyLocalSurfaceError()
	}
	return PrivacyLocalSurfaceEvidence{
		surface: request.Surface, localProofKind: proofKind,
		scannerPolicyVersion: "1", counts: clonePrivacyLocalCounts(counts),
	}, nil
}

func privacyLocalRoute(surface smoke.PrivacySmokeSurface) (privacyLocalArtifactKind, string, bool) {
	switch surface {
	case smoke.PrivacySmokeSurfaceAPI:
		return privacyLocalArtifactAPISummary, "bounded_memory_scan", true
	case smoke.PrivacySmokeSurfaceApplicationLog:
		return privacyLocalArtifactApplicationLog, "pre_export_projection", true
	case smoke.PrivacySmokeSurfaceCollectorQueue:
		return privacyLocalArtifactCollectorComposite, "prequeue_configuration_telemetry", true
	case smoke.PrivacySmokeSurfaceReport:
		return privacyLocalArtifactChatReport, "contained_artifact_scan", true
	default:
		return "", "", false
	}
}

func smokePrivacyArtifactKind(kind privacyLocalArtifactKind) (smoke.PrivacyArtifactKind, bool) {
	switch kind {
	case privacyLocalArtifactAPISummary:
		return smoke.PrivacyArtifactKindAPISummary, true
	case privacyLocalArtifactApplicationLog:
		return smoke.PrivacyArtifactKindApplicationLogProjection, true
	case privacyLocalArtifactCollectorComposite:
		return smoke.PrivacyArtifactKindCollectorCompositeProof, true
	case privacyLocalArtifactChatReport:
		return smoke.PrivacyArtifactKindChatFixtureReport, true
	default:
		return "", false
	}
}

func validPrivacyLocalConfig(config PrivacyLocalSurfacesConfig) bool {
	return privacyLocalDigest.MatchString(config.RuntimeConfigDigest) &&
		privacyLocalDigest.MatchString(config.ExpectedPrequeueArtifactSHA256) &&
		config.CollectorComponent == "otlphttp/loki" && safePrivacyLocalOpaque(config.ExportAdmissionCorrelation)
}

func validPrivacyLocalRequest(request PrivacyLocalSurfaceScanRequest) bool {
	if !safePrivacyLocalOpaque(request.RunID) || !safePrivacyLocalOpaque(request.Marker) || !safePrivacyLocalOpaque(request.RequestID) ||
		!safePrivacyLocalOpaque(request.AITraceID) || !privacyLocalTraceID.MatchString(request.ServiceTraceID) ||
		!privacyLocalSpanID.MatchString(request.SpanID) || !safePrivacyLocalRef(request.ManifestRef) || request.StartedAt.IsZero() ||
		request.Deadline.IsZero() || request.StartedAt.Location() != time.UTC || request.Deadline.Location() != time.UTC ||
		!request.Deadline.After(request.StartedAt) || request.Deadline.Sub(request.StartedAt) > time.Minute {
		return false
	}
	_, err := newPrivacyLocalScanner(request.ForbiddenCanary)
	return err == nil
}

func safePrivacyLocalOpaque(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for index := range value {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	lower := strings.ToLower(value)
	for _, forbidden := range []string{"authorization", "bearer", "credential", "payload", "secret", "token"} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	return true
}

func safePrivacyLocalRef(value string) bool {
	return len(value) >= 6 && len(value) <= 160 && strings.HasSuffix(value, ".json") &&
		value != "." && value != ".." && !strings.Contains(value, "/") && !strings.Contains(value, `\`)
}

func clonePrivacyLocalCounts(input map[string]int) map[string]int {
	result := make(map[string]int, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

type privacyLocalSurfaceError struct{}

func (privacyLocalSurfaceError) Error() string { return errPrivacyLocalSurface.Error() }
func (privacyLocalSurfaceError) Class() string { return "unexpected_evidence" }
func (privacyLocalSurfaceError) Unwrap() error { return errPrivacyLocalSurface }

func newPrivacyLocalSurfaceError() error { return privacyLocalSurfaceError{} }
