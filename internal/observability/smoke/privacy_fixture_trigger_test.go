package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1chat "github.com/ashjazz/Longtermism/api/v1/chat"
)

const (
	t187Canary        = "T187_SYNTHETIC_CANARY"
	t187Authorization = "t187-independent-smoke-credential"
	t187Marker        = "marker-t187-protected"
	t187RawResponse   = "raw-t187-provider-response"
)

// TestProtectedPrivacyFixtureTriggerPostsAuthenticatedLoopbackChat 固定 concrete trigger 的
// 成功事实链。只有真实 loopback POST 已被服务端接收、正式 chat envelope 已通过严格解析，
// 后续 manifest 与 artifact capability 才能被调用；调用方不能用布尔字段伪造这段事实。
func TestProtectedPrivacyFixtureTriggerPostsAuthenticatedLoopbackChat(t *testing.T) {
	startedAt := time.Now().UTC()
	order := &t187EventOrder{}
	requestValidation := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		order.add("transport")
		requestValidation <- validateT187ProtectedChatRequest(request, t187Marker, t187Authorization, t187Canary)
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Request-ID", "request-t187")
		_, _ = writer.Write(t187SuccessfulChatEnvelope("safe fixture reply"))
	}))
	defer server.Close()

	trigger, err := NewProtectedPrivacyFixtureTrigger(ProtectedPrivacyFixtureTriggerConfig{
		Endpoint: server.URL, MasterSmokeEnabled: true, ChatSmokeEnabled: true,
		Authorization: t187Authorization, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewProtectedPrivacyFixtureTrigger() failed with class %q", t187ErrorClass(err))
	}
	manifest := &t187ManifestConsumer{order: order, manifest: ChatRunManifestInput{
		SmokeRunID: t187Marker, RequestID: "request-t187", AITraceID: "ai-trace-t187",
		ServiceTraceID: "1234567890abcdef1234567890abcdef", SpanID: "1234567890abcdef",
	}}
	writer := &t187ArtifactWriter{order: order, refs: PrivacyFixtureArtifactRefs{
		ManifestRef: "manifest-t187.json", APISummaryRef: "api-summary-t187.json",
		ApplicationLogRef: "application-log-t187.json", ChatReportRef: "chat-report-t187.json",
		CollectorArtifactRef: "collector-proof-t187.json",
	}}

	result, err := RunPrivacyFixture(context.Background(), t187FixtureRequest(startedAt), PrivacyFixtureDependencies{
		Trigger: trigger, Manifest: manifest, Writer: writer,
	})
	if err != nil {
		t.Fatalf("RunPrivacyFixture() failed with class %q", t187ErrorClass(err))
	}
	if requestErr := <-requestValidation; requestErr != nil {
		t.Fatalf("protected chat request contract: %v", requestErr)
	}
	if got := order.snapshot(); !reflect.DeepEqual(got, []string{"transport", "manifest", "writer"}) {
		t.Fatalf("fixture event order = %v, want transport then manifest then writer", got)
	}
	if manifest.calls != 1 || writer.calls != 1 || !result.RequestSent || !result.ChatSucceeded {
		t.Fatalf("downstream proof = manifest:%d writer:%d sent:%t succeeded:%t, want 1/1/true/true", manifest.calls, writer.calls, result.RequestSent, result.ChatSucceeded)
	}
	if result.RequestID != "request-t187" || result.AITraceID != "ai-trace-t187" || result.Marker != t187Marker {
		t.Fatal("fixture did not preserve the strict envelope and runner-owned marker identities")
	}
	assertT187LowSensitiveValue(t, result)
	var _ PrivacyFixtureTrigger = trigger
}

func TestProtectedPrivacyFixtureTriggerRejectsUnsafeConfigurationBeforeTransport(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	valid := ProtectedPrivacyFixtureTriggerConfig{
		Endpoint: server.URL, MasterSmokeEnabled: true, ChatSmokeEnabled: true,
		Authorization: t187Authorization, Timeout: time.Second,
	}
	tests := []struct {
		name   string
		mutate func(*ProtectedPrivacyFixtureTriggerConfig)
	}{
		{name: "master smoke gate disabled", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.MasterSmokeEnabled = false }},
		{name: "chat smoke gate disabled", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.ChatSmokeEnabled = false }},
		{name: "missing credential", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Authorization = "" }},
		{name: "short credential", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Authorization = "too-short" }},
		{name: "oversized credential", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Authorization = strings.Repeat("x", 513) }},
		{name: "credential contains NUL", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Authorization += "\x00" }},
		{name: "credential contains line break", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Authorization += "\r\nforged: value" }},
		{name: "missing endpoint", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Endpoint = "" }},
		{name: "remote endpoint", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Endpoint = "http://192.0.2.1:18000" }},
		{name: "remote hostname", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Endpoint = "http://example.com:18000" }},
		{name: "unspecified IPv4", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Endpoint = "http://0.0.0.0:18000" }},
		{name: "unspecified IPv6", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Endpoint = "http://[::]:18000" }},
		{name: "private network", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Endpoint = "http://10.0.0.1:18000" }},
		{name: "link local", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Endpoint = "http://169.254.169.254:18000" }},
		{name: "endpoint userinfo", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) {
			config.Endpoint = "http://user:pass@127.0.0.1:18000"
		}},
		{name: "endpoint path override", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Endpoint = server.URL + "/admin" }},
		{name: "endpoint query override", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Endpoint = server.URL + "?target=remote" }},
		{name: "endpoint fragment", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Endpoint = server.URL + "#fragment" }},
		{name: "missing timeout", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Timeout = 0 }},
		{name: "excessive timeout", mutate: func(config *ProtectedPrivacyFixtureTriggerConfig) { config.Timeout = time.Minute }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := valid
			tt.mutate(&config)
			trigger, err := NewProtectedPrivacyFixtureTrigger(config)
			if err == nil || trigger != nil {
				t.Fatal("unsafe configuration must fail before constructing a transport capability")
			}
			assertT187LowSensitiveError(t, err, config.Authorization, config.Endpoint)
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("unsafe constructor transport attempts = %d, want 0", requests.Load())
	}
}

func TestProtectedPrivacyFixtureTriggerRevalidatesEveryResolvedAddressAtDialTime(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	endpoint := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	tests := []struct {
		name     string
		resolver PrivacyFixtureHostResolver
	}{
		{name: "mixed loopback and remote constructor result", resolver: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.0.2.1")}, nil
		}},
		{name: "DNS rebinding before dial", resolver: func() PrivacyFixtureHostResolver {
			var calls atomic.Int64
			return func(context.Context, string) ([]net.IP, error) {
				if calls.Add(1) == 1 {
					return []net.IP{net.ParseIP("127.0.0.1")}, nil
				}
				return []net.IP{net.ParseIP("192.0.2.1")}, nil
			}
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trigger, err := NewProtectedPrivacyFixtureTrigger(ProtectedPrivacyFixtureTriggerConfig{
				Endpoint: endpoint, MasterSmokeEnabled: true, ChatSmokeEnabled: true,
				Authorization: t187Authorization, Timeout: time.Second, ResolveHost: tt.resolver,
			})
			if err == nil {
				_, err = trigger.Trigger(context.Background(), PrivacyFixtureTriggerRequest{RunID: "run-t187-privacy", Marker: t187Marker, ForbiddenCanary: t187Canary})
			}
			if err == nil {
				t.Fatal("unsafe resolution must fail before dialing")
			}
			assertT187LowSensitiveError(t, err, endpoint)
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("unsafe DNS transport attempts = %d, want 0", requests.Load())
	}
}

func TestProtectedPrivacyFixtureTriggerDialsTheRevalidatedNumericLoopbackAddress(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Request-ID", "request-t187")
		_, _ = writer.Write(t187SuccessfulChatEnvelope("safe fixture reply"))
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal("parse loopback test server URL")
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal("split loopback test server address")
	}
	endpoint := "http://privacy-fixture.invalid:" + port
	var dialedAddress string
	trigger, err := newProtectedPrivacyFixtureTriggerForTest(ProtectedPrivacyFixtureTriggerConfig{
		Endpoint: endpoint, MasterSmokeEnabled: true, ChatSmokeEnabled: true,
		Authorization: t187Authorization, Timeout: time.Second,
		ResolveHost: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		},
	}, ProtectedPrivacyFixtureTriggerTestDependencies{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialedAddress = address
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	})
	if err != nil {
		t.Fatal("protected trigger must accept an injected loopback resolution")
	}
	result, err := trigger.Trigger(context.Background(), PrivacyFixtureTriggerRequest{
		RunID: "run-t187-privacy", Marker: t187Marker, ForbiddenCanary: t187Canary,
	})
	if err != nil || requests.Load() != 1 {
		t.Fatal("protected trigger must reach the loopback server through the verified address")
	}
	host, gotPort, splitErr := net.SplitHostPort(dialedAddress)
	if splitErr != nil || host != "127.0.0.1" || gotPort != port || strings.Contains(dialedAddress, "privacy-fixture.invalid") {
		t.Fatalf("dial target = %q, want the verified numeric loopback IP and original port", dialedAddress)
	}
	assertT187LowSensitiveValue(t, result)
}

func TestProtectedPrivacyFixtureTriggerIgnoresEnvironmentProxies(t *testing.T) {
	var proxyCalls atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		proxyCalls.Add(1)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	var loopbackCalls atomic.Int64
	loopback := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		loopbackCalls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Request-ID", "request-t187")
		_, _ = writer.Write(t187SuccessfulChatEnvelope("safe fixture reply"))
	}))
	defer loopback.Close()
	parsed, err := url.Parse(loopback.URL)
	if err != nil {
		t.Fatal("parse loopback test server URL")
	}
	_, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal("split loopback test server address")
	}
	endpoint := "http://privacy-proxy-test.invalid:" + port
	trigger, err := NewProtectedPrivacyFixtureTrigger(ProtectedPrivacyFixtureTriggerConfig{
		Endpoint: endpoint, MasterSmokeEnabled: true, ChatSmokeEnabled: true,
		Authorization: t187Authorization, Timeout: time.Second,
		ResolveHost: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		},
	})
	if err != nil {
		t.Fatal("protected trigger must accept the controlled loopback hostname")
	}
	transport := protectedPrivacyFixtureTransportForTest(trigger)
	if transport == nil || transport.Proxy != nil {
		t.Fatal("production protected trigger transport must disable proxies explicitly")
	}
	if _, err := trigger.Trigger(context.Background(), PrivacyFixtureTriggerRequest{
		RunID: "run-t187-privacy", Marker: t187Marker, ForbiddenCanary: t187Canary,
	}); err != nil || loopbackCalls.Load() != 1 || proxyCalls.Load() != 0 {
		t.Fatalf("proxy isolation = err:%t loopback:%d proxy:%d, want direct protected loopback request", err != nil, loopbackCalls.Load(), proxyCalls.Load())
	}
}

// admission/replay/non-2xx failures may prove that a transport was attempted, but they do not
// prove a successful protected chat. They must therefore stop before consuming the native
// identity manifest or publishing any local privacy artifact.
func TestProtectedPrivacyFixtureTriggerRejectsAdmissionReplayAndHTTPFailuresBeforeDownstreamFacts(t *testing.T) {
	startedAt := time.Now().UTC()
	tests := []struct {
		name          string
		authorization string
		status        int
		body          []byte
		calls         int
	}{
		{name: "wrong authorization", authorization: "t187-wrong-smoke-credential", status: http.StatusNotFound, body: []byte(t187RawResponse), calls: 1},
		{name: "replayed marker", authorization: t187Authorization, status: http.StatusNotFound, body: []byte(t187RawResponse), calls: 1},
		{name: "unauthorized", authorization: t187Authorization, status: http.StatusUnauthorized, body: []byte(t187RawResponse), calls: 1},
		{name: "rate limited", authorization: t187Authorization, status: http.StatusTooManyRequests, body: []byte(t187RawResponse), calls: 1},
		{name: "upstream failure", authorization: t187Authorization, status: http.StatusBadGateway, body: []byte(t187RawResponse), calls: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				if request.Header.Get(v1chat.ChatSmokeAuthorizationHeader) != t187Authorization {
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				writer.WriteHeader(tt.status)
				_, _ = writer.Write(tt.body)
			}))
			defer server.Close()

			trigger := t187NewTrigger(t, server.URL, tt.authorization)
			manifest := &t187ManifestConsumer{}
			writer := &t187ArtifactWriter{}
			_, err := RunPrivacyFixture(context.Background(), t187FixtureRequest(startedAt), PrivacyFixtureDependencies{Trigger: trigger, Manifest: manifest, Writer: writer})
			if err == nil || calls.Load() != int64(tt.calls) || manifest.calls != 0 || writer.calls != 0 {
				t.Fatal("failed admission must make one real attempt and publish no downstream facts")
			}
			assertT187LowSensitiveError(t, err, tt.authorization, string(tt.body), server.URL)
		})
	}
}

func TestProtectedPrivacyFixtureTriggerRejectsTheSameRunnerMarkerReplay(t *testing.T) {
	startedAt := time.Now().UTC()
	var calls atomic.Int64
	var markerMu sync.Mutex
	consumed := make(map[string]struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		marker := request.Header.Get(v1chat.ChatSmokeRunIDHeader)
		markerMu.Lock()
		_, replayed := consumed[marker]
		if !replayed {
			consumed[marker] = struct{}{}
		}
		markerMu.Unlock()
		if replayed {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Request-ID", "request-t187")
		_, _ = writer.Write(t187SuccessfulChatEnvelope("safe fixture reply"))
	}))
	defer server.Close()

	trigger := t187NewTrigger(t, server.URL, t187Authorization)
	firstManifest := &t187ManifestConsumer{manifest: ChatRunManifestInput{
		SmokeRunID: t187Marker, RequestID: "request-t187", AITraceID: "ai-trace-t187",
		ServiceTraceID: "1234567890abcdef1234567890abcdef", SpanID: "1234567890abcdef",
	}}
	firstWriter := &t187ArtifactWriter{refs: PrivacyFixtureArtifactRefs{
		ManifestRef: "manifest-t187.json", APISummaryRef: "api-summary-t187.json",
		ApplicationLogRef: "application-log-t187.json", ChatReportRef: "chat-report-t187.json",
		CollectorArtifactRef: "collector-proof-t187.json",
	}}
	if _, err := RunPrivacyFixture(context.Background(), t187FixtureRequest(startedAt), PrivacyFixtureDependencies{
		Trigger: trigger, Manifest: firstManifest, Writer: firstWriter,
	}); err != nil {
		t.Fatal("first protected marker use must succeed")
	}

	replayManifest := &t187ManifestConsumer{}
	replayWriter := &t187ArtifactWriter{}
	_, err := RunPrivacyFixture(context.Background(), t187FixtureRequest(startedAt), PrivacyFixtureDependencies{
		Trigger: trigger, Manifest: replayManifest, Writer: replayWriter,
	})
	if err == nil || calls.Load() != 2 || firstManifest.calls != 1 || firstWriter.calls != 1 || replayManifest.calls != 0 || replayWriter.calls != 0 {
		t.Fatal("a replayed runner marker must be rejected by the protected server before new downstream facts")
	}
	assertT187LowSensitiveError(t, err, t187Authorization, server.URL)
}

func TestProtectedPrivacyFixtureTriggerDoesNotFollowRedirects(t *testing.T) {
	for _, status := range []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			startedAt := time.Now().UTC()
			var redirectedCalls atomic.Int64
			redirected := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
				redirectedCalls.Add(1)
				if request.Header.Get(v1chat.ChatSmokeAuthorizationHeader) != "" {
					t.Error("redirect leaked the independent smoke credential")
				}
			}))
			defer redirected.Close()
			var initialCalls atomic.Int64
			initial := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				initialCalls.Add(1)
				writer.Header().Set("Location", redirected.URL)
				writer.WriteHeader(status)
			}))
			defer initial.Close()

			manifest := &t187ManifestConsumer{}
			writer := &t187ArtifactWriter{}
			_, err := RunPrivacyFixture(context.Background(), t187FixtureRequest(startedAt), PrivacyFixtureDependencies{
				Trigger: t187NewTrigger(t, initial.URL, t187Authorization), Manifest: manifest, Writer: writer,
			})
			if err == nil || initialCalls.Load() != 1 || redirectedCalls.Load() != 0 || manifest.calls != 0 || writer.calls != 0 {
				t.Fatal("redirect must stop after the initial transport attempt and before downstream facts")
			}
			assertT187LowSensitiveError(t, err, initial.URL, redirected.URL)
		})
	}
}

func TestProtectedPrivacyFixtureTriggerBoundsAndClosesResponseBeforeDecode(t *testing.T) {
	body := &t187CountingBody{remaining: maximumPrivacyFixtureResponseBytes * 8}
	trigger, err := newProtectedPrivacyFixtureTriggerForTest(ProtectedPrivacyFixtureTriggerConfig{
		Endpoint: "http://127.0.0.1:18000", MasterSmokeEnabled: true, ChatSmokeEnabled: true,
		Authorization: t187Authorization, Timeout: time.Second,
	}, t187RoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}, "X-Request-ID": {"request-t187"}},
			Body:       body,
		}, nil
	}))
	if err != nil {
		t.Fatal("test-only protected trigger construction failed")
	}
	_, err = trigger.Trigger(context.Background(), PrivacyFixtureTriggerRequest{RunID: "run-t187-privacy", Marker: t187Marker, ForbiddenCanary: t187Canary})
	if err == nil || body.read.Load() > maximumPrivacyFixtureResponseBytes+1 || !body.closed.Load() {
		t.Fatalf("bounded body proof = error:%t read:%d closed:%t, want failure after at most max+1 bytes and close", err != nil, body.read.Load(), body.closed.Load())
	}
	assertT187LowSensitiveError(t, err)
}

func TestProtectedPrivacyFixtureTriggerRequiresStrictBoundedChatSuccessEnvelope(t *testing.T) {
	startedAt := time.Now().UTC()
	oversized := append([]byte(`{"code":0,"message":"success","data":{"content":"`), bytes.Repeat([]byte("x"), maximumPrivacyFixtureResponseBytes)...)
	tests := []struct {
		name   string
		status int
		header string
		body   []byte
	}{
		{name: "business failure", status: http.StatusOK, header: "request-t187", body: []byte(`{"code":502,"message":"upstream failed","data":null,"meta":{"request_id":"request-t187","ai_trace_id":"ai-trace-t187"}}`)},
		{name: "non-contract success status", status: http.StatusCreated, header: "request-t187", body: t187SuccessfulChatEnvelope("safe")},
		{name: "missing JSON content type", status: http.StatusOK, header: "request-t187", body: t187SuccessfulChatEnvelope("safe")},
		{name: "empty body", status: http.StatusOK, header: "request-t187"},
		{name: "malformed JSON", status: http.StatusOK, header: "request-t187", body: []byte(`{"code":0`)},
		{name: "trailing JSON", status: http.StatusOK, header: "request-t187", body: append(t187SuccessfulChatEnvelope("safe"), []byte(` {}`)...)},
		{name: "unknown field", status: http.StatusOK, header: "request-t187", body: []byte(`{"code":0,"message":"success","unexpected":true,"data":{"content":"safe","model":"test-model","finish_reason":"stop","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}},"meta":{"request_id":"request-t187","ai_trace_id":"ai-trace-t187"}}`)},
		{name: "nested unknown field", status: http.StatusOK, header: "request-t187", body: []byte(`{"code":0,"message":"success","data":{"content":"safe","model":"test-model","finish_reason":"stop","unexpected":true,"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}},"meta":{"request_id":"request-t187","ai_trace_id":"ai-trace-t187"}}`)},
		{name: "invalid UTF-8", status: http.StatusOK, header: "request-t187", body: append(t187SuccessfulChatEnvelope("safe"), 0xff)},
		{name: "oversized body", status: http.StatusOK, header: "request-t187", body: oversized},
		{name: "missing data", status: http.StatusOK, header: "request-t187", body: []byte(`{"code":0,"message":"success","meta":{"request_id":"request-t187","ai_trace_id":"ai-trace-t187"}}`)},
		{name: "missing request identity", status: http.StatusOK, body: []byte(`{"code":0,"message":"success","data":{"content":"safe","model":"test-model","finish_reason":"stop","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}},"meta":{"ai_trace_id":"ai-trace-t187"}}`)},
		{name: "response header identity conflict", status: http.StatusOK, header: "foreign-request-t187", body: t187SuccessfulChatEnvelope("safe")},
		{name: "unknown finish reason", status: http.StatusOK, header: "request-t187", body: []byte(`{"code":0,"message":"success","data":{"content":"safe","model":"test-model","finish_reason":"invented","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}},"meta":{"request_id":"request-t187","ai_trace_id":"ai-trace-t187"}}`)},
		{name: "negative usage", status: http.StatusOK, header: "request-t187", body: []byte(`{"code":0,"message":"success","data":{"content":"safe","model":"test-model","finish_reason":"stop","usage":{"input_tokens":-1,"output_tokens":1,"total_tokens":0}},"meta":{"request_id":"request-t187","ai_trace_id":"ai-trace-t187"}}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if tt.name != "missing JSON content type" {
					writer.Header().Set("Content-Type", "application/json")
				}
				if tt.header != "" {
					writer.Header().Set("X-Request-ID", tt.header)
				}
				writer.WriteHeader(tt.status)
				_, _ = writer.Write(tt.body)
			}))
			defer server.Close()
			manifest := &t187ManifestConsumer{}
			writer := &t187ArtifactWriter{}
			_, err := RunPrivacyFixture(context.Background(), t187FixtureRequest(startedAt), PrivacyFixtureDependencies{
				Trigger: t187NewTrigger(t, server.URL, t187Authorization), Manifest: manifest, Writer: writer,
			})
			if err == nil || calls.Load() != 1 || manifest.calls != 0 || writer.calls != 0 {
				t.Fatal("invalid chat envelope must fail after one bounded attempt and before downstream facts")
			}
			assertT187LowSensitiveError(t, err, string(tt.body), server.URL)
		})
	}
}

func TestProtectedPrivacyFixtureTriggerRejectsForbiddenResponseCategoriesBeforeManifest(t *testing.T) {
	startedAt := time.Now().UTC()
	tests := []struct{ name, content string }{
		{name: "synthetic canary", content: t187Canary},
		{name: "independent smoke credential raw value", content: t187Authorization},
		{name: "credential", content: "sk-proj-t187forbiddencredential000000"},
		{name: "authorization", content: "Authorization: Bearer t187-forbidden"},
		{name: "token", content: "token=t187-forbidden-token"},
		{name: "recognized email", content: "privacy-t187@example.com"},
		{name: "recognized phone", content: "+86 13800138000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.Header().Set("X-Request-ID", "request-t187")
				_, _ = writer.Write(t187SuccessfulChatEnvelope(tt.content))
			}))
			defer server.Close()
			manifest := &t187ManifestConsumer{}
			writer := &t187ArtifactWriter{}
			_, err := RunPrivacyFixture(context.Background(), t187FixtureRequest(startedAt), PrivacyFixtureDependencies{
				Trigger: t187NewTrigger(t, server.URL, t187Authorization), Manifest: manifest, Writer: writer,
			})
			if err == nil || manifest.calls != 0 || writer.calls != 0 {
				t.Fatal("forbidden response category must fail before consuming persistent facts")
			}
			assertT187LowSensitiveError(t, err, tt.content, string(t187SuccessfulChatEnvelope(tt.content)), server.URL)
		})
	}
}

// This test is intentionally independent from the not-yet-implemented constructor. It gives
// T187 a runtime RED that names the existing domain flaw: transport proof must be created by the
// concrete capability, not exported as caller-writable fields on a generic DTO.
func TestPrivacyFixtureTransportProofCannotBeCallerReported(t *testing.T) {
	resultType := reflect.TypeOf(PrivacyFixtureTriggerResult{})
	for index := 0; index < resultType.NumField(); index++ {
		field := resultType.Field(index)
		if field.IsExported() {
			t.Fatalf("PrivacyFixtureTriggerResult exposes caller-writable proof/raw field %s", field.Name)
		}
	}
}

// The concrete trigger is a security boundary, not an operational logging surface. Keeping the
// implementation free of logging dependencies makes it impossible to accidentally emit its
// short-lived authorization, canary, endpoint, or raw response before sanitization.
func TestProtectedPrivacyFixtureTriggerHasNoLoggingCapability(t *testing.T) {
	path := filepath.Join("privacy_fixture_trigger.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("protected privacy fixture trigger implementation is missing")
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal("protected trigger source must be valid Go")
	}
	for _, imported := range file.Imports {
		path := strings.Trim(imported.Path.Value, `"`)
		if path == "os" || strings.Contains(path, "log") || strings.Contains(path, "observability") {
			t.Fatalf("protected trigger imports forbidden output capability %q", path)
		}
	}
	forbiddenCalls := map[string]struct{}{
		"print": {}, "println": {}, "Print": {}, "Printf": {}, "Println": {},
		"Fprint": {}, "Fprintf": {}, "Fprintln": {}, "Log": {}, "Logf": {}, "WriteString": {},
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.CallExpr:
			name := ""
			switch function := value.Fun.(type) {
			case *ast.Ident:
				name = function.Name
			case *ast.SelectorExpr:
				name = function.Sel.Name
			}
			if _, forbidden := forbiddenCalls[name]; forbidden {
				t.Errorf("protected trigger calls forbidden output function %q", name)
			}
		case *ast.Field:
			fieldText := strings.ToLower(fmt.Sprint(value.Type))
			for _, name := range value.Names {
				fieldText += strings.ToLower(name.Name)
			}
			if strings.Contains(fieldText, "writer") || strings.Contains(fieldText, "logger") || strings.Contains(fieldText, "callback") {
				t.Errorf("protected trigger exposes forbidden output field")
			}
		}
		return true
	})
}

func t187NewTrigger(t *testing.T, endpoint, authorization string) PrivacyFixtureTrigger {
	t.Helper()
	trigger, err := NewProtectedPrivacyFixtureTrigger(ProtectedPrivacyFixtureTriggerConfig{
		Endpoint: endpoint, MasterSmokeEnabled: true, ChatSmokeEnabled: true,
		Authorization: authorization, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("construct protected trigger failed with class %q", t187ErrorClass(err))
	}
	return trigger
}

func t187FixtureRequest(startedAt time.Time) PrivacyFixtureRequest {
	return PrivacyFixtureRequest{
		RunID: "run-t187-privacy", Marker: t187Marker, Profile: "grafana",
		ForbiddenCanary: t187Canary, StartedAt: startedAt, Deadline: startedAt.Add(time.Minute),
	}
}

func t187SuccessfulChatEnvelope(content string) []byte {
	payload, _ := json.Marshal(map[string]any{
		"code": 0, "message": "success",
		"data": map[string]any{
			"content": content, "model": "test-model", "finish_reason": "stop",
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
		},
		"meta": map[string]any{"request_id": "request-t187", "ai_trace_id": "ai-trace-t187"},
	})
	return payload
}

func validateT187ProtectedChatRequest(request *http.Request, marker, authorization, canary string) error {
	if request.Method != http.MethodPost || request.URL.Path != "/api/v1/chat" || request.URL.RawQuery != "" {
		return errors.New("must use fixed chat POST path")
	}
	if request.Header.Get("Content-Type") != "application/json" || request.Header.Get(v1chat.ChatSmokeRunIDHeader) != marker ||
		request.Header.Get(v1chat.ChatSmokeAuthorizationHeader) != authorization || request.Header.Get("Authorization") != "" {
		return errors.New("must use independent smoke headers")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 1<<16))
	if err != nil {
		return errors.New("request body read failed")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var input v1chat.ChatReq
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.Message != canary {
		return errors.New("must contain only runner canary")
	}
	return nil
}

func assertT187LowSensitiveError(t *testing.T, err error, dynamicForbidden ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a stable low-sensitive error")
	}
	if !errors.Is(err, errPrivacyFixtureFailed) {
		type classified interface{ Class() string }
		var target classified
		if !errors.As(err, &target) || !slices.Contains([]string{
			"invalid_config", "invalid_request", "authentication_failed", "backend_unavailable",
			"backend_timeout", "malformed_response", "unexpected_evidence", "privacy_fixture_failed",
		}, target.Class()) {
			t.Fatal("protected trigger error must expose only a stable allowlisted class")
		}
	}
	text := strings.ToLower(err.Error())
	forbiddenValues := append([]string{
		strings.ToLower(t187Authorization), strings.ToLower(t187Canary), strings.ToLower(t187RawResponse),
		"authorization:", "bearer ", "api/v1/chat", "127.0.0.1", "localhost",
	}, dynamicForbidden...)
	for _, forbidden := range forbiddenValues {
		forbidden = strings.ToLower(forbidden)
		if forbidden == "" {
			continue
		}
		if strings.Contains(text, forbidden) {
			t.Fatal("protected trigger error leaked a forbidden request, endpoint, credential, canary, or raw body fact")
		}
	}
}

type t187RoundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip t187RoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type t187CountingBody struct {
	remaining int
	read      atomic.Int64
	closed    atomic.Bool
}

func (body *t187CountingBody) Read(buffer []byte) (int, error) {
	if body.remaining == 0 {
		return 0, io.EOF
	}
	count := len(buffer)
	if count > body.remaining {
		count = body.remaining
	}
	for index := range count {
		buffer[index] = 'x'
	}
	body.remaining -= count
	body.read.Add(int64(count))
	return count, nil
}

func (body *t187CountingBody) Close() error {
	body.closed.Store(true)
	return nil
}

func assertT187LowSensitiveValue(t *testing.T, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal low-sensitive fixture result: %v", err)
	}
	text := strings.ToLower(string(encoded))
	for _, forbidden := range []string{strings.ToLower(t187Authorization), strings.ToLower(t187Canary), strings.ToLower(t187RawResponse), "authorization", "message", "body", "endpoint"} {
		if strings.Contains(text, forbidden) {
			t.Fatal("fixture DTO leaked a forbidden request, endpoint, credential, canary, or raw body fact")
		}
	}
}

func t187ErrorClass(err error) string {
	if err == nil {
		return ""
	}
	type classified interface{ Class() string }
	var target classified
	if errors.As(err, &target) {
		return target.Class()
	}
	return "privacy_fixture_failed"
}

type t187EventOrder struct {
	mu     sync.Mutex
	events []string
}

func (order *t187EventOrder) add(event string) {
	order.mu.Lock()
	defer order.mu.Unlock()
	order.events = append(order.events, event)
}

func (order *t187EventOrder) snapshot() []string {
	order.mu.Lock()
	defer order.mu.Unlock()
	return append([]string(nil), order.events...)
}

type t187ManifestConsumer struct {
	calls    int
	manifest ChatRunManifestInput
	err      error
	order    *t187EventOrder
}

func (consumer *t187ManifestConsumer) Consume(_ context.Context, marker string) (ChatRunManifestInput, error) {
	consumer.calls++
	if consumer.order != nil {
		consumer.order.add("manifest")
	}
	if marker != t187Marker {
		return ChatRunManifestInput{}, fmt.Errorf("unexpected marker")
	}
	return consumer.manifest, consumer.err
}

type t187ArtifactWriter struct {
	calls int
	refs  PrivacyFixtureArtifactRefs
	err   error
	order *t187EventOrder
}

func (writer *t187ArtifactWriter) Write(_ context.Context, _ PrivacyFixtureArtifactInput) (PrivacyFixtureArtifactRefs, error) {
	writer.calls++
	if writer.order != nil {
		writer.order.add("writer")
	}
	return writer.refs, writer.err
}
