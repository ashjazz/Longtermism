package backend

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

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

// TestGrafanaInfrastructureSmokeBackendDelegatesBoundedEvidence keeps the runner's backend
// deliberately boring: it delegates marker facts to the existing adapter and projects only the
// one low-cardinality HTTP counter. The smoke runner must never receive a Grafana document.
func TestGrafanaInfrastructureSmokeBackendDelegatesBoundedEvidence(t *testing.T) {
	startedAt := time.Now().UTC().Add(-10 * time.Second)
	target := smoke.PollMarkerTarget{Marker: "infra-t065b-marker", StartedAt: startedAt, Deadline: startedAt.Add(time.Minute)}
	prometheusCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		switch request.URL.Path {
		case "/api/search":
			assertExactMarkerPredicate(t, "Tempo", query.Get("q"), "longtermism.smoke.run_id", target.Marker)
			if query.Get("start") != fmt.Sprint(target.StartedAt.Unix()) || query.Get("end") != fmt.Sprint(target.Deadline.Unix()) {
				t.Errorf("Tempo window = start:%q end:%q, want target window", query.Get("start"), query.Get("end"))
			}
			_, _ = fmt.Fprintf(writer, `{"traces":[{"startTimeUnixNano":"%d","raw":"raw-t065b-tempo"}]}`, startedAt.UnixNano())
		case "/loki/api/v1/query_range":
			assertExactMarkerPredicate(t, "Loki", query.Get("query"), "smoke_run_id", target.Marker)
			if query.Get("start") != target.StartedAt.UTC().Format(time.RFC3339Nano) || query.Get("end") != target.Deadline.UTC().Format(time.RFC3339Nano) {
				t.Errorf("Loki window = start:%q end:%q, want target window", query.Get("start"), query.Get("end"))
			}
			_, _ = fmt.Fprintf(writer, `{"status":"success","data":{"resultType":"streams","result":[{"values":[["%d","http request completed",{"smoke_run_id":"%s"}]]}]}}`, startedAt.UnixNano(), target.Marker)
		case "/api/v1/query":
			prometheusCalls++
			assertInfraHTTPCountQuery(t, query.Get("query"))
			count := 41
			if prometheusCalls == 2 {
				count = 42
			}
			_, _ = fmt.Fprintf(writer, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"http_route":"/api/v1/observability/infra-smoke","http_request_method":"GET","http_response_status_class":"2xx"},"value":[%d,"%d"]}]}}`, startedAt.Unix(), count)
		default:
			t.Errorf("unexpected Grafana request path %q", request.URL.Path)
		}
	}))
	defer server.Close()

	client := NewGrafanaQueryClient(GrafanaQueryConfig{PrometheusURL: server.URL, LokiURL: server.URL, TempoURL: server.URL, Timeout: time.Second})
	backend, err := NewGrafanaInfrastructureSmokeBackend(GrafanaInfrastructureSmokeBackendConfig{
		Grafana:  client,
		Langfuse: fakeNegativeMarkerCounter{},
		AIPlane:  fakeNegativeMarkerCounter{},
	})
	if err != nil {
		t.Fatalf("NewGrafanaInfrastructureSmokeBackend() error = %v", err)
	}

	tempo, err := backend.QueryTempo(context.Background(), target)
	if err != nil {
		t.Fatalf("QueryTempo() error = %v", err)
	}
	loki, err := backend.QueryLoki(context.Background(), target)
	if err != nil {
		t.Fatalf("QueryLoki() error = %v", err)
	}
	baseline, err := backend.BaselineHTTPRequestCount(context.Background())
	if err != nil {
		t.Fatalf("BaselineHTTPRequestCount() error = %v", err)
	}
	after, err := backend.HTTPRequestCount(context.Background())
	if err != nil {
		t.Fatalf("HTTPRequestCount() error = %v", err)
	}

	if len(tempo) != 1 || len(loki) != 1 || baseline != 41 || after != 42 {
		t.Fatalf("backend evidence = tempo:%#v loki:%#v baseline:%d after:%d, want marker observations and 41 -> 42", tempo, loki, baseline, after)
	}
	assertNoRawBackendDataProjected(t, tempo)
	assertNoRawBackendDataProjected(t, loki)
}

func TestGrafanaInfrastructureSmokeBackendClassifiesMalformedMarkerResponses(t *testing.T) {
	startedAt := time.Now().UTC().Add(-time.Second)
	target := smoke.PollMarkerTarget{Marker: "infra-t175-malformed", StartedAt: startedAt, Deadline: startedAt.Add(time.Minute)}
	tests := []struct {
		name  string
		path  string
		query func(*GrafanaInfrastructureSmokeBackend) error
	}{
		{name: "Tempo", path: "/api/search", query: func(backend *GrafanaInfrastructureSmokeBackend) error {
			_, err := backend.QueryTempo(context.Background(), target)
			return err
		}},
		{name: "Loki", path: "/loki/api/v1/query_range", query: func(backend *GrafanaInfrastructureSmokeBackend) error {
			_, err := backend.QueryLoki(context.Background(), target)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != tt.path {
					t.Errorf("query path = %q, want %q", request.URL.Path, tt.path)
				}
				_, _ = writer.Write([]byte(`{"raw":"raw-t175-malformed-response"}`))
			}))
			defer server.Close()

			client := NewGrafanaQueryClient(GrafanaQueryConfig{PrometheusURL: server.URL, LokiURL: server.URL, TempoURL: server.URL, Timeout: time.Second})
			backend, err := NewGrafanaInfrastructureSmokeBackend(GrafanaInfrastructureSmokeBackendConfig{Grafana: client, Langfuse: fakeNegativeMarkerCounter{}, AIPlane: fakeNegativeMarkerCounter{}})
			if err != nil {
				t.Fatalf("NewGrafanaInfrastructureSmokeBackend() error = %v", err)
			}
			err = tt.query(backend)
			if errorClass(err) != "malformed_response" {
				t.Fatalf("marker response error class = %q, want malformed_response", errorClass(err))
			}
			if strings.Contains(err.Error(), "raw-t175-malformed-response") {
				t.Fatal("marker response error leaked raw backend data")
			}
		})
	}
}

func TestGrafanaInfrastructureSmokeBackendFailsClosed(t *testing.T) {
	selector := SmokeHTTPCountSelector{Route: "/api/v1/observability/infra-smoke", Method: "GET", StatusClass: "2xx"}
	tests := []struct {
		name      string
		config    GrafanaInfrastructureSmokeBackendConfig
		wantClass string
	}{
		{name: "missing Grafana query client", config: GrafanaInfrastructureSmokeBackendConfig{Langfuse: fakeNegativeMarkerCounter{}, AIPlane: fakeNegativeMarkerCounter{}}},
		{name: "missing Langfuse counter", config: GrafanaInfrastructureSmokeBackendConfig{Grafana: loopbackGrafanaQueryClient(), AIPlane: fakeNegativeMarkerCounter{}}},
		{name: "missing AI plane counter", config: GrafanaInfrastructureSmokeBackendConfig{Grafana: loopbackGrafanaQueryClient(), Langfuse: fakeNegativeMarkerCounter{}}},
		{name: "incomplete HTTP selector", config: GrafanaInfrastructureSmokeBackendConfig{Grafana: loopbackGrafanaQueryClient(), Langfuse: fakeNegativeMarkerCounter{}, AIPlane: fakeNegativeMarkerCounter{}, HTTPCountSelector: SmokeHTTPCountSelector{Route: selector.Route, Method: selector.Method}}, wantClass: "invalid_query"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewGrafanaInfrastructureSmokeBackend(tt.config)
			if err == nil {
				t.Fatal("NewGrafanaInfrastructureSmokeBackend() error = nil, want fail-fast rejection")
			}
			if tt.wantClass != "" && errorClass(err) != tt.wantClass {
				t.Fatalf("NewGrafanaInfrastructureSmokeBackend() class = %q, want %q", errorClass(err), tt.wantClass)
			}
		})
	}
}

func TestGrafanaInfrastructureSmokeBackendPreservesNegativeCounterFailures(t *testing.T) {
	now := time.Now().UTC()
	target := smoke.PollMarkerTarget{Marker: "infra-t065b-marker", StartedAt: now, Deadline: now.Add(time.Minute)}
	backend, err := NewGrafanaInfrastructureSmokeBackend(GrafanaInfrastructureSmokeBackendConfig{
		Grafana:  loopbackGrafanaQueryClient(),
		Langfuse: fakeNegativeMarkerCounter{err: fmt.Errorf("wrapped: %w", newBackendQueryError("langfuse", "authentication_failed"))},
		AIPlane:  fakeNegativeMarkerCounter{err: newBackendQueryError("collector", "malformed_response")},
	})
	if err != nil {
		t.Fatalf("NewGrafanaInfrastructureSmokeBackend() error = %v", err)
	}

	tests := []struct {
		name  string
		query func(context.Context, smoke.PollMarkerTarget) (int, error)
		want  string
	}{
		{name: "Langfuse authentication", query: backend.QueryLangfuse, want: "authentication_failed"},
		{name: "AI plane malformed response", query: backend.QueryAIPlane, want: "malformed_response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count, err := tt.query(context.Background(), target)
			if count != 0 || errorClass(err) != tt.want {
				t.Fatalf("negative query = count:%d error:%v class:%q, want 0 and %q", count, err, errorClass(err), tt.want)
			}
		})
	}
}

type fakeNegativeMarkerCounter struct {
	count int
	err   error
}

func (f fakeNegativeMarkerCounter) Query(context.Context, smoke.PollMarkerTarget) (int, error) {
	return f.count, f.err
}

func assertInfraHTTPCountQuery(t *testing.T, query string) {
	t.Helper()
	for _, required := range []string{"longtermism_http_server_request_count_total", `http_route="/api/v1/observability/infra-smoke"`, `http_request_method="GET"`, `http_response_status_class="2xx"`} {
		if !strings.Contains(query, required) {
			t.Errorf("Prometheus query = %q, want low-cardinality selector %q", query, required)
		}
	}
	for _, forbidden := range []string{"request_id", "trace_id", "smoke_run_id", "marker"} {
		if strings.Contains(query, forbidden) {
			t.Errorf("Prometheus query = %q, must not contain high-cardinality label %q", query, forbidden)
		}
	}
}

func errorClass(err error) string {
	var classified *BackendQueryError
	if errors.As(err, &classified) {
		return classified.Class()
	}
	return ""
}

func loopbackGrafanaQueryClient() *GrafanaQueryClient {
	return NewGrafanaQueryClient(GrafanaQueryConfig{PrometheusURL: "http://127.0.0.1:9090", LokiURL: "http://127.0.0.1:3100", TempoURL: "http://127.0.0.1:3200"})
}

func TestGrafanaInfrastructureSmokeBackendDoesNotExposeRawResults(t *testing.T) {
	result := backendQueryResultForTest(`{"raw":"raw-t065b-query-document"}`)
	if _, err := json.Marshal(result); err == nil {
		t.Fatal("BackendQueryResult must stay non-serializable outside the backend boundary")
	}
}

// TestInfrastructureSmokeRunnerPreservesNegativeQueryClass prevents a platform adapter from
// correctly classifying an authentication or decoding failure only for the runner to flatten it
// into query_failed. A failed negative query is never evidence of absence.
func TestInfrastructureSmokeRunnerPreservesNegativeQueryClass(t *testing.T) {
	now := time.Now().UTC()
	backend := runnerClassBackend{negativeErr: newBackendQueryError("langfuse", "authentication_failed")}
	report, err := smoke.RunInfrastructureSmoke(context.Background(), smoke.InfrastructureSmokeRequest{Profile: "grafana", Deadline: now.Add(time.Minute)}, smoke.InfrastructureSmokeRunnerDependencies{
		Backend: &backend,
		IdentityFactory: func(context.Context) (smoke.InfrastructureSmokeIdentity, error) {
			return smoke.InfrastructureSmokeIdentity{RunID: "run-t065b-marker"}, nil
		},
		Trigger:      func(context.Context, smoke.InfrastructureSmokeIdentity) error { return nil },
		Clock:        fixedRunnerClock{now: now},
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunInfrastructureSmoke() error = %v", err)
	}
	foundLangfuse := false
	for _, check := range report.Checks() {
		if check.Backend == "langfuse_trace" && check.ErrorClass != "authentication_failed" {
			t.Fatalf("Langfuse report class = %q, want authentication_failed", check.ErrorClass)
		}
		foundLangfuse = foundLangfuse || check.Backend == "langfuse_trace"
	}
	if !foundLangfuse {
		t.Fatal("infra report omitted the langfuse_trace negative check")
	}
}

type fixedRunnerClock struct{ now time.Time }

func (c fixedRunnerClock) Now() time.Time                          { return c.now }
func (fixedRunnerClock) Wait(context.Context, time.Duration) error { return nil }

type runnerClassBackend struct{ negativeErr error }

func (b *runnerClassBackend) QueryTempo(_ context.Context, target smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error) {
	return []smoke.MarkerObservation{{Marker: target.Marker, ObservedAt: target.StartedAt}}, nil
}

func (b *runnerClassBackend) QueryLoki(_ context.Context, target smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error) {
	return []smoke.MarkerObservation{{Marker: target.Marker, ObservedAt: target.StartedAt}}, nil
}

func (*runnerClassBackend) BaselineHTTPRequestCount(context.Context) (int64, error) { return 1, nil }
func (*runnerClassBackend) HTTPRequestCount(context.Context) (int64, error)         { return 2, nil }
func (b *runnerClassBackend) QueryLangfuse(context.Context, smoke.PollMarkerTarget) (int, error) {
	return 0, b.negativeErr
}
func (*runnerClassBackend) QueryAIPlane(context.Context, smoke.PollMarkerTarget) (int, error) {
	return 0, nil
}
