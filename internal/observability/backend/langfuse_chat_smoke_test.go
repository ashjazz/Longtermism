package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

// TestLangfuseChatSmokeQueryUsesCompleteStructuredIdentity 固定 Langfuse 的最小权限
// 查询协议。完整 identity 与窗口必须在同一个结构化 filter 中，避免先宽查再由 runner
// 猜测关联；响应只投影 runner 所需 DTO，不保留 prompt、output 或平台原始文档。
func TestLangfuseChatSmokeQueryUsesCompleteStructuredIdentity(t *testing.T) {
	target := chatSmokeTargetForTest()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/public/v2/observations" {
			t.Errorf("request = %s %s, want read-only observations query", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Basic cHVibGljOnJlYWQtc2VjcmV0" {
			t.Error("query did not use configured read credential")
		}
		query := request.URL.Query()
		if query.Get("fields") != "core" || query.Get("limit") != "100" || query.Get("cursor") != "" || query.Get("expandMetadata") != "" {
			t.Errorf("Langfuse query = %v, want bounded core-only first page", query)
		}
		assertLangfuseChatFilter(t, query.Get("filter"), target)
		_, _ = fmt.Fprintf(writer, `{"data":[{"id":"platform-observation-t178","traceId":"platform-trace-t178","startTime":"%s","metadata":{"longtermism.smoke.run_id":"%s","request_id":"%s","ai_trace_id":"%s","service_trace_id":"%s","span_id":"%s"},"input":"raw-t178-input","output":"raw-t178-output"}],"meta":{"cursor":null}}`, target.StartedAt.Add(time.Second).Format(time.RFC3339Nano), target.Marker, target.RequestID, target.AITraceID, target.ServiceTraceID, target.SpanID)
	}))
	defer server.Close()

	client, err := NewLangfuseChatSmokeQueryClient(LangfuseChatSmokeQueryConfig{BaseURL: server.URL, Credential: "cHVibGljOnJlYWQtc2VjcmV0", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewLangfuseChatSmokeQueryClient() error = %v", err)
	}
	observations, err := client.Query(context.Background(), target)
	if err != nil || len(observations) != 1 {
		t.Fatalf("Query() = (%#v, %v), want one observation", observations, err)
	}
	assertExactChatObservation(t, observations[0], target, target.StartedAt.Add(time.Second))
	assertChatObservationIsLowSensitivity(t, observations[0])
}

func TestLangfuseChatSmokeQueryFailsClosed(t *testing.T) {
	target := chatSmokeTargetForTest()
	tests := []struct {
		name      string
		status    int
		body      string
		wantClass string
	}{
		{name: "authentication failure", status: http.StatusUnauthorized, body: `raw-t178-auth`, wantClass: "authentication_failed"},
		{name: "upstream failure", status: http.StatusBadGateway, body: `raw-t178-upstream`, wantClass: "backend_unavailable"},
		{name: "malformed response", status: http.StatusOK, body: `{`, wantClass: "malformed_response"},
		{name: "oversized response", status: http.StatusOK, body: strings.Repeat("x", maximumBackendResponseSize+1), wantClass: "malformed_response"},
		{name: "missing response identity", status: http.StatusOK, body: `{"data":[{"startTime":"2026-01-01T00:00:00Z","metadata":{}}]}`, wantClass: "malformed_response"},
		{name: "conflicting response identity", status: http.StatusOK, body: `{"data":[{"startTime":"2026-01-01T00:00:00Z","metadata":{"longtermism.smoke.run_id":"foreign-t178","request_id":"request-t178","ai_trace_id":"ai-trace-t178","service_trace_id":"0123456789abcdef0123456789abcdef","span_id":"0123456789abcdef"}}]}`, wantClass: "malformed_response"},
		{name: "truncated page", status: http.StatusOK, body: `{"data":[],"meta":{"cursor":"next-page-t178"}}`, wantClass: "malformed_response"},
		{name: "too many results", status: http.StatusOK, body: langfuseChatResultsForTest(101, target), wantClass: "malformed_response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(tt.status)
				_, _ = writer.Write([]byte(tt.body))
			}))
			defer server.Close()
			client, err := NewLangfuseChatSmokeQueryClient(LangfuseChatSmokeQueryConfig{BaseURL: server.URL, Credential: "cHVibGljOnJlYWQtc2VjcmV0", Timeout: time.Second})
			if err != nil {
				t.Fatalf("constructor error = %v", err)
			}
			observations, err := client.Query(context.Background(), target)
			if observations != nil || errorClass(err) != tt.wantClass {
				t.Fatalf("Query() = (%#v, %v) class %q, want nil and %q", observations, err, errorClass(err), tt.wantClass)
			}
			for _, forbidden := range []string{"raw-t178", server.URL, "cHVibGljOnJlYWQ"} {
				if strings.Contains(fmt.Sprint(err), forbidden) {
					t.Fatalf("stable error leaked %q: %v", forbidden, err)
				}
			}
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

func assertLangfuseChatFilter(t *testing.T, rendered string, target smoke.ChatSmokeTarget) {
	t.Helper()
	var filters []struct {
		Type, Column, Key, Operator, Value string
	}
	if err := json.Unmarshal([]byte(rendered), &filters); err != nil {
		t.Fatalf("filter = %q is not structured JSON: %v", rendered, err)
	}
	want := map[string]string{
		"stringObject|metadata|longtermism.smoke.run_id|=": target.Marker,
		"stringObject|metadata|request_id|=":               target.RequestID,
		"stringObject|metadata|ai_trace_id|=":              target.AITraceID,
		"stringObject|metadata|service_trace_id|=":         target.ServiceTraceID,
		"stringObject|metadata|span_id|=":                  target.SpanID,
		"datetime|startTime||>=":                           target.StartedAt.UTC().Format(time.RFC3339Nano),
		"datetime|startTime||<=":                           target.Deadline.UTC().Format(time.RFC3339Nano),
	}
	if len(filters) != len(want) {
		t.Fatalf("Langfuse filter = %s, want exactly %d constraints", rendered, len(want))
	}
	for _, filter := range filters {
		key := strings.Join([]string{filter.Type, filter.Column, filter.Key, filter.Operator}, "|")
		if value, ok := want[key]; !ok || value != filter.Value {
			t.Fatalf("Langfuse filter contains an extra or conflicting constraint: %s", rendered)
		} else {
			delete(want, key)
		}
	}
	if len(want) != 0 {
		t.Fatalf("Langfuse filter missing constraints %#v", want)
	}
}

func langfuseChatResultsForTest(count int, target smoke.ChatSmokeTarget) string {
	data := make([]map[string]any, 0, count)
	for index := 0; index < count; index++ {
		data = append(data, map[string]any{
			"startTime": target.StartedAt.Add(time.Second).Format(time.RFC3339Nano),
			"metadata":  map[string]string{"longtermism.smoke.run_id": target.Marker, "request_id": target.RequestID, "ai_trace_id": target.AITraceID, "service_trace_id": target.ServiceTraceID, "span_id": target.SpanID},
		})
	}
	encoded, _ := json.Marshal(map[string]any{"data": data, "meta": map[string]any{"cursor": nil}})
	return string(encoded)
}
