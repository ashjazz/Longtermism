// Package backend contains read-only adapters for observability backends.
package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBackendQueryTimeout = 10 * time.Second
	maximumBackendQueryTimeout = 30 * time.Second
	maximumBackendResponseSize = 1 << 20
	maximumQueryWindowAge      = time.Minute
	maximumQueryLength         = 4096
	maximumDatasourceUIDLength = 128
	backendQueryStep           = "15s"
	backendQueryLimit          = "100"
)

var ErrStaleQueryWindow = errors.New("stale backend query window")

// GrafanaQueryConfig keeps backend addresses at the infrastructure boundary. The client never
// copies them into errors: a smoke report needs the backend and error class, not deployment URLs.
type GrafanaQueryConfig struct {
	PrometheusURL string
	LokiURL       string
	TempoURL      string
	GrafanaURL    string
	Timeout       time.Duration
	HTTPClient    *http.Client
}

// GrafanaQueryClient is deliberately read-only. It is an adapter for the smoke runner, not an
// application-facing API client, so all requests are GET and each response is size-bounded before
// decoding. This prevents a failed backend from becoming an unbounded memory or sensitive-body
// sink in the diagnostic path.
type GrafanaQueryClient struct {
	prometheusURL string
	lokiURL       string
	tempoURL      string
	grafanaURL    string
	timeout       time.Duration
	httpClient    *http.Client
}

// BackendQueryResult keeps a successful backend document private by default. The raw response
// may contain log or trace content, so it cannot be JSON-marshaled into a smoke report; callers
// must explicitly decode only the fields needed for low-sensitivity evidence.
type BackendQueryResult struct{ payload json.RawMessage }

func (r BackendQueryResult) Decode(target any) error { return json.Unmarshal(r.payload, target) }

func (BackendQueryResult) MarshalJSON() ([]byte, error) {
	return nil, errors.New("backend query results are not serializable")
}

// NewGrafanaQueryClient creates the narrow query boundary. The zero timeout is normalized to a
// bounded default; callers can still supply a shorter context deadline for a specific smoke run.
func NewGrafanaQueryClient(config GrafanaQueryConfig) *GrafanaQueryClient {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultBackendQueryTimeout
	}
	if timeout > maximumBackendQueryTimeout {
		timeout = maximumBackendQueryTimeout
	}
	client := newBoundedHTTPClient(config.HTTPClient)
	return &GrafanaQueryClient{
		prometheusURL: strings.TrimRight(config.PrometheusURL, "/"),
		lokiURL:       strings.TrimRight(config.LokiURL, "/"),
		tempoURL:      strings.TrimRight(config.TempoURL, "/"),
		grafanaURL:    strings.TrimRight(config.GrafanaURL, "/"),
		timeout:       timeout,
		httpClient:    client,
	}
}

func (c *GrafanaQueryClient) QueryPrometheus(ctx context.Context, expression string) (BackendQueryResult, error) {
	return c.query(ctx, "prometheus", c.prometheusURL, "/api/v1/query", url.Values{"query": {expression}})
}

func (c *GrafanaQueryClient) QueryLoki(ctx context.Context, expression string) (BackendQueryResult, error) {
	return c.query(ctx, "loki", c.lokiURL, "/loki/api/v1/query_range", url.Values{"query": {expression}})
}

func (c *GrafanaQueryClient) QueryTempo(ctx context.Context, query string) (BackendQueryResult, error) {
	return c.query(ctx, "tempo", c.tempoURL, "/api/search", url.Values{"q": {query}})
}

func (c *GrafanaQueryClient) QueryGrafanaDatasourceHealth(ctx context.Context, datasourceUID string) (BackendQueryResult, error) {
	if !isSafeDatasourceUID(datasourceUID) {
		return BackendQueryResult{}, newBackendQueryError("grafana", "invalid_query")
	}
	return c.query(ctx, "grafana", c.grafanaURL, "/api/datasources/uid/"+datasourceUID+"/health", nil)
}

// QueryPrometheusSince rejects evidence that ended before the current smoke window. Prometheus
// accepts arbitrary historical ranges, but accepting them here would let a prior run satisfy a
// current-run verification by accident.
func (c *GrafanaQueryClient) QueryPrometheusSince(ctx context.Context, expression string, startedAt, endedAt time.Time) (BackendQueryResult, error) {
	if err := validateCurrentQueryWindow(startedAt, endedAt); err != nil {
		return BackendQueryResult{}, err
	}
	return c.query(ctx, "prometheus", c.prometheusURL, "/api/v1/query_range", url.Values{
		"query": {expression},
		"start": {startedAt.UTC().Format(time.RFC3339Nano)},
		"end":   {endedAt.UTC().Format(time.RFC3339Nano)},
		"step":  {backendQueryStep},
	})
}

func (c *GrafanaQueryClient) QueryLokiSince(ctx context.Context, expression string, startedAt, endedAt time.Time) (BackendQueryResult, error) {
	if err := validateCurrentQueryWindow(startedAt, endedAt); err != nil {
		return BackendQueryResult{}, err
	}
	return c.query(ctx, "loki", c.lokiURL, "/loki/api/v1/query_range", url.Values{"query": {expression}, "start": {startedAt.UTC().Format(time.RFC3339Nano)}, "end": {endedAt.UTC().Format(time.RFC3339Nano)}, "limit": {backendQueryLimit}})
}

func (c *GrafanaQueryClient) QueryTempoSince(ctx context.Context, traceQL string, startedAt, endedAt time.Time) (BackendQueryResult, error) {
	if err := validateCurrentQueryWindow(startedAt, endedAt); err != nil {
		return BackendQueryResult{}, err
	}
	return c.query(ctx, "tempo", c.tempoURL, "/api/search", url.Values{"q": {traceQL}, "start": {strconv.FormatInt(startedAt.Unix(), 10)}, "end": {strconv.FormatInt(endedAt.Unix(), 10)}, "limit": {backendQueryLimit}})
}

func (c *GrafanaQueryClient) query(ctx context.Context, backend, baseURL, path string, values url.Values) (BackendQueryResult, error) {
	if !isSafeQuery(values) {
		return BackendQueryResult{}, newBackendQueryError(backend, "invalid_query")
	}
	requestURL, err := backendRequestURL(baseURL, path, values)
	if err != nil {
		return BackendQueryResult{}, newBackendQueryError(backend, "backend_unavailable")
	}

	requestContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, requestURL, nil)
	if err != nil {
		return BackendQueryResult{}, newBackendQueryError(backend, "backend_unavailable")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		if errors.Is(requestContext.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return BackendQueryResult{}, newBackendQueryError(backend, "backend_timeout")
		}
		return BackendQueryResult{}, newBackendQueryError(backend, "backend_unavailable")
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return BackendQueryResult{}, newBackendQueryError(backend, "authentication_failed")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return BackendQueryResult{}, newBackendQueryError(backend, "backend_unavailable")
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maximumBackendResponseSize+1))
	if err != nil || len(body) > maximumBackendResponseSize || !json.Valid(body) {
		return BackendQueryResult{}, newBackendQueryError(backend, "malformed_response")
	}
	return BackendQueryResult{payload: json.RawMessage(body)}, nil
}

func backendRequestURL(baseURL, path string, values url.Values) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("invalid backend URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

// BackendQueryError exposes only stable classification for report adapters. It deliberately has
// no URL, request header, or response-body field, so errors.As cannot become a secret side channel.
type BackendQueryError struct {
	backend string
	class   string
}

func (e *BackendQueryError) Error() string { return e.backend + ":" + e.class }

func (e *BackendQueryError) Backend() string { return e.backend }

func (e *BackendQueryError) Class() string { return e.class }

func newBackendQueryError(backend, class string) error {
	return &BackendQueryError{backend: backend, class: class}
}

func newBoundedHTTPClient(source *http.Client) *http.Client {
	if source == nil {
		source = http.DefaultClient
	}
	copy := *source
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

func validateCurrentQueryWindow(startedAt, endedAt time.Time) error {
	if startedAt.IsZero() || endedAt.Before(startedAt) || endedAt.Before(time.Now().Add(-maximumQueryWindowAge)) {
		return ErrStaleQueryWindow
	}
	return nil
}

func isSafeQuery(values url.Values) bool {
	for _, value := range values["query"] {
		if strings.TrimSpace(value) == "" || len(value) > maximumQueryLength {
			return false
		}
	}
	for _, value := range values["q"] {
		if strings.TrimSpace(value) == "" || len(value) > maximumQueryLength {
			return false
		}
	}
	return true
}

func isSafeDatasourceUID(value string) bool {
	if value == "" || len(value) > maximumDatasourceUIDLength {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '-') {
			return false
		}
	}
	return true
}
