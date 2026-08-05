package langfuse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	scoreClientResponseBodyLimit = 64 << 10
	maxScoreClientTimeout        = 60 * time.Second
)

var (
	ErrScoreTimeout          = errors.New("langfuse score request timed out")
	ErrScoreRateLimited      = errors.New("langfuse score request was rate limited")
	ErrScoreUpstream         = errors.New("langfuse score upstream failed")
	ErrScoreRejected         = errors.New("langfuse score request was rejected")
	ErrScoreResponseTooLarge = errors.New("langfuse score response exceeded limit")
	errInvalidScoreClient    = errors.New("langfuse score client configuration is invalid")
)

type ScoreClientErrorClass string

const (
	ScoreClientErrorClassTimeout          ScoreClientErrorClass = "timeout"
	ScoreClientErrorClassRateLimited      ScoreClientErrorClass = "rate_limited"
	ScoreClientErrorClassUpstream         ScoreClientErrorClass = "upstream"
	ScoreClientErrorClassRejected         ScoreClientErrorClass = "rejected"
	ScoreClientErrorClassResponseTooLarge ScoreClientErrorClass = "response_too_large"
)

// ScoreClientFailureLog intentionally exposes only stable, low-sensitivity facts.
// Response bodies, credentials, and evaluation identities must remain outside logs.
type ScoreClientFailureLog struct {
	Class      ScoreClientErrorClass `json:"class"`
	StatusCode int                   `json:"status_code"`
}

type ScoreClientFailureLogger interface {
	LogScoreClientFailure(context.Context, ScoreClientFailureLog)
}

type ScoreClientConfig struct {
	BaseURL       string
	PublicKey     string
	SecretKey     string
	Timeout       time.Duration
	FailureLogger ScoreClientFailureLogger
}

// ScoreSender is the narrow boundary consumed by the asynchronous delivery worker.
type ScoreSender interface {
	Create(context.Context, ScoreProjection) error
}

type ScoreClient struct {
	endpoint      *url.URL
	publicKey     string
	secretKey     string
	timeout       time.Duration
	httpClient    *http.Client
	failureLogger ScoreClientFailureLogger
}

type scoreRequest struct {
	ID            string  `json:"id"`
	TraceID       string  `json:"traceId"`
	ObservationID string  `json:"observationId,omitempty"`
	Name          string  `json:"name"`
	Value         float64 `json:"value"`
	DataType      string  `json:"dataType"`
	Timestamp     string  `json:"timestamp"`
}

func NewScoreClient(config ScoreClientConfig) (*ScoreClient, error) {
	endpoint, err := buildScoreEndpoint(config.BaseURL)
	if err != nil || !isValidCredential(config.PublicKey) || !isValidCredential(config.SecretKey) ||
		config.Timeout <= 0 || config.Timeout > maxScoreClientTimeout {
		return nil, errInvalidScoreClient
	}

	return &ScoreClient{
		endpoint:  endpoint,
		publicKey: config.PublicKey,
		secretKey: config.SecretKey,
		timeout:   config.Timeout,
		httpClient: &http.Client{Timeout: config.Timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			// Never forward Basic Auth across redirects; callers classify the 3xx response.
			return http.ErrUseLastResponse
		}},
		failureLogger: config.FailureLogger,
	}, nil
}

func (client *ScoreClient) Create(ctx context.Context, projection ScoreProjection) error {
	if client == nil || ctx == nil || !isValidScoreRequestProjection(projection) {
		return ErrScoreRejected
	}

	payload, err := marshalScoreRequest(projection.Snapshot())
	if err != nil {
		return client.fail(ctx, ScoreClientErrorClassRejected, 0, ErrScoreRejected)
	}
	requestCtx, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, client.endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return client.fail(ctx, ScoreClientErrorClassRejected, 0, ErrScoreRejected)
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth(client.publicKey, client.secretKey)

	response, err := client.httpClient.Do(request)
	if err != nil {
		if errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return client.fail(ctx, ScoreClientErrorClassTimeout, 0, ErrScoreTimeout)
		}
		if errors.Is(requestCtx.Err(), context.Canceled) {
			// Lifecycle cancellation is not a platform failure and must not be retried.
			return context.Canceled
		}
		return client.fail(ctx, ScoreClientErrorClassUpstream, 0, ErrScoreUpstream)
	}
	defer response.Body.Close()

	if err := readBoundedScoreResponse(response.Body); err != nil {
		class, publicErr := ScoreClientErrorClassUpstream, ErrScoreUpstream
		if errors.Is(err, ErrScoreResponseTooLarge) {
			class, publicErr = ScoreClientErrorClassResponseTooLarge, ErrScoreResponseTooLarge
		}
		return client.fail(ctx, class, response.StatusCode, publicErr)
	}
	class, publicErr := classifyScoreStatus(response.StatusCode)
	if publicErr == nil {
		return nil
	}
	return client.fail(ctx, class, response.StatusCode, publicErr)
}

func buildScoreEndpoint(rawBaseURL string) (*url.URL, error) {
	if strings.TrimSpace(rawBaseURL) != rawBaseURL || rawBaseURL == "" {
		return nil, errInvalidScoreClient
	}
	parsed, err := url.Parse(rawBaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errInvalidScoreClient
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errInvalidScoreClient
	}
	if parsed.Scheme == "http" && !isLoopbackURL(parsed) {
		return nil, errInvalidScoreClient
	}
	parsed.Path = "/api/public/scores"
	return parsed, nil
}

func isLoopbackURL(endpoint *url.URL) bool {
	host := endpoint.Hostname()
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isValidCredential(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func isValidScoreRequestProjection(projection ScoreProjection) bool {
	return isValidScoreTarget(projection.Target) &&
		projection.ProjectionID == deriveProjectionID(projection.Target, projection.Evidence) &&
		projection.Target.PlatformTraceID() == projection.Evidence.ServiceTraceID &&
		projection.Evidence.MetricName != "" &&
		strings.TrimSpace(projection.Evidence.MetricName) == projection.Evidence.MetricName &&
		len(projection.Evidence.MetricName) <= maxProjectionFactBytes &&
		!math.IsNaN(projection.Evidence.Score) && !math.IsInf(projection.Evidence.Score, 0) &&
		projection.Evidence.Score >= 0 && projection.Evidence.Score <= 1 && !projection.CreatedAt.IsZero()
}

func marshalScoreRequest(projection ScoreProjection) ([]byte, error) {
	return json.Marshal(scoreRequest{
		ID:            projection.ProjectionID,
		TraceID:       projection.Target.PlatformTraceID(),
		ObservationID: projection.Target.PlatformObservationID(),
		Name:          projection.Evidence.MetricName,
		Value:         projection.Evidence.Score,
		DataType:      "NUMERIC",
		Timestamp:     projection.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
}

func readBoundedScoreResponse(body io.Reader) error {
	responseBody, err := io.ReadAll(io.LimitReader(body, scoreClientResponseBodyLimit+1))
	if err != nil {
		return ErrScoreUpstream
	}
	if len(responseBody) > scoreClientResponseBodyLimit {
		return ErrScoreResponseTooLarge
	}
	return nil
}

func classifyScoreStatus(statusCode int) (ScoreClientErrorClass, error) {
	switch {
	case statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices:
		return "", nil
	case statusCode == http.StatusTooManyRequests:
		return ScoreClientErrorClassRateLimited, ErrScoreRateLimited
	case statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError:
		return ScoreClientErrorClassRejected, ErrScoreRejected
	default:
		return ScoreClientErrorClassUpstream, ErrScoreUpstream
	}
}

func (client *ScoreClient) fail(ctx context.Context, class ScoreClientErrorClass, statusCode int, publicErr error) error {
	if client.failureLogger != nil {
		client.failureLogger.LogScoreClientFailure(ctx, ScoreClientFailureLog{Class: class, StatusCode: statusCode})
	}
	return fmt.Errorf("score delivery failed: %w", publicErr)
}
