package smoke

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	v1chat "github.com/ashjazz/Longtermism/api/v1/chat"
	"github.com/ashjazz/Longtermism/internal/observability/privacy"
)

const (
	minimumPrivacyFixtureCredentialBytes = 16
	maximumPrivacyFixtureCredentialBytes = 512
	maximumPrivacyFixtureTimeout         = 30 * time.Second
	privacyFixtureChatPath               = "/api/v1/chat"
)

var privacyFixtureModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

type PrivacyFixtureHostResolver func(context.Context, string) ([]net.IP, error)

type ProtectedPrivacyFixtureTriggerConfig struct {
	Endpoint                             string
	MasterSmokeEnabled, ChatSmokeEnabled bool
	Authorization                        string
	Timeout                              time.Duration
	ResolveHost                          PrivacyFixtureHostResolver
}

type ProtectedPrivacyFixtureTrigger struct {
	endpoint      *url.URL
	authorization string
	timeout       time.Duration
	client        *http.Client
}

type ProtectedPrivacyFixtureTriggerTestDependencies struct {
	DialContext func(context.Context, string, string) (net.Conn, error)
}

type privacyFixtureTriggerError struct{ class string }

// privacyFixtureRawResponse makes the lifetime boundary explicit: raw platform bytes exist only
// between the bounded read and the strict scan/projection below, and cannot accidentally become a
// serializable DTO. The sealed proof never retains this value.
type privacyFixtureRawResponse struct{ body []byte }

func (privacyFixtureRawResponse) MarshalJSON() ([]byte, error) {
	return nil, errors.New("privacy fixture raw response serialization is forbidden")
}

func (raw *privacyFixtureRawResponse) release() {
	if raw == nil {
		return
	}
	clear(raw.body)
	raw.body = nil
}

func (err privacyFixtureTriggerError) Error() string { return "privacy fixture failed" }
func (err privacyFixtureTriggerError) Class() string { return err.class }
func (err privacyFixtureTriggerError) Unwrap() error { return errPrivacyFixtureFailed }

func NewProtectedPrivacyFixtureTrigger(config ProtectedPrivacyFixtureTriggerConfig) (*ProtectedPrivacyFixtureTrigger, error) {
	return newProtectedPrivacyFixtureTrigger(config, nil, nil)
}

func newProtectedPrivacyFixtureTriggerForTest(config ProtectedPrivacyFixtureTriggerConfig, dependency any) (*ProtectedPrivacyFixtureTrigger, error) {
	switch value := dependency.(type) {
	case ProtectedPrivacyFixtureTriggerTestDependencies:
		return newProtectedPrivacyFixtureTrigger(config, value.DialContext, nil)
	case http.RoundTripper:
		return newProtectedPrivacyFixtureTrigger(config, nil, value)
	default:
		return nil, newPrivacyFixtureTriggerError("invalid_config")
	}
}

func newProtectedPrivacyFixtureTrigger(config ProtectedPrivacyFixtureTriggerConfig, dialContext func(context.Context, string, string) (net.Conn, error), roundTripper http.RoundTripper) (*ProtectedPrivacyFixtureTrigger, error) {
	if !config.MasterSmokeEnabled || !config.ChatSmokeEnabled || !validPrivacyFixtureCredential(config.Authorization) ||
		config.Timeout <= 0 || config.Timeout > maximumPrivacyFixtureTimeout {
		return nil, newPrivacyFixtureTriggerError("invalid_config")
	}
	resolve := config.ResolveHost
	if resolve == nil {
		resolve = defaultPrivacyFixtureHostResolver
	}
	resolverContext, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()
	endpoint, err := parsePrivacyFixtureEndpoint(resolverContext, config.Endpoint, resolve)
	if err != nil {
		return nil, newPrivacyFixtureTriggerError("invalid_config")
	}
	transport := newPrivacyFixtureTransport(resolve, dialContext)
	if roundTripper == nil {
		roundTripper = transport
	}
	client := &http.Client{
		Transport: roundTripper,
		Timeout:   config.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &ProtectedPrivacyFixtureTrigger{
		endpoint: endpoint, authorization: config.Authorization, timeout: config.Timeout, client: client,
	}, nil
}

func (trigger *ProtectedPrivacyFixtureTrigger) Trigger(ctx context.Context, request PrivacyFixtureTriggerRequest) (PrivacyFixtureTriggerResult, error) {
	if trigger == nil || ctx == nil || ctx.Err() != nil || !safePrivacyOpaqueID(request.RunID) ||
		!safePrivacyOpaqueID(request.Marker) || !isSafePollMarker(request.ForbiddenCanary) {
		return PrivacyFixtureTriggerResult{}, newPrivacyFixtureTriggerError("invalid_request")
	}
	payload, err := json.Marshal(v1chat.ChatReq{Message: request.ForbiddenCanary})
	if err != nil {
		return PrivacyFixtureTriggerResult{}, newPrivacyFixtureTriggerError("invalid_request")
	}
	requestContext, cancel := context.WithTimeout(ctx, trigger.timeout)
	defer cancel()
	requestURL := *trigger.endpoint
	requestURL.Path = privacyFixtureChatPath
	httpRequest, err := http.NewRequestWithContext(requestContext, http.MethodPost, requestURL.String(), bytes.NewReader(payload))
	if err != nil {
		return PrivacyFixtureTriggerResult{}, newPrivacyFixtureTriggerError("invalid_request")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set(v1chat.ChatSmokeRunIDHeader, request.Marker)
	httpRequest.Header.Set(v1chat.ChatSmokeAuthorizationHeader, trigger.authorization)

	response, err := trigger.client.Do(httpRequest)
	if err != nil {
		if errors.Is(requestContext.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return PrivacyFixtureTriggerResult{}, newPrivacyFixtureTriggerError("backend_timeout")
		}
		return PrivacyFixtureTriggerResult{}, newPrivacyFixtureTriggerError("backend_unavailable")
	}
	defer response.Body.Close()
	raw := privacyFixtureRawResponse{}
	defer raw.release()
	raw.body, err = io.ReadAll(io.LimitReader(response.Body, maximumPrivacyFixtureResponseBytes+1))
	if err != nil || len(raw.body) > maximumPrivacyFixtureResponseBytes {
		return PrivacyFixtureTriggerResult{}, newPrivacyFixtureTriggerError("malformed_response")
	}
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound {
			return PrivacyFixtureTriggerResult{}, newPrivacyFixtureTriggerError("authentication_failed")
		}
		return PrivacyFixtureTriggerResult{}, newPrivacyFixtureTriggerError("backend_unavailable")
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return PrivacyFixtureTriggerResult{}, newPrivacyFixtureTriggerError("malformed_response")
	}
	identity, summary, err := inspectProtectedPrivacyFixtureResponse(raw, response.Header.Values("X-Request-ID"), request.ForbiddenCanary, trigger.authorization)
	if err != nil {
		return PrivacyFixtureTriggerResult{}, err
	}
	return PrivacyFixtureTriggerResult{proof: &privacyFixtureTriggerProof{
		runID: request.RunID, marker: request.Marker, canaryDigest: sha256.Sum256([]byte(request.ForbiddenCanary)), identity: identity, summary: summary,
	}}, nil
}

type privacyFixtureSuccessEnvelope struct {
	Code    *int                    `json:"code"`
	Message *string                 `json:"message"`
	Data    *v1chat.ChatData        `json:"data"`
	Meta    *v1chat.ChatSuccessMeta `json:"meta"`
}

func inspectProtectedPrivacyFixtureResponse(raw privacyFixtureRawResponse, requestHeaders []string, canary, authorization string) (privacyFixtureResponseIdentity, privacy.ScanResult, error) {
	body := raw.body
	if len(body) == 0 || !utf8.Valid(body) || len(requestHeaders) != 1 || !safePrivacyOpaqueID(requestHeaders[0]) {
		return privacyFixtureResponseIdentity{}, privacy.ScanResult{}, newPrivacyFixtureTriggerError("malformed_response")
	}
	var envelope privacyFixtureSuccessEnvelope
	if rejectDuplicatePrivacyFixtureKeys(body) != nil {
		return privacyFixtureResponseIdentity{}, privacy.ScanResult{}, newPrivacyFixtureTriggerError("malformed_response")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.Code == nil || envelope.Message == nil || envelope.Data == nil || envelope.Meta == nil {
		return privacyFixtureResponseIdentity{}, privacy.ScanResult{}, newPrivacyFixtureTriggerError("malformed_response")
	}
	if *envelope.Code != 0 || !validPrivacyChatData(*envelope.Data) || !safePrivacyOpaqueID(envelope.Meta.RequestID) ||
		!safePrivacyOpaqueID(envelope.Meta.AITraceID) || requestHeaders[0] != envelope.Meta.RequestID {
		return privacyFixtureResponseIdentity{}, privacy.ScanResult{}, newPrivacyFixtureTriggerError("malformed_response")
	}
	scanner, err := privacy.NewScanner([]string{canary})
	if err != nil {
		return privacyFixtureResponseIdentity{}, privacy.ScanResult{}, newPrivacyFixtureTriggerError("invalid_request")
	}
	semantic, err := privacyFixtureSemanticText(body)
	if err != nil {
		return privacyFixtureResponseIdentity{}, privacy.ScanResult{}, newPrivacyFixtureTriggerError("malformed_response")
	}
	if bytes.Contains(body, []byte(authorization)) || strings.Contains(semantic, authorization) {
		return privacyFixtureResponseIdentity{}, privacy.ScanResult{}, newPrivacyFixtureTriggerError("unexpected_evidence")
	}
	summary, err := scanner.Scan([]privacy.SurfaceText{
		{Surface: privacy.SurfaceAPI, Text: string(body)},
		{Surface: privacy.SurfaceAPI, Text: semantic},
	})
	if err != nil {
		return privacyFixtureResponseIdentity{}, privacy.ScanResult{}, newPrivacyFixtureTriggerError("malformed_response")
	}
	if privacyScanHasHits(summary) {
		return privacyFixtureResponseIdentity{}, privacy.ScanResult{}, newPrivacyFixtureTriggerError("unexpected_evidence")
	}
	return privacyFixtureResponseIdentity{RequestID: envelope.Meta.RequestID, AITraceID: envelope.Meta.AITraceID}, summary, nil
}

func validPrivacyChatData(data v1chat.ChatData) bool {
	if !privacyFixtureModelPattern.MatchString(data.Model) {
		return false
	}
	switch data.FinishReason {
	case v1chat.FinishReasonStop, v1chat.FinishReasonLength, v1chat.FinishReasonToolCalls, v1chat.FinishReasonContentFilter:
	default:
		return false
	}
	usage := data.Usage
	return usage.InputTokens >= 0 && usage.OutputTokens >= 0 && usage.ReasoningTokens >= 0 &&
		usage.CacheReadTokens >= 0 && usage.CacheWriteTokens >= 0 && usage.TotalTokens >= usage.InputTokens+usage.OutputTokens
}

func rejectDuplicatePrivacyFixtureKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := consumePrivacyFixtureJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("malformed response")
	}
	return nil
}

func consumePrivacyFixtureJSONValue(decoder *json.Decoder) error {
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
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return errors.New("malformed response")
			}
			// encoding/json matches struct fields case-insensitively. Requiring the canonical
			// lowercase wire spelling prevents aliases such as Code from overriding code.
			if key != strings.ToLower(key) {
				return errors.New("malformed response")
			}
			folded := strings.ToLower(key)
			if _, duplicate := seen[folded]; duplicate {
				return errors.New("malformed response")
			}
			seen[folded] = struct{}{}
			if err := consumePrivacyFixtureJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := consumePrivacyFixtureJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("malformed response")
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	want := json.Delim('}')
	if delimiter == '[' {
		want = ']'
	}
	if closing != want {
		return errors.New("malformed response")
	}
	return nil
}

func privacyFixtureSemanticText(body []byte) (string, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return "", errors.New("malformed response")
	}
	values := make([]string, 0, 16)
	collectPrivacyFixtureStrings(value, &values)
	return strings.Join(values, "\n"), nil
}

func collectPrivacyFixtureStrings(value any, values *[]string) {
	switch current := value.(type) {
	case string:
		*values = append(*values, current)
	case []any:
		for _, item := range current {
			collectPrivacyFixtureStrings(item, values)
		}
	case map[string]any:
		for key, item := range current {
			*values = append(*values, key)
			collectPrivacyFixtureStrings(item, values)
		}
	}
}

func validPrivacyFixtureCredential(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) >= minimumPrivacyFixtureCredentialBytes &&
		len(value) <= maximumPrivacyFixtureCredentialBytes && !strings.ContainsAny(value, "\x00\r\n")
}

func parsePrivacyFixtureEndpoint(ctx context.Context, value string, resolve PrivacyFixtureHostResolver) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || parsed.Path != "" {
		return nil, errors.New("unsafe endpoint")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || strings.Contains(parsed.Hostname(), "%") {
		return nil, errors.New("unsafe endpoint")
	}
	if _, err := resolvePrivacyFixtureLoopback(ctx, parsed.Hostname(), resolve); err != nil {
		return nil, err
	}
	return parsed, nil
}

func resolvePrivacyFixtureLoopback(ctx context.Context, host string, resolve PrivacyFixtureHostResolver) ([]net.IP, error) {
	addresses, err := resolve(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("unresolved endpoint")
	}
	verified := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		if address == nil || !address.IsLoopback() {
			return nil, errors.New("unsafe endpoint")
		}
		verified = append(verified, append(net.IP(nil), address...))
	}
	return verified, nil
}

func defaultPrivacyFixtureHostResolver(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

func newPrivacyFixtureTransport(resolve PrivacyFixtureHostResolver, dialContext func(context.Context, string, string) (net.Conn, error)) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableCompression = true
	transport.DisableKeepAlives = true
	transport.MaxResponseHeaderBytes = 32 << 10
	if dialContext == nil {
		dialContext = (&net.Dialer{}).DialContext
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("invalid dial target")
		}
		addresses, err := resolvePrivacyFixtureLoopback(ctx, host, resolve)
		if err != nil {
			return nil, errors.New("unsafe dial target")
		}
		return dialContext(ctx, network, net.JoinHostPort(addresses[0].String(), port))
	}
	return transport
}

func protectedPrivacyFixtureTransportForTest(trigger *ProtectedPrivacyFixtureTrigger) *http.Transport {
	if trigger == nil || trigger.client == nil {
		return nil
	}
	transport, _ := trigger.client.Transport.(*http.Transport)
	return transport
}

func newPrivacyFixtureTriggerError(class string) error {
	return privacyFixtureTriggerError{class: class}
}

var _ PrivacyFixtureTrigger = (*ProtectedPrivacyFixtureTrigger)(nil)
