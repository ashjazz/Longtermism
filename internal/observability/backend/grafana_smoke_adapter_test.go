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

// TestGrafanaSmokeEvidenceAdapterQueriesCurrentMarkers 固定 marker 的事实来源：adapter
// 必须以 target 的受控 marker attribute 构造 Tempo TraceQL/Loki LogQL 条件和当前窗口；
// Tempo search summary 不回显任意 attribute，因此只在“精确查询已成功”的事实基础上投影
// target marker 与命中时间。原始 trace/log 文本绝不能进入可持久化 smoke evidence。
func TestGrafanaSmokeEvidenceAdapterQueriesCurrentMarkers(t *testing.T) {
	startedAt := time.Now().UTC().Add(-10 * time.Second)
	deadline := startedAt.Add(time.Minute)
	target := smoke.PollMarkerTarget{Marker: "infra-t064b-marker", StartedAt: startedAt, Deadline: deadline}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("start") == "" || query.Get("end") == "" {
			t.Errorf("query window = start:%q end:%q, want both bounds", query.Get("start"), query.Get("end"))
		}
		switch request.URL.Path {
		case "/api/search":
			assertExactMarkerPredicate(t, "Tempo", query.Get("q"), "longtermism.smoke.run_id", target.Marker)
			_, _ = fmt.Fprintf(writer, `{"traces":[{"startTimeUnixNano":"%d","rootTraceName":"GET /api/v1/observability/infra-smoke","rootServiceName":"raw-t064b-tempo-secret"},{"startTimeUnixNano":"%d","rootTraceName":"GET /api/v1/observability/infra-smoke","rootServiceName":"raw-t064b-tempo-secret"}]}`, startedAt.UnixNano(), deadline.UnixNano())
		case "/loki/api/v1/query_range":
			assertExactMarkerPredicate(t, "Loki", query.Get("query"), "smoke_run_id", target.Marker)
			_, _ = fmt.Fprintf(writer, `{"status":"success","data":{"resultType":"streams","result":[{"values":[["%d","raw-t064b-loki-secret"],["%d","raw-t064b-loki-secret"]]}]}}`, startedAt.UnixNano(), deadline.UnixNano())
		default:
			t.Errorf("path = %q, want Tempo or Loki query", request.URL.Path)
		}
	}))
	defer server.Close()
	client := NewGrafanaQueryClient(GrafanaQueryConfig{TempoURL: server.URL, LokiURL: server.URL, Timeout: time.Second})
	adapter := NewGrafanaSmokeEvidenceAdapter(client)

	tests := []struct {
		name  string
		query func(context.Context, smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error)
	}{
		{name: "Tempo", query: adapter.QueryTempoMarker},
		{name: "Loki", query: adapter.QueryLokiMarker},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observations, err := tt.query(context.Background(), target)
			if err != nil {
				t.Fatalf("marker query error = %v", err)
			}
			want := []smoke.MarkerObservation{{Marker: target.Marker, ObservedAt: startedAt}, {Marker: target.Marker, ObservedAt: deadline}}
			if !equalMarkerObservations(observations, want) {
				t.Fatalf("marker observations = %#v, want %#v", observations, want)
			}
			assertNoRawBackendDataProjected(t, observations)
		})
	}
}

func TestGrafanaSmokeEvidenceAdapterRejectsUnsafeOrUnboundedTargets(t *testing.T) {
	now := time.Now().UTC()
	adapter := NewGrafanaSmokeEvidenceAdapter(nil)
	tests := []struct {
		name   string
		target smoke.PollMarkerTarget
	}{
		{name: "sensitive marker", target: smoke.PollMarkerTarget{Marker: "secret-marker", StartedAt: now, Deadline: now.Add(time.Minute)}},
		{name: "window longer than one minute", target: smoke.PollMarkerTarget{Marker: "infra-t064c-marker", StartedAt: now, Deadline: now.Add(2 * time.Minute)}},
		{name: "future window", target: smoke.PollMarkerTarget{Marker: "infra-t064c-marker", StartedAt: now.Add(2 * time.Minute), Deadline: now.Add(3 * time.Minute)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := adapter.QueryTempoMarker(context.Background(), tt.target)
			if !errors.Is(err, errInvalidSmokeQueryTarget) {
				t.Fatalf("QueryTempoMarker() error = %v, want invalid smoke target", err)
			}
			if strings.Contains(err.Error(), tt.target.Marker) {
				t.Fatal("invalid target error leaked a caller-controlled marker")
			}
		})
	}
}

func assertExactMarkerPredicate(t *testing.T, backend, query, field, marker string) {
	t.Helper()
	if !strings.Contains(query, marker) || !strings.Contains(query, field) {
		t.Errorf("%s marker query = %q, want exact %s predicate for %q", backend, query, field, marker)
	}
}

// TestGrafanaSmokeEvidenceAdapterDecodesLowCardinalityCounts proves that the adapter accepts
// only the fixed route/status aggregate needed by the smoke. It must not turn request, trace, or
// smoke identity labels into a query result or a report DTO.
func TestGrafanaSmokeEvidenceAdapterDecodesLowCardinalityCounts(t *testing.T) {
	adapter := NewGrafanaSmokeEvidenceAdapter(nil)
	selector := SmokeHTTPCountSelector{Route: "/api/v1/observability/infra-smoke", Method: "GET", StatusClass: "2xx"}

	tests := []struct {
		name    string
		result  BackendQueryResult
		want    int64
		wantErr bool
	}{
		{
			name: "baseline count",
			result: backendQueryResultForTest(`{"status":"success","data":{"resultType":"vector","result":[
                {"metric":{"http_route":"/api/v1/observability/infra-smoke","http_request_method":"GET","http_response_status_class":"2xx"},"value":[1784541600,"41"]},
                {"metric":{"http_route":"/api/v1/observability/infra-smoke","http_request_method":"GET","http_response_status_class":"2xx","request_id":"raw-t064b-request-id"},"value":[1784541600,"999"]}
            ]}}`),
			want: 41,
		},
		{
			name: "after count",
			result: backendQueryResultForTest(`{"status":"success","data":{"resultType":"vector","result":[
                {"metric":{"http_route":"/api/v1/observability/infra-smoke","http_request_method":"GET","http_response_status_class":"2xx"},"value":[1784541601,"42"]}
            ]}}`),
			want: 42,
		},
		{
			name: "rejects ambiguous exact series",
			result: backendQueryResultForTest(`{"status":"success","data":{"resultType":"vector","result":[
                {"metric":{"http_route":"/api/v1/observability/infra-smoke","http_request_method":"GET","http_response_status_class":"2xx"},"value":[1784541601,"42"]},
                {"metric":{"http_route":"/api/v1/observability/infra-smoke","http_request_method":"GET","http_response_status_class":"2xx"},"value":[1784541601,"43"]}
            ]}}`),
			wantErr: true,
		},
		{
			name:    "rejects a non-vector response",
			result:  backendQueryResultForTest(`{"status":"success","data":{"resultType":"matrix","result":[]}}`),
			wantErr: true,
		},
		{
			name: "rejects an identity label instead of projecting it",
			result: backendQueryResultForTest(`{"status":"success","data":{"resultType":"vector","result":[
                {"metric":{"http_route":"/api/v1/observability/infra-smoke","http_request_method":"GET","http_response_status_class":"2xx","trace_id":"raw-t064c-trace-id"},"value":[1784541601,"42"]}
            ]}}`),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evidence, err := adapter.DecodePrometheusHTTPCount(tt.result, selector)
			if tt.wantErr {
				if err == nil {
					t.Fatal("DecodePrometheusHTTPCount() error = nil, want fail-closed rejection")
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodePrometheusHTTPCount() error = %v", err)
			}
			if evidence.Count != tt.want {
				t.Fatalf("count = %d, want %d", evidence.Count, tt.want)
			}
			assertNoRawBackendDataProjected(t, evidence)
		})
	}
}

// The runner intentionally uses sum(...) so Prometheus returns one aggregate sample with no
// labels. Treating that safe aggregate as a malformed raw series makes real smoke runs fail even
// though the protected counter was observed.
func TestDecodePrometheusHTTPCountAcceptsSingleAggregatedSample(t *testing.T) {
	adapter := NewGrafanaSmokeEvidenceAdapter(nil)
	result := backendQueryResultForTest(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1784608032,"42"]}]}}`)
	evidence, err := adapter.DecodePrometheusHTTPCount(result, SmokeHTTPCountSelector{Route: "/api/v1/observability/infra-smoke", Method: "GET", StatusClass: "2xx"})
	if err != nil || evidence.Count != 42 {
		t.Fatalf("DecodePrometheusHTTPCount() = (%#v, %v), want aggregate count 42", evidence, err)
	}
}

func TestGrafanaSmokeEvidenceAdapterRejectsMalformedMarkerDocuments(t *testing.T) {
	target := smoke.PollMarkerTarget{Marker: "infra-t064c-marker", StartedAt: time.Now().UTC(), Deadline: time.Now().UTC().Add(time.Minute)}
	tests := []struct {
		name   string
		decode func(BackendQueryResult, smoke.PollMarkerTarget) ([]smoke.MarkerObservation, error)
		result BackendQueryResult
	}{
		{name: "Tempo missing traces", decode: decodeTempoMarkerObservations, result: backendQueryResultForTest(`{}`)},
		{name: "Tempo null traces", decode: decodeTempoMarkerObservations, result: backendQueryResultForTest(`{"traces":null}`)},
		{name: "Loki non-string line", decode: decodeLokiMarkerObservations, result: backendQueryResultForTest(`{"status":"success","data":{"resultType":"streams","result":[{"values":[["1784541600000000000",1]]}]}}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.decode(tt.result, target); !errors.Is(err, errMalformedSmokeEvidence) {
				t.Fatalf("decode() error = %v, want malformed smoke evidence", err)
			}
		})
	}
}

func TestGrafanaSmokeEvidenceAdapterDecodesGrafanaHealthAndNegativeCounts(t *testing.T) {
	adapter := NewGrafanaSmokeEvidenceAdapter(nil)

	health, err := adapter.DecodeGrafanaDatasourceHealth(backendQueryResultForTest(`{"status":"OK","message":"raw-t064b-grafana-secret"}`))
	if err != nil {
		t.Fatalf("DecodeGrafanaDatasourceHealth() error = %v", err)
	}
	if !health.Healthy {
		t.Fatal("Grafana datasource health = false, want true")
	}
	assertNoRawBackendDataProjected(t, health)

	tests := []struct {
		name string
		body string
		want int64
	}{
		{name: "Langfuse negative surface", body: `{"data":{"count":0,"debug":"raw-t064b-langfuse-secret"}}`, want: 0},
		{name: "AI plane negative surface", body: `{"data":{"count":1,"debug":"raw-t064b-ai-secret"}}`, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evidence, err := adapter.DecodeNegativeCount(backendQueryResultForTest(tt.body))
			if err != nil {
				t.Fatalf("DecodeNegativeCount() error = %v", err)
			}
			if evidence.Count != tt.want {
				t.Fatalf("negative count = %d, want %d", evidence.Count, tt.want)
			}
			assertNoRawBackendDataProjected(t, evidence)
		})
	}
}

// TestGrafanaSmokeEvidenceAdapterMapsErrorsToReportClasses keeps transport failures distinct
// from stale caller windows. The returned class is immediately checked by SmokeReport, so a new
// adapter cannot accidentally emit arbitrary provider text into the machine-readable report.
func TestGrafanaSmokeEvidenceAdapterMapsErrorsToReportClasses(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "stale window", err: ErrStaleQueryWindow, want: "invalid_query"},
		{name: "authentication", err: newBackendQueryError("tempo", "authentication_failed"), want: "authentication_failed"},
		{name: "timeout", err: newBackendQueryError("loki", "backend_timeout"), want: "backend_timeout"},
		{name: "malformed response", err: newBackendQueryError("prometheus", "malformed_response"), want: "malformed_response"},
		{name: "unexpected backend class", err: newBackendQueryError("grafana", "untrusted-t064b-secret"), want: "query_failed"},
		{name: "unknown error", err: errors.New("raw-t064b-error-secret"), want: "query_failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := smokeReportErrorClass(tt.err)
			if got != tt.want {
				t.Fatalf("smokeReportErrorClass() = %q, want %q", got, tt.want)
			}
			assertErrorClassBuildsSmokeReport(t, got)
			if strings.Contains(got, "secret") {
				t.Fatalf("error class leaked raw error text: %q", got)
			}
		})
	}
}

func TestTraceQLMarkerQueryQuotesDottedSpanAttribute(t *testing.T) {
	if got, want := traceQLMarkerQuery("run-t066-marker"), `{ span."longtermism.smoke.run_id" = "run-t066-marker" }`; got != want {
		t.Fatalf("TraceQL marker query = %q, want %q", got, want)
	}
}

func backendQueryResultForTest(document string) BackendQueryResult {
	return BackendQueryResult{payload: json.RawMessage(document)}
}

func equalMarkerObservations(got, want []smoke.MarkerObservation) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index].Marker != want[index].Marker || !got[index].ObservedAt.Equal(want[index].ObservedAt) {
			return false
		}
	}
	return true
}

func assertNoRawBackendDataProjected(t *testing.T, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("MarshalJSON() error = %v", err)
	}
	for _, forbidden := range []string{"raw-t064b", "request-id", "authorization", "raw_payload"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("adapter DTO leaked raw backend content %q: %s", forbidden, encoded)
		}
	}
}

func assertErrorClassBuildsSmokeReport(t *testing.T, errorClass string) {
	t.Helper()
	startedAt := time.Now().UTC()
	_, err := smoke.BuildSmokeReport(smoke.SmokeReportInput{
		RunID:      "adapter-t064b-run",
		Marker:     "adapter-t064b-marker",
		Profile:    "grafana",
		Scenario:   "infra",
		StartedAt:  startedAt,
		Deadline:   startedAt.Add(time.Minute),
		FinishedAt: startedAt,
		Checks: []smoke.BackendCheckInput{{
			Backend: "tempo", Status: "failed", FailureStage: "query", ErrorClass: errorClass,
			Evidence: map[string]any{"matched_spans": int64(0)},
		}},
		Cleanup: smoke.SmokeCleanupInput{Status: "not_required", TemporaryCredentials: "not_created", TemporaryData: "not_created"},
	})
	if err != nil {
		t.Fatalf("SmokeReport rejected adapter error class %q: %v", errorClass, err)
	}
}
