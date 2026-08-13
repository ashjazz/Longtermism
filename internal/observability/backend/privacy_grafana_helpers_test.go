package backend

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

const (
	t190Canary = "T190_SYNTHETIC_CANARY"
	t190Raw    = "t190-provider-body-must-not-escape"
)

type t190RequestLog struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (log *t190RequestLog) append(request *http.Request) {
	clone := request.Clone(request.Context())
	clone.URL = new(url.URL)
	*clone.URL = *request.URL
	log.mu.Lock()
	log.requests = append(log.requests, clone)
	log.mu.Unlock()
}

func (log *t190RequestLog) snapshot() []*http.Request {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]*http.Request(nil), log.requests...)
}

func t190Request(surface smoke.PrivacySmokeSurface) PrivacyGrafanaScanRequest {
	deadline := time.Now().UTC().Add(-5 * time.Second).Truncate(time.Second)
	return PrivacyGrafanaScanRequest{
		Surface: surface, RunID: "run-t190", Marker: "marker-t190", ForbiddenCanary: t190Canary,
		RequestID: "request-t190", AITraceID: "ai-trace-t190",
		ServiceTraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef",
		StartedAt: deadline.Add(-20 * time.Second), Deadline: deadline, Limit: 100,
	}
}

func t190ProtectedClient(t *testing.T, handler http.Handler) (*GrafanaQueryClient, *t190RequestLog, func()) {
	t.Helper()
	log := &t190RequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		log.append(request)
		handler.ServeHTTP(writer, request)
	}))
	client, err := NewGrafanaSmokeQueryClient(GrafanaQueryConfig{
		LokiURL: server.URL, TempoURL: server.URL, Timeout: time.Second,
	})
	if err != nil {
		server.Close()
		t.Fatalf("create protected Grafana client: %v", err)
	}
	return client, log, server.Close
}

func t190Surfaces(t *testing.T, handler http.Handler) (*PrivacyGrafanaSurfaces, *t190RequestLog, func()) {
	t.Helper()
	client, log, closeServer := t190ProtectedClient(t, handler)
	surfaces, err := NewPrivacyGrafanaSurfaces(client)
	if err != nil {
		closeServer()
		t.Fatalf("create privacy Grafana surfaces: %q", t190ErrorClass(err))
	}
	return surfaces, log, closeServer
}

func t190Handler(request PrivacyGrafanaScanRequest, sensitive string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		switch {
		case incoming.URL.Path == "/api/search":
			writeT190JSON(writer, t190TempoSearch(request, 1))
		case incoming.URL.Path == "/api/v2/traces/"+request.ServiceTraceID:
			writeT190JSON(writer, t190TempoDocument(request, sensitive))
		case incoming.URL.Path == "/loki/api/v1/query_range":
			writeT190JSON(writer, t190LokiResponse(request, sensitive, 1))
		default:
			http.NotFound(writer, incoming)
		}
	})
}

func t190TempoSearch(request PrivacyGrafanaScanRequest, count int) map[string]any {
	traces := make([]any, 0, count)
	for range count {
		traces = append(traces, map[string]any{
			"traceID": request.ServiceTraceID, "startTimeUnixNano": strconv.FormatInt(request.StartedAt.Add(time.Second).UnixNano(), 10),
		})
	}
	return map[string]any{"traces": traces, "metrics": map[string]any{"inspectedTraces": count, "completedJobs": 1, "totalJobs": 1}}
}

func t190TempoDocument(request PrivacyGrafanaScanRequest, sensitive string) map[string]any {
	return map[string]any{"status": "COMPLETE", "message": "", "trace": map[string]any{"batches": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{
			t190OTLPAttribute("service.name", "longtermism"), t190OTLPAttribute("longtermism.smoke.run_id", request.Marker),
			t190OTLPAttribute("request.id", request.RequestID), t190OTLPAttribute("longtermism.ai.trace_id", request.AITraceID),
			t190OTLPAttribute("privacy.test.value", sensitive),
		}},
		"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{
			"traceId": t190OTLPID(request.ServiceTraceID), "spanId": t190OTLPID(request.SpanID), "name": "chat.completion",
			"startTimeUnixNano": strconv.FormatInt(request.StartedAt.Add(time.Second).UnixNano(), 10),
			"endTimeUnixNano":   strconv.FormatInt(request.StartedAt.Add(2*time.Second).UnixNano(), 10),
			"attributes":        []any{t190OTLPAttribute("privacy.span.value", sensitive)},
		}}},
		}}}}}
}

func t190LokiResponse(request PrivacyGrafanaScanRequest, sensitive string, count int) map[string]any {
	values := make([]any, 0, count)
	for index := range count {
		metadata := map[string]string{
			"smoke_run_id": request.Marker, "request_id": request.RequestID, "ai_trace_id": request.AITraceID,
			"trace_id": request.ServiceTraceID, "span_id": request.SpanID, "privacy_test_value": sensitive,
		}
		values = append(values, []any{strconv.FormatInt(request.StartedAt.Add(time.Duration(index+1)*time.Second).UnixNano(), 10), "http request completed", metadata})
	}
	return map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{map[string]any{"stream": map[string]string{"service_name": "longtermism"}, "values": values}}}}
}

func t190OTLPAttribute(key, value string) map[string]any {
	return map[string]any{"key": key, "value": map[string]any{"stringValue": value}}
}

func t190OTLPID(value string) string {
	decoded, err := hex.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(decoded)
}

func writeT190JSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		panic(err)
	}
}

func assertT190Query(t *testing.T, request *http.Request, target PrivacyGrafanaScanRequest) {
	t.Helper()
	if request.Method != http.MethodGet {
		t.Fatalf("method = %q, want GET", request.Method)
	}
	queryParameter := "q"
	if request.URL.Path == "/loki/api/v1/query_range" {
		queryParameter = "query"
	}
	query := request.URL.Query().Get(queryParameter)
	wantQuery := fmt.Sprintf(`{ span."longtermism.smoke.run_id" = %q && span."request.id" = %q && span."longtermism.ai.trace_id" = %q && trace:id = %q && span:id = %q }`, target.Marker, target.RequestID, target.AITraceID, target.ServiceTraceID, target.SpanID)
	if request.URL.Path == "/loki/api/v1/query_range" {
		wantQuery = fmt.Sprintf(`{service_name="longtermism"} | smoke_run_id = %q | request_id = %q | ai_trace_id = %q | trace_id = %q | span_id = %q`, target.Marker, target.RequestID, target.AITraceID, target.ServiceTraceID, target.SpanID)
	}
	if query != wantQuery {
		t.Fatal("query was not the fixed exact identity expression")
	}
	if strings.Contains(query, target.ForbiddenCanary) || request.URL.Query().Get("limit") != "100" {
		t.Fatal("query leaked canary or did not retain the bounded limit")
	}
	if request.URL.Path == "/loki/api/v1/query_range" &&
		(request.URL.Query().Get("start") != target.StartedAt.UTC().Format(time.RFC3339Nano) ||
			request.URL.Query().Get("end") != target.Deadline.UTC().Format(time.RFC3339Nano)) {
		t.Fatal("Loki query did not preserve the exact fixture window")
	}
	if request.URL.Path == "/api/search" &&
		(request.URL.Query().Get("start") != strconv.FormatInt(target.StartedAt.Unix(), 10) ||
			request.URL.Query().Get("end") != strconv.FormatInt(target.Deadline.Unix(), 10)) {
		t.Fatal("Tempo search did not preserve the exact fixture window")
	}
}

func assertT190Counts(t *testing.T, got map[string]int, category string) {
	t.Helper()
	want := map[string]int{"synthetic_canary": 0, "credential": 0, "authorization": 0, "token": 0, "recognized_pii": 0}
	if category != "" {
		want[category] = 1
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("counts = %v, want %v", got, want)
	}
}

func t190ErrorClass(err error) string {
	type classified interface{ Class() string }
	if value, ok := err.(classified); ok {
		return value.Class()
	}
	if err == nil {
		return ""
	}
	return err.Error()
}
