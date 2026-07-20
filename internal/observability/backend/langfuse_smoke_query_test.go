package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

// TestLangfuseSmokeQueryBindsTheExactMarker fixes the one real Langfuse protocol used by the
// infra negative check. `filter` takes precedence in Langfuse v2, so marker and both time bounds
// must be in the same filter rather than leaving a caller-provided historical query path.
func TestLangfuseSmokeQueryBindsTheExactMarker(t *testing.T) {
	startedAt := time.Now().UTC().Add(-10 * time.Second)
	target := smoke.PollMarkerTarget{Marker: "infra-t065b-marker", StartedAt: startedAt, Deadline: startedAt.Add(time.Minute)}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/public/v2/observations" {
			t.Errorf("request = %s %s, want GET Langfuse observations v2", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Basic cHVibGljOnNlY3JldA==" {
			t.Errorf("Authorization = %q, want only the configured read credential", got)
		}
		query := request.URL.Query()
		if query.Get("fields") != "core" || query.Get("limit") != "1" || query.Get("cursor") != "" || query.Get("expandMetadata") != "" {
			t.Errorf("Langfuse query = %v, want bounded core-only first page", query)
		}
		assertLangfuseSmokeFilter(t, query.Get("filter"), target)
		_, _ = fmt.Fprint(writer, `{"data":[{"id":"raw-t065b-platform-id","traceId":"raw-t065b-trace-id","input":"raw-t065b-input"}],"meta":{"cursor":null}}`)
	}))
	defer server.Close()

	client, err := NewLangfuseSmokeQueryClient(LangfuseSmokeQueryConfig{BaseURL: server.URL, Credential: "cHVibGljOnNlY3JldA==", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewLangfuseSmokeQueryClient() error = %v", err)
	}
	count, err := client.Query(context.Background(), target)
	if err != nil || count != 1 {
		t.Fatalf("Query() = count:%d error:%v, want 1:nil", count, err)
	}
	if strings.Contains(fmt.Sprint(count), "raw-t065b") {
		t.Fatal("negative evidence leaked a raw Langfuse observation")
	}
}

func TestNegativeSmokeQueryFailsClosed(t *testing.T) {
	target := smoke.PollMarkerTarget{Marker: "infra-t065b-marker", StartedAt: time.Now().UTC(), Deadline: time.Now().UTC().Add(time.Minute)}
	tests := []struct {
		name      string
		port      string
		status    int
		body      string
		block     bool
		config    negativeSmokeQueryTestConfig
		wantCount int
		wantClass string
	}{
		{name: "Langfuse empty result", port: "langfuse", status: http.StatusOK, body: `{"data":[],"meta":{"cursor":null}}`, wantCount: 0},
		{name: "AI plane empty result", port: "ai-plane", status: http.StatusOK, body: `{"data":{"count":0,"raw":"raw-t065b-ai"}}`, wantCount: 0},
		{name: "Langfuse authentication failure", port: "langfuse", status: http.StatusUnauthorized, body: `raw-t065b-auth`, wantClass: "authentication_failed"},
		{name: "Langfuse forbidden", port: "langfuse", status: http.StatusForbidden, body: `raw-t065b-forbidden`, wantClass: "authentication_failed"},
		{name: "AI plane forbidden", port: "ai-plane", status: http.StatusForbidden, body: `raw-t065b-forbidden`, wantClass: "authentication_failed"},
		{name: "AI plane rate limited", port: "ai-plane", status: http.StatusTooManyRequests, body: `raw-t065b-rate-limit`, wantClass: "backend_unavailable"},
		{name: "Langfuse malformed response", port: "langfuse", status: http.StatusOK, body: `{`, wantClass: "malformed_response"},
		{name: "AI plane negative count", port: "ai-plane", status: http.StatusOK, body: `{"data":{"count":-1}}`, wantClass: "malformed_response"},
		{name: "AI plane exact marker match", port: "ai-plane", status: http.StatusOK, body: `{"data":{"count":1}}`, wantCount: 1},
		{name: "Langfuse caller deadline", port: "langfuse", status: http.StatusOK, block: true, wantClass: "backend_timeout"},
		{name: "missing credential fails before request", port: "langfuse", config: negativeSmokeQueryTestConfig{skipDefaultCredential: true}, wantClass: "authentication_failed"},
		{name: "unsafe endpoint fails before request", port: "ai-plane", config: negativeSmokeQueryTestConfig{BaseURL: "https://example.com"}, wantClass: "backend_unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls++
				assertNegativeSmokeQueryRequest(t, tt.port, request, target)
				if tt.block {
					<-request.Context().Done()
					return
				}
				writer.WriteHeader(tt.status)
				_, _ = writer.Write([]byte(tt.body))
			}))
			defer server.Close()

			config := tt.config.withDefaults(server.URL)
			query, err := newNegativeSmokeQueryForTest(tt.port, config)
			if tt.wantClass != "" && (tt.config.skipDefaultCredential || tt.config.BaseURL != "") {
				if err == nil || errorClass(err) != tt.wantClass || calls != 0 {
					t.Fatalf("constructor = error:%v class:%q calls:%d, want %q and no request", err, errorClass(err), calls, tt.wantClass)
				}
				return
			}
			if err != nil {
				t.Fatalf("constructor error = %v", err)
			}
			ctx := context.Background()
			if tt.block {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			count, err := query(ctx, target)
			if tt.wantClass == "" {
				if err != nil || count != tt.wantCount {
					t.Fatalf("Query() = count:%d error:%v, want %d:nil", count, err, tt.wantCount)
				}
				return
			}
			if count != 0 || errorClass(err) != tt.wantClass {
				t.Fatalf("Query() = count:%d error:%v class:%q, want 0 and %q", count, err, errorClass(err), tt.wantClass)
			}
			for _, forbidden := range []string{"raw-t065b", server.URL, "cHVibGlj"} {
				if strings.Contains(fmt.Sprint(err), forbidden) {
					t.Fatalf("stable error leaked %q: %v", forbidden, err)
				}
			}
		})
	}
}

// TestNegativeSmokeQueryRejectsUnsafeEndpointsBeforeNetwork fixes the local-only diagnostic
// boundary. In particular, a URL must not smuggle credentials, a path/query override, or a DNS
// name that resolves outside loopback into an otherwise read-only smoke command.
func TestNegativeSmokeQueryRejectsUnsafeEndpointsBeforeNetwork(t *testing.T) {
	loopback := func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	remote := func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("203.0.113.9")}, nil }
	tests := []struct {
		name     string
		baseURL  string
		resolver func(context.Context, string) ([]net.IP, error)
	}{
		{name: "remote hostname", baseURL: "http://example.com:3100", resolver: remote},
		{name: "private network literal", baseURL: "http://10.0.0.1:3100", resolver: loopback},
		{name: "userinfo", baseURL: "http://user:pass@127.0.0.1:3100", resolver: loopback},
		{name: "query override", baseURL: "http://127.0.0.1:3100?marker=raw-t065b", resolver: loopback},
		{name: "fragment override", baseURL: "http://127.0.0.1:3100#raw-t065b", resolver: loopback},
		{name: "path override", baseURL: "http://127.0.0.1:3100/other", resolver: loopback},
		{name: "unsupported scheme", baseURL: "ftp://127.0.0.1:3100", resolver: loopback},
	}
	for _, port := range []string{"langfuse", "ai-plane"} {
		for _, tt := range tests {
			t.Run(port+"/"+tt.name, func(t *testing.T) {
				_, err := newNegativeSmokeQueryForTest(port, negativeSmokeQueryTestConfig{BaseURL: tt.baseURL, Credential: "cHVibGljOnNlY3JldA==", ResolveHost: tt.resolver})
				if errorClass(err) != "backend_unavailable" {
					t.Fatalf("newNegativeSmokeQueryForTest() error = %v, class = %q, want backend_unavailable", err, errorClass(err))
				}
			})
		}
	}
}

func TestNegativeSmokeQueryRejectsUnsafeTargetsBeforeNetwork(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name   string
		target smoke.PollMarkerTarget
	}{
		{name: "short marker", target: smoke.PollMarkerTarget{Marker: "short", StartedAt: now, Deadline: now.Add(time.Minute)}},
		{name: "reversed window", target: smoke.PollMarkerTarget{Marker: "infra-t065b-marker", StartedAt: now, Deadline: now.Add(-time.Second)}},
		{name: "oversized window", target: smoke.PollMarkerTarget{Marker: "infra-t065b-marker", StartedAt: now, Deadline: now.Add(61 * time.Second)}},
	}
	for _, port := range []string{"langfuse", "ai-plane"} {
		for _, tt := range tests {
			t.Run(port+"/"+tt.name, func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
				defer server.Close()
				query, err := newNegativeSmokeQueryForTest(port, negativeSmokeQueryTestConfig{BaseURL: server.URL, Credential: "cHVibGljOnNlY3JldA=="})
				if err != nil {
					t.Fatalf("constructor error = %v", err)
				}
				_, err = query(context.Background(), tt.target)
				if errorClass(err) != "invalid_query" || calls != 0 {
					t.Fatalf("Query() error = %v class:%q calls:%d, want invalid_query and no request", err, errorClass(err), calls)
				}
			})
		}
	}
}

type negativeSmokeQueryTestConfig struct {
	BaseURL               string
	Credential            string
	skipDefaultCredential bool
	ResolveHost           func(context.Context, string) ([]net.IP, error)
}

func (c negativeSmokeQueryTestConfig) withDefaults(serverURL string) negativeSmokeQueryTestConfig {
	if c.BaseURL == "" {
		c.BaseURL = serverURL
	}
	if c.Credential == "" && !c.skipDefaultCredential {
		c.Credential = "cHVibGljOnNlY3JldA=="
	}
	return c
}

func assertNegativeSmokeQueryRequest(t *testing.T, port string, request *http.Request, target smoke.PollMarkerTarget) {
	t.Helper()
	if request.Method != http.MethodGet {
		t.Errorf("%s method = %s, want GET", port, request.Method)
	}
	if request.Header.Get("Authorization") != "Basic cHVibGljOnNlY3JldA==" {
		t.Errorf("%s query did not use the configured read credential", port)
	}
	query := request.URL.Query()
	switch port {
	case "langfuse":
		if request.URL.Path != "/api/public/v2/observations" {
			t.Errorf("Langfuse path = %q", request.URL.Path)
		}
		assertLangfuseSmokeFilter(t, query.Get("filter"), target)
	case "ai-plane":
		if request.URL.Path != "/api/v1/observability/smoke/marker-count" || query.Get("marker") != target.Marker || query.Get("started_at") != target.StartedAt.UTC().Format(time.RFC3339Nano) || query.Get("deadline") != target.Deadline.UTC().Format(time.RFC3339Nano) {
			t.Errorf("AI plane request = path:%q query:%v, want exact marker and bounded window", request.URL.Path, query)
		}
	}
}

func newNegativeSmokeQueryForTest(port string, config negativeSmokeQueryTestConfig) (func(context.Context, smoke.PollMarkerTarget) (int, error), error) {
	switch port {
	case "langfuse":
		client, err := NewLangfuseSmokeQueryClient(LangfuseSmokeQueryConfig{BaseURL: config.BaseURL, Credential: config.Credential, Timeout: time.Second, ResolveHost: config.ResolveHost})
		if err != nil {
			return nil, err
		}
		return client.Query, nil
	case "ai-plane":
		client, err := NewAIPlaneSmokeQueryClient(AIPlaneSmokeQueryConfig{BaseURL: config.BaseURL, Credential: config.Credential, Timeout: time.Second, ResolveHost: config.ResolveHost})
		if err != nil {
			return nil, err
		}
		return client.Query, nil
	default:
		return nil, fmt.Errorf("unknown test port %q", port)
	}
}

func assertLangfuseSmokeFilter(t *testing.T, rendered string, target smoke.PollMarkerTarget) {
	t.Helper()
	var filters []struct {
		Type     string `json:"type"`
		Column   string `json:"column"`
		Key      string `json:"key"`
		Operator string `json:"operator"`
		Value    string `json:"value"`
	}
	if err := json.Unmarshal([]byte(rendered), &filters); err != nil {
		t.Fatalf("filter = %q is not structured JSON: %v", rendered, err)
	}
	want := map[string]string{
		"stringObject|metadata|longtermism.smoke.run_id|=": target.Marker,
		"datetime|startTime||>=":                           target.StartedAt.UTC().Format(time.RFC3339Nano),
		"datetime|startTime||<=":                           target.Deadline.UTC().Format(time.RFC3339Nano),
	}
	if len(filters) != len(want) {
		t.Fatalf("Langfuse filter = %s, want exactly %d constraints", rendered, len(want))
	}
	for _, filter := range filters {
		key := strings.Join([]string{filter.Type, filter.Column, filter.Key, filter.Operator}, "|")
		value, ok := want[key]
		if !ok || value != filter.Value {
			t.Fatalf("Langfuse filter = %s, contains an extra or conflicting constraint", rendered)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("Langfuse filter = %s, missing exact constraints %#v", rendered, want)
	}
}
