package backend

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

// TestPrivacyGrafanaSurfacesReadRealTempoAndLokiFacts 固定远端隐私证据的来源：Tempo
// 必须先精确定位 trace、再读取完整 V2 trace document；Loki 必须读取精确结构化查询的
// line 与 metadata。query 已发送或 target 中已有身份都不能替代平台实际返回事实。
func TestPrivacyGrafanaSurfacesReadRealTempoAndLokiFacts(t *testing.T) {
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceTempo, smoke.PrivacySmokeSurfaceLoki} {
		t.Run(string(surface), func(t *testing.T) {
			request := t190Request(surface)
			surfaces, log, closeServer := t190Surfaces(t, t190Handler(request, "safe-value"))
			defer closeServer()
			evidence, err := surfaces.Scan(context.Background(), request)
			if err != nil {
				t.Fatalf("Scan failed with class %q", t190ErrorClass(err))
			}
			method := map[smoke.PrivacySmokeSurface]string{
				smoke.PrivacySmokeSurfaceTempo: "bounded_trace_document",
				smoke.PrivacySmokeSurfaceLoki:  "exact_structured_query",
			}[surface]
			if evidence.Surface() != surface || evidence.EvidenceMethod() != method || evidence.ScannerPolicyVersion() != "1" {
				t.Fatal("evidence did not retain the closed surface contract")
			}
			assertT190Counts(t, evidence.Counts(), "")
			counts := evidence.Counts()
			counts["token"] = 99
			assertT190Counts(t, evidence.Counts(), "")

			requests := log.snapshot()
			wantRequests := 1
			if surface == smoke.PrivacySmokeSurfaceTempo {
				wantRequests = 2
			}
			if len(requests) != wantRequests {
				t.Fatalf("requests = %d, want %d", len(requests), wantRequests)
			}
			assertT190Query(t, requests[0], request)
			if surface == smoke.PrivacySmokeSurfaceTempo {
				if requests[0].URL.Path != "/api/search" || requests[1].URL.Path != "/api/v2/traces/"+request.ServiceTraceID {
					t.Fatal("Tempo did not perform search then bounded V2 trace retrieval")
				}
				if requests[1].URL.Query().Get("start") != strconv.FormatInt(request.StartedAt.Unix(), 10) ||
					requests[1].URL.Query().Get("end") != strconv.FormatInt(request.Deadline.Unix(), 10) {
					t.Fatal("Tempo trace document request did not retain the exact window")
				}
			} else if requests[0].URL.Path != "/loki/api/v1/query_range" {
				t.Fatal("Loki did not use the fixed range-query path")
			}
		})
	}

	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceTempo, smoke.PrivacySmokeSurfaceLoki} {
		t.Run(string(surface)+" raw duplicate key", func(t *testing.T) {
			request := t190Request(surface)
			handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				if surface == smoke.PrivacySmokeSurfaceTempo && incoming.URL.Path == "/api/search" {
					writeT190JSON(writer, t190TempoSearch(request, 1))
					return
				}
				var document map[string]any
				if surface == smoke.PrivacySmokeSurfaceTempo {
					document = t190TempoDocument(request, "safe")
				} else {
					document = t190LokiResponse(request, "safe", 1)
				}
				encoded, _ := json.Marshal(document)
				encoded = encoded[:len(encoded)-1]
				_, _ = writer.Write(append(encoded, []byte(`,"duplicate":"T190_SYNTHETIC_CANARY","duplicate":"safe"}`)...))
			})
			surfaces, _, closeServer := t190Surfaces(t, handler)
			defer closeServer()
			evidence, err := surfaces.Scan(context.Background(), request)
			if err == nil {
				assertT190Counts(t, evidence.Counts(), "synthetic_canary")
			}
		})
	}

	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceTempo, smoke.PrivacySmokeSurfaceLoki} {
		t.Run(string(surface)+" scans or rejects second container", func(t *testing.T) {
			request := t190Request(surface)
			handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				if surface == smoke.PrivacySmokeSurfaceTempo && incoming.URL.Path == "/api/search" {
					writeT190JSON(writer, t190TempoSearch(request, 1))
					return
				}
				if surface == smoke.PrivacySmokeSurfaceLoki {
					response := t190LokiResponse(request, "safe", 1)
					second := t190LokiResponse(request, t190Canary, 1)["data"].(map[string]any)["result"].([]any)[0]
					response["data"].(map[string]any)["result"] = append(response["data"].(map[string]any)["result"].([]any), second)
					writeT190JSON(writer, response)
					return
				}
				document := t190TempoDocument(request, "safe")
				span := document["trace"].(map[string]any)["batches"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)[0].(map[string]any)
				sibling := make(map[string]any, len(span))
				for key, value := range span {
					sibling[key] = value
				}
				sibling["spanId"] = t190OTLPID("fedcba9876543210")
				sibling["name"] = t190Canary
				spans := document["trace"].(map[string]any)["batches"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)
				spans["spans"] = append(spans["spans"].([]any), sibling)
				writeT190JSON(writer, document)
			})
			surfaces, _, closeServer := t190Surfaces(t, handler)
			defer closeServer()
			evidence, err := surfaces.Scan(context.Background(), request)
			if surface == smoke.PrivacySmokeSurfaceLoki {
				if err == nil || !reflect.ValueOf(evidence).IsZero() {
					t.Fatal("duplicate cross-stream Loki fact was accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("Tempo sibling-span leak was discarded: %q", t190ErrorClass(err))
			}
			assertT190Counts(t, evidence.Counts(), "synthetic_canary")
		})
		t.Run(string(surface)+" validates second container", func(t *testing.T) {
			request := t190Request(surface)
			handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				if surface == smoke.PrivacySmokeSurfaceTempo && incoming.URL.Path == "/api/search" {
					writeT190JSON(writer, t190TempoSearch(request, 1))
					return
				}
				foreign := request
				foreign.RequestID = "foreign-request"
				if surface == smoke.PrivacySmokeSurfaceLoki {
					response := t190LokiResponse(request, "safe", 1)
					second := t190LokiResponse(foreign, "safe", 1)["data"].(map[string]any)["result"].([]any)[0]
					response["data"].(map[string]any)["result"] = append(response["data"].(map[string]any)["result"].([]any), second)
					writeT190JSON(writer, response)
					return
				}
				document := t190TempoDocument(request, "safe")
				second := t190TempoDocument(foreign, "safe")["trace"].(map[string]any)["batches"].([]any)[0]
				trace := document["trace"].(map[string]any)
				trace["batches"] = append(trace["batches"].([]any), second)
				writeT190JSON(writer, document)
			})
			surfaces, _, closeServer := t190Surfaces(t, handler)
			defer closeServer()
			if evidence, err := surfaces.Scan(context.Background(), request); err == nil || !reflect.ValueOf(evidence).IsZero() {
				t.Fatal("foreign second container became zero evidence")
			}
		})
	}
}

func TestPrivacyGrafanaTempoAcceptsProtoDefaultCompleteStatus(t *testing.T) {
	request := t190Request(smoke.PrivacySmokeSurfaceTempo)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		if incoming.URL.Path == "/api/search" {
			writeT190JSON(writer, t190TempoSearch(request, 1))
			return
		}
		document := t190TempoDocument(request, "safe-value")
		delete(document, "status")
		delete(document, "message")
		writeT190JSON(writer, document)
	})
	surfaces, _, closeServer := t190Surfaces(t, handler)
	defer closeServer()
	evidence, err := surfaces.Scan(context.Background(), request)
	if err != nil {
		t.Fatalf("Tempo proto-default COMPLETE response failed: %q", t190ErrorClass(err))
	}
	assertT190Counts(t, evidence.Counts(), "")
}

// TestPrivacyGrafanaSurfacesDetectEveryClosedCategory proves counts come from decoded platform
// documents. The escaped canary case specifically prevents scanning raw JSON bytes only.
func TestPrivacyGrafanaSurfacesDetectEveryClosedCategory(t *testing.T) {
	tests := []struct {
		name, text, category string
	}{
		{"canary", t190Canary, "synthetic_canary"},
		{"credential", "api_key=sk-t190-fixture", "credential"},
		{"authorization", "Authorization: Bearer t190-fixture", "authorization"},
		{"token", "token=t190.fixture.value", "token"},
		{"pii", "owner@example.com", "recognized_pii"},
	}
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceTempo, smoke.PrivacySmokeSurfaceLoki} {
		for _, tt := range tests {
			t.Run(string(surface)+"/"+tt.name, func(t *testing.T) {
				request := t190Request(surface)
				surfaces, _, closeServer := t190Surfaces(t, t190Handler(request, tt.text))
				defer closeServer()
				evidence, err := surfaces.Scan(context.Background(), request)
				if err != nil {
					t.Fatalf("Scan failed with class %q", t190ErrorClass(err))
				}
				assertT190Counts(t, evidence.Counts(), tt.category)
			})
		}
	}

	t.Run("decoded unicode escape", func(t *testing.T) {
		request := t190Request(smoke.PrivacySmokeSurfaceLoki)
		handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			timestamp := request.StartedAt.Add(time.Second).UnixNano()
			// JSON 中没有 canary 的明文字节；只有解码后扫描语义值才能命中。
			body := strings.ReplaceAll(t190Canary, "T", `\u0054`)
			_, _ = writer.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[{"stream":{"service_name":"longtermism"},"values":[["` +
				strconv.FormatInt(timestamp, 10) + `","http request completed",{"smoke_run_id":"` + request.Marker + `","request_id":"` + request.RequestID +
				`","ai_trace_id":"` + request.AITraceID + `","trace_id":"` + request.ServiceTraceID + `","span_id":"` + request.SpanID +
				`","privacy_test_value":"` + body + `"}]]}]}}`))
		})
		surfaces, _, closeServer := t190Surfaces(t, handler)
		defer closeServer()
		evidence, err := surfaces.Scan(context.Background(), request)
		if err != nil {
			t.Fatalf("Scan failed with class %q", t190ErrorClass(err))
		}
		assertT190Counts(t, evidence.Counts(), "synthetic_canary")
	})

	t.Run("Loki line not metadata", func(t *testing.T) {
		request := t190Request(smoke.PrivacySmokeSurfaceLoki)
		handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
			response := t190LokiResponse(request, "safe-value", 1)
			values := response["data"].(map[string]any)["result"].([]any)[0].(map[string]any)["values"].([]any)
			values[0].([]any)[1] = "http request completed " + t190Canary
			writeT190JSON(writer, response)
		})
		surfaces, _, closeServer := t190Surfaces(t, handler)
		defer closeServer()
		evidence, err := surfaces.Scan(context.Background(), request)
		if err != nil {
			t.Fatalf("Scan failed: %q", t190ErrorClass(err))
		}
		assertT190Counts(t, evidence.Counts(), "synthetic_canary")
	})

	t.Run("Tempo span event not attributes", func(t *testing.T) {
		request := t190Request(smoke.PrivacySmokeSurfaceTempo)
		handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
			if incoming.URL.Path == "/api/search" {
				writeT190JSON(writer, t190TempoSearch(request, 1))
				return
			}
			document := t190TempoDocument(request, "safe-value")
			span := document["trace"].(map[string]any)["batches"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)[0].(map[string]any)
			span["events"] = []any{map[string]any{"timeUnixNano": strconv.FormatInt(request.StartedAt.Add(time.Second).UnixNano(), 10), "name": t190Canary}}
			writeT190JSON(writer, document)
		})
		surfaces, _, closeServer := t190Surfaces(t, handler)
		defer closeServer()
		evidence, err := surfaces.Scan(context.Background(), request)
		if err != nil {
			t.Fatalf("Scan failed: %q", t190ErrorClass(err))
		}
		assertT190Counts(t, evidence.Counts(), "synthetic_canary")
	})

	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceTempo, smoke.PrivacySmokeSurfaceLoki} {
		t.Run(string(surface)+" unknown nested escaped value", func(t *testing.T) {
			request := t190Request(surface)
			handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				if surface == smoke.PrivacySmokeSurfaceTempo && incoming.URL.Path == "/api/search" {
					writeT190JSON(writer, t190TempoSearch(request, 1))
					return
				}
				var document map[string]any
				if surface == smoke.PrivacySmokeSurfaceTempo {
					document = t190TempoDocument(request, "safe")
				} else {
					document = t190LokiResponse(request, "safe", 1)
				}
				encoded, _ := json.Marshal(document)
				encoded = encoded[:len(encoded)-1]
				_, _ = writer.Write(append(encoded, []byte(`,"unknown_nested":{"value":"\u0054190_SYNTHETIC_CANARY"}}`)...))
			})
			surfaces, _, closeServer := t190Surfaces(t, handler)
			defer closeServer()
			evidence, err := surfaces.Scan(context.Background(), request)
			if err != nil {
				t.Fatalf("Scan failed: %q", t190ErrorClass(err))
			}
			assertT190Counts(t, evidence.Counts(), "synthetic_canary")
		})
	}
}

func TestPrivacyGrafanaSurfacesFailClosedOnMissingAmbiguousOrForeignFacts(t *testing.T) {
	tests := []struct {
		name    string
		surface smoke.PrivacySmokeSurface
		handler func(PrivacyGrafanaScanRequest) http.Handler
	}{
		{"tempo missing", smoke.PrivacySmokeSurfaceTempo, func(request PrivacyGrafanaScanRequest) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				writeT190JSON(writer, t190TempoSearch(request, 0))
			})
		}},
		{"tempo ambiguous", smoke.PrivacySmokeSurfaceTempo, func(request PrivacyGrafanaScanRequest) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				writeT190JSON(writer, t190TempoSearch(request, 2))
			})
		}},
		{"tempo foreign search trace", smoke.PrivacySmokeSurfaceTempo, func(request PrivacyGrafanaScanRequest) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				foreign := request
				foreign.ServiceTraceID = "fedcba9876543210fedcba9876543210"
				writeT190JSON(writer, t190TempoSearch(foreign, 1))
			})
		}},
		{"tempo search outside window", smoke.PrivacySmokeSurfaceTempo, func(request PrivacyGrafanaScanRequest) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				foreign := request
				foreign.StartedAt = request.StartedAt.Add(-time.Minute)
				writeT190JSON(writer, t190TempoSearch(foreign, 1))
			})
		}},
		{"tempo incomplete search", smoke.PrivacySmokeSurfaceTempo, func(request PrivacyGrafanaScanRequest) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				response := t190TempoSearch(request, 1)
				response["metrics"] = map[string]any{"completedJobs": 1, "totalJobs": 2}
				writeT190JSON(writer, response)
			})
		}},
		{"tempo missing completeness", smoke.PrivacySmokeSurfaceTempo, func(request PrivacyGrafanaScanRequest) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				response := t190TempoSearch(request, 1)
				response["metrics"] = map[string]any{"inspectedTraces": 1}
				writeT190JSON(writer, response)
			})
		}},
		{"tempo foreign document", smoke.PrivacySmokeSurfaceTempo, func(request PrivacyGrafanaScanRequest) http.Handler {
			foreign := request
			foreign.RequestID = "foreign-request"
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				if incoming.URL.Path == "/api/search" {
					writeT190JSON(writer, t190TempoSearch(request, 1))
					return
				}
				writeT190JSON(writer, t190TempoDocument(foreign, "safe-value"))
			})
		}},
		{"tempo span outside window", smoke.PrivacySmokeSurfaceTempo, func(request PrivacyGrafanaScanRequest) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				if incoming.URL.Path == "/api/search" {
					writeT190JSON(writer, t190TempoSearch(request, 1))
					return
				}
				foreign := request
				foreign.StartedAt = request.StartedAt.Add(-time.Minute)
				writeT190JSON(writer, t190TempoDocument(foreign, "safe-value"))
			})
		}},
		{"tempo partial document", smoke.PrivacySmokeSurfaceTempo, func(request PrivacyGrafanaScanRequest) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				if incoming.URL.Path == "/api/search" {
					writeT190JSON(writer, t190TempoSearch(request, 1))
					return
				}
				response := t190TempoDocument(request, "safe-value")
				response["status"] = "PARTIAL"
				response["message"] = t190Raw
				writeT190JSON(writer, response)
			})
		}},
		{"loki missing", smoke.PrivacySmokeSurfaceLoki, func(request PrivacyGrafanaScanRequest) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				writeT190JSON(writer, t190LokiResponse(request, "safe", 0))
			})
		}},
		{"loki duplicate", smoke.PrivacySmokeSurfaceLoki, func(request PrivacyGrafanaScanRequest) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				writeT190JSON(writer, t190LokiResponse(request, "safe", 2))
			})
		}},
		{"loki foreign identity", smoke.PrivacySmokeSurfaceLoki, func(request PrivacyGrafanaScanRequest) http.Handler {
			foreign := request
			foreign.Marker = "foreign-marker"
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				writeT190JSON(writer, t190LokiResponse(foreign, "safe", 1))
			})
		}},
		{"loki outside window", smoke.PrivacySmokeSurfaceLoki, func(request PrivacyGrafanaScanRequest) http.Handler {
			foreign := request
			foreign.StartedAt = request.StartedAt.Add(-time.Minute)
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				writeT190JSON(writer, t190LokiResponse(foreign, "safe", 1))
			})
		}},
		{"loki unsuccessful envelope", smoke.PrivacySmokeSurfaceLoki, func(request PrivacyGrafanaScanRequest) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				response := t190LokiResponse(request, "safe", 1)
				response["status"] = "error"
				writeT190JSON(writer, response)
			})
		}},
		{"loki wrong result type", smoke.PrivacySmokeSurfaceLoki, func(request PrivacyGrafanaScanRequest) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				response := t190LokiResponse(request, "safe", 1)
				response["data"].(map[string]any)["resultType"] = "matrix"
				writeT190JSON(writer, response)
			})
		}},
		{"loki identity only in line", smoke.PrivacySmokeSurfaceLoki, func(request PrivacyGrafanaScanRequest) http.Handler {
			return http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				response := t190LokiResponse(request, "safe", 1)
				value := response["data"].(map[string]any)["result"].([]any)[0].(map[string]any)["values"].([]any)[0].([]any)
				value[1] = request.Marker + request.RequestID + request.AITraceID + request.ServiceTraceID + request.SpanID
				value[2] = map[string]string{"smoke_run_id": "foreign-marker"}
				writeT190JSON(writer, response)
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := t190Request(tt.surface)
			surfaces, log, closeServer := t190Surfaces(t, tt.handler(request))
			defer closeServer()
			evidence, err := surfaces.Scan(context.Background(), request)
			if err == nil || !reflect.ValueOf(evidence).IsZero() {
				t.Fatal("missing, ambiguous, paginated, or foreign platform facts became zero evidence")
			}
			if tt.name == "tempo partial document" && strings.Contains(err.Error(), t190Raw) {
				t.Fatal("Tempo partial-status error exposed the backend message")
			}
			for _, secret := range []string{t190Raw, request.ForbiddenCanary, request.Marker, request.RequestID, request.AITraceID, request.ServiceTraceID, request.SpanID} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("semantic failure exposed remote or identity content")
				}
			}
			if tt.surface == smoke.PrivacySmokeSurfaceTempo && strings.Contains(tt.name, "search") && len(log.snapshot()) > 1 {
				t.Fatal("invalid Tempo search result reached the trace-document endpoint")
			}
		})
	}

	t.Run("Loki result at limit is incomplete", func(t *testing.T) {
		request := t190Request(smoke.PrivacySmokeSurfaceLoki)
		request.Limit = 1
		surfaces, log, closeServer := t190Surfaces(t, http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
			writeT190JSON(writer, t190LokiResponse(request, "safe", 1))
		}))
		defer closeServer()
		if evidence, err := surfaces.Scan(context.Background(), request); err == nil || !reflect.ValueOf(evidence).IsZero() {
			t.Fatal("result count equal to limit became complete zero evidence")
		}
		requests := log.snapshot()
		if len(requests) != 1 || requests[0].URL.Query().Get("limit") != "1" {
			t.Fatal("bounded request limit was not sent to Loki")
		}
	})
}

// TestPrivacyGrafanaSurfacesRevalidateEveryReturnedIdentity prevents a precise outbound query
// from being mistaken for proof that the backend returned the same fact. Every identity is
// revalidated in the actual Tempo resource/span document or Loki structured metadata.
func TestPrivacyGrafanaSurfacesRevalidateEveryReturnedIdentity(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*PrivacyGrafanaScanRequest)
	}{
		{"marker", func(value *PrivacyGrafanaScanRequest) { value.Marker = "foreign-marker" }},
		{"request", func(value *PrivacyGrafanaScanRequest) { value.RequestID = "foreign-request" }},
		{"ai-trace", func(value *PrivacyGrafanaScanRequest) { value.AITraceID = "foreign-ai-trace" }},
		{"service-trace", func(value *PrivacyGrafanaScanRequest) { value.ServiceTraceID = "fedcba9876543210fedcba9876543210" }},
		{"span", func(value *PrivacyGrafanaScanRequest) { value.SpanID = "fedcba9876543210" }},
	}
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceTempo, smoke.PrivacySmokeSurfaceLoki} {
		for _, mutation := range mutations {
			t.Run(string(surface)+"/"+mutation.name, func(t *testing.T) {
				request := t190Request(surface)
				foreign := request
				mutation.mutate(&foreign)
				handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
					if surface == smoke.PrivacySmokeSurfaceTempo && incoming.URL.Path == "/api/search" {
						writeT190JSON(writer, t190TempoSearch(request, 1))
						return
					}
					if surface == smoke.PrivacySmokeSurfaceTempo {
						writeT190JSON(writer, t190TempoDocument(foreign, "safe-value"))
						return
					}
					writeT190JSON(writer, t190LokiResponse(foreign, "safe-value", 1))
				})
				surfaces, _, closeServer := t190Surfaces(t, handler)
				defer closeServer()
				evidence, err := surfaces.Scan(context.Background(), request)
				if err == nil || !reflect.ValueOf(evidence).IsZero() {
					t.Fatal("foreign returned identity became valid zero evidence")
				}
			})
		}
	}
}

func TestPrivacyGrafanaSurfacesRejectMissingReturnedIdentity(t *testing.T) {
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceTempo, smoke.PrivacySmokeSurfaceLoki} {
		for _, field := range []string{"marker", "request", "ai_trace", "service_trace", "span"} {
			t.Run(string(surface)+"/"+field, func(t *testing.T) {
				request := t190Request(surface)
				handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
					if surface == smoke.PrivacySmokeSurfaceTempo && incoming.URL.Path == "/api/search" {
						writeT190JSON(writer, t190TempoSearch(request, 1))
						return
					}
					if surface == smoke.PrivacySmokeSurfaceLoki {
						response := t190LokiResponse(request, "safe", 1)
						metadata := response["data"].(map[string]any)["result"].([]any)[0].(map[string]any)["values"].([]any)[0].([]any)[2].(map[string]string)
						delete(metadata, map[string]string{"marker": "smoke_run_id", "request": "request_id", "ai_trace": "ai_trace_id", "service_trace": "trace_id", "span": "span_id"}[field])
						writeT190JSON(writer, response)
						return
					}
					document := t190TempoDocument(request, "safe")
					resource := document["trace"].(map[string]any)["batches"].([]any)[0].(map[string]any)
					if field == "service_trace" || field == "span" {
						span := resource["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)[0].(map[string]any)
						if field == "service_trace" {
							delete(span, "traceId")
						} else {
							delete(span, "spanId")
						}
					} else {
						key := map[string]string{"marker": "longtermism.smoke.run_id", "request": "request.id", "ai_trace": "longtermism.ai.trace_id"}[field]
						attrs := resource["resource"].(map[string]any)["attributes"].([]any)
						filtered := attrs[:0]
						for _, raw := range attrs {
							if raw.(map[string]any)["key"] != key {
								filtered = append(filtered, raw)
							}
						}
						resource["resource"].(map[string]any)["attributes"] = filtered
					}
					writeT190JSON(writer, document)
				})
				surfaces, _, closeServer := t190Surfaces(t, handler)
				defer closeServer()
				if evidence, err := surfaces.Scan(context.Background(), request); err == nil || !reflect.ValueOf(evidence).IsZero() {
					t.Fatal("missing returned identity was backfilled from target")
				}
			})
		}
	}
}

func TestPrivacyGrafanaSurfacesRequireProtectedCompleteClientAndSafeInput(t *testing.T) {
	if _, err := NewPrivacyGrafanaSurfaces(nil); err == nil {
		t.Fatal("nil client was accepted")
	}
	ordinary := NewGrafanaQueryClient(GrafanaQueryConfig{LokiURL: "http://127.0.0.1:1", TempoURL: "http://127.0.0.1:1"})
	if _, err := NewPrivacyGrafanaSurfaces(ordinary); err == nil {
		t.Fatal("ordinary non-smoke Grafana client was accepted")
	}
	for _, missing := range []string{"loki", "tempo"} {
		config := GrafanaQueryConfig{LokiURL: "http://127.0.0.1:1", TempoURL: "http://127.0.0.1:1"}
		if missing == "loki" {
			config.LokiURL = ""
		} else {
			config.TempoURL = ""
		}
		client, err := NewGrafanaSmokeQueryClient(config)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = NewPrivacyGrafanaSurfaces(client); err == nil {
			t.Fatalf("protected client missing %s endpoint was accepted", missing)
		}
	}
	protected, _, protectedClose := t190ProtectedClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer protectedClose()
	transport, ok := protected.httpClient.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("protected Grafana transport can consult environment proxies")
	}

	t.Run("dial-time DNS revalidation", func(t *testing.T) {
		serverCalls := 0
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { serverCalls++ }))
		defer server.Close()
		localhostURL := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
		resolveCalls := 0
		client, err := NewGrafanaSmokeQueryClient(GrafanaQueryConfig{
			LokiURL: localhostURL, TempoURL: localhostURL,
			ResolveHost: func(context.Context, string) ([]net.IP, error) {
				resolveCalls++
				if resolveCalls <= 2 {
					return []net.IP{net.ParseIP("127.0.0.1")}, nil
				}
				return []net.IP{net.ParseIP("192.0.2.10")}, nil
			},
		})
		if err != nil {
			t.Fatalf("constructor unexpectedly failed: %v", err)
		}
		adapter, err := NewPrivacyGrafanaSurfaces(client)
		if err != nil {
			t.Fatalf("adapter constructor failed: %v", err)
		}
		if _, err = adapter.Scan(context.Background(), t190Request(smoke.PrivacySmokeSurfaceLoki)); err == nil || serverCalls != 0 {
			t.Fatal("dial-time DNS rebinding reached the server")
		}
	})

	request := t190Request(smoke.PrivacySmokeSurfaceLoki)
	surfaces, log, closeServer := t190Surfaces(t, t190Handler(request, "safe"))
	defer closeServer()
	tests := []func(*PrivacyGrafanaScanRequest){
		func(value *PrivacyGrafanaScanRequest) { value.Surface = smoke.PrivacySmokeSurfaceAPI },
		func(value *PrivacyGrafanaScanRequest) { value.Marker = `bad" | unwrap secret` },
		func(value *PrivacyGrafanaScanRequest) { value.RunID = "bad\nrun" },
		func(value *PrivacyGrafanaScanRequest) { value.RequestID = "bad|request" },
		func(value *PrivacyGrafanaScanRequest) { value.AITraceID = "bad}ai" },
		func(value *PrivacyGrafanaScanRequest) { value.ServiceTraceID = "not-hex" },
		func(value *PrivacyGrafanaScanRequest) { value.SpanID = "bad-span" },
		func(value *PrivacyGrafanaScanRequest) { value.ForbiddenCanary = "" },
		func(value *PrivacyGrafanaScanRequest) { value.ForbiddenCanary = "bad canary" },
		func(value *PrivacyGrafanaScanRequest) { value.StartedAt = time.Time{} },
		func(value *PrivacyGrafanaScanRequest) { value.Deadline = value.StartedAt.Add(2 * time.Minute) },
		func(value *PrivacyGrafanaScanRequest) { value.Limit = 101 },
		func(value *PrivacyGrafanaScanRequest) { value.Limit = 0 },
		func(value *PrivacyGrafanaScanRequest) { value.Deadline = value.StartedAt },
		func(value *PrivacyGrafanaScanRequest) {
			value.Deadline = time.Now().UTC().Add(-2 * time.Minute)
			value.StartedAt = value.Deadline.Add(-time.Second)
		},
	}
	for _, mutate := range tests {
		candidate := request
		mutate(&candidate)
		if _, err := surfaces.Scan(context.Background(), candidate); err == nil {
			t.Fatal("unsafe request was accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := surfaces.Scan(ctx, request); err == nil {
		t.Fatal("canceled context was accepted")
	}
	if len(log.snapshot()) != 0 {
		t.Fatal("unsafe request reached the network")
	}
}

func TestPrivacyGrafanaSurfacesRejectRedirectOversizeAndMalformedBodies(t *testing.T) {
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceLoki, smoke.PrivacySmokeSurfaceTempo} {
		request := t190Request(surface)
		for _, tt := range []struct {
			name    string
			handler http.Handler
		}{
			{"malformed", http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) { _, _ = writer.Write([]byte(`{"status":`)) })},
			{"trailing", http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) { _, _ = writer.Write([]byte(`{} {}`)) })},
			{"oversize", http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				_, _ = writer.Write([]byte(`{"x":"` + strings.Repeat("a", maximumBackendResponseSize) + `"}`))
			})},
		} {
			t.Run(string(surface)+"/"+tt.name, func(t *testing.T) {
				handler := tt.handler
				if surface == smoke.PrivacySmokeSurfaceTempo {
					handler = http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
						if incoming.URL.Path == "/api/search" {
							writeT190JSON(writer, t190TempoSearch(request, 1))
							return
						}
						tt.handler.ServeHTTP(writer, incoming)
					})
				}
				surfaces, _, closeServer := t190Surfaces(t, handler)
				defer closeServer()
				if _, err := surfaces.Scan(context.Background(), request); err == nil {
					t.Fatal("unsafe response was accepted")
				}
			})
		}
	}

	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceLoki, smoke.PrivacySmokeSurfaceTempo} {
		request := t190Request(surface)
		redirectTargetCalls := 0
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectTargetCalls++ }))
		defer target.Close()
		redirect := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
			if surface == smoke.PrivacySmokeSurfaceTempo && incoming.URL.Path == "/api/search" {
				writeT190JSON(writer, t190TempoSearch(request, 1))
				return
			}
			writer.Header().Set("Location", target.URL)
			writer.WriteHeader(http.StatusTemporaryRedirect)
		})
		surfaces, _, closeServer := t190Surfaces(t, redirect)
		defer closeServer()
		if _, err := surfaces.Scan(context.Background(), request); err == nil || redirectTargetCalls != 0 {
			t.Fatal("protected Grafana client followed a redirect")
		}
	}
}
