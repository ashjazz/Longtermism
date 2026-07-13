package privacy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestScannerAggregatesLowSensitivityFindingsAcrossObservabilitySurfaces(t *testing.T) {
	const canary = "synthetic-private-canary-t022"
	scanner, err := NewScanner([]string{canary})
	if err != nil {
		t.Fatal("NewScanner() returned an unexpected error")
	}

	// 每个表面只验证“是否泄漏了某一类内容”。报告不应携带命中的字段、表面或原文，
	// 否则隐私扫描本身会成为第二条敏感数据传播链路。
	result := scanner.Scan([]SurfaceText{
		{Surface: SurfaceAPI, Text: "Authorization: Bearer t022-opaque-token"},
		{Surface: SurfaceLog, Text: "request completed marker=" + canary},
		{Surface: SurfaceQueue, Text: "token=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0MDIyIn0.signature"},
		{Surface: SurfaceBackend, Text: "email=t022.user@example.test phone=13800138000 id=110101199001011234"},
		{Surface: SurfaceReport, Text: "api_key=sk-t022-test-key"},
	})

	assertScannerJSON(t, result, map[string]any{
		"counts": map[string]any{
			"api_key":          float64(1),
			"authorization":    float64(1),
			"pii":              float64(1),
			"synthetic_canary": float64(1),
			"token":            float64(1),
		},
	})
	assertScannerOutputDoesNotReflect(t, result, []string{
		canary,
		"t022-opaque-token",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0MDIyIn0.signature",
		"t022.user@example.test",
		"13800138000",
		"110101199001011234",
		"sk-t022-test-key",
	})
}

func TestScannerDoesNotFlagExplicitlyRedactedValues(t *testing.T) {
	scanner, err := NewScanner([]string{"synthetic-private-canary-t022"})
	if err != nil {
		t.Fatal("NewScanner() returned an unexpected error")
	}

	result := scanner.Scan([]SurfaceText{
		{Surface: SurfaceAPI, Text: "Authorization: [REDACTED]"},
		{Surface: SurfaceLog, Text: "token=<redacted>"},
		{Surface: SurfaceQueue, Text: "api_key=***"},
		{Surface: SurfaceBackend, Text: "email=[REDACTED]"},
		{Surface: SurfaceReport, Text: "marker=[REDACTED]"},
	})

	assertScannerJSON(t, result, map[string]any{"counts": map[string]any{}})
}

func TestScannerCopiesCanaries(t *testing.T) {
	canaries := []string{"synthetic-private-canary-t022"}
	scanner, err := NewScanner(canaries)
	if err != nil {
		t.Fatal("NewScanner() returned an unexpected error")
	}
	canaries[0] = "mutated-after-construction"

	result := scanner.Scan([]SurfaceText{
		{Surface: SurfaceLog, Text: "synthetic-private-canary-t022"},
	})
	assertScannerJSON(t, result, map[string]any{
		"counts": map[string]any{"synthetic_canary": float64(1)},
	})
}

func assertScannerJSON(t *testing.T, result ScanResult, want map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal("ScanResult could not be encoded as JSON")
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal("ScanResult did not encode as valid JSON")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("ScanResult did not contain only the expected category counts")
	}
}

func assertScannerOutputDoesNotReflect(t *testing.T, result ScanResult, forbidden []string) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal("ScanResult could not be encoded as JSON")
	}
	for _, value := range forbidden {
		if strings.Contains(string(encoded), value) {
			t.Fatal("ScanResult reflected a sensitive input or source surface")
		}
	}
}
