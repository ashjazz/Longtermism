package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

const negativeSmokeQueryLimit = "1"

type HostResolver func(context.Context, string) ([]net.IP, error)

type LangfuseSmokeQueryConfig struct {
	BaseURL     string
	Credential  string
	Timeout     time.Duration
	ResolveHost HostResolver
}

type AIPlaneSmokeQueryConfig struct {
	BaseURL     string
	Credential  string
	Timeout     time.Duration
	ResolveHost HostResolver
}

type LangfuseSmokeQueryClient struct{ query *negativeSmokeQueryClient }
type AIPlaneSmokeQueryClient struct{ query *negativeSmokeQueryClient }

func NewLangfuseSmokeQueryClient(config LangfuseSmokeQueryConfig) (*LangfuseSmokeQueryClient, error) {
	query, err := newNegativeSmokeQueryClient("langfuse", "/api/public/v2/observations", config.BaseURL, config.Credential, config.Timeout, config.ResolveHost)
	if err != nil {
		return nil, err
	}
	return &LangfuseSmokeQueryClient{query: query}, nil
}

func NewAIPlaneSmokeQueryClient(config AIPlaneSmokeQueryConfig) (*AIPlaneSmokeQueryClient, error) {
	query, err := newNegativeSmokeQueryClient("collector", "/api/v1/observability/smoke/marker-count", config.BaseURL, config.Credential, config.Timeout, config.ResolveHost)
	if err != nil {
		return nil, err
	}
	return &AIPlaneSmokeQueryClient{query: query}, nil
}

func (c *LangfuseSmokeQueryClient) Query(ctx context.Context, target smoke.PollMarkerTarget) (int, error) {
	return c.query.queryLangfuse(ctx, target)
}

func (c *AIPlaneSmokeQueryClient) Query(ctx context.Context, target smoke.PollMarkerTarget) (int, error) {
	return c.query.queryAIPlane(ctx, target)
}

type negativeSmokeQueryClient struct {
	backend    string
	baseURL    *url.URL
	credential string
	timeout    time.Duration
	httpClient *http.Client
	resolve    HostResolver
}

func newNegativeSmokeQueryClient(backend, fixedPath, baseURL, credential string, timeout time.Duration, resolve HostResolver) (*negativeSmokeQueryClient, error) {
	if strings.TrimSpace(credential) == "" {
		return nil, newBackendQueryError(backend, "authentication_failed")
	}
	parsed, err := parseLoopbackQueryBaseURL(baseURL, resolve)
	if err != nil {
		return nil, newBackendQueryError(backend, "backend_unavailable")
	}
	parsed.Path = fixedPath
	if timeout <= 0 {
		timeout = defaultBackendQueryTimeout
	}
	if timeout > maximumBackendQueryTimeout {
		timeout = maximumBackendQueryTimeout
	}
	if resolve == nil {
		resolve = defaultHostResolver
	}
	return &negativeSmokeQueryClient{backend: backend, baseURL: parsed, credential: credential, timeout: timeout, httpClient: newLoopbackHTTPClient(resolve), resolve: resolve}, nil
}

func (c *negativeSmokeQueryClient) queryLangfuse(ctx context.Context, target smoke.PollMarkerTarget) (int, error) {
	filter, err := langfuseMarkerFilter(target)
	if err != nil {
		return 0, newBackendQueryError(c.backend, "invalid_query")
	}
	body, err := c.get(ctx, url.Values{"fields": {"core"}, "limit": {negativeSmokeQueryLimit}, "filter": {filter}})
	if err != nil {
		return 0, err
	}
	var response struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil || len(response.Data) == 0 || response.Data[0] != '[' {
		return 0, newBackendQueryError(c.backend, "malformed_response")
	}
	var observations []json.RawMessage
	if err := json.Unmarshal(response.Data, &observations); err != nil {
		return 0, newBackendQueryError(c.backend, "malformed_response")
	}
	return len(observations), nil
}

func (c *negativeSmokeQueryClient) queryAIPlane(ctx context.Context, target smoke.PollMarkerTarget) (int, error) {
	if !isSafeSmokeQueryTarget(target) {
		return 0, newBackendQueryError(c.backend, "invalid_query")
	}
	body, err := c.get(ctx, url.Values{"marker": {target.Marker}, "started_at": {target.StartedAt.UTC().Format(time.RFC3339Nano)}, "deadline": {target.Deadline.UTC().Format(time.RFC3339Nano)}})
	if err != nil {
		return 0, err
	}
	var response struct {
		Data struct {
			Count json.Number `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil || response.Data.Count == "" {
		return 0, newBackendQueryError(c.backend, "malformed_response")
	}
	count, err := strconv.Atoi(response.Data.Count.String())
	if err != nil || count < 0 {
		return 0, newBackendQueryError(c.backend, "malformed_response")
	}
	return count, nil
}

func langfuseMarkerFilter(target smoke.PollMarkerTarget) (string, error) {
	if !isSafeSmokeQueryTarget(target) {
		return "", errors.New("unsafe smoke target")
	}
	filters := []map[string]string{
		{"type": "stringObject", "column": "metadata", "key": "longtermism.smoke.run_id", "operator": "=", "value": target.Marker},
		{"type": "datetime", "column": "startTime", "operator": ">=", "value": target.StartedAt.UTC().Format(time.RFC3339Nano)},
		{"type": "datetime", "column": "startTime", "operator": "<=", "value": target.Deadline.UTC().Format(time.RFC3339Nano)},
	}
	encoded, err := json.Marshal(filters)
	return string(encoded), err
}

func (c *negativeSmokeQueryClient) get(ctx context.Context, values url.Values) ([]byte, error) {
	if err := validateLoopbackHost(ctx, c.baseURL.Hostname(), c.resolve); err != nil {
		return nil, newBackendQueryError(c.backend, "backend_unavailable")
	}
	requestContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	requestURL := *c.baseURL
	requestURL.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, newBackendQueryError(c.backend, "backend_unavailable")
	}
	request.Header.Set("Authorization", "Basic "+c.credential)
	response, err := c.httpClient.Do(request)
	if err != nil {
		if errors.Is(requestContext.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return nil, newBackendQueryError(c.backend, "backend_timeout")
		}
		return nil, newBackendQueryError(c.backend, "backend_unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, newBackendQueryError(c.backend, "authentication_failed")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, newBackendQueryError(c.backend, "backend_unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumBackendResponseSize+1))
	if err != nil || len(body) > maximumBackendResponseSize || !json.Valid(body) {
		return nil, newBackendQueryError(c.backend, "malformed_response")
	}
	return body, nil
}

func parseLoopbackQueryBaseURL(value string, resolve HostResolver) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("unsafe query base URL")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("unsafe query port")
	}
	if resolve == nil {
		resolve = defaultHostResolver
	}
	if err := validateLoopbackHost(context.Background(), parsed.Hostname(), resolve); err != nil {
		return nil, err
	}
	return parsed, nil
}

func defaultHostResolver(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

func validateLoopbackHost(ctx context.Context, host string, resolve HostResolver) error {
	if host == "127.0.0.1" {
		return nil
	}
	if host != "localhost" {
		return errors.New("non-loopback query host")
	}
	addresses, err := resolve(ctx, host)
	if err != nil || len(addresses) == 0 {
		return errors.New("unresolved query host")
	}
	for _, address := range addresses {
		if !address.IsLoopback() {
			return errors.New("non-loopback query resolution")
		}
	}
	return nil
}

func newLoopbackHTTPClient(resolve HostResolver) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if host == "localhost" {
			addresses, err := resolve(ctx, host)
			if err != nil || len(addresses) == 0 {
				return nil, errors.New("unresolved localhost")
			}
			for _, candidate := range addresses {
				if !candidate.IsLoopback() {
					return nil, errors.New("non-loopback query resolution")
				}
			}
			host = addresses[0].String()
		} else if host != "127.0.0.1" {
			return nil, errors.New("non-loopback query host")
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(host, port))
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

var _ smokeNegativeCounter = (*LangfuseSmokeQueryClient)(nil)
var _ smokeNegativeCounter = (*AIPlaneSmokeQueryClient)(nil)
