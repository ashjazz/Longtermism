package backend

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ashjazz/Longtermism/internal/observability/privacy"
)

var privacyGrafanaCategories = []string{"synthetic_canary", "credential", "authorization", "token", "recognized_pii"}

func validatePrivacyTempoSearch(payload []byte, request PrivacyGrafanaScanRequest) (string, any, error) {
	document, err := decodePrivacyGrafanaDocument(payload)
	if err != nil {
		return "", nil, err
	}
	root, ok := document.(map[string]any)
	if !ok {
		return "", nil, errPrivacyGrafanaSurface
	}
	traces, ok := root["traces"].([]any)
	metrics, metricsOK := root["metrics"].(map[string]any)
	completed, completedOK := privacyGrafanaInt64(metrics["completedJobs"])
	total, totalOK := privacyGrafanaInt64(metrics["totalJobs"])
	if !ok || len(traces) != 1 || len(traces) >= request.Limit || !metricsOK || !completedOK || !totalOK || completed != 1 || total != 1 {
		return "", nil, errPrivacyGrafanaSurface
	}
	trace, ok := traces[0].(map[string]any)
	traceID, traceOK := trace["traceID"].(string)
	started, startOK := privacyGrafanaTimestamp(trace["startTimeUnixNano"])
	if !ok || !traceOK || traceID != request.ServiceTraceID || !startOK || !privacyGrafanaInWindow(started, request) {
		return "", nil, errPrivacyGrafanaSurface
	}
	return traceID, document, nil
}

func validatePrivacyTempoDocument(payload []byte, request PrivacyGrafanaScanRequest) (any, error) {
	document, err := decodePrivacyGrafanaDocument(payload)
	if err != nil {
		return nil, err
	}
	root, ok := document.(map[string]any)
	if !ok {
		return nil, errPrivacyGrafanaSurface
	}
	status, hasStatus := root["status"]
	if hasStatus {
		value, stringOK := status.(string)
		if !stringOK || value != "COMPLETE" {
			return nil, errPrivacyGrafanaSurface
		}
	}
	trace, ok := root["trace"].(map[string]any)
	batches, okBatches := trace["batches"].([]any)
	if !ok || !okBatches || len(batches) == 0 {
		return nil, errPrivacyGrafanaSurface
	}
	targetSpans := 0
	for _, rawBatch := range batches {
		batch, ok := rawBatch.(map[string]any)
		if !ok || validatePrivacyTempoResource(batch["resource"], request) != nil {
			return nil, errPrivacyGrafanaSurface
		}
		scopes, ok := batch["scopeSpans"].([]any)
		if !ok || len(scopes) == 0 {
			return nil, errPrivacyGrafanaSurface
		}
		for _, rawScope := range scopes {
			scope, ok := rawScope.(map[string]any)
			spans, spansOK := scope["spans"].([]any)
			if !ok || !spansOK || len(spans) == 0 {
				return nil, errPrivacyGrafanaSurface
			}
			for _, rawSpan := range spans {
				span, ok := rawSpan.(map[string]any)
				if !ok {
					return nil, errPrivacyGrafanaSurface
				}
				traceID, traceOK := privacyGrafanaOTLPID(span["traceId"], 16)
				spanID, spanOK := privacyGrafanaOTLPID(span["spanId"], 8)
				started, startOK := privacyGrafanaTimestamp(span["startTimeUnixNano"])
				ended, endOK := privacyGrafanaTimestamp(span["endTimeUnixNano"])
				if !traceOK || !spanOK || traceID != request.ServiceTraceID || !startOK || !endOK || ended.Before(started) ||
					!privacyGrafanaInWindow(started, request) || !privacyGrafanaInWindow(ended, request) {
					return nil, errPrivacyGrafanaSurface
				}
				if spanID == request.SpanID {
					targetSpans++
				}
			}
		}
	}
	if targetSpans != 1 {
		return nil, errPrivacyGrafanaSurface
	}
	return document, nil
}

func validatePrivacyTempoResource(value any, request PrivacyGrafanaScanRequest) error {
	resource, ok := value.(map[string]any)
	attributes, okAttributes := resource["attributes"].([]any)
	if !ok || !okAttributes {
		return errPrivacyGrafanaSurface
	}
	want := map[string]string{
		"longtermism.smoke.run_id": request.Marker,
		"request.id":               request.RequestID,
		"longtermism.ai.trace_id":  request.AITraceID,
	}
	seen := make(map[string]struct{}, len(want))
	for _, raw := range attributes {
		attribute, ok := raw.(map[string]any)
		key, keyOK := attribute["key"].(string)
		value, valueOK := attribute["value"].(map[string]any)
		if !ok || !keyOK || !valueOK {
			return errPrivacyGrafanaSurface
		}
		if expected, required := want[key]; required {
			stringValue, stringOK := value["stringValue"].(string)
			if !stringOK {
				return errPrivacyGrafanaSurface
			}
			if stringValue != expected {
				return errPrivacyGrafanaSurface
			}
			if _, duplicate := seen[key]; duplicate {
				return errPrivacyGrafanaSurface
			}
			seen[key] = struct{}{}
		}
	}
	if len(seen) != len(want) {
		return errPrivacyGrafanaSurface
	}
	return nil
}

func validatePrivacyLokiDocument(payload []byte, request PrivacyGrafanaScanRequest) (any, error) {
	document, err := decodePrivacyGrafanaDocument(payload)
	if err != nil {
		return nil, err
	}
	root, ok := document.(map[string]any)
	data, dataOK := root["data"].(map[string]any)
	resultType, typeOK := data["resultType"].(string)
	results, resultsOK := data["result"].([]any)
	if !ok || root["status"] != "success" || !dataOK || !typeOK || resultType != "streams" || !resultsOK {
		return nil, errPrivacyGrafanaSurface
	}
	entries := 0
	for _, rawResult := range results {
		result, ok := rawResult.(map[string]any)
		stream, streamOK := result["stream"].(map[string]any)
		values, valuesOK := result["values"].([]any)
		if !ok || !streamOK || stream["service_name"] != "longtermism" || !valuesOK {
			return nil, errPrivacyGrafanaSurface
		}
		for _, rawValue := range values {
			value, ok := rawValue.([]any)
			if !ok || len(value) != 3 {
				return nil, errPrivacyGrafanaSurface
			}
			if _, ok := value[1].(string); !ok {
				return nil, errPrivacyGrafanaSurface
			}
			observedAt, timeOK := privacyGrafanaTimestamp(value[0])
			metadata, metadataOK := value[2].(map[string]any)
			if !timeOK || !privacyGrafanaInWindow(observedAt, request) || !metadataOK ||
				metadata["smoke_run_id"] != request.Marker || metadata["request_id"] != request.RequestID ||
				metadata["ai_trace_id"] != request.AITraceID || metadata["trace_id"] != request.ServiceTraceID ||
				metadata["span_id"] != request.SpanID {
				return nil, errPrivacyGrafanaSurface
			}
			entries++
		}
	}
	if entries != 1 || entries >= request.Limit {
		return nil, errPrivacyGrafanaSurface
	}
	return document, nil
}

func scanPrivacyGrafanaDocuments(canary string, documents ...any) (map[string]int, error) {
	texts := make([]string, 0, 64)
	for _, document := range documents {
		collectPrivacyGrafanaStrings(document, &texts)
	}
	sort.Strings(texts)
	scanner, err := privacy.NewScanner([]string{canary})
	if err != nil {
		return nil, errPrivacyGrafanaSurface
	}
	result, err := scanner.Scan([]privacy.SurfaceText{{Surface: privacy.SurfaceBackend, Text: strings.Join(texts, "\n")}})
	if err != nil {
		return nil, errPrivacyGrafanaSurface
	}
	counts := privacyGrafanaZeroCounts()
	for category, count := range result.Counts {
		mapped, ok := mapPrivacyGrafanaCategory(category)
		if !ok || count < 0 {
			return nil, errPrivacyGrafanaSurface
		}
		counts[mapped] = count
	}
	return counts, nil
}

func decodePrivacyGrafanaDocument(payload []byte) (any, error) {
	if len(payload) == 0 || len(payload) > maximumBackendResponseSize || !utf8.Valid(payload) || rejectDuplicatePrivacyGrafanaKeys(payload) != nil {
		return nil, errPrivacyGrafanaSurface
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var document any
	if decoder.Decode(&document) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errPrivacyGrafanaSurface
	}
	return document, nil
}

func rejectDuplicatePrivacyGrafanaKeys(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if consumePrivacyGrafanaJSON(decoder) != nil {
		return errPrivacyGrafanaSurface
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errPrivacyGrafanaSurface
	}
	return nil
}

func consumePrivacyGrafanaJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			if err != nil || !ok {
				return errPrivacyGrafanaSurface
			}
			folded := strings.ToLower(key)
			if _, duplicate := seen[folded]; duplicate {
				return errPrivacyGrafanaSurface
			}
			seen[folded] = struct{}{}
			if consumePrivacyGrafanaJSON(decoder) != nil {
				return errPrivacyGrafanaSurface
			}
		}
	case '[':
		for decoder.More() {
			if consumePrivacyGrafanaJSON(decoder) != nil {
				return errPrivacyGrafanaSurface
			}
		}
	default:
		return errPrivacyGrafanaSurface
	}
	closing, err := decoder.Token()
	want := map[json.Delim]json.Delim{'{': '}', '[': ']'}[delimiter]
	if err != nil || closing != want {
		return errPrivacyGrafanaSurface
	}
	return nil
}

func collectPrivacyGrafanaStrings(value any, destination *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			*destination = append(*destination, key)
			collectPrivacyGrafanaStrings(nested, destination)
		}
	case []any:
		for _, nested := range typed {
			collectPrivacyGrafanaStrings(nested, destination)
		}
	case string:
		*destination = append(*destination, typed)
	}
}

func privacyGrafanaTimestamp(value any) (time.Time, bool) {
	var rendered string
	switch typed := value.(type) {
	case string:
		rendered = typed
	case json.Number:
		rendered = typed.String()
	default:
		return time.Time{}, false
	}
	nanoseconds, err := strconv.ParseInt(rendered, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, nanoseconds).UTC(), true
}

func privacyGrafanaInt64(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	integer, err := number.Int64()
	return integer, err == nil
}

func privacyGrafanaOTLPID(value any, size int) (string, bool) {
	rendered, ok := value.(string)
	if !ok {
		return "", false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(rendered)
	if err != nil || len(decoded) != size {
		return "", false
	}
	return hex.EncodeToString(decoded), true
}

func privacyGrafanaInWindow(value time.Time, request PrivacyGrafanaScanRequest) bool {
	return !value.Before(request.StartedAt) && !value.After(request.Deadline)
}

func privacyGrafanaZeroCounts() map[string]int {
	counts := make(map[string]int, len(privacyGrafanaCategories))
	for _, category := range privacyGrafanaCategories {
		counts[category] = 0
	}
	return counts
}

func mapPrivacyGrafanaCategory(category string) (string, bool) {
	switch category {
	case "api_key":
		return "credential", true
	case "pii":
		return "recognized_pii", true
	case "synthetic_canary", "authorization", "token":
		return category, true
	default:
		return "", false
	}
}
