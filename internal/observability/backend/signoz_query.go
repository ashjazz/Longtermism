package backend

// SigNoz 备选 profile 的只读查询客户端（T143）。安全边界与 Grafana 主线客户端
// （grafana_query.go）逐条对齐：GET only、响应大小有界、JSON 合法性校验、错误只暴露
// backend 名与稳定类别。SigNoz 专属约束：查询认证使用 ingestion key（X-Signoz-Api-Key），
// key 只进入请求头，任何错误通道都不得回显它的值——BackendQueryError 从结构上就没有
// 能承载凭据的字段，这是防线而不是约定。

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

// SignozQueryConfig keeps the backend address and ingestion key at the infrastructure
// boundary. The client never copies either into errors: a smoke report needs the
// backend and error class, not deployment URLs or credentials.
type SignozQueryConfig struct {
	SignozURL    string
	IngestionKey string
	Timeout      time.Duration
	HTTPClient   *http.Client
	ResolveHost  HostResolver
}

// SignozQueryClient is deliberately read-only. It backs the alternate-profile smoke
// runner, so all requests are GET with a bounded response body, preventing a failed
// backend from becoming an unbounded memory or sensitive-body sink in diagnostics.
type SignozQueryClient struct {
	signozURL      string
	ingestionKey   string
	timeout        time.Duration
	httpClient     *http.Client
	smokeProtected bool
}

// NewSignozSmokeQueryClient creates the live-smoke variant: only explicit loopback
// endpoints are accepted and DNS is revalidated at dial time, so CLI configuration
// cannot become an SSRF primitive (same boundary as the Grafana smoke variant).
func NewSignozSmokeQueryClient(config SignozQueryConfig) (*SignozQueryClient, error) {
	resolve := config.ResolveHost
	if resolve == nil {
		resolve = defaultHostResolver
	}
	if config.SignozURL == "" {
		return nil, newBackendQueryError("signoz", "backend_unavailable")
	}
	if _, err := parseLoopbackQueryBaseURL(config.SignozURL, resolve); err != nil {
		return nil, newBackendQueryError("signoz", "backend_unavailable")
	}
	client := NewSignozQueryClient(config)
	client.httpClient = newLoopbackHTTPClient(resolve)
	client.smokeProtected = true
	return client, nil
}

// NewSignozQueryClient creates the narrow query boundary. The zero timeout is
// normalized to a bounded default; callers can still supply a shorter context
// deadline for a specific smoke run.
func NewSignozQueryClient(config SignozQueryConfig) *SignozQueryClient {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultBackendQueryTimeout
	}
	if timeout > maximumBackendQueryTimeout {
		timeout = maximumBackendQueryTimeout
	}
	return &SignozQueryClient{
		signozURL:    strings.TrimRight(config.SignozURL, "/"),
		ingestionKey: config.IngestionKey,
		timeout:      timeout,
		httpClient:   newBoundedHTTPClient(config.HTTPClient),
	}
}

// QueryMetrics issues a Prometheus-compatible instant query against the SigNoz
// query API. The expression uses the shared "query" parameter family guarded by
// isSafeQuery, so size limits cannot be bypassed by switching backends.
func (c *SignozQueryClient) QueryMetrics(ctx context.Context, expression string) (BackendQueryResult, error) {
	return c.query(ctx, "/api/v1/query", url.Values{"query": {expression}})
}

// QueryLogs searches stored logs with the SigNoz filter syntax ("q" parameter).
func (c *SignozQueryClient) QueryLogs(ctx context.Context, filter string) (BackendQueryResult, error) {
	return c.query(ctx, "/api/v1/logs", url.Values{"q": {filter}})
}

// QueryTraces searches stored traces with the SigNoz filter syntax.
func (c *SignozQueryClient) QueryTraces(ctx context.Context, filter string) (BackendQueryResult, error) {
	return c.query(ctx, "/api/v1/traces", url.Values{"q": {filter}})
}

// QueryMetricsSince rejects evidence that ended before the current smoke window:
// SigNoz accepts arbitrary historical ranges, but accepting them here would let a
// prior run satisfy a current-run verification by accident (ErrStaleQueryWindow).
func (c *SignozQueryClient) QueryMetricsSince(ctx context.Context, expression string, startedAt, endedAt time.Time) (BackendQueryResult, error) {
	if err := validateCurrentQueryWindow(startedAt, endedAt); err != nil {
		return BackendQueryResult{}, err
	}
	return c.query(ctx, "/api/v1/query_range", url.Values{
		"query": {expression},
		"start": {startedAt.UTC().Format(time.RFC3339Nano)},
		"end":   {endedAt.UTC().Format(time.RFC3339Nano)},
		"step":  {backendQueryStep},
	})
}

func (c *SignozQueryClient) QueryLogsSince(ctx context.Context, filter string, startedAt, endedAt time.Time) (BackendQueryResult, error) {
	if err := validateCurrentQueryWindow(startedAt, endedAt); err != nil {
		return BackendQueryResult{}, err
	}
	return c.query(ctx, "/api/v1/logs", url.Values{
		"q":     {filter},
		"start": {strconv.FormatInt(startedAt.Unix(), 10)},
		"end":   {strconv.FormatInt(endedAt.Unix(), 10)},
		"limit": {backendQueryLimit},
	})
}

func (c *SignozQueryClient) QueryTracesSince(ctx context.Context, filter string, startedAt, endedAt time.Time) (BackendQueryResult, error) {
	if err := validateCurrentQueryWindow(startedAt, endedAt); err != nil {
		return BackendQueryResult{}, err
	}
	return c.query(ctx, "/api/v1/traces", url.Values{
		"q":     {filter},
		"start": {strconv.FormatInt(startedAt.Unix(), 10)},
		"end":   {strconv.FormatInt(endedAt.Unix(), 10)},
		"limit": {backendQueryLimit},
	})
}

func (c *SignozQueryClient) query(ctx context.Context, path string, values url.Values) (BackendQueryResult, error) {
	if !isSafeQuery(values) {
		return BackendQueryResult{}, newBackendQueryError("signoz", "invalid_query")
	}
	requestURL, err := backendRequestURL(c.signozURL, path, values)
	if err != nil {
		return BackendQueryResult{}, newBackendQueryError("signoz", "backend_unavailable")
	}

	requestContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, requestURL, nil)
	if err != nil {
		return BackendQueryResult{}, newBackendQueryError("signoz", "backend_unavailable")
	}
	// ingestion key 只允许出现在请求头（认证通道）；错误通道没有承载它的字段。
	if c.ingestionKey != "" {
		request.Header.Set("X-Signoz-Api-Key", c.ingestionKey)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		if isDeadlineExceeded(requestContext, err) {
			return BackendQueryResult{}, newBackendQueryError("signoz", "backend_timeout")
		}
		return BackendQueryResult{}, newBackendQueryError("signoz", "backend_unavailable")
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return BackendQueryResult{}, newBackendQueryError("signoz", "authentication_failed")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return BackendQueryResult{}, newBackendQueryError("signoz", "backend_unavailable")
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maximumBackendResponseSize+1))
	if err != nil || len(body) > maximumBackendResponseSize || !json.Valid(body) {
		return BackendQueryResult{}, newBackendQueryError("signoz", "malformed_response")
	}
	return BackendQueryResult{payload: json.RawMessage(body)}, nil
}

func isDeadlineExceeded(requestContext context.Context, err error) bool {
	return errors.Is(requestContext.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded)
}
