package backend

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

func TestPrivacyGrafanaEvidenceAndFailuresStayLowSensitive(t *testing.T) {
	for _, surface := range []smoke.PrivacySmokeSurface{smoke.PrivacySmokeSurfaceLoki, smoke.PrivacySmokeSurfaceTempo} {
		request := t190Request(surface)
		var rawQuery string
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
			rawQuery = incoming.URL.RawQuery
			if surface == smoke.PrivacySmokeSurfaceTempo && incoming.URL.Path == "/api/search" {
				writeT190JSON(writer, t190TempoSearch(request, 1))
				return
			}
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = writer.Write([]byte(t190Raw + request.ForbiddenCanary + request.Marker + request.AITraceID + request.SpanID))
		}))
		client, clientErr := NewGrafanaSmokeQueryClient(GrafanaQueryConfig{LokiURL: server.URL, TempoURL: server.URL})
		if clientErr != nil {
			server.Close()
			t.Fatal(clientErr)
		}
		surfaces, constructorErr := NewPrivacyGrafanaSurfaces(client)
		if constructorErr != nil {
			server.Close()
			t.Fatal(constructorErr)
		}
		evidence, err := surfaces.Scan(context.Background(), request)
		server.Close()
		if err == nil || !reflect.ValueOf(evidence).IsZero() {
			t.Fatal("backend failure produced evidence")
		}
		decodedQuery, _ := url.QueryUnescape(rawQuery)
		for _, secret := range []string{t190Raw, request.ForbiddenCanary, request.RunID, request.Marker, request.RequestID, request.AITraceID, request.ServiceTraceID, request.SpanID, server.URL, rawQuery, decodedQuery} {
			if secret != "" && strings.Contains(err.Error(), secret) {
				t.Fatal("error exposed remote, query, or identity content")
			}
		}
	}

	evidenceType := reflect.TypeOf(PrivacyGrafanaSurfaceEvidence{})
	constructorType := reflect.TypeOf(NewPrivacyGrafanaSurfaces)
	wantConstructor := reflect.TypeOf((func(*GrafanaQueryClient) (*PrivacyGrafanaSurfaces, error))(nil))
	if constructorType != wantConstructor || constructorType.IsVariadic() {
		t.Fatal("production constructor does not require the concrete protected Grafana client")
	}
	wantMethods := map[string]reflect.Type{
		"Surface": reflect.TypeOf((*PrivacyGrafanaSurfaceEvidence).Surface), "EvidenceMethod": reflect.TypeOf((*PrivacyGrafanaSurfaceEvidence).EvidenceMethod),
		"ScannerPolicyVersion": reflect.TypeOf((*PrivacyGrafanaSurfaceEvidence).ScannerPolicyVersion), "Counts": reflect.TypeOf((*PrivacyGrafanaSurfaceEvidence).Counts),
		"MarshalJSON": reflect.TypeOf((*PrivacyGrafanaSurfaceEvidence).MarshalJSON),
	}
	if evidenceType.NumField() == 0 {
		t.Fatal("evidence must contain sealed proof state")
	}
	for index := 0; index < evidenceType.NumField(); index++ {
		if evidenceType.Field(index).IsExported() {
			t.Fatal("evidence exposes caller-writable proof state")
		}
	}
	publicType := reflect.TypeOf((*PrivacyGrafanaSurfaceEvidence)(nil))
	if publicType.NumMethod() != len(wantMethods) {
		t.Fatal("evidence exposes an accessor outside the low-sensitive allowlist")
	}
	for name, signature := range wantMethods {
		method, ok := publicType.MethodByName(name)
		if !ok || method.Type != signature {
			t.Fatalf("evidence method %q has an unsafe signature", name)
		}
	}
	requestType := reflect.TypeOf(PrivacyGrafanaScanRequest{})
	for index := 0; index < requestType.NumField(); index++ {
		name := strings.ToLower(requestType.Field(index).Name)
		for _, forbidden := range []string{"attempt", "querysent", "verified", "raw", "url", "query", "count"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("request exposes caller-reported proof field %q", requestType.Field(index).Name)
			}
		}
	}
	if _, marshalErr := json.Marshal(PrivacyGrafanaSurfaceEvidence{}); marshalErr == nil {
		t.Fatal("evidence must not be JSON serializable")
	}
}

func TestPrivacyGrafanaImplementationReusesProtectedClientWithoutLoggingOrFilesystem(t *testing.T) {
	matches, err := filepath.Glob("privacy_grafana*.go")
	if err != nil || len(matches) == 0 {
		t.Fatal("privacy Grafana implementation source is missing")
	}
	for _, path := range matches {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		for _, imported := range parsed.Imports {
			name := strings.Trim(imported.Path.Value, `"`)
			for _, forbidden := range []string{"net/http", "net", "net/url", "crypto/tls", "os/exec", "log", "log/slog", "os", "path/filepath", "io/fs", "syscall", "golang.org/x/sys/unix"} {
				if name == forbidden || strings.Contains(name, "zap") || strings.Contains(name, "zerolog") || strings.Contains(name, "glog") {
					t.Fatalf("%s imports forbidden capability %q", path, name)
				}
			}
			if strings.Contains(strings.ToLower(name), "log") {
				t.Fatalf("%s imports logging capability %q", path, name)
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if identifier, ok := call.Fun.(*ast.Ident); ok && (identifier.Name == "print" || identifier.Name == "println") {
					t.Fatalf("%s can print sensitive remote content", path)
				}
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
					if strings.HasPrefix(selector.Sel.Name, "Print") {
						t.Fatalf("%s can print sensitive remote content", path)
					}
					for _, name := range []string{"Info", "Infof", "Warn", "Warnf", "Error", "Errorf", "Debug", "Debugf", "Fatal", "Panic"} {
						if selector.Sel.Name == name {
							t.Fatalf("%s can log sensitive remote content", path)
						}
					}
				}
			}
			return true
		})
	}
}
