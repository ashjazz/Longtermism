package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	localeval "github.com/ashjazz/Longtermism/internal/eval"
	"github.com/ashjazz/Longtermism/internal/observability/langfuse"
	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

// TestPrivacyLangfuseSurfacesReadAndScanRealVersionedDocuments locks the protocol split for
// self-hosted 3.185: observations v1 returns full rows with page metadata, while scores v3
// returns core+details+subject with cursor metadata. Neither low-sensitivity smoke DTO is enough.
func TestPrivacyLangfuseSurfacesReadAndScanRealVersionedDocuments(t *testing.T) {
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceLangfuseTrace, smoke.PrivacySmokeSurfaceLangfuseScore} {
		t.Run(string(surface), func(t *testing.T) {
			request := t191Request(surface)
			surfaces, log, store, closeServer := t191Surfaces(t, request, t191Handler(request, "safe-value"), []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
			defer closeServer()
			evidence, err := surfaces.Scan(context.Background(), request)
			if err != nil {
				t.Fatalf("Scan failed with class %q", t191ErrorClass(err))
			}
			if evidence.Surface() != surface || evidence.EvidenceMethod() != "bounded_platform_document" || evidence.ScannerPolicyVersion() != "1" {
				t.Fatal("evidence did not retain the closed Langfuse contract")
			}
			assertT191Counts(t, evidence.Counts(), "")
			counts := evidence.Counts()
			counts["token"] = 99
			assertT191Counts(t, evidence.Counts(), "")
			requests := log.snapshot()
			if len(requests) != 1 {
				t.Fatalf("requests=%d want=1", len(requests))
			}
			assertT191Query(t, requests[0], request)
			if surface == smoke.PrivacySmokeSurfaceLangfuseTrace {
				if requests[0].URL.Path != "/api/public/observations" || strings.Contains(requests[0].URL.Path, "/v2/") {
					t.Fatal("trace did not use self-hosted v3 observations v1")
				}
				if len(store.runIDs) != 0 {
					t.Fatal("trace scan consulted score projection state")
				}
			} else {
				if requests[0].URL.Path != "/api/public/v3/scores" {
					t.Fatal("score did not use scores v3")
				}
				if len(store.runIDs) != 1 || store.runIDs[0] != request.Marker {
					t.Fatal("score did not bind the run to one local sent projection")
				}
			}
		})
	}

	t.Run("trace status message", func(t *testing.T) {
		request := t191Request(smoke.PrivacySmokeSurfaceLangfuseTrace)
		handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
			response := t191TraceResponse(request, "safe", 1)
			response["data"].([]any)[0].(map[string]any)["statusMessage"] = t191Canary
			writeT191JSON(writer, response)
		})
		surfaces, _, _, closeServer := t191Surfaces(t, request, handler, []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
		defer closeServer()
		evidence, err := surfaces.Scan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		assertT191Counts(t, evidence.Counts(), "synthetic_canary")
	})
	t.Run("score metadata", func(t *testing.T) {
		request := t191Request(smoke.PrivacySmokeSurfaceLangfuseScore)
		handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
			response := t191ScoreResponse(request, "safe", 1)
			response["data"].([]any)[0].(map[string]any)["metadata"].(map[string]any)["nested"] = t191Canary
			writeT191JSON(writer, response)
		})
		surfaces, _, _, closeServer := t191Surfaces(t, request, handler, []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
		defer closeServer()
		evidence, err := surfaces.Scan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		assertT191Counts(t, evidence.Counts(), "synthetic_canary")
	})
	t.Run("trace output and model parameters", func(t *testing.T) {
		request := t191Request(smoke.PrivacySmokeSurfaceLangfuseTrace)
		handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			response := t191TraceResponse(request, "safe", 1)
			row := response["data"].([]any)[0].(map[string]any)
			row["output"] = map[string]any{"content": t191Canary}
			row["modelParameters"] = map[string]any{"stop": []any{"\u0054191_SYNTHETIC_CANARY"}}
			writeT191JSON(writer, response)
		})
		surfaces, _, _, closeServer := t191Surfaces(t, request, handler, []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
		defer closeServer()
		evidence, err := surfaces.Scan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if evidence.Counts()["synthetic_canary"] == 0 {
			t.Fatal("trace output and model parameters were not scanned")
		}
	})
	t.Run("score text value", func(t *testing.T) {
		request := t191Request(smoke.PrivacySmokeSurfaceLangfuseScore)
		handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			response := t191ScoreResponse(request, "safe", 1)
			row := response["data"].([]any)[0].(map[string]any)
			row["dataType"], row["value"] = "TEXT", t191Canary
			writeT191JSON(writer, response)
		})
		surfaces, _, _, closeServer := t191Surfaces(t, request, handler, []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
		defer closeServer()
		evidence, err := surfaces.Scan(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		assertT191Counts(t, evidence.Counts(), "synthetic_canary")
	})
}

func TestPrivacyLangfuseSurfacesDetectEveryClosedCategory(t *testing.T) {
	tests := []struct{ name, text, category string }{
		{"canary", t191Canary, "synthetic_canary"}, {"credential", "api_key=sk-t191-fixture", "credential"},
		{"authorization", "Authorization: Bearer t191-fixture", "authorization"}, {"token", "token=t191.fixture", "token"},
		{"pii", "owner@example.com", "recognized_pii"},
	}
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceLangfuseTrace, smoke.PrivacySmokeSurfaceLangfuseScore} {
		for _, tt := range tests {
			t.Run(string(surface)+"/"+tt.name, func(t *testing.T) {
				request := t191Request(surface)
				surfaces, _, _, closeServer := t191Surfaces(t, request, t191Handler(request, tt.text), []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
				defer closeServer()
				evidence, err := surfaces.Scan(context.Background(), request)
				if err != nil {
					t.Fatalf("Scan failed with class %q", t191ErrorClass(err))
				}
				assertT191Counts(t, evidence.Counts(), tt.category)
			})
		}
	}

	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceLangfuseTrace, smoke.PrivacySmokeSurfaceLangfuseScore} {
		t.Run(string(surface)+" decoded unknown field", func(t *testing.T) {
			request := t191Request(surface)
			handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				var response map[string]any
				if surface == smoke.PrivacySmokeSurfaceLangfuseTrace {
					response = t191TraceResponse(request, "safe", 1)
				} else {
					response = t191ScoreResponse(request, "safe", 1)
				}
				encoded, _ := json.Marshal(response)
				encoded = encoded[:len(encoded)-1]
				_, _ = writer.Write(append(encoded, []byte(`,"unknown_nested":{"value":"\u0054191_SYNTHETIC_CANARY"}}`)...))
			})
			surfaces, _, _, closeServer := t191Surfaces(t, request, handler, []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
			defer closeServer()
			evidence, err := surfaces.Scan(context.Background(), request)
			if err != nil {
				t.Fatalf("decoded unknown field failed: %q", t191ErrorClass(err))
			}
			assertT191Counts(t, evidence.Counts(), "synthetic_canary")
		})

		t.Run(string(surface)+" raw duplicate key", func(t *testing.T) {
			request := t191Request(surface)
			handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				var response map[string]any
				if surface == smoke.PrivacySmokeSurfaceLangfuseTrace {
					response = t191TraceResponse(request, "safe", 1)
				} else {
					response = t191ScoreResponse(request, "safe", 1)
				}
				encoded, _ := json.Marshal(response)
				encoded = encoded[:len(encoded)-1]
				_, _ = writer.Write(append(encoded, []byte(`,"duplicate":"T191_SYNTHETIC_CANARY","duplicate":"safe"}`)...))
			})
			surfaces, _, _, closeServer := t191Surfaces(t, request, handler, []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
			defer closeServer()
			evidence, err := surfaces.Scan(context.Background(), request)
			if err == nil {
				assertT191Counts(t, evidence.Counts(), "synthetic_canary")
			}
		})
	}
}

// A bounded platform document is scanned before it is accepted as unique evidence. This
// preserves a confirmed leak count even when a second row also makes the response ambiguous.
func TestPrivacyLangfuseSurfacesScanEveryReturnedRowBeforeSemanticRejection(t *testing.T) {
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceLangfuseTrace, smoke.PrivacySmokeSurfaceLangfuseScore} {
		t.Run(string(surface)+" second row leak", func(t *testing.T) {
			request := t191Request(surface)
			handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				var response map[string]any
				if surface == smoke.PrivacySmokeSurfaceLangfuseTrace {
					response = t191TraceResponse(request, "safe", 2)
					response["data"].([]any)[1].(map[string]any)["statusMessage"] = t191Canary
				} else {
					response = t191ScoreResponse(request, "safe", 2)
					response["data"].([]any)[1].(map[string]any)["metadata"] = map[string]any{"nested": t191Canary}
				}
				writeT191JSON(writer, response)
			})
			adapter, _, _, closeServer := t191Surfaces(t, request, handler, []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
			defer closeServer()
			evidence, err := adapter.Scan(context.Background(), request)
			if err != nil {
				t.Fatalf("confirmed second-row leak was discarded: %q", t191ErrorClass(err))
			}
			assertT191Counts(t, evidence.Counts(), "synthetic_canary")
		})

		t.Run(string(surface)+" second row conflict", func(t *testing.T) {
			request := t191Request(surface)
			handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				var response map[string]any
				if surface == smoke.PrivacySmokeSurfaceLangfuseTrace {
					response = t191TraceResponse(request, "safe", 2)
					response["data"].([]any)[1].(map[string]any)["traceId"] = "fedcba9876543210fedcba9876543210"
				} else {
					response = t191ScoreResponse(request, "safe", 2)
					response["data"].([]any)[1].(map[string]any)["subject"].(map[string]any)["traceId"] = "fedcba9876543210fedcba9876543210"
				}
				writeT191JSON(writer, response)
			})
			adapter, _, _, closeServer := t191Surfaces(t, request, handler, []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
			defer closeServer()
			if evidence, err := adapter.Scan(context.Background(), request); err == nil || !reflect.ValueOf(evidence).IsZero() {
				t.Fatal("second-row identity conflict became evidence")
			}
		})
	}
}

func TestPrivacyLangfuseTraceRejectsMissingDuplicateForeignWindowOrPaginationFacts(t *testing.T) {
	request := t191Request(smoke.PrivacySmokeSurfaceLangfuseTrace)
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing", func(response map[string]any) { response["data"] = []any{} }},
		{"duplicate", func(response map[string]any) {
			response["data"] = append(response["data"].([]any), response["data"].([]any)[0])
		}},
		{"foreign marker", func(response map[string]any) {
			response["data"].([]any)[0].(map[string]any)["metadata"].(map[string]any)["longtermism.smoke.run_id"] = "foreign"
		}},
		{"missing marker", func(response map[string]any) {
			delete(response["data"].([]any)[0].(map[string]any)["metadata"].(map[string]any), "longtermism.smoke.run_id")
		}},
		{"missing request", func(response map[string]any) {
			delete(response["data"].([]any)[0].(map[string]any)["metadata"].(map[string]any), "request_id")
		}},
		{"foreign request", func(response map[string]any) {
			response["data"].([]any)[0].(map[string]any)["metadata"].(map[string]any)["request_id"] = "foreign"
		}},
		{"foreign AI", func(response map[string]any) {
			response["data"].([]any)[0].(map[string]any)["metadata"].(map[string]any)["ai_trace_id"] = "foreign"
		}},
		{"missing AI", func(response map[string]any) {
			delete(response["data"].([]any)[0].(map[string]any)["metadata"].(map[string]any), "ai_trace_id")
		}},
		{"foreign trace", func(response map[string]any) {
			response["data"].([]any)[0].(map[string]any)["traceId"] = "fedcba9876543210fedcba9876543210"
		}},
		{"missing trace", func(response map[string]any) { delete(response["data"].([]any)[0].(map[string]any), "traceId") }},
		{"missing span", func(response map[string]any) { delete(response["data"].([]any)[0].(map[string]any), "id") }},
		{"foreign span", func(response map[string]any) { response["data"].([]any)[0].(map[string]any)["id"] = "fedcba9876543210" }},
		{"missing time", func(response map[string]any) { delete(response["data"].([]any)[0].(map[string]any), "startTime") }},
		{"before window", func(response map[string]any) {
			response["data"].([]any)[0].(map[string]any)["startTime"] = request.StartedAt.Add(-time.Nanosecond).Format(time.RFC3339Nano)
		}},
		{"outside window", func(response map[string]any) {
			response["data"].([]any)[0].(map[string]any)["startTime"] = request.Deadline.Format(time.RFC3339Nano)
		}},
		{"second page", func(response map[string]any) { response["meta"].(map[string]any)["page"] = 2 }},
		{"truncated", func(response map[string]any) { response["meta"].(map[string]any)["totalPages"] = 2 }},
		{"limit mismatch", func(response map[string]any) { response["meta"].(map[string]any)["limit"] = 99 }},
		{"total mismatch", func(response map[string]any) { response["meta"].(map[string]any)["totalItems"] = 2 }},
		{"missing meta", func(response map[string]any) { delete(response, "meta") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				response := t191TraceResponse(request, "safe", 1)
				tt.mutate(response)
				writeT191JSON(writer, response)
			})
			surfaces, _, _, closeServer := t191Surfaces(t, request, handler, []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
			defer closeServer()
			evidence, err := surfaces.Scan(context.Background(), request)
			if err == nil || !reflect.ValueOf(evidence).IsZero() {
				t.Fatal("invalid observations v1 response became zero evidence")
			}
			assertT191LowSensitive(t, err, request)
		})
	}
}

func TestPrivacyLangfuseScoreRequiresUniqueSentLocalProjectionBeforeNetwork(t *testing.T) {
	request := t191Request(smoke.PrivacySmokeSurfaceLangfuseScore)
	valid := t191Snapshot(request)
	tests := []struct {
		name    string
		records []localeval.ScoreProjectionSnapshot
	}{
		{"missing", nil}, {"duplicate", []localeval.ScoreProjectionSnapshot{valid, valid}},
		{"unsent", []localeval.ScoreProjectionSnapshot{func() localeval.ScoreProjectionSnapshot {
			v := valid
			v.Status = langfuse.ScoreProjectionStatusQueued
			return v
		}()}},
		{"foreign run", []localeval.ScoreProjectionSnapshot{func() localeval.ScoreProjectionSnapshot { v := valid; v.RunID = "foreign"; return v }()}},
		{"foreign request", []localeval.ScoreProjectionSnapshot{func() localeval.ScoreProjectionSnapshot { v := valid; v.RequestID = "foreign"; return v }()}},
		{"foreign AI", []localeval.ScoreProjectionSnapshot{func() localeval.ScoreProjectionSnapshot { v := valid; v.AITraceID = "foreign"; return v }()}},
		{"foreign trace", []localeval.ScoreProjectionSnapshot{func() localeval.ScoreProjectionSnapshot {
			v := valid
			v.PlatformTraceID = "fedcba9876543210fedcba9876543210"
			return v
		}()}},
		{"foreign observation", []localeval.ScoreProjectionSnapshot{func() localeval.ScoreProjectionSnapshot {
			v := valid
			v.PlatformObservationID = "fedcba9876543210"
			return v
		}()}},
		{"invalid projection", []localeval.ScoreProjectionSnapshot{func() localeval.ScoreProjectionSnapshot { v := valid; v.ProjectionID = ""; return v }()}},
		{"invalid eval run", []localeval.ScoreProjectionSnapshot{func() localeval.ScoreProjectionSnapshot { v := valid; v.EvalRunID = ""; return v }()}},
		{"negative attempt", []localeval.ScoreProjectionSnapshot{func() localeval.ScoreProjectionSnapshot { v := valid; v.Attempt = -1; return v }()}},
		{"missing created time", []localeval.ScoreProjectionSnapshot{func() localeval.ScoreProjectionSnapshot { v := valid; v.CreatedAt = time.Time{}; return v }()}},
		{"missing observed time", []localeval.ScoreProjectionSnapshot{func() localeval.ScoreProjectionSnapshot { v := valid; v.ObservedAt = time.Time{}; return v }()}},
		{"observed before window", []localeval.ScoreProjectionSnapshot{func() localeval.ScoreProjectionSnapshot {
			v := valid
			v.ObservedAt = request.StartedAt.Add(-time.Nanosecond)
			return v
		}()}},
		{"observed at deadline", []localeval.ScoreProjectionSnapshot{func() localeval.ScoreProjectionSnapshot { v := valid; v.ObservedAt = request.Deadline; return v }()}},
		{"created after observed", []localeval.ScoreProjectionSnapshot{func() localeval.ScoreProjectionSnapshot {
			v := valid
			v.CreatedAt = v.ObservedAt.Add(time.Nanosecond)
			return v
		}()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ })
			surfaces, _, _, closeServer := t191Surfaces(t, request, handler, tt.records)
			defer closeServer()
			evidence, err := surfaces.Scan(context.Background(), request)
			if err == nil || !reflect.ValueOf(evidence).IsZero() || calls != 0 {
				t.Fatal("invalid local projection reached Langfuse or became evidence")
			}
			assertT191LowSensitive(t, err, request)
		})
	}
}

func TestPrivacyLangfuseScoreRejectsMissingDuplicateForeignWindowOrCursorFacts(t *testing.T) {
	request := t191Request(smoke.PrivacySmokeSurfaceLangfuseScore)
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing", func(response map[string]any) { response["data"] = []any{} }},
		{"duplicate", func(response map[string]any) {
			response["data"] = append(response["data"].([]any), response["data"].([]any)[0])
		}},
		{"foreign id", func(response map[string]any) { response["data"].([]any)[0].(map[string]any)["id"] = "foreign" }},
		{"missing id", func(response map[string]any) { delete(response["data"].([]any)[0].(map[string]any), "id") }},
		{"foreign subject kind", func(response map[string]any) {
			response["data"].([]any)[0].(map[string]any)["subject"].(map[string]any)["kind"] = "trace"
		}},
		{"missing subject kind", func(response map[string]any) {
			delete(response["data"].([]any)[0].(map[string]any)["subject"].(map[string]any), "kind")
		}},
		{"missing observation", func(response map[string]any) {
			delete(response["data"].([]any)[0].(map[string]any)["subject"].(map[string]any), "id")
		}},
		{"foreign observation", func(response map[string]any) {
			response["data"].([]any)[0].(map[string]any)["subject"].(map[string]any)["id"] = "fedcba9876543210"
		}},
		{"foreign trace", func(response map[string]any) {
			response["data"].([]any)[0].(map[string]any)["subject"].(map[string]any)["traceId"] = "fedcba9876543210fedcba9876543210"
		}},
		{"missing trace", func(response map[string]any) {
			delete(response["data"].([]any)[0].(map[string]any)["subject"].(map[string]any), "traceId")
		}},
		{"missing timestamp", func(response map[string]any) { delete(response["data"].([]any)[0].(map[string]any), "timestamp") }},
		{"before window", func(response map[string]any) {
			response["data"].([]any)[0].(map[string]any)["timestamp"] = request.StartedAt.Add(-time.Nanosecond).Format(time.RFC3339Nano)
		}},
		{"outside deadline", func(response map[string]any) {
			response["data"].([]any)[0].(map[string]any)["timestamp"] = request.Deadline.Format(time.RFC3339Nano)
		}},
		{"cursor", func(response map[string]any) { response["meta"].(map[string]any)["cursor"] = "next" }},
		{"limit mismatch", func(response map[string]any) { response["meta"].(map[string]any)["limit"] = 99 }},
		{"missing meta", func(response map[string]any) { delete(response, "meta") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
				response := t191ScoreResponse(request, "safe", 1)
				tt.mutate(response)
				writeT191JSON(writer, response)
			})
			surfaces, _, _, closeServer := t191Surfaces(t, request, handler, []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
			defer closeServer()
			evidence, err := surfaces.Scan(context.Background(), request)
			if err == nil || !reflect.ValueOf(evidence).IsZero() {
				t.Fatal("invalid scores v3 response became zero evidence")
			}
			assertT191LowSensitive(t, err, request)
		})
	}
}

func TestPrivacyLangfuseSurfacesRequireProtectedClientsAndSafeInputBeforeNetwork(t *testing.T) {
	if _, err := NewPrivacyLangfuseSurfaces(nil, nil, nil); err == nil {
		t.Fatal("nil clients were accepted")
	}
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceLangfuseTrace, smoke.PrivacySmokeSurfaceLangfuseScore} {
		t.Run(string(surface), func(t *testing.T) {
			request := t191Request(surface)
			surfaces, log, _, closeServer := t191Surfaces(t, request, t191Handler(request, "safe"), []localeval.ScoreProjectionSnapshot{t191Snapshot(request)})
			defer closeServer()
			tests := []func(*PrivacyLangfuseScanRequest){
				func(value *PrivacyLangfuseScanRequest) { value.Surface = smoke.PrivacySmokeSurfaceTempo },
				func(value *PrivacyLangfuseScanRequest) { value.RunID = "bad\nrun" },
				func(value *PrivacyLangfuseScanRequest) { value.Marker = `bad" filter` },
				func(value *PrivacyLangfuseScanRequest) { value.RequestID = "bad&request" },
				func(value *PrivacyLangfuseScanRequest) { value.AITraceID = "bad\nai" },
				func(value *PrivacyLangfuseScanRequest) { value.ServiceTraceID = "bad-trace" },
				func(value *PrivacyLangfuseScanRequest) { value.SpanID = "bad-span" },
				func(value *PrivacyLangfuseScanRequest) { value.ForbiddenCanary = "" },
				func(value *PrivacyLangfuseScanRequest) { value.ForbiddenCanary = "short" },
				func(value *PrivacyLangfuseScanRequest) { value.Limit = 0 },
				func(value *PrivacyLangfuseScanRequest) { value.Limit = 101 },
				func(value *PrivacyLangfuseScanRequest) { value.Deadline = value.StartedAt },
				func(value *PrivacyLangfuseScanRequest) { value.Deadline = value.StartedAt.Add(2 * time.Minute) },
				func(value *PrivacyLangfuseScanRequest) {
					value.StartedAt = time.Now().UTC().Add(-2 * time.Minute)
					value.Deadline = time.Now().UTC().Add(-time.Minute)
				},
			}
			for _, mutate := range tests {
				candidate := request
				mutate(&candidate)
				if _, err := surfaces.Scan(context.Background(), candidate); err == nil {
					t.Fatal("unsafe request was accepted")
				}
			}
			for _, dangerous := range []string{`quote"value`, `slash\value`, "line\nbreak", "pipe|brace}", "ampersand&equals=", "percent%2f", "控制字符"} {
				for _, mutate := range []func(*PrivacyLangfuseScanRequest){
					func(value *PrivacyLangfuseScanRequest) { value.RunID = dangerous },
					func(value *PrivacyLangfuseScanRequest) { value.Marker = dangerous },
					func(value *PrivacyLangfuseScanRequest) { value.RequestID = dangerous },
					func(value *PrivacyLangfuseScanRequest) { value.AITraceID = dangerous },
				} {
					candidate := request
					mutate(&candidate)
					if _, err := surfaces.Scan(context.Background(), candidate); err == nil {
						t.Fatal("query injection value was accepted")
					}
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := surfaces.Scan(ctx, request); err == nil {
				t.Fatal("canceled context was accepted")
			}
			if _, err := surfaces.Scan(nil, request); err == nil {
				t.Fatal("nil context was accepted")
			}
			if len(log.snapshot()) != 0 {
				t.Fatal("unsafe request reached Langfuse")
			}
		})
	}
}

func assertT191LowSensitive(t *testing.T, err error, request PrivacyLangfuseScanRequest) {
	assertT191LowSensitiveValues(t, err, request)
}

func assertT191LowSensitiveValues(t *testing.T, err error, request PrivacyLangfuseScanRequest, extra ...string) {
	t.Helper()
	forbiddenValues := append([]string{
		t191Raw, t191Foreign, "foreign", "fedcba9876543210fedcba9876543210", "fedcba9876543210",
		t191Canary, t191Credential, request.RunID, request.Marker, request.RequestID, request.AITraceID,
		request.ServiceTraceID, request.SpanID, t191ProjectionID,
	}, extra...)
	for _, forbidden := range forbiddenValues {
		if forbidden == "" {
			continue
		}
		if strings.Contains(err.Error(), forbidden) {
			t.Fatal("error exposed Langfuse content or identity")
		}
	}
}
