package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

// TestGrafanaChatSmokeBackendQueriesExactCorrelationFacts 固定 chat smoke 的查询事实：
// Tempo/Loki 必须在服务端同时约束 runner marker、应用 identity 与原生 OTel identity。
// adapter 只能把低敏 identity 与命中时间投影给 runner，原始 trace/log 文本不得逸出。
func TestGrafanaChatSmokeBackendQueriesExactCorrelationFacts(t *testing.T) {
	target := chatSmokeTargetForTest()
	requests := newChatRequestRecorder()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.add(request)
		switch request.URL.Path {
		case "/api/search":
			// Tempo search summary只保证返回 trace identity 与时间；其余 identity 的事实性
			// 由服务端完整 TraceQL predicate证明，adapter不得臆造未查询的关联字段。
			_, _ = fmt.Fprintf(writer, `{"traces":[{"traceID":"%s","startTimeUnixNano":"%d","rootTraceName":"raw-t178-tempo-body"}]}`, target.ServiceTraceID, target.StartedAt.Add(time.Second).UnixNano())
		case "/loki/api/v1/query_range":
			metadata := lokiChatMetadataForTest(target.Marker, target.RequestID, target.AITraceID, target.ServiceTraceID, "fedcba9876543210")
			_, _ = writer.Write([]byte(lokiChatResponseForTest(target.StartedAt.Add(2*time.Second), metadata)))
		case "/api/v1/query":
			_, _ = fmt.Fprint(writer, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1784541600,"42"]}]}}`)
		default:
			t.Errorf("unexpected backend path %q", request.URL.Path)
		}
	}))
	defer server.Close()

	langfuse := &recordingChatObservationQuery{observations: []smoke.ChatObservation{{
		Marker: target.Marker, RequestID: target.RequestID, AITraceID: target.AITraceID,
		ServiceTraceID: target.ServiceTraceID, SpanID: target.SpanID, ObservedAt: target.StartedAt.Add(3 * time.Second),
	}}}
	grafana, err := NewGrafanaSmokeQueryClient(GrafanaQueryConfig{PrometheusURL: server.URL, LokiURL: server.URL, TempoURL: server.URL, Timeout: time.Second})
	if err != nil {
		t.Fatalf("protected Grafana client: %v", err)
	}
	backend := NewGrafanaChatSmokeBackend(GrafanaChatSmokeBackendConfig{
		Grafana:  grafana,
		Langfuse: langfuse,
	})

	tests := []struct {
		name  string
		query func(context.Context, smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error)
		want  time.Time
	}{
		{name: "Tempo", query: backend.QueryTempoChat, want: target.StartedAt.Add(time.Second)},
		{name: "Loki", query: backend.QueryLokiChat, want: target.StartedAt.Add(2 * time.Second)},
		{name: "Langfuse", query: backend.QueryLangfuseChat, want: target.StartedAt.Add(3 * time.Second)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observations, err := tt.query(context.Background(), target)
			if err != nil || len(observations) != 1 {
				t.Fatalf("query = (%#v, %v), want one observation", observations, err)
			}
			if tt.name == "Loki" {
				want := smoke.ChatObservation{Marker: target.Marker, RequestID: target.RequestID, AITraceID: target.AITraceID, ServiceTraceID: target.ServiceTraceID, SpanID: "fedcba9876543210", ObservedAt: tt.want}
				if observations[0] != want {
					t.Fatalf("Loki observation = %#v, want actual root span %#v", observations[0], want)
				}
			} else {
				assertExactChatObservation(t, observations[0], target, tt.want)
			}
			assertChatObservationIsLowSensitivity(t, observations[0])
		})
	}

	baseline, err := backend.BaselineLLMRequestCount(context.Background())
	if err != nil || baseline != 42 {
		t.Fatalf("BaselineLLMRequestCount() = (%d, %v), want (42, nil)", baseline, err)
	}
	after, err := backend.LLMRequestCount(context.Background())
	if err != nil || after != 42 {
		t.Fatalf("LLMRequestCount() = (%d, %v), want (42, nil)", after, err)
	}

	for _, recorded := range requests.snapshot() {
		assertBoundedChatRequest(t, recorded, target)
	}
	if got := requests.counts(); got["/api/search"] != 1 || got["/loki/api/v1/query_range"] != 1 || got["/api/v1/query"] != 2 {
		t.Fatalf("backend request counts = %#v, want Tempo:1 Loki:1 Prometheus:2", got)
	}
	if len(langfuse.targets) != 1 || langfuse.targets[0] != target {
		t.Fatalf("Langfuse targets = %#v, want exact runner target", langfuse.targets)
	}
	var _ smoke.ChatSmokeBackend = backend
}

// Prometheus 只能证明低基数 LLM counter 增量；把 run/request/trace identity 放进
// metric selector 会制造高基数时序，也会把 smoke 查询变成身份数据出口。
func TestGrafanaChatSmokeBackendUsesOnlyLowCardinalityLLMCounter(t *testing.T) {
	var expressions []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		expressions = append(expressions, request.URL.Query().Get("query"))
		_, _ = fmt.Fprint(writer, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1784541600,"7"]}]}}`)
	}))
	defer server.Close()
	grafana, err := NewGrafanaSmokeQueryClient(GrafanaQueryConfig{PrometheusURL: server.URL, Timeout: time.Second})
	if err != nil {
		t.Fatalf("protected Grafana client: %v", err)
	}
	backend := NewGrafanaChatSmokeBackend(GrafanaChatSmokeBackendConfig{Grafana: grafana, Langfuse: &recordingChatObservationQuery{}})
	_, _ = backend.BaselineLLMRequestCount(context.Background())
	_, _ = backend.LLMRequestCount(context.Background())
	if len(expressions) != 2 {
		t.Fatalf("Prometheus query count = %d, want 2", len(expressions))
	}
	for _, expression := range expressions {
		if expression != "sum(longtermism_llm_request_count_total)" {
			t.Fatalf("Prometheus expression = %q, want fixed aggregate", expression)
		}
		for _, forbidden := range []string{"request_id", "trace_id", "span_id", "ai_trace_id", "smoke_run_id", "marker", "user_id", "session_id", "prompt_hash"} {
			if strings.Contains(expression, forbidden) {
				t.Fatalf("Prometheus expression contains high-cardinality field %q", forbidden)
			}
		}
	}
}

func TestGrafanaChatSmokeBackendRejectsUnsafeTargetsBeforeNetwork(t *testing.T) {
	valid := chatSmokeTargetForTest()
	tests := []struct {
		name   string
		mutate func(smoke.ChatSmokeTarget) smoke.ChatSmokeTarget
	}{
		{name: "missing marker", mutate: func(v smoke.ChatSmokeTarget) smoke.ChatSmokeTarget { v.Marker = ""; return v }},
		{name: "missing request id", mutate: func(v smoke.ChatSmokeTarget) smoke.ChatSmokeTarget { v.RequestID = ""; return v }},
		{name: "missing AI trace id", mutate: func(v smoke.ChatSmokeTarget) smoke.ChatSmokeTarget { v.AITraceID = ""; return v }},
		{name: "missing service trace id", mutate: func(v smoke.ChatSmokeTarget) smoke.ChatSmokeTarget { v.ServiceTraceID = ""; return v }},
		{name: "missing span id", mutate: func(v smoke.ChatSmokeTarget) smoke.ChatSmokeTarget { v.SpanID = ""; return v }},
		{name: "unsafe identity", mutate: func(v smoke.ChatSmokeTarget) smoke.ChatSmokeTarget { v.RequestID = `req-{raw}`; return v }},
		{name: "reversed window", mutate: func(v smoke.ChatSmokeTarget) smoke.ChatSmokeTarget { v.Deadline = v.StartedAt; return v }},
		{name: "oversized window", mutate: func(v smoke.ChatSmokeTarget) smoke.ChatSmokeTarget {
			v.Deadline = v.StartedAt.Add(61 * time.Second)
			return v
		}},
		{name: "zero limit", mutate: func(v smoke.ChatSmokeTarget) smoke.ChatSmokeTarget { v.Limit = 0; return v }},
		{name: "limit above server bound", mutate: func(v smoke.ChatSmokeTarget) smoke.ChatSmokeTarget { v.Limit = 101; return v }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
			defer server.Close()
			langfuse := &recordingChatObservationQuery{}
			grafana, clientErr := NewGrafanaSmokeQueryClient(GrafanaQueryConfig{TempoURL: server.URL, Timeout: time.Second})
			if clientErr != nil {
				t.Fatalf("protected Grafana client: %v", clientErr)
			}
			backend := NewGrafanaChatSmokeBackend(GrafanaChatSmokeBackendConfig{Grafana: grafana, Langfuse: langfuse})
			queries := []func(context.Context, smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error){backend.QueryTempoChat, backend.QueryLokiChat, backend.QueryLangfuseChat}
			for _, query := range queries {
				_, err := query(context.Background(), tt.mutate(valid))
				if errorClass(err) != "invalid_query" {
					t.Fatalf("unsafe query class = %q, want invalid_query", errorClass(err))
				}
			}
			if calls != 0 || len(langfuse.targets) != 0 {
				t.Fatalf("unsafe query sends = grafana:%d langfuse:%d, want zero", calls, len(langfuse.targets))
			}
		})
	}
}

func TestGrafanaChatSmokeBackendRequiresProtectedLoopbackClient(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
	}{
		{name: "remote", endpoint: "https://example.com:443"},
		{name: "userinfo", endpoint: "http://user:pass@127.0.0.1:3000"},
		{name: "path override", endpoint: "http://127.0.0.1:3000/admin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewGrafanaSmokeQueryClient(GrafanaQueryConfig{TempoURL: tt.endpoint, Timeout: time.Second})
			if client != nil || errorClass(err) != "backend_unavailable" {
				t.Fatalf("protected constructor = (%#v,%v), want fail-closed", client, err)
			}
			if strings.Contains(fmt.Sprint(err), tt.endpoint) {
				t.Fatal("protected constructor leaked endpoint")
			}
		})
	}
	ordinary := NewGrafanaQueryClient(GrafanaQueryConfig{TempoURL: "http://127.0.0.1:3000", Timeout: time.Second})
	backend := NewGrafanaChatSmokeBackend(GrafanaChatSmokeBackendConfig{Grafana: ordinary, Langfuse: &recordingChatObservationQuery{}})
	if _, err := backend.QueryTempoChat(context.Background(), chatSmokeTargetForTest()); errorClass(err) != "invalid_query" {
		t.Fatalf("ordinary Grafana client class = %q, want invalid_query", errorClass(err))
	}
}

// 底层 transport 的通用保护并不能替代 chat decoder 的契约：畸形/超大平台文档
// 必须在原始内容进入 runner 之前稳定失败，且错误不能回显查询 identity、URL 或 body。
func TestGrafanaChatSmokeBackendFailuresStayBoundedAndLowSensitivity(t *testing.T) {
	target := chatSmokeTargetForTest()
	tests := []struct {
		name, path, body, wantClass string
		status                      int
		query                       func(*GrafanaChatSmokeBackend) func(context.Context) error
	}{
		{name: "Tempo malformed", path: "/api/search", status: http.StatusOK, body: `{`, wantClass: "malformed_response", query: tempoChatErrorQuery},
		{name: "Tempo oversized", path: "/api/search", status: http.StatusOK, body: strings.Repeat("x", maximumBackendResponseSize+1), wantClass: "malformed_response", query: tempoChatErrorQuery},
		{name: "Loki authentication", path: "/loki/api/v1/query_range", status: http.StatusUnauthorized, body: "raw-t178-loki-auth", wantClass: "authentication_failed", query: lokiChatErrorQuery},
		{name: "Loki upstream", path: "/loki/api/v1/query_range", status: http.StatusBadGateway, body: "raw-t178-loki-upstream", wantClass: "backend_unavailable", query: lokiChatErrorQuery},
		{name: "Prometheus ambiguous series", path: "/api/v1/query", status: http.StatusOK, body: `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]},{"metric":{},"value":[1,"2"]}]}}`, wantClass: "malformed_response", query: prometheusChatErrorQuery},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != tt.path {
					t.Errorf("path = %q, want %q", request.URL.Path, tt.path)
				}
				writer.WriteHeader(tt.status)
				_, _ = writer.Write([]byte(tt.body))
			}))
			defer server.Close()
			grafana, clientErr := NewGrafanaSmokeQueryClient(GrafanaQueryConfig{TempoURL: server.URL, LokiURL: server.URL, PrometheusURL: server.URL, Timeout: time.Second})
			if clientErr != nil {
				t.Fatalf("protected Grafana client: %v", clientErr)
			}
			backend := NewGrafanaChatSmokeBackend(GrafanaChatSmokeBackendConfig{Grafana: grafana, Langfuse: &recordingChatObservationQuery{}})
			err := tt.query(backend)(context.Background())
			if errorClass(err) != tt.wantClass {
				t.Fatalf("error = %v class %q, want %q", err, errorClass(err), tt.wantClass)
			}
			for _, forbidden := range []string{"raw-t178", server.URL, target.Marker, target.RequestID, target.AITraceID, target.ServiceTraceID, target.SpanID} {
				if strings.Contains(fmt.Sprint(err), forbidden) {
					t.Fatalf("stable error leaked %q: %v", forbidden, err)
				}
			}
		})
	}
}

// Tempo summary 只回显 native trace ID，而 Loki entry 可回显完整结构化 identity。
// 两者都必须拒绝 foreign/缺失/窗口外事实，防止“有一行结果就从 target 回填”的假阳性。
func TestGrafanaChatSmokeBackendRejectsConflictingBackendFacts(t *testing.T) {
	target := chatSmokeTargetForTest()
	validLokiLine := lokiChatMetadataForTest(target.Marker, target.RequestID, target.AITraceID, target.ServiceTraceID, "fedcba9876543210")
	tests := []struct {
		name, path, body string
		query            func(*GrafanaChatSmokeBackend) func(context.Context) error
	}{
		{name: "Tempo foreign trace", path: "/api/search", body: fmt.Sprintf(`{"traces":[{"traceID":"ffffffffffffffffffffffffffffffff","startTimeUnixNano":"%d"}]}`, target.StartedAt.Add(time.Second).UnixNano()), query: tempoChatErrorQuery},
		{name: "Tempo missing trace identity", path: "/api/search", body: fmt.Sprintf(`{"traces":[{"startTimeUnixNano":"%d"}]}`, target.StartedAt.Add(time.Second).UnixNano()), query: tempoChatErrorQuery},
		{name: "Tempo ambiguous matches", path: "/api/search", body: fmt.Sprintf(`{"traces":[{"traceID":"%s","startTimeUnixNano":"%d"},{"traceID":"%s","startTimeUnixNano":"%d"}]}`, target.ServiceTraceID, target.StartedAt.Add(time.Second).UnixNano(), target.ServiceTraceID, target.StartedAt.Add(2*time.Second).UnixNano()), query: tempoChatErrorQuery},
		{name: "Tempo outside window", path: "/api/search", body: fmt.Sprintf(`{"traces":[{"traceID":"%s","startTimeUnixNano":"%d"}]}`, target.ServiceTraceID, target.Deadline.Add(time.Second).UnixNano()), query: tempoChatErrorQuery},
		{name: "Loki missing marker", path: "/loki/api/v1/query_range", body: lokiChatResponseForTest(target.StartedAt.Add(time.Second), lokiChatMetadataForTest("", target.RequestID, target.AITraceID, target.ServiceTraceID, target.SpanID)), query: lokiChatErrorQuery},
		{name: "Loki foreign marker", path: "/loki/api/v1/query_range", body: lokiChatResponseForTest(target.StartedAt.Add(time.Second), lokiChatMetadataForTest("foreign-marker-t178", target.RequestID, target.AITraceID, target.ServiceTraceID, target.SpanID)), query: lokiChatErrorQuery},
		{name: "Loki missing request", path: "/loki/api/v1/query_range", body: lokiChatResponseForTest(target.StartedAt.Add(time.Second), lokiChatMetadataForTest(target.Marker, "", target.AITraceID, target.ServiceTraceID, target.SpanID)), query: lokiChatErrorQuery},
		{name: "Loki foreign request", path: "/loki/api/v1/query_range", body: lokiChatResponseForTest(target.StartedAt.Add(time.Second), lokiChatMetadataForTest(target.Marker, "foreign-request-t178", target.AITraceID, target.ServiceTraceID, target.SpanID)), query: lokiChatErrorQuery},
		{name: "Loki missing AI trace", path: "/loki/api/v1/query_range", body: lokiChatResponseForTest(target.StartedAt.Add(time.Second), lokiChatMetadataForTest(target.Marker, target.RequestID, "", target.ServiceTraceID, target.SpanID)), query: lokiChatErrorQuery},
		{name: "Loki foreign AI trace", path: "/loki/api/v1/query_range", body: lokiChatResponseForTest(target.StartedAt.Add(time.Second), lokiChatMetadataForTest(target.Marker, target.RequestID, "foreign-ai-trace-t178", target.ServiceTraceID, target.SpanID)), query: lokiChatErrorQuery},
		{name: "Loki missing service trace", path: "/loki/api/v1/query_range", body: lokiChatResponseForTest(target.StartedAt.Add(time.Second), lokiChatMetadataForTest(target.Marker, target.RequestID, target.AITraceID, "", target.SpanID)), query: lokiChatErrorQuery},
		{name: "Loki foreign service trace", path: "/loki/api/v1/query_range", body: lokiChatResponseForTest(target.StartedAt.Add(time.Second), lokiChatMetadataForTest(target.Marker, target.RequestID, target.AITraceID, "ffffffffffffffffffffffffffffffff", target.SpanID)), query: lokiChatErrorQuery},
		{name: "Loki missing span", path: "/loki/api/v1/query_range", body: lokiChatResponseForTest(target.StartedAt.Add(time.Second), lokiChatMetadataForTest(target.Marker, target.RequestID, target.AITraceID, target.ServiceTraceID, "")), query: lokiChatErrorQuery},
		{name: "Loki malformed span", path: "/loki/api/v1/query_range", body: lokiChatResponseForTest(target.StartedAt.Add(time.Second), lokiChatMetadataForTest(target.Marker, target.RequestID, target.AITraceID, target.ServiceTraceID, "not-a-span")), query: lokiChatErrorQuery},
		{name: "Loki ambiguous matches", path: "/loki/api/v1/query_range", body: fmt.Sprintf(`{"status":"success","data":{"resultType":"streams","result":[{"values":[["%d","http request completed",%s],["%d","http request completed",%s]]}]}}`, target.StartedAt.Add(time.Second).UnixNano(), validLokiLine, target.StartedAt.Add(2*time.Second).UnixNano(), validLokiLine), query: lokiChatErrorQuery},
		{name: "Loki outside window", path: "/loki/api/v1/query_range", body: lokiChatResponseForTest(target.Deadline.Add(time.Second), validLokiLine), query: lokiChatErrorQuery},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != tt.path {
					t.Errorf("path = %q, want %q", request.URL.Path, tt.path)
				}
				_, _ = writer.Write([]byte(tt.body))
			}))
			defer server.Close()
			grafana, clientErr := NewGrafanaSmokeQueryClient(GrafanaQueryConfig{TempoURL: server.URL, LokiURL: server.URL, Timeout: time.Second})
			if clientErr != nil {
				t.Fatalf("protected Grafana client: %v", clientErr)
			}
			backend := NewGrafanaChatSmokeBackend(GrafanaChatSmokeBackendConfig{Grafana: grafana, Langfuse: &recordingChatObservationQuery{}})
			err := tt.query(backend)(context.Background())
			if errorClass(err) != "malformed_response" {
				t.Fatalf("conflicting evidence error = %v class %q, want malformed_response", err, errorClass(err))
			}
			for _, forbidden := range []string{"foreign-", target.Marker, target.RequestID, target.ServiceTraceID} {
				if strings.Contains(fmt.Sprint(err), forbidden) {
					t.Fatalf("conflicting evidence error leaked %q: %v", forbidden, err)
				}
			}
		})
	}
}

func lokiChatResponseForTest(observedAt time.Time, metadata string) string {
	return fmt.Sprintf(`{"status":"success","data":{"resultType":"streams","result":[{"values":[["%d","http request completed",%s]]}]}}`, observedAt.UnixNano(), metadata)
}

func lokiChatMetadataForTest(marker, requestID, aiTraceID, observedTraceID, spanID string) string {
	encoded, _ := json.Marshal(map[string]string{"smoke_run_id": marker, "request_id": requestID, "ai_trace_id": aiTraceID, "trace_id": observedTraceID, "span_id": spanID})
	return string(encoded)
}

func tempoChatErrorQuery(backend *GrafanaChatSmokeBackend) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := backend.QueryTempoChat(ctx, chatSmokeTargetForTest())
		return err
	}
}

func lokiChatErrorQuery(backend *GrafanaChatSmokeBackend) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := backend.QueryLokiChat(ctx, chatSmokeTargetForTest())
		return err
	}
}

func prometheusChatErrorQuery(backend *GrafanaChatSmokeBackend) func(context.Context) error {
	return func(ctx context.Context) error { _, err := backend.LLMRequestCount(ctx); return err }
}

type recordingChatObservationQuery struct {
	targets      []smoke.ChatSmokeTarget
	observations []smoke.ChatObservation
	err          error
}

func (q *recordingChatObservationQuery) Query(_ context.Context, target smoke.ChatSmokeTarget) ([]smoke.ChatObservation, error) {
	q.targets = append(q.targets, target)
	return append([]smoke.ChatObservation(nil), q.observations...), q.err
}

type recordedChatRequest struct {
	path  string
	query url.Values
}

type chatRequestRecorder struct {
	mu       sync.Mutex
	requests []recordedChatRequest
}

func newChatRequestRecorder() *chatRequestRecorder { return &chatRequestRecorder{} }

func (r *chatRequestRecorder) add(request *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, recordedChatRequest{path: request.URL.Path, query: request.URL.Query()})
}

func (r *chatRequestRecorder) snapshot() []recordedChatRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedChatRequest(nil), r.requests...)
}

func (r *chatRequestRecorder) counts() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := make(map[string]int)
	for _, request := range r.requests {
		counts[request.path]++
	}
	return counts
}

func chatSmokeTargetForTest() smoke.ChatSmokeTarget {
	startedAt := time.Now().UTC().Add(-10 * time.Second)
	return smoke.ChatSmokeTarget{Marker: "chat-marker-t178", RequestID: "request-t178", AITraceID: "ai-trace-t178", ServiceTraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef", StartedAt: startedAt, Deadline: startedAt.Add(time.Minute), Limit: 100}
}

func assertBoundedChatRequest(t *testing.T, request recordedChatRequest, target smoke.ChatSmokeTarget) {
	t.Helper()
	if request.path == "/api/v1/query" {
		return
	}
	if request.query.Get("start") == "" || request.query.Get("end") == "" || request.query.Get("limit") != "100" {
		t.Fatalf("%s query = %v, want bounded window and server limit", request.path, request.query)
	}
	expression := request.query.Get("q") + request.query.Get("query")
	identities := []string{target.Marker, target.RequestID, target.AITraceID, target.ServiceTraceID, target.SpanID}
	if request.path == "/loki/api/v1/query_range" {
		// completion log属于 HTTP root span，而 manifest 中 SpanID 是 ai.chat bridge span。
		// 跨 surface 依靠同一 trace 和低敏关联身份，不能伪造“所有 surface 共用一个 span”。
		identities = identities[:4]
	}
	for _, identity := range identities {
		if !strings.Contains(expression, identity) {
			t.Fatalf("%s expression = %q, missing exact identity %q", request.path, expression, identity)
		}
	}
}

func assertExactChatObservation(t *testing.T, observation smoke.ChatObservation, target smoke.ChatSmokeTarget, observedAt time.Time) {
	t.Helper()
	want := smoke.ChatObservation{Marker: target.Marker, RequestID: target.RequestID, AITraceID: target.AITraceID, ServiceTraceID: target.ServiceTraceID, SpanID: target.SpanID, ObservedAt: observedAt}
	if observation != want {
		t.Fatalf("observation = %#v, want %#v", observation, want)
	}
}

func assertChatObservationIsLowSensitivity(t *testing.T, observation smoke.ChatObservation) {
	t.Helper()
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatalf("marshal observation: %v", err)
	}
	for _, forbidden := range []string{"raw-t178", "authorization", "credential", "input", "output", "query", "endpoint"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("observation leaked %q: %s", forbidden, encoded)
		}
	}
}
