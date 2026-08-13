package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/privacy"
	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

var (
	errPrivacyGrafanaSurface = errors.New("privacy Grafana surface unavailable")
	privacyGrafanaOpaque     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,127}$`)
)

type PrivacyGrafanaScanRequest struct {
	Surface                                      smoke.PrivacySmokeSurface
	RunID, Marker, ForbiddenCanary               string
	RequestID, AITraceID, ServiceTraceID, SpanID string
	StartedAt, Deadline                          time.Time
	Limit                                        int
}

// PrivacyGrafanaSurfaceEvidence is constructed only after a real protected query, complete
// protocol validation and full-document scan. Its private fields prevent callers from reporting
// an attempted query or verified zero without executing that chain.
type PrivacyGrafanaSurfaceEvidence struct {
	surface              smoke.PrivacySmokeSurface
	evidenceMethod       string
	scannerPolicyVersion string
	counts               map[string]int
}

func (evidence *PrivacyGrafanaSurfaceEvidence) Surface() smoke.PrivacySmokeSurface {
	if evidence == nil {
		return ""
	}
	return evidence.surface
}

func (evidence *PrivacyGrafanaSurfaceEvidence) EvidenceMethod() string {
	if evidence == nil {
		return ""
	}
	return evidence.evidenceMethod
}

func (evidence *PrivacyGrafanaSurfaceEvidence) ScannerPolicyVersion() string {
	if evidence == nil {
		return ""
	}
	return evidence.scannerPolicyVersion
}

func (evidence *PrivacyGrafanaSurfaceEvidence) Counts() map[string]int {
	if evidence == nil {
		return nil
	}
	return clonePrivacyGrafanaCounts(evidence.counts)
}

func (PrivacyGrafanaSurfaceEvidence) MarshalJSON() ([]byte, error) {
	return nil, errPrivacyGrafanaSurface
}

type PrivacyGrafanaSurfaces struct {
	client *GrafanaQueryClient
}

func NewPrivacyGrafanaSurfaces(client *GrafanaQueryClient) (*PrivacyGrafanaSurfaces, error) {
	if client == nil || !client.smokeProtected || client.tempoURL == "" || client.lokiURL == "" || client.httpClient == nil {
		return nil, newPrivacyGrafanaError()
	}
	return &PrivacyGrafanaSurfaces{client: client}, nil
}

func (surfaces *PrivacyGrafanaSurfaces) Scan(ctx context.Context, request PrivacyGrafanaScanRequest) (PrivacyGrafanaSurfaceEvidence, error) {
	if surfaces == nil || surfaces.client == nil || ctx == nil || ctx.Err() != nil || !validPrivacyGrafanaRequest(request) {
		return PrivacyGrafanaSurfaceEvidence{}, newPrivacyGrafanaError()
	}
	var (
		counts map[string]int
		method string
		err    error
	)
	switch request.Surface {
	case smoke.PrivacySmokeSurfaceTempo:
		method = "bounded_trace_document"
		counts, err = surfaces.scanTempo(ctx, request)
	case smoke.PrivacySmokeSurfaceLoki:
		method = "exact_structured_query"
		counts, err = surfaces.scanLoki(ctx, request)
	default:
		return PrivacyGrafanaSurfaceEvidence{}, newPrivacyGrafanaError()
	}
	if err != nil || ctx.Err() != nil {
		return PrivacyGrafanaSurfaceEvidence{}, newPrivacyGrafanaError()
	}
	return PrivacyGrafanaSurfaceEvidence{
		surface: request.Surface, evidenceMethod: method,
		scannerPolicyVersion: "1", counts: clonePrivacyGrafanaCounts(counts),
	}, nil
}

func (surfaces *PrivacyGrafanaSurfaces) scanTempo(ctx context.Context, request PrivacyGrafanaScanRequest) (map[string]int, error) {
	search, err := surfaces.client.privacyTempoSearch(ctx, privacyTempoQuery(request), request.StartedAt, request.Deadline, request.Limit)
	if err != nil {
		return nil, err
	}
	traceID, searchDocument, err := validatePrivacyTempoSearch(search.payload, request)
	if err != nil || ctx.Err() != nil {
		return nil, errPrivacyGrafanaSurface
	}
	document, err := surfaces.client.privacyTempoTrace(ctx, traceID, request.StartedAt, request.Deadline)
	if err != nil {
		return nil, err
	}
	semantic, err := validatePrivacyTempoDocument(document.payload, request)
	if err != nil {
		return nil, err
	}
	return scanPrivacyGrafanaDocuments(request.ForbiddenCanary, searchDocument, semantic)
}

func (surfaces *PrivacyGrafanaSurfaces) scanLoki(ctx context.Context, request PrivacyGrafanaScanRequest) (map[string]int, error) {
	result, err := surfaces.client.privacyLokiRange(ctx, privacyLokiQuery(request), request.StartedAt, request.Deadline, request.Limit)
	if err != nil {
		return nil, err
	}
	semantic, err := validatePrivacyLokiDocument(result.payload, request)
	if err != nil {
		return nil, err
	}
	return scanPrivacyGrafanaDocuments(request.ForbiddenCanary, semantic)
}

func privacyTempoQuery(request PrivacyGrafanaScanRequest) string {
	return fmt.Sprintf(`{ span."longtermism.smoke.run_id" = %q && span."request.id" = %q && span."longtermism.ai.trace_id" = %q && trace:id = %q && span:id = %q }`,
		request.Marker, request.RequestID, request.AITraceID, request.ServiceTraceID, request.SpanID)
}

func privacyLokiQuery(request PrivacyGrafanaScanRequest) string {
	return fmt.Sprintf(`{service_name="longtermism"} | smoke_run_id = %q | request_id = %q | ai_trace_id = %q | trace_id = %q | span_id = %q`,
		request.Marker, request.RequestID, request.AITraceID, request.ServiceTraceID, request.SpanID)
}

func validPrivacyGrafanaRequest(request PrivacyGrafanaScanRequest) bool {
	if request.Surface != smoke.PrivacySmokeSurfaceTempo && request.Surface != smoke.PrivacySmokeSurfaceLoki {
		return false
	}
	if !safePrivacyGrafanaOpaque(request.RunID) || !safePrivacyGrafanaOpaque(request.Marker) ||
		!safePrivacyGrafanaOpaque(request.RequestID) || !safePrivacyGrafanaOpaque(request.AITraceID) ||
		!isLowerHex(request.ServiceTraceID, 32) || !isLowerHex(request.SpanID, 16) || request.Limit < 1 || request.Limit > 100 ||
		request.StartedAt.IsZero() || request.Deadline.IsZero() || request.StartedAt.Location() != time.UTC || request.Deadline.Location() != time.UTC ||
		!request.Deadline.After(request.StartedAt) || request.Deadline.Sub(request.StartedAt) > time.Minute ||
		request.Deadline.Before(time.Now().Add(-time.Minute)) {
		return false
	}
	_, err := privacy.NewScanner([]string{request.ForbiddenCanary})
	return err == nil
}

func safePrivacyGrafanaOpaque(value string) bool {
	if !privacyGrafanaOpaque.MatchString(value) {
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

func clonePrivacyGrafanaCounts(input map[string]int) map[string]int {
	result := make(map[string]int, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

type privacyGrafanaError struct{}

func (privacyGrafanaError) Error() string { return "privacy Grafana surface unavailable" }
func (privacyGrafanaError) Class() string { return "unexpected_evidence" }
func (privacyGrafanaError) Unwrap() error { return errPrivacyGrafanaSurface }
func newPrivacyGrafanaError() error       { return privacyGrafanaError{} }

var _ json.Marshaler = PrivacyGrafanaSurfaceEvidence{}
