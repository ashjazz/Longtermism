package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/gogf/gf/v2/net/ghttp"
)

var expectedOpaqueRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func TestRequestIdentityMiddleware(t *testing.T) {
	tests := []struct {
		name               string
		incomingID         string
		wantStatus         int
		wantPreserved      bool
		wantHandlerCalled  bool
		forbiddenFragments []string
	}{
		{name: "accepts a valid opaque request ID", incomingID: "req-client-01_opaque", wantStatus: http.StatusOK, wantPreserved: true, wantHandlerCalled: true},
		{name: "rejects a request ID with path separators", incomingID: "req/client/01", wantStatus: http.StatusBadRequest},
		{name: "rejects a request ID with whitespace", incomingID: "req client 01", wantStatus: http.StatusBadRequest},
		{name: "rejects a request ID with tab", incomingID: "req\tclient", wantStatus: http.StatusBadRequest},
		{name: "rejects a request ID with header control characters", incomingID: "req-01\r\nX-Request-Identity-Canary: t016-crlf-canary", wantStatus: http.StatusBadRequest, forbiddenFragments: []string{"X-Request-Identity-Canary", "t016-crlf-canary"}},
		{name: "rejects a request ID with a NUL byte", incomingID: "req\x00client", wantStatus: http.StatusBadRequest},
		{name: "rejects a request ID with non ASCII characters", incomingID: "req-你好", wantStatus: http.StatusBadRequest},
		{name: "accepts the 128 byte request ID boundary", incomingID: "r" + strings.Repeat("a", 127), wantStatus: http.StatusOK, wantPreserved: true, wantHandlerCalled: true},
		{name: "rejects the 129 byte request ID boundary", incomingID: "r" + strings.Repeat("a", 128), wantStatus: http.StatusBadRequest},
		{name: "generates an identity when request header is absent", wantStatus: http.StatusOK, wantHandlerCalled: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, handlerCalls := newRequestIdentityProbeServer(t)
			response := serveRequestIdentityProbe(server, tt.incomingID)
			if response.Code != tt.wantStatus {
				t.Fatalf("response status = %d, want %d", response.Code, tt.wantStatus)
			}

			responseID := response.Header().Get(RequestIDHeader)
			if responseID == "" {
				t.Fatal("response did not include X-Request-ID")
			}
			if !expectedOpaqueRequestIDPattern.MatchString(responseID) {
				t.Fatal("response request ID is not an opaque valid identity")
			}
			if tt.wantPreserved && responseID != tt.incomingID {
				t.Fatal("valid incoming request ID was not preserved")
			}
			if !tt.wantPreserved && tt.incomingID != "" && responseID == tt.incomingID {
				t.Fatal("invalid incoming request ID was accepted")
			}

			result := decodeRequestIdentityProbeResult(response)
			if result.err != nil {
				t.Fatal("failed to decode request identity response metadata")
			}
			envelope := requestIdentityEnvelope{Meta: ResponseMeta{RequestID: result.metaID}}
			if envelope.Meta.RequestID != responseID {
				t.Fatal("response meta request ID did not match X-Request-ID")
			}
			if !tt.wantPreserved && tt.incomingID != "" {
				assertRequestIdentityInputWasNotReflected(t, response, result.body, append([]string{tt.incomingID}, tt.forbiddenFragments...))
			}
			if got := handlerCalls(); got != tt.wantHandlerCalled {
				t.Fatalf("handler called = %v, want %v", got, tt.wantHandlerCalled)
			}
		})
	}
}

func TestRequestIdentityMiddlewareDoesNotShareIdentityAcrossConcurrentRequests(t *testing.T) {
	const requestCount = 16

	started := make(chan struct{}, requestCount)
	release := make(chan struct{})
	server, _ := newRequestIdentityProbeServerWithGate(t, started, release)
	results := make(chan requestIdentityProbeResult, requestCount)
	var waitGroup sync.WaitGroup
	for index := 0; index < requestCount; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			requestID := fmt.Sprintf("req-concurrent-%02d", index)
			result := decodeRequestIdentityProbeResult(serveRequestIdentityProbe(server, requestID))
			result.expectedID = requestID
			results <- result
		}(index)
	}
	for index := 0; index < requestCount; index++ {
		<-started
	}
	close(release)
	waitGroup.Wait()
	close(results)

	seen := make(map[string]struct{}, requestCount)
	for result := range results {
		if result.err != nil || result.headerID != result.expectedID || result.metaID != result.expectedID {
			t.Fatal("a concurrent request did not retain its own header/meta identity")
		}
		if _, duplicated := seen[result.headerID]; duplicated {
			t.Fatal("concurrent requests shared a generated request identity")
		}
		seen[result.headerID] = struct{}{}
	}
	if len(seen) != requestCount {
		t.Fatal("concurrent requests did not each receive an identity")
	}
}

func newRequestIdentityProbeServer(t *testing.T) (*ghttp.Server, func() bool) {
	return newRequestIdentityProbeServerWithGate(t, nil, nil)
}

func newRequestIdentityProbeServerWithGate(t *testing.T, started chan<- struct{}, release <-chan struct{}) (*ghttp.Server, func() bool) {
	t.Helper()
	server := ghttp.GetServer("t016-" + strings.ReplaceAll(t.Name(), "/", "-"))
	server.SetDumpRouterMap(false)

	var (
		mu     sync.Mutex
		called bool
	)
	server.Use(RequestIdentityMiddleware)
	server.BindHandler("/identity", func(request *ghttp.Request) {
		mu.Lock()
		called = true
		mu.Unlock()
		if started != nil {
			started <- struct{}{}
			<-release
		}
		request.Response.WriteJsonExit(requestIdentityEnvelope{Meta: NewResponseMeta(request.GetCtx())})
	})
	return server, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return called
	}
}

func serveRequestIdentityProbe(server *ghttp.Server, incomingID string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/identity", nil)
	if incomingID != "" {
		request.Header.Set(RequestIDHeader, incomingID)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

type requestIdentityEnvelope struct {
	Meta ResponseMeta `json:"meta"`
}

type requestIdentityProbeResult struct {
	headerID   string
	metaID     string
	expectedID string
	body       string
	err        error
}

func decodeRequestIdentityProbeResult(response *httptest.ResponseRecorder) requestIdentityProbeResult {
	result := requestIdentityProbeResult{headerID: response.Header().Get(RequestIDHeader), body: response.Body.String()}
	var envelope requestIdentityEnvelope
	if err := json.NewDecoder(strings.NewReader(result.body)).Decode(&envelope); err != nil {
		result.err = err
		return result
	}
	result.metaID = envelope.Meta.RequestID
	return result
}

func assertRequestIdentityInputWasNotReflected(t *testing.T, response *httptest.ResponseRecorder, body string, forbiddenFragments []string) {
	t.Helper()
	for _, fragment := range forbiddenFragments {
		if strings.Contains(body, fragment) {
			t.Fatal("rejected request identity was reflected in the response body")
		}
		for key, values := range response.Header() {
			if strings.Contains(key, fragment) {
				t.Fatal("rejected request identity was reflected in a response header name")
			}
			for _, value := range values {
				if strings.Contains(value, fragment) {
					t.Fatal("rejected request identity was reflected in a response header value")
				}
			}
		}
	}
}
