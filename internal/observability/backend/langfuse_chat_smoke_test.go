package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

const langfuseReadCredentialForTest = "cHVibGljOnJlYWQtc2VjcmV0"

// TestLangfuseV3185ChatSmokeQueryUsesNativeFilterAndNestedCorrelation 固定 self-hosted
// Langfuse v3.185 的 observations v1 事实边界：服务端只查询平台可靠支持的原生字段，
// 三个 Longtermism correlation identity 必须从响应的 metadata.attributes 二次核验。
// 这能避免把平台不可靠的 metadata filter 误当成授权边界，也禁止 adapter 猜测旧 schema。
func TestLangfuseV3185ChatSmokeQueryUsesNativeFilterAndNestedCorrelation(t *testing.T) {
	target := chatSmokeTargetForTest()
	observedAt := target.StartedAt.Add(time.Second)
	type recordedRequest struct {
		method        string
		path          string
		authorization string
		query         url.Values
	}
	fixtureBody := langfuseV3185DocumentForTest(t, []map[string]any{
		langfuseV3185ObservationForTest(target, observedAt),
	}, langfuseV3185MetaForTest(1, 100, 1, 1))
	requests := make(chan recordedRequest, 1)
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestCount.Add(1)
		recorded := recordedRequest{
			method:        request.Method,
			path:          request.URL.Path,
			authorization: request.Header.Get("Authorization"),
			query:         request.URL.Query(),
		}
		// 额外请求本身会由原子计数判错；非阻塞采样避免错误实现触发多次查询后
		// 卡住 handler，进而让 Server.Close 永久等待。
		select {
		case requests <- recorded:
		default:
		}
		if _, err := writer.Write([]byte(fixtureBody)); err != nil {
			t.Errorf("write Langfuse fixture: %v", err)
		}
	}))
	defer server.Close()

	client, err := NewLangfuseChatSmokeQueryClient(LangfuseChatSmokeQueryConfig{
		BaseURL: server.URL, Credential: langfuseReadCredentialForTest, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewLangfuseChatSmokeQueryClient() error = %v", err)
	}
	observations, queryErr := client.Query(context.Background(), target)

	var recorded recordedRequest
	select {
	case recorded = <-requests:
	case <-time.After(time.Second):
		t.Fatal("Langfuse query did not reach the loopback fixture")
	}
	if recorded.method != http.MethodGet || recorded.path != "/api/public/observations" {
		t.Errorf("request = %s %s, want read-only observations query", recorded.method, recorded.path)
	}
	if recorded.authorization != "Basic "+langfuseReadCredentialForTest {
		t.Error("query did not use configured read credential")
	}
	if got := recorded.query.Get("limit"); got != "100" {
		t.Errorf("limit = %q, want 100", got)
	}
	if got := recorded.query.Get("page"); got != "1" {
		t.Errorf("page = %q, want 1", got)
	}
	if got, want := recorded.query.Get("fromStartTime"), target.StartedAt.UTC().Format(time.RFC3339Nano); got != want {
		t.Errorf("fromStartTime = %q, want %q", got, want)
	}
	if got, want := recorded.query.Get("toStartTime"), target.Deadline.UTC().Format(time.RFC3339Nano); got != want {
		t.Errorf("toStartTime = %q, want %q", got, want)
	}
	if recorded.query.Get("fields") != "" || recorded.query.Get("cursor") != "" {
		t.Errorf("Langfuse v1 query contains unsupported fields/cursor: %v", recorded.query)
	}
	assertLangfuseV3185NativeChatFilter(t, recorded.query.Get("filter"), target)
	if got := requestCount.Load(); got != 1 {
		t.Errorf("request count = %d, want exactly 1", got)
	}

	if queryErr != nil || len(observations) != 1 {
		t.Errorf("Query() = (%#v, %v), want one observation", observations, queryErr)
		return
	}
	assertExactChatObservation(t, observations[0], target, observedAt)
	assertChatObservationIsLowSensitivity(t, observations[0])
}

// Langfuse 的时间窗口用于异步 evidence convergence：窗口边界上的 observation 是合法
// 事实，尚未收敛时返回的完整空第一页也是正常结果，不能被误判为平台响应损坏。
func TestLangfuseV3185ChatSmokeQueryAcceptsInclusiveWindowAndCompleteEmptyPage(t *testing.T) {
	target := chatSmokeTargetForTest()
	tests := []struct {
		name       string
		data       []map[string]any
		meta       map[string]any
		wantCount  int
		observedAt time.Time
	}{
		{
			name: "observation at lower boundary", data: []map[string]any{
				langfuseV3185ObservationForTest(target, target.StartedAt),
			}, meta: langfuseV3185MetaForTest(1, 100, 1, 1), wantCount: 1, observedAt: target.StartedAt,
		},
		{
			name: "observation at upper boundary", data: []map[string]any{
				langfuseV3185ObservationForTest(target, target.Deadline),
			}, meta: langfuseV3185MetaForTest(1, 100, 1, 1), wantCount: 1, observedAt: target.Deadline,
		},
		{
			name: "complete empty first page", data: []map[string]any{},
			meta: langfuseV3185MetaForTest(1, 100, 0, 0), wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := langfuseV3185DocumentForTest(t, tt.data, tt.meta)
			requests := newLangfuseV3185RequestRecorder()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.record(request)
				if _, err := writer.Write([]byte(body)); err != nil {
					t.Errorf("write Langfuse fixture: %v", err)
				}
			}))
			defer server.Close()
			client, err := NewLangfuseChatSmokeQueryClient(LangfuseChatSmokeQueryConfig{
				BaseURL: server.URL, Credential: langfuseReadCredentialForTest, Timeout: time.Second,
			})
			if err != nil {
				t.Fatalf("constructor error = %v", err)
			}
			observations, queryErr := client.Query(context.Background(), target)
			if queryErr != nil || len(observations) != tt.wantCount {
				t.Errorf("Query() = (%#v, %v), want %d observations", observations, queryErr, tt.wantCount)
			} else if tt.wantCount == 1 {
				assertExactChatObservation(t, observations[0], target, tt.observedAt)
				assertChatObservationIsLowSensitivity(t, observations[0])
			}
			requests.assertSingleV1ObservationsQuery(t)
		})
	}
}

func TestLangfuseV3185ChatSmokeQueryRejectsUntrustedResponseFacts(t *testing.T) {
	target := chatSmokeTargetForTest()
	observedAt := target.StartedAt.Add(time.Second)
	validMeta := func() map[string]any { return langfuseV3185MetaForTest(1, 100, 1, 1) }
	validDocument := func(t *testing.T, mutate func(map[string]any)) string {
		t.Helper()
		observation := langfuseV3185ObservationForTest(target, observedAt)
		mutate(observation)
		return langfuseV3185DocumentForTest(t, []map[string]any{observation}, validMeta())
	}

	tests := []struct {
		name string
		body func(*testing.T) string
	}{
		{
			name: "legacy top-level metadata only",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) {
					observation["metadata"] = langfuseLegacyChatMetadataForTest(target)
				})
			},
		},
		{
			name: "legacy keys cannot rescue foreign nested marker",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) {
					metadata := observation["metadata"].(map[string]any)
					attributes := metadata["attributes"].(map[string]any)
					attributes["longtermism.smoke.run_id"] = "foreign-t204"
					for key, value := range langfuseLegacyChatMetadataForTest(target) {
						metadata[key] = value
					}
				})
			},
		},
		{
			name: "foreign nested request id",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) {
					langfuseV3185AttributesForTest(t, observation)["request.id"] = "foreign-t204"
				})
			},
		},
		{
			name: "foreign nested AI trace id",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) {
					langfuseV3185AttributesForTest(t, observation)["longtermism.ai.trace_id"] = "foreign-t204"
				})
			},
		},
		{
			name: "missing nested marker",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) {
					delete(langfuseV3185AttributesForTest(t, observation), "longtermism.smoke.run_id")
				})
			},
		},
		{
			name: "missing nested request id",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) {
					delete(langfuseV3185AttributesForTest(t, observation), "request.id")
				})
			},
		},
		{
			name: "missing nested AI trace id",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) {
					delete(langfuseV3185AttributesForTest(t, observation), "longtermism.ai.trace_id")
				})
			},
		},
		{
			name: "missing metadata",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) { delete(observation, "metadata") })
			},
		},
		{
			name: "missing attributes",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) {
					delete(observation["metadata"].(map[string]any), "attributes")
				})
			},
		},
		{
			name: "attributes wrong shape",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) {
					observation["metadata"].(map[string]any)["attributes"] = "raw-t204-attributes"
				})
			},
		},
		{
			name: "nested identity is not a string",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) {
					langfuseV3185AttributesForTest(t, observation)["request.id"] = 204
				})
			},
		},
		{
			name: "foreign observation id",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) { observation["id"] = "fedcba9876543210" })
			},
		},
		{
			name: "missing observation id",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) { delete(observation, "id") })
			},
		},
		{
			name: "foreign trace id",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) { observation["traceId"] = strings.Repeat("f", 32) })
			},
		},
		{
			name: "missing trace id",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) { delete(observation, "traceId") })
			},
		},
		{
			name: "missing start time",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) { delete(observation, "startTime") })
			},
		},
		{
			name: "invalid start time",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) { observation["startTime"] = "raw-t204-time" })
			},
		},
		{
			name: "before bounded window",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) {
					observation["startTime"] = target.StartedAt.Add(-time.Nanosecond).Format(time.RFC3339Nano)
				})
			},
		},
		{
			name: "after bounded window",
			body: func(t *testing.T) string {
				return validDocument(t, func(observation map[string]any) {
					observation["startTime"] = target.Deadline.Add(time.Nanosecond).Format(time.RFC3339Nano)
				})
			},
		},
		{
			name: "duplicate exact observations",
			body: func(t *testing.T) string {
				rows := []map[string]any{
					langfuseV3185ObservationForTest(target, observedAt),
					langfuseV3185ObservationForTest(target, observedAt),
				}
				return langfuseV3185DocumentForTest(t, rows, langfuseV3185MetaForTest(1, 100, 2, 1))
			},
		},
		{
			name: "more results than query limit",
			body: func(t *testing.T) string {
				rows := make([]map[string]any, 0, 101)
				for range 101 {
					rows = append(rows, langfuseV3185ObservationForTest(target, observedAt))
				}
				return langfuseV3185DocumentForTest(t, rows, langfuseV3185MetaForTest(1, 100, 101, 2))
			},
		},
		{
			name: "incomplete pagination",
			body: func(t *testing.T) string {
				return langfuseV3185DocumentForTest(t, []map[string]any{
					langfuseV3185ObservationForTest(target, observedAt),
				}, langfuseV3185MetaForTest(1, 100, 2, 2))
			},
		},
		{
			name: "unexpected page",
			body: func(t *testing.T) string {
				return langfuseV3185DocumentForTest(t, []map[string]any{
					langfuseV3185ObservationForTest(target, observedAt),
				}, langfuseV3185MetaForTest(2, 100, 1, 2))
			},
		},
		{
			name: "missing pagination metadata",
			body: func(t *testing.T) string {
				return langfuseV3185DocumentForTest(t, []map[string]any{
					langfuseV3185ObservationForTest(target, observedAt),
				}, nil)
			},
		},
		{
			name: "incomplete pagination metadata",
			body: func(t *testing.T) string {
				meta := langfuseV3185MetaForTest(1, 100, 1, 1)
				delete(meta, "totalItems")
				return langfuseV3185DocumentForTest(t, []map[string]any{
					langfuseV3185ObservationForTest(target, observedAt),
				}, meta)
			},
		},
		{
			name: "pagination limit differs from request",
			body: func(t *testing.T) string {
				return langfuseV3185DocumentForTest(t, []map[string]any{
					langfuseV3185ObservationForTest(target, observedAt),
				}, langfuseV3185MetaForTest(1, 99, 1, 1))
			},
		},
		{
			name: "missing pagination limit",
			body: func(t *testing.T) string {
				meta := langfuseV3185MetaForTest(1, 100, 1, 1)
				delete(meta, "limit")
				return langfuseV3185DocumentForTest(t, []map[string]any{
					langfuseV3185ObservationForTest(target, observedAt),
				}, meta)
			},
		},
		{
			name: "pagination total differs from data",
			body: func(t *testing.T) string {
				return langfuseV3185DocumentForTest(t, []map[string]any{
					langfuseV3185ObservationForTest(target, observedAt),
				}, langfuseV3185MetaForTest(1, 100, 2, 1))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.body(t)
			requests := newLangfuseV3185RequestRecorder()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.record(request)
				if _, err := writer.Write([]byte(body)); err != nil {
					t.Errorf("write Langfuse fixture: %v", err)
				}
			}))
			defer server.Close()
			client, err := NewLangfuseChatSmokeQueryClient(LangfuseChatSmokeQueryConfig{
				BaseURL: server.URL, Credential: langfuseReadCredentialForTest, Timeout: time.Second,
			})
			if err != nil {
				t.Fatalf("constructor error = %v", err)
			}
			observations, queryErr := client.Query(context.Background(), target)
			if observations != nil || errorClass(queryErr) != "malformed_response" {
				t.Errorf("Query() = (%#v, %v) class %q, want nil and malformed_response", observations, queryErr, errorClass(queryErr))
			}
			requests.assertSingleV1ObservationsQuery(t)
			assertLangfuseErrorIsLowSensitivity(t, queryErr, server.URL, target)
		})
	}
}

func TestLangfuseChatSmokeQueryFailsClosed(t *testing.T) {
	target := chatSmokeTargetForTest()
	oversizedDocument := langfuseV3185DocumentForTest(t, nil, langfuseV3185MetaForTest(1, 100, 0, 0)) + strings.Repeat(" ", maximumBackendResponseSize)
	tests := []struct {
		name      string
		status    int
		body      string
		wantClass string
	}{
		{name: "authentication failure", status: http.StatusUnauthorized, body: `raw-t204-auth`, wantClass: "authentication_failed"},
		{name: "upstream failure", status: http.StatusBadGateway, body: `raw-t204-upstream`, wantClass: "backend_unavailable"},
		{name: "malformed response", status: http.StatusOK, body: `{`, wantClass: "malformed_response"},
		{name: "oversized response", status: http.StatusOK, body: oversizedDocument, wantClass: "malformed_response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requests := newLangfuseV3185RequestRecorder()
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.record(request)
				writer.WriteHeader(tt.status)
				if _, err := writer.Write([]byte(tt.body)); err != nil {
					t.Errorf("write Langfuse fixture: %v", err)
				}
			}))
			defer server.Close()
			client, err := NewLangfuseChatSmokeQueryClient(LangfuseChatSmokeQueryConfig{
				BaseURL: server.URL, Credential: langfuseReadCredentialForTest, Timeout: time.Second,
			})
			if err != nil {
				t.Fatalf("constructor error = %v", err)
			}
			observations, queryErr := client.Query(context.Background(), target)
			if observations != nil || errorClass(queryErr) != tt.wantClass {
				t.Errorf("Query() = (%#v, %v) class %q, want nil and %q", observations, queryErr, errorClass(queryErr), tt.wantClass)
			}
			requests.assertSingleV1ObservationsQuery(t)
			assertLangfuseErrorIsLowSensitivity(t, queryErr, server.URL, target)
		})
	}
}

func TestLangfuseChatSmokeQueryRejectsUnsafeConfigurationBeforeNetwork(t *testing.T) {
	tests := []struct {
		name       string
		baseURL    string
		credential string
		wantClass  string
	}{
		{name: "missing credential", baseURL: "http://127.0.0.1:3000", wantClass: "authentication_failed"},
		{name: "remote endpoint", baseURL: "https://example.com:443", credential: "read-secret", wantClass: "backend_unavailable"},
		{name: "userinfo endpoint", baseURL: "http://user:pass@127.0.0.1:3000", credential: "read-secret", wantClass: "backend_unavailable"},
		{name: "path override", baseURL: "http://127.0.0.1:3000/admin", credential: "read-secret", wantClass: "backend_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewLangfuseChatSmokeQueryClient(LangfuseChatSmokeQueryConfig{BaseURL: tt.baseURL, Credential: tt.credential, Timeout: time.Second})
			if client != nil || errorClass(err) != tt.wantClass {
				t.Fatalf("constructor = (%#v, %v) class %q, want nil and %q", client, err, errorClass(err), tt.wantClass)
			}
			for _, forbidden := range []string{tt.baseURL, tt.credential} {
				if forbidden != "" && strings.Contains(fmt.Sprint(err), forbidden) {
					t.Fatalf("constructor error leaked configuration: %v", err)
				}
			}
		})
	}
}

func assertLangfuseV3185NativeChatFilter(t *testing.T, rendered string, target smoke.ChatSmokeTarget) {
	t.Helper()
	var filters []struct {
		Type, Column, Key, Operator, Value string
	}
	if err := json.Unmarshal([]byte(rendered), &filters); err != nil {
		t.Errorf("filter = %q is not structured JSON: %v", rendered, err)
		return
	}
	want := map[string]string{
		"string|traceId||=":      target.ServiceTraceID,
		"string|id||=":           target.SpanID,
		"datetime|startTime||>=": target.StartedAt.UTC().Format(time.RFC3339Nano),
		"datetime|startTime||<=": target.Deadline.UTC().Format(time.RFC3339Nano),
	}
	if len(filters) != len(want) {
		t.Errorf("Langfuse filter = %s, want exactly %d native constraints", rendered, len(want))
	}
	for _, filter := range filters {
		key := strings.Join([]string{filter.Type, filter.Column, filter.Key, filter.Operator}, "|")
		value, ok := want[key]
		if !ok || value != filter.Value {
			t.Errorf("Langfuse filter contains unsupported or conflicting constraint: %+v", filter)
			continue
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Errorf("Langfuse filter missing native constraints %#v", want)
	}
	for _, forbidden := range []string{"metadata", target.Marker, target.RequestID, target.AITraceID} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("Langfuse server filter contains client-only correlation fact %q", forbidden)
		}
	}
}

func langfuseV3185ObservationForTest(target smoke.ChatSmokeTarget, observedAt time.Time) map[string]any {
	return map[string]any{
		"id":        target.SpanID,
		"traceId":   target.ServiceTraceID,
		"startTime": observedAt.UTC().Format(time.RFC3339Nano),
		"type":      "SPAN",
		"name":      "ai.generation",
		"metadata": map[string]any{
			"attributes": map[string]any{
				"longtermism.smoke.run_id":    target.Marker,
				"request.id":                  target.RequestID,
				"longtermism.ai.trace_id":     target.AITraceID,
				"longtermism.raw.canary.t204": "raw-t204-attribute",
			},
		},
		"input":  "raw-t204-input",
		"output": "raw-t204-output",
	}
}

func langfuseLegacyChatMetadataForTest(target smoke.ChatSmokeTarget) map[string]any {
	return map[string]any{
		"longtermism.smoke.run_id": target.Marker,
		"request_id":               target.RequestID,
		"ai_trace_id":              target.AITraceID,
	}
}

func langfuseV3185AttributesForTest(t *testing.T, observation map[string]any) map[string]any {
	t.Helper()
	metadata, ok := observation["metadata"].(map[string]any)
	if !ok {
		t.Fatal("test fixture metadata is not an object")
	}
	attributes, ok := metadata["attributes"].(map[string]any)
	if !ok {
		t.Fatal("test fixture metadata.attributes is not an object")
	}
	return attributes
}

func langfuseV3185MetaForTest(page, limit, totalItems, totalPages int) map[string]any {
	return map[string]any{
		"page": page, "limit": limit, "totalItems": totalItems, "totalPages": totalPages,
	}
}

func langfuseV3185DocumentForTest(t *testing.T, data []map[string]any, meta map[string]any) string {
	t.Helper()
	document := map[string]any{"data": data}
	if meta != nil {
		document["meta"] = meta
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal Langfuse v3.185 fixture: %v", err)
	}
	return string(encoded)
}

func assertLangfuseErrorIsLowSensitivity(t *testing.T, err error, serverURL string, target smoke.ChatSmokeTarget) {
	t.Helper()
	for _, forbidden := range []string{
		"raw-t204", serverURL, langfuseReadCredentialForTest,
		target.Marker, target.RequestID, target.AITraceID, target.ServiceTraceID, target.SpanID,
	} {
		if strings.Contains(fmt.Sprint(err), forbidden) {
			t.Fatalf("stable error leaked %q: %v", forbidden, err)
		}
	}
}

type langfuseV3185RequestRecorder struct {
	count atomic.Int32
	paths chan string
}

func newLangfuseV3185RequestRecorder() *langfuseV3185RequestRecorder {
	return &langfuseV3185RequestRecorder{paths: make(chan string, 1)}
}

func (recorder *langfuseV3185RequestRecorder) record(request *http.Request) {
	recorder.count.Add(1)
	select {
	case recorder.paths <- request.URL.Path:
	default:
	}
}

func (recorder *langfuseV3185RequestRecorder) assertSingleV1ObservationsQuery(t *testing.T) {
	t.Helper()
	if got := recorder.count.Load(); got != 1 {
		t.Errorf("Langfuse request count = %d, want exactly one request without schema fallback", got)
	}
	if recorder.count.Load() == 0 {
		return
	}
	select {
	case path := <-recorder.paths:
		if path != "/api/public/observations" {
			t.Errorf("Langfuse request path = %q, want locked v1 observations endpoint", path)
		}
	default:
		t.Error("Langfuse request path was not recorded")
	}
}
