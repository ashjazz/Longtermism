package backend

import (
	"context"
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

	localeval "github.com/ashjazz/Longtermism/internal/eval"
	"github.com/ashjazz/Longtermism/internal/observability/langfuse"
	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

const (
	t191Canary       = "T191_SYNTHETIC_CANARY"
	t191Credential   = "cHVibGljOnJlYWQtc2VjcmV0"
	t191Raw          = "t191-platform-body-must-not-escape"
	t191Foreign      = "T191_RESPONSE_ONLY_FOREIGN"
	t191ProjectionID = "projection-t191"
	t191EvalRunID    = "eval-t191"
)

type t191RequestLog struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (log *t191RequestLog) append(request *http.Request) {
	clone := request.Clone(request.Context())
	clone.URL = new(url.URL)
	*clone.URL = *request.URL
	log.mu.Lock()
	log.requests = append(log.requests, clone)
	log.mu.Unlock()
}

func (log *t191RequestLog) snapshot() []*http.Request {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]*http.Request(nil), log.requests...)
}

type t191ProjectionStore struct {
	records []localeval.ScoreProjectionSnapshot
	runIDs  []string
}

func (store *t191ProjectionStore) FindByRunID(_ context.Context, runID string) ([]localeval.ScoreProjectionSnapshot, error) {
	store.runIDs = append(store.runIDs, runID)
	return append([]localeval.ScoreProjectionSnapshot(nil), store.records...), nil
}

func t191Request(surface smoke.PrivacySmokeSurface) PrivacyLangfuseScanRequest {
	deadline := time.Now().UTC().Add(-5 * time.Second).Truncate(time.Second)
	return PrivacyLangfuseScanRequest{
		Surface: surface, RunID: "run-t191", Marker: "marker-t191", ForbiddenCanary: t191Canary,
		RequestID: "request-t191", AITraceID: "ai-trace-t191",
		ServiceTraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef",
		StartedAt: deadline.Add(-20 * time.Second), Deadline: deadline, Limit: 100,
	}
}

func t191Snapshot(request PrivacyLangfuseScanRequest) localeval.ScoreProjectionSnapshot {
	return localeval.ScoreProjectionSnapshot{
		RunID: request.Marker, EvalRunID: t191EvalRunID, ProjectionID: t191ProjectionID,
		RequestID: request.RequestID, AITraceID: request.AITraceID, PlatformTraceID: request.ServiceTraceID,
		PlatformObservationID: request.SpanID, Status: langfuse.ScoreProjectionStatusSent, Attempt: 1,
		CreatedAt: request.StartedAt, ObservedAt: request.StartedAt.Add(time.Nanosecond),
	}
}

func t191Surfaces(t *testing.T, request PrivacyLangfuseScanRequest, handler http.Handler, records []localeval.ScoreProjectionSnapshot) (*PrivacyLangfuseSurfaces, *t191RequestLog, *t191ProjectionStore, func()) {
	t.Helper()
	log := &t191RequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		log.append(incoming)
		handler.ServeHTTP(writer, incoming)
	}))
	trace, err := NewLangfuseChatSmokeQueryClient(LangfuseChatSmokeQueryConfig{BaseURL: server.URL, Credential: t191Credential, Timeout: time.Second})
	if err != nil {
		server.Close()
		t.Fatalf("trace client: %v", err)
	}
	store := &t191ProjectionStore{records: records}
	score, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: server.URL, Credential: t191Credential, Timeout: time.Second, ProjectionStore: store})
	if err != nil {
		server.Close()
		t.Fatalf("score client: %v", err)
	}
	surfaces, err := newPrivacyLangfuseSurfacesForTest(trace, score)
	if err != nil {
		server.Close()
		t.Fatalf("privacy constructor: %q", t191ErrorClass(err))
	}
	return surfaces, log, store, server.Close
}

func t191Handler(request PrivacyLangfuseScanRequest, sensitive string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		switch incoming.URL.Path {
		case "/api/public/observations":
			writeT191JSON(writer, t191TraceResponse(request, sensitive, 1))
		case "/api/public/v3/scores":
			writeT191JSON(writer, t191ScoreResponse(request, sensitive, 1))
		default:
			http.NotFound(writer, incoming)
		}
	})
}

func t191TraceResponse(request PrivacyLangfuseScanRequest, sensitive string, count int) map[string]any {
	data := make([]any, 0, count)
	for range count {
		data = append(data, map[string]any{
			"id": request.SpanID, "traceId": request.ServiceTraceID, "startTime": request.StartedAt.Add(time.Second).Format(time.RFC3339Nano),
			"type": "GENERATION", "name": "chat.completion", "input": map[string]any{"safe": sensitive},
			"output": map[string]any{"content": "safe"}, "statusMessage": "", "modelParameters": map[string]any{"temperature": 0},
			"metadata": map[string]any{"longtermism.smoke.run_id": request.Marker, "request_id": request.RequestID, "ai_trace_id": request.AITraceID},
		})
	}
	return map[string]any{"data": data, "meta": map[string]any{"page": 1, "limit": request.Limit, "totalItems": count, "totalPages": 1}}
}

func t191ScoreResponse(request PrivacyLangfuseScanRequest, sensitive string, count int) map[string]any {
	data := make([]any, 0, count)
	for range count {
		data = append(data, map[string]any{
			"id": t191ProjectionID, "projectId": "project-t191", "name": "quality", "value": 1,
			"dataType": "NUMERIC", "source": "API", "timestamp": request.StartedAt.Add(time.Second).Format(time.RFC3339Nano),
			"environment": "default", "createdAt": request.StartedAt.Add(time.Second).Format(time.RFC3339Nano),
			"updatedAt": request.StartedAt.Add(time.Second).Format(time.RFC3339Nano), "comment": sensitive,
			"metadata": map[string]any{"safe": "value"},
			"subject":  map[string]any{"kind": "observation", "id": request.SpanID, "traceId": request.ServiceTraceID},
		})
	}
	return map[string]any{"data": data, "meta": map[string]any{"limit": request.Limit, "cursor": nil}}
}

func writeT191JSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		panic(err)
	}
}

func assertT191Query(t *testing.T, incoming *http.Request, request PrivacyLangfuseScanRequest) {
	t.Helper()
	if incoming.Method != http.MethodGet || incoming.Header.Get("Authorization") != "Basic "+t191Credential {
		t.Fatal("Langfuse privacy query did not use protected read-only authentication")
	}
	if strings.Contains(incoming.URL.String(), t191Credential) || strings.Contains(incoming.URL.RawQuery, request.RunID) || strings.Contains(incoming.URL.RawQuery, request.ForbiddenCanary) {
		t.Fatal("credential, outer privacy run, or canary entered the platform query")
	}
	query := incoming.URL.Query()
	if incoming.URL.Path == "/api/public/observations" {
		if query.Get("page") != "1" || query.Get("limit") != strconv.Itoa(request.Limit) || query.Get("fields") != "" || query.Get("cursor") != "" ||
			query.Get("fromStartTime") != request.StartedAt.Format(time.RFC3339Nano) || query.Get("toStartTime") != request.Deadline.Format(time.RFC3339Nano) {
			t.Fatal("observations v1 query did not retain exact page/window/limit")
		}
		assertT191Filter(t, query.Get("filter"), request)
		return
	}
	want := url.Values{
		"id": {t191ProjectionID}, "traceId": {request.ServiceTraceID}, "observationId": {request.SpanID},
		"fields": {"details,subject"}, "limit": {strconv.Itoa(request.Limit)},
		"fromTimestamp": {request.StartedAt.Format(time.RFC3339Nano)}, "toTimestamp": {request.Deadline.Format(time.RFC3339Nano)},
	}
	if query.Encode() != want.Encode() {
		t.Fatal("scores v3 query did not retain exact identity/window/field groups")
	}
}

func assertT191Filter(t *testing.T, rendered string, request PrivacyLangfuseScanRequest) {
	t.Helper()
	var filters []map[string]string
	if json.Unmarshal([]byte(rendered), &filters) != nil {
		t.Fatal("observations filter is not JSON")
	}
	want := map[string]string{
		"stringObject|metadata|longtermism.smoke.run_id|=": request.Marker,
		"stringObject|metadata|request_id|=":               request.RequestID,
		"stringObject|metadata|ai_trace_id|=":              request.AITraceID,
		"string|traceId||=":                                request.ServiceTraceID, "string|id||=": request.SpanID,
		"datetime|startTime||>=": request.StartedAt.Format(time.RFC3339Nano), "datetime|startTime||<": request.Deadline.Format(time.RFC3339Nano),
	}
	if len(filters) != len(want) {
		t.Fatal("observations filter is not the closed seven-condition query")
	}
	for _, filter := range filters {
		key := strings.Join([]string{filter["type"], filter["column"], filter["key"], filter["operator"]}, "|")
		if want[key] != filter["value"] {
			t.Fatal("observations filter contains a foreign or broad condition")
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatal("observations filter omitted required identity")
	}
}

func assertT191Counts(t *testing.T, got map[string]int, category string) {
	t.Helper()
	want := map[string]int{"synthetic_canary": 0, "credential": 0, "authorization": 0, "token": 0, "recognized_pii": 0}
	if category != "" {
		want[category] = 1
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("counts=%v want=%v", got, want)
	}
}

func t191ErrorClass(err error) string {
	type classified interface{ Class() string }
	if value, ok := err.(classified); ok {
		return value.Class()
	}
	if err == nil {
		return ""
	}
	return err.Error()
}
