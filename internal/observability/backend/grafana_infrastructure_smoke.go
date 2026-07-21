package backend

import (
	"context"
	"fmt"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

// The Prometheus exporter follows the Prometheus counter convention and appends `_total` to the
// OTel counter name. Query the exported contract, not the pre-export OTel instrument name.
const infraHTTPCountQuery = `sum(longtermism_http_server_request_count_total{http_route="/api/v1/observability/infra-smoke",http_request_method="GET",http_response_status_class="2xx"})`

var defaultInfraHTTPCountSelector = SmokeHTTPCountSelector{
	Route:       "/api/v1/observability/infra-smoke",
	Method:      "GET",
	StatusClass: "2xx",
}

// smokeNegativeCounter is intentionally local to the infrastructure adapter. It keeps platform
// protocols out of the runner, which needs only a count and a stable failure classification.
type smokeNegativeCounter interface {
	Query(context.Context, smoke.PollMarkerTarget) (int, error)
}

// GrafanaInfrastructureSmokeBackendConfig connects only bounded evidence ports. It contains no
// endpoint or credential strings beyond the already-created clients, so reports cannot expose
// deployment configuration by accident.
type GrafanaInfrastructureSmokeBackendConfig struct {
	Grafana           *GrafanaQueryClient
	Langfuse          smokeNegativeCounter
	AIPlane           smokeNegativeCounter
	HTTPCountSelector SmokeHTTPCountSelector
}

// GrafanaInfrastructureSmokeBackend adapts Grafana and the two negative-query ports to the
// platform-neutral runner interface. It is the last location where Grafana documents are decoded.
type GrafanaInfrastructureSmokeBackend struct {
	evidence *GrafanaSmokeEvidenceAdapter
	langfuse smokeNegativeCounter
	aiPlane  smokeNegativeCounter
	selector SmokeHTTPCountSelector
}

func NewGrafanaInfrastructureSmokeBackend(config GrafanaInfrastructureSmokeBackendConfig) (*GrafanaInfrastructureSmokeBackend, error) {
	if config.Grafana == nil {
		return nil, newBackendQueryError("grafana", "backend_unavailable")
	}
	if config.Langfuse == nil || config.AIPlane == nil {
		return nil, newBackendQueryError("negative_query", "authentication_failed")
	}
	selector := config.HTTPCountSelector
	if selector == (SmokeHTTPCountSelector{}) {
		selector = defaultInfraHTTPCountSelector
	}
	if selector != defaultInfraHTTPCountSelector {
		return nil, newBackendQueryError("prometheus", "invalid_query")
	}
	return &GrafanaInfrastructureSmokeBackend{
		evidence: NewGrafanaSmokeEvidenceAdapter(config.Grafana),
		langfuse: config.Langfuse,
		aiPlane:  config.AIPlane,
		selector: selector,
	}, nil
}

func (b *GrafanaInfrastructureSmokeBackend) QueryTempo(ctx context.Context, target smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error) {
	return b.evidence.QueryTempoMarker(ctx, target)
}

func (b *GrafanaInfrastructureSmokeBackend) QueryLoki(ctx context.Context, target smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error) {
	return b.evidence.QueryLokiMarker(ctx, target)
}

func (b *GrafanaInfrastructureSmokeBackend) BaselineHTTPRequestCount(ctx context.Context) (int64, error) {
	return b.httpRequestCount(ctx)
}

func (b *GrafanaInfrastructureSmokeBackend) HTTPRequestCount(ctx context.Context) (int64, error) {
	return b.httpRequestCount(ctx)
}

func (b *GrafanaInfrastructureSmokeBackend) httpRequestCount(ctx context.Context) (int64, error) {
	result, err := b.evidence.client.QueryPrometheus(ctx, infraHTTPCountQuery)
	if err != nil {
		return 0, err
	}
	evidence, err := b.evidence.DecodePrometheusHTTPCount(result, b.selector)
	if err != nil {
		return 0, newBackendQueryError("prometheus", smokeReportErrorClass(err))
	}
	return evidence.Count, nil
}

func (b *GrafanaInfrastructureSmokeBackend) QueryLangfuse(ctx context.Context, target smoke.PollMarkerTarget) (int, error) {
	return b.langfuse.Query(ctx, target)
}

func (b *GrafanaInfrastructureSmokeBackend) QueryAIPlane(ctx context.Context, target smoke.PollMarkerTarget) (int, error) {
	return b.aiPlane.Query(ctx, target)
}

var _ smoke.InfrastructureSmokeBackend = (*GrafanaInfrastructureSmokeBackend)(nil)

func (b *GrafanaInfrastructureSmokeBackend) String() string {
	return fmt.Sprintf("GrafanaInfrastructureSmokeBackend{%s}", b.selector.Route)
}
