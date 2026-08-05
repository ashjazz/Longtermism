package langfuse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	t079PublicKey = "pk-t079-public"
	t079SecretKey = "sk-t079-secret"
)

// TestScoreClientCreateUsesBasicAuthAndStableProjectionID 固定 Langfuse Public API 的
// 写入契约：稳定 projection ID、metric name 和 timestamp 共同保证 at-least-once 重试更新同一 score。
func TestScoreClientCreateUsesBasicAuthAndStableProjectionID(t *testing.T) {
	projection := mustNewT078Projection(t)
	var receivedIDs []string
	var receivedTimestamps []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/public/scores" {
			t.Errorf("request = %s %s, want POST /api/public/scores", request.Method, request.URL.Path)
		}
		publicKey, secretKey, ok := request.BasicAuth()
		if !ok || publicKey != t079PublicKey || secretKey != t079SecretKey {
			t.Errorf("BasicAuth() = (%q, %q, %t), want configured public and secret key", publicKey, secretKey, ok)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", request.Header.Get("Content-Type"))
		}
		assertT079CredentialsOnlyUseAuthorization(t, request)
		var body map[string]json.RawMessage
		if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 8<<10)).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		assertT079ScoreRequest(t, body, projection)
		var id string
		if err := json.Unmarshal(body["id"], &id); err != nil {
			t.Fatalf("decode score ID: %v", err)
		}
		receivedIDs = append(receivedIDs, id)
		var timestamp string
		if err := json.Unmarshal(body["timestamp"], &timestamp); err != nil {
			t.Fatalf("decode score timestamp: %v", err)
		}
		receivedTimestamps = append(receivedTimestamps, timestamp)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"id":%q}`, id)
	}))
	defer server.Close()

	client := mustNewT079ScoreClient(t, server.URL, time.Second)
	for range 2 {
		if err := client.Create(context.Background(), projection); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	if len(receivedIDs) != 2 || receivedIDs[0] != projection.ProjectionID || receivedIDs[1] != projection.ProjectionID {
		t.Fatalf("sent score IDs = %#v, want stable projection ID %q on every attempt", receivedIDs, projection.ProjectionID)
	}
	if len(receivedTimestamps) != 2 || receivedTimestamps[0] != projection.CreatedAt.UTC().Format(time.RFC3339Nano) || receivedTimestamps[1] != receivedTimestamps[0] {
		t.Fatalf("sent score timestamps = %#v, want stable projection timestamp %q on every attempt", receivedTimestamps, projection.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
}

func TestScoreClientCreateOmitsObservationIDForTraceScore(t *testing.T) {
	projection, err := NewScoreProjection(newT078ProjectionInput(t, newT078Evidence(t, "answer_relevance"), ScoreTargetKindTrace))
	if err != nil {
		t.Fatalf("NewScoreProjection() error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, exists := body["observationId"]; exists {
			t.Fatalf("trace score must omit observationId: %#v", body)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := mustNewT079ScoreClient(t, server.URL, time.Second).Create(context.Background(), projection); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
}

func TestNewScoreClientRejectsInsecureRemoteURLAndExcessiveTimeout(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		timeout time.Duration
	}{
		{name: "remote plaintext URL", baseURL: "http://example.com", timeout: time.Second},
		{name: "timeout above platform bound", baseURL: "https://example.com", timeout: maxScoreClientTimeout + time.Nanosecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewScoreClient(ScoreClientConfig{
				BaseURL: tt.baseURL, PublicKey: t079PublicKey, SecretKey: t079SecretKey, Timeout: tt.timeout,
			})
			if !errors.Is(err, errInvalidScoreClient) {
				t.Fatalf("NewScoreClient() error = %v, want sanitized invalid configuration", err)
			}
		})
	}
}

func TestScoreClientCreateClassifiesTimeoutAndHTTPFailures(t *testing.T) {
	projection := mustNewT078Projection(t)
	tests := []struct {
		name      string
		status    int
		block     bool
		wantError error
	}{
		{name: "caller timeout", block: true, wantError: ErrScoreTimeout},
		{name: "rate limited", status: http.StatusTooManyRequests, wantError: ErrScoreRateLimited},
		{name: "upstream internal error", status: http.StatusInternalServerError, wantError: ErrScoreUpstream},
		{name: "upstream unavailable", status: http.StatusServiceUnavailable, wantError: ErrScoreUpstream},
		{name: "client validation error", status: http.StatusBadRequest, wantError: ErrScoreRejected},
		{name: "authentication error", status: http.StatusUnauthorized, wantError: ErrScoreRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			releaseBlockedHandler := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if tt.block {
					// Some HTTP/1.1 transports return the caller deadline before the
					// server observes a disconnected peer. The explicit release keeps
					// this fixture deterministic without weakening the timeout check.
					select {
					case <-request.Context().Done():
					case <-releaseBlockedHandler:
					}
					return
				}
				writer.WriteHeader(tt.status)
				_, _ = writer.Write([]byte("raw-t079-upstream-response"))
			}))
			defer server.Close()
			defer close(releaseBlockedHandler)

			client := mustNewT079ScoreClient(t, server.URL, time.Second)
			ctx := context.Background()
			if tt.block {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			err := client.Create(ctx, projection)
			if !errors.Is(err, tt.wantError) {
				t.Fatalf("Create() error = %v, want errors.Is(%v)", err, tt.wantError)
			}
			assertT079ClientErrorIsSanitized(t, err)
		})
	}
}

func TestScoreClientCreatePreservesCallerCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Error("canceled request must not reach upstream")
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := mustNewT079ScoreClient(t, server.URL, time.Second).Create(ctx, mustNewT078Projection(t))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Create() error = %v, want context.Canceled", err)
	}
}

func TestScoreClientCreateBoundsResponseBodyAndRedactsErrors(t *testing.T) {
	projection := mustNewT078Projection(t)
	oversizedBody := "raw-t079-evidence " + strings.Repeat("x", scoreClientResponseBodyLimit)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(oversizedBody))
	}))
	defer server.Close()

	err := mustNewT079ScoreClient(t, server.URL, time.Second).Create(context.Background(), projection)
	if !errors.Is(err, ErrScoreResponseTooLarge) {
		t.Fatalf("Create() error = %v, want errors.Is(ErrScoreResponseTooLarge)", err)
	}
	assertT079ClientErrorIsSanitized(t, err)
}

func TestScoreClientCreateBoundsSuccessfulResponseBody(t *testing.T) {
	projection := mustNewT078Projection(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(strings.Repeat("x", scoreClientResponseBodyLimit+1)))
	}))
	defer server.Close()

	err := mustNewT079ScoreClient(t, server.URL, time.Second).Create(context.Background(), projection)
	if !errors.Is(err, ErrScoreResponseTooLarge) {
		t.Fatalf("Create() error = %v, want errors.Is(ErrScoreResponseTooLarge)", err)
	}
	assertT079ClientErrorIsSanitized(t, err)
}

func TestScoreClientCreateRejectsRedirectBeforeCredentialsReachAnotherHost(t *testing.T) {
	projection := mustNewT078Projection(t)
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		targetCalls++
		if request.Header.Get("Authorization") != "" {
			t.Error("redirect target must never receive score credentials")
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/api/public/scores", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	err := mustNewT079ScoreClient(t, redirector.URL, time.Second).Create(context.Background(), projection)
	if !errors.Is(err, ErrScoreUpstream) || targetCalls != 0 {
		t.Fatalf("Create() error = %v target calls = %d, want upstream redirect rejection before target access", err, targetCalls)
	}
	assertT079ClientErrorIsSanitized(t, err)
}

func TestScoreClientFailureLogContainsOnlyStableDiagnosticFields(t *testing.T) {
	projection := mustNewT078Projection(t)
	projection.Evidence.FailureSummary = "raw-t079-failure-summary"
	logger := &t079FailureLogger{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte("raw-t079-evidence and sk-t079-secret"))
	}))
	defer server.Close()

	client, err := NewScoreClient(ScoreClientConfig{
		BaseURL:       server.URL,
		PublicKey:     t079PublicKey,
		SecretKey:     t079SecretKey,
		Timeout:       time.Second,
		FailureLogger: logger,
	})
	if err != nil {
		t.Fatalf("NewScoreClient() error = %v", err)
	}
	err = client.Create(context.Background(), projection)
	if !errors.Is(err, ErrScoreUpstream) || len(logger.entries) != 1 {
		t.Fatalf("Create() error = %v log entries = %#v, want upstream error and one diagnostic", err, logger.entries)
	}
	entry := logger.entries[0]
	if entry.Class != ScoreClientErrorClassUpstream || entry.StatusCode != http.StatusBadGateway {
		t.Fatalf("failure log = %#v, want upstream class and status only", entry)
	}
	serialized := assertT079FailureLogSchema(t, entry)
	for _, forbidden := range []string{
		t079PublicKey,
		t079SecretKey,
		"raw-t079-evidence",
		projection.ProjectionID,
		projection.Evidence.EvalRunID,
		projection.Evidence.RequestID,
		projection.Evidence.AITraceID,
		projection.Evidence.ServiceTraceID,
		projection.Evidence.SpanID,
		projection.Evidence.Dataset.Name,
		projection.Evidence.Dataset.Version,
		projection.Evidence.SampleID,
		projection.Evidence.FailureSummary,
	} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("failure log leaked %q: %s", forbidden, serialized)
		}
	}
}

func mustNewT079ScoreClient(t *testing.T, baseURL string, timeout time.Duration) *ScoreClient {
	t.Helper()
	client, err := NewScoreClient(ScoreClientConfig{
		BaseURL:   baseURL,
		PublicKey: t079PublicKey,
		SecretKey: t079SecretKey,
		Timeout:   timeout,
	})
	if err != nil {
		t.Fatalf("NewScoreClient() error = %v", err)
	}
	return client
}

func assertT079ScoreRequest(t *testing.T, body map[string]json.RawMessage, projection ScoreProjection) {
	t.Helper()
	if len(body) != 7 {
		t.Fatalf("score request keys = %#v, want exactly id/traceId/observationId/name/value/dataType/timestamp", body)
	}
	want := map[string]any{
		"id":            projection.ProjectionID,
		"traceId":       projection.Target.PlatformTraceID(),
		"observationId": projection.Target.PlatformObservationID(),
		"name":          projection.Evidence.MetricName,
		"value":         projection.Evidence.Score,
		"dataType":      "NUMERIC",
		"timestamp":     projection.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	for key, expected := range want {
		var actual any
		if err := json.Unmarshal(body[key], &actual); err != nil || fmt.Sprint(actual) != fmt.Sprint(expected) {
			t.Fatalf("score request %q = %s, want %#v", key, body[key], expected)
		}
	}
	for _, forbidden := range []string{"evidence", "dataset", "sampleId", "requestId", "aiTraceId", "failureSummary", "comment"} {
		if _, exists := body[forbidden]; exists {
			t.Fatalf("score request must not export local evidence field %q", forbidden)
		}
	}
}

func assertT079CredentialsOnlyUseAuthorization(t *testing.T, request *http.Request) {
	t.Helper()
	if request.URL.RawQuery != "" || strings.Contains(request.URL.String(), t079PublicKey) || strings.Contains(request.URL.String(), t079SecretKey) {
		t.Fatalf("score request URL must not contain credentials: %s", request.URL)
	}
	for header, values := range request.Header {
		if header == "Authorization" {
			continue
		}
		for _, value := range values {
			if strings.Contains(value, t079PublicKey) || strings.Contains(value, t079SecretKey) {
				t.Fatalf("score request header %q must not contain credentials", header)
			}
		}
	}
}

func assertT079ClientErrorIsSanitized(t *testing.T, err error) {
	t.Helper()
	for _, forbidden := range []string{t079PublicKey, t079SecretKey, "raw-t079-upstream-response", "raw-t079-evidence"} {
		if err != nil && strings.Contains(err.Error(), forbidden) {
			t.Fatalf("score client error leaked %q: %v", forbidden, err)
		}
	}
}

func assertT079FailureLogSchema(t *testing.T, entry ScoreClientFailureLog) []byte {
	t.Helper()
	serialized, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal failure log: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(serialized, &fields); err != nil {
		t.Fatalf("unmarshal failure log: %v", err)
	}
	if len(fields) != 2 || fields["class"] == nil || fields["status_code"] == nil {
		t.Fatalf("failure log fields = %#v, want only class and status_code", fields)
	}
	return serialized
}

type t079FailureLogger struct{ entries []ScoreClientFailureLog }

func (l *t079FailureLogger) LogScoreClientFailure(_ context.Context, entry ScoreClientFailureLog) {
	l.entries = append(l.entries, entry)
}
