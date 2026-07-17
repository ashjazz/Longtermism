// Package observability 固定 infra-smoke 的 HTTP API 契约。
//
// 这些测试只保护 T049 的请求/响应类型与输入规则；真实路由注册、disabled 404
// 和 X-Request-ID header 的运行时行为由后续 T040/T052 在应用装配层覆盖。
package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gogf/gf/v2/os/gsession"
	"github.com/gogf/gf/v2/util/gvalid"
)

func TestInfraSmokeRequestContract(t *testing.T) {
	requestType := reflect.TypeFor[InfraSmokeReq]()
	meta, ok := requestType.FieldByName("Meta")
	if !ok {
		t.Fatal("InfraSmokeReq must declare GoFrame route metadata")
	}
	if meta.Tag.Get("path") != "/observability/infra-smoke" || meta.Tag.Get("method") != "get" {
		t.Fatal("InfraSmokeReq route metadata must describe GET /observability/infra-smoke")
	}

	runMarker, ok := requestType.FieldByName("SmokeRunID")
	if !ok {
		t.Fatal("InfraSmokeReq must expose the smoke run marker")
	}
	if runMarker.Tag.Get("p") != SmokeRunIDHeader || runMarker.Tag.Get("in") != "header" || runMarker.Tag.Get("json") != "-" {
		t.Fatal("smoke run marker must be header-only and never read from a JSON body")
	}

	tests := []struct {
		name    string
		marker  string
		wantErr bool
	}{
		{name: "allows omitted marker", marker: ""},
		{name: "allows eight byte opaque marker", marker: "run-0001"},
		{name: "allows 128 byte opaque marker", marker: "r" + strings.Repeat("a", 127)},
		{name: "rejects marker shorter than eight bytes", marker: "short", wantErr: true},
		{name: "rejects marker longer than 128 bytes", marker: "r" + strings.Repeat("a", 128), wantErr: true},
		{name: "rejects whitespace", marker: "run marker", wantErr: true},
		{name: "rejects path separator", marker: "run/marker", wantErr: true},
		{name: "rejects non ASCII marker", marker: "run-标记", wantErr: true},
		{name: "rejects NUL byte", marker: "run\x00marker", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := gvalid.New().Data(InfraSmokeReq{SmokeRunID: tt.marker}).Run(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("validation error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestInfraSmokeRequestBindsRunMarkerFromHeaderOnly(t *testing.T) {
	server := newInfraSmokeRequestBindingServer(t)
	tests := []struct {
		name       string
		query      string
		body       string
		header     string
		wantMarker string
	}{
		{name: "uses the header over query and body", query: "query-marker", body: "body-marker", header: "run-header-01", wantMarker: "run-header-01"},
		{name: "rejects query when header is absent", query: "query-marker"},
		{name: "rejects body when header is absent", body: "body-marker"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/contract?"+SmokeRunIDHeader+"="+tt.query, strings.NewReader(`{"`+SmokeRunIDHeader+`":"`+tt.body+`"}`))
			request.Header.Set("Content-Type", "application/json")
			if tt.header != "" {
				request.Header.Set(SmokeRunIDHeader, tt.header)
			}
			response := httptest.NewRecorder()

			server.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("header binding response status = %d, want %d", response.Code, http.StatusOK)
			}
			var parsed struct {
				SmokeRunID string `json:"smoke_run_id"`
			}
			if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
				t.Fatalf("decode header binding response: %v", err)
			}
			if parsed.SmokeRunID != tt.wantMarker {
				t.Fatalf("bound smoke run marker = %q, want %q", parsed.SmokeRunID, tt.wantMarker)
			}
		})
	}
}

func TestInfraSmokeSuccessEnvelopeContract(t *testing.T) {
	tests := []struct {
		name           string
		meta           InfraSmokeMeta
		wantSmokeRunID bool
	}{
		{name: "returns a valid marker", meta: InfraSmokeMeta{RequestID: "req-infra-smoke-01", SmokeRunID: "run-0001"}, wantSmokeRunID: true},
		{name: "omits an absent marker", meta: InfraSmokeMeta{RequestID: "req-infra-smoke-02"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelope := InfraSmokeSuccessEnvelope{
				Code:    0,
				Message: "OK",
				Data:    InfraSmokeData{Status: InfraSmokeStatusOK},
				Meta:    tt.meta,
			}
			assertInfraSmokeEnvelopeContract(t, envelope, tt.wantSmokeRunID, true)
		})
	}
	assertInfraSmokeMetaHasRequestIDJSONKey(t, InfraSmokeMeta{})
}

func TestInfraSmokeDisabledEnvelopeContract(t *testing.T) {
	if InfraSmokeDisabledHTTPStatus != http.StatusNotFound {
		t.Fatalf("disabled infra smoke status = %d, want %d", InfraSmokeDisabledHTTPStatus, http.StatusNotFound)
	}
	envelope := InfraSmokeErrorEnvelope{
		Code:    InfraSmokeDisabledHTTPStatus,
		Message: "infra smoke disabled",
		Data:    nil,
		Meta:    InfraSmokeMeta{RequestID: "req-infra-smoke-disabled"},
	}
	assertInfraSmokeEnvelopeContract(t, envelope, false, false)
}

func TestInfraSmokeMetaDoesNotModelAIFields(t *testing.T) {
	metaType := reflect.TypeFor[InfraSmokeMeta]()
	wantFields := map[string]struct{}{
		"RequestID":  {},
		"SmokeRunID": {},
	}
	if metaType.NumField() != len(wantFields) {
		t.Fatalf("InfraSmokeMeta fields = %d, want only request and smoke identities", metaType.NumField())
	}
	for index := range metaType.NumField() {
		field := metaType.Field(index)
		if _, ok := wantFields[field.Name]; !ok {
			t.Fatalf("InfraSmokeMeta must not model %q", field.Name)
		}
	}
}

func assertInfraSmokeEnvelopeContract(t *testing.T, envelope any, wantSmokeRunID bool, wantSuccessData bool) {
	t.Helper()

	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal infra smoke success envelope: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("decode infra smoke success envelope: %v", err)
	}
	if len(raw) != 4 || raw["code"] == nil || raw["message"] == nil || raw["data"] == nil || raw["meta"] == nil {
		t.Fatalf("success envelope fields = %v, want code/message/data/meta only", raw)
	}

	if wantSuccessData {
		var data map[string]json.RawMessage
		if err := json.Unmarshal(raw["data"], &data); err != nil {
			t.Fatalf("decode infra smoke data: %v", err)
		}
		if len(data) != 1 || string(data["status"]) != `"ok"` {
			t.Fatalf("infra smoke data = %s, want only status=ok", raw["data"])
		}
	} else if string(raw["data"]) != "null" {
		t.Fatalf("disabled infra smoke data = %s, want null", raw["data"])
	}

	var meta map[string]json.RawMessage
	if err := json.Unmarshal(raw["meta"], &meta); err != nil {
		t.Fatalf("decode infra smoke metadata: %v", err)
	}
	if meta["request_id"] == nil {
		t.Fatalf("infra smoke metadata must always include request_id: %s", raw["meta"])
	}
	if (meta["smoke_run_id"] != nil) != wantSmokeRunID {
		t.Fatalf("infra smoke smoke_run_id presence = %v, want %v", meta["smoke_run_id"] != nil, wantSmokeRunID)
	}
	if len(meta) != 1+boolToInt(wantSmokeRunID) || meta["ai_trace_id"] != nil || meta["eval_summary"] != nil {
		t.Fatalf("infra-only metadata must contain only request/smoke identities: %s", raw["meta"])
	}
}

func assertInfraSmokeMetaHasRequestIDJSONKey(t *testing.T, meta InfraSmokeMeta) {
	t.Helper()
	payload, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal zero-value infra smoke metadata: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("decode zero-value infra smoke metadata: %v", err)
	}
	if raw["request_id"] == nil {
		t.Fatal("request_id must not be omitted when its value is empty")
	}
}

func newInfraSmokeRequestBindingServer(t *testing.T) *ghttp.Server {
	t.Helper()
	server := ghttp.GetServer("t037-" + strings.ReplaceAll(t.Name(), "/", "-"))
	server.SetDumpRouterMap(false)
	server.SetSessionStorage(gsession.NewStorageMemory())
	server.SetPort(0)
	server.BindHandler("/contract", func(request *ghttp.Request) {
		var input InfraSmokeReq
		if err := request.Parse(&input); err != nil {
			request.Response.Status = http.StatusBadRequest
			request.Response.WriteJsonExit(map[string]string{"error": "invalid request"})
		}
		request.Response.WriteJsonExit(map[string]string{"smoke_run_id": input.SmokeRunID})
	})
	if err := server.Start(); err != nil {
		t.Fatalf("start infra smoke request binding server: %v", err)
	}
	t.Cleanup(func() { server.Shutdown() })
	return server
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
