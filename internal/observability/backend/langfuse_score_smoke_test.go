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

	localeval "github.com/ashjazz/Longtermism/internal/eval"
	"github.com/ashjazz/Longtermism/internal/observability/langfuse"
	"github.com/ashjazz/Longtermism/internal/observability/smoke"
)

// TestLangfuseScoreSmokeBackendConfirmsOneLocallyIndexedProjection 固定事实分层：
// run/eval/status/attempt 来自本地耐久索引；Langfuse v3 Scores API 只确认稳定 score ID、
// native trace/observation subject 与平台 timestamp，不能猜测未导出的 worker 状态。
func TestLangfuseScoreSmokeBackendConfirmsOneLocallyIndexedProjection(t *testing.T) {
	target := scoreSmokeTargetForT179()
	local := scoreSnapshotForT179(target, langfuse.ScoreProjectionStatusSent, 1)
	store := &t179ScoreLookup{records: []localeval.ScoreProjectionSnapshot{local}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/public/v3/scores" {
			t.Errorf("request=%s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Basic cHVibGljOnJlYWQtc2VjcmV0" {
			t.Error("missing read credential")
		}
		query := request.URL.Query()
		if query.Get("id") != target.ProjectionID || query.Get("traceId") != target.PlatformTraceID || query.Get("observationId") != target.PlatformObservationID || query.Get("fields") != "subject" || query.Get("limit") != "100" || query.Get("cursor") != "" || query.Get("fromTimestamp") != target.StartedAt.Format(time.RFC3339Nano) || query.Get("toTimestamp") != target.Deadline.Format(time.RFC3339Nano) {
			t.Fatalf("query=%v, want exact projection/trace/window and bounded subject response", query)
		}
		_, _ = fmt.Fprintf(writer, `{"data":[{"id":"%s","timestamp":"%s","subject":{"kind":"observation","id":"%s","traceId":"%s"},"name":"raw-t179-metric","value":0.91}],"meta":{"limit":100,"cursor":null}}`, target.ProjectionID, target.StartedAt.Add(time.Second).Format(time.RFC3339Nano), target.PlatformObservationID, target.PlatformTraceID)
	}))
	defer server.Close()

	backend, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: server.URL, Credential: "cHVibGljOnJlYWQtc2VjcmV0", Timeout: time.Second, ProjectionStore: store})
	if err != nil || !backend.IsConfigured(context.Background()) {
		t.Fatalf("constructor/configured=(%#v,%v)", backend, err)
	}
	observations, err := backend.ProjectionStates(context.Background(), target)
	if err != nil || len(observations) != 1 {
		t.Fatalf("ProjectionStates()=(%#v,%v)", observations, err)
	}
	want := smoke.ScoreSmokeProjectionObservation{ProjectionID: target.ProjectionID, Status: "sent", Attempt: 1, ObservedAt: target.StartedAt.Add(time.Second)}
	if observations[0] != want {
		t.Fatalf("observation=%#v want=%#v", observations[0], want)
	}
	encoded, _ := json.Marshal(observations)
	for _, forbidden := range []string{"raw-t179", target.RunID, target.EvalRunID, target.RequestID, target.AITraceID} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("DTO leaked %q", forbidden)
		}
	}
	if len(store.runIDs) != 1 || store.runIDs[0] != target.RunID {
		t.Fatalf("local lookups=%v", store.runIDs)
	}
	var _ smoke.ScoreSmokeBackend = backend
}

func TestLangfuseScoreSmokeBackendRejectsLocalOrPlatformAmbiguity(t *testing.T) {
	target := scoreSmokeTargetForT179()
	valid := scoreSnapshotForT179(target, langfuse.ScoreProjectionStatusSent, 1)
	tests := []struct {
		name, body, wantClass string
		records               []localeval.ScoreProjectionSnapshot
	}{
		{name: "missing local fact", records: nil, body: `{"data":[],"meta":{"cursor":null}}`, wantClass: "unexpected_evidence"},
		{name: "duplicate local fact", records: []localeval.ScoreProjectionSnapshot{valid, valid}, body: `{"data":[],"meta":{"cursor":null}}`, wantClass: "unexpected_evidence"},
		{name: "foreign local projection", records: []localeval.ScoreProjectionSnapshot{scoreSnapshotForT179(target, langfuse.ScoreProjectionStatusSent, 1)}, body: `{"data":[],"meta":{"cursor":null}}`, wantClass: "unexpected_evidence"},
		{name: "truncated platform page", records: []localeval.ScoreProjectionSnapshot{valid}, body: `{"data":[],"meta":{"cursor":"next-t179"}}`, wantClass: "malformed_response"},
		{name: "duplicate platform score", records: []localeval.ScoreProjectionSnapshot{valid}, body: scoreResultsForT179(target, 2), wantClass: "unexpected_evidence"},
		{name: "foreign platform subject", records: []localeval.ScoreProjectionSnapshot{valid}, body: fmt.Sprintf(`{"data":[{"id":"%s","timestamp":"%s","subject":{"kind":"observation","id":"ffffffffffffffff","traceId":"%s"}}],"meta":{"cursor":null}}`, target.ProjectionID, target.StartedAt.Add(time.Second).Format(time.RFC3339Nano), target.PlatformTraceID), wantClass: "unexpected_evidence"},
		{name: "foreign platform score id", records: []localeval.ScoreProjectionSnapshot{valid}, body: fmt.Sprintf(`{"data":[{"id":"foreign-projection-t179","timestamp":"%s","subject":{"kind":"observation","id":"%s","traceId":"%s"}}],"meta":{"cursor":null}}`, target.StartedAt.Add(time.Second).Format(time.RFC3339Nano), target.PlatformObservationID, target.PlatformTraceID), wantClass: "unexpected_evidence"},
		{name: "foreign platform trace", records: []localeval.ScoreProjectionSnapshot{valid}, body: fmt.Sprintf(`{"data":[{"id":"%s","timestamp":"%s","subject":{"kind":"observation","id":"%s","traceId":"ffffffffffffffffffffffffffffffff"}}],"meta":{"cursor":null}}`, target.ProjectionID, target.StartedAt.Add(time.Second).Format(time.RFC3339Nano), target.PlatformObservationID), wantClass: "unexpected_evidence"},
		{name: "platform result outside window", records: []localeval.ScoreProjectionSnapshot{valid}, body: fmt.Sprintf(`{"data":[{"id":"%s","timestamp":"%s","subject":{"kind":"observation","id":"%s","traceId":"%s"}}],"meta":{"cursor":null}}`, target.ProjectionID, target.Deadline.Add(time.Nanosecond).Format(time.RFC3339Nano), target.PlatformObservationID, target.PlatformTraceID), wantClass: "unexpected_evidence"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.name == "foreign local projection" {
				tt.records[0].ProjectionID = "foreign-projection-t179"
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { calls++; _, _ = writer.Write([]byte(tt.body)) }))
			defer server.Close()
			backend, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: server.URL, Credential: "read-secret-t179", Timeout: time.Second, ProjectionStore: &t179ScoreLookup{records: tt.records}})
			if err != nil {
				t.Fatal(err)
			}
			observations, err := backend.ProjectionStates(context.Background(), target)
			if observations != nil || errorClass(err) != tt.wantClass {
				t.Fatalf("result=(%#v,%v) class=%q", observations, err, errorClass(err))
			}
			if strings.Contains(fmt.Sprint(err), target.ProjectionID) || strings.Contains(fmt.Sprint(err), server.URL) || strings.Contains(fmt.Sprint(err), "read-secret") {
				t.Fatal("error leaked identity/config")
			}
			if (tt.name == "missing local fact" || tt.name == "duplicate local fact" || tt.name == "foreign local projection") && calls != 0 {
				t.Fatal("platform queried before unique local fact")
			}
		})
	}
}

func TestLangfuseScoreSmokeBackendRejectsEveryConflictingLocalIdentityBeforeNetwork(t *testing.T) {
	target := scoreSmokeTargetForT179()
	valid := scoreSnapshotForT179(target, langfuse.ScoreProjectionStatusSent, 1)
	tests := []struct {
		name   string
		mutate func(localeval.ScoreProjectionSnapshot) localeval.ScoreProjectionSnapshot
	}{
		{name: "run", mutate: func(v localeval.ScoreProjectionSnapshot) localeval.ScoreProjectionSnapshot {
			v.RunID = "foreign-run-t179"
			return v
		}},
		{name: "eval", mutate: func(v localeval.ScoreProjectionSnapshot) localeval.ScoreProjectionSnapshot {
			v.EvalRunID = "foreign-eval-t179"
			return v
		}},
		{name: "request", mutate: func(v localeval.ScoreProjectionSnapshot) localeval.ScoreProjectionSnapshot {
			v.RequestID = "foreign-request-t179"
			return v
		}},
		{name: "AI trace", mutate: func(v localeval.ScoreProjectionSnapshot) localeval.ScoreProjectionSnapshot {
			v.AITraceID = "foreign-ai-trace-t179"
			return v
		}},
		{name: "platform trace", mutate: func(v localeval.ScoreProjectionSnapshot) localeval.ScoreProjectionSnapshot {
			v.PlatformTraceID = "ffffffffffffffffffffffffffffffff"
			return v
		}},
		{name: "platform observation", mutate: func(v localeval.ScoreProjectionSnapshot) localeval.ScoreProjectionSnapshot {
			v.PlatformObservationID = "ffffffffffffffff"
			return v
		}},
		{name: "invalid attempt", mutate: func(v localeval.ScoreProjectionSnapshot) localeval.ScoreProjectionSnapshot { v.Attempt = -1; return v }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { calls++ }))
			defer server.Close()
			backend, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: server.URL, Credential: "read-secret-t179", Timeout: time.Second, ProjectionStore: &t179ScoreLookup{records: []localeval.ScoreProjectionSnapshot{tt.mutate(valid)}}})
			if err != nil {
				t.Fatal(err)
			}
			observations, err := backend.ProjectionStates(context.Background(), target)
			if observations != nil || errorClass(err) != "unexpected_evidence" || calls != 0 {
				t.Fatalf("result=(%#v,%v) class=%q calls=%d", observations, err, errorClass(err), calls)
			}
		})
	}
}

func TestLangfuseScoreSmokeBackendReturnsLocalNonSentStatesWithoutGuessingPlatformFacts(t *testing.T) {
	target := scoreSmokeTargetForT179()
	statuses := []langfuse.ScoreProjectionStatus{langfuse.ScoreProjectionStatusQueued, langfuse.ScoreProjectionStatusSending, langfuse.ScoreProjectionStatusRetryWait, langfuse.ScoreProjectionStatusFailedPermanent, langfuse.ScoreProjectionStatusDroppedQueueFull, langfuse.ScoreProjectionStatusFailedShutdownTimeout}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { calls++ }))
			defer server.Close()
			snapshot := scoreSnapshotForT179(target, status, 1)
			backend, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: server.URL, Credential: "read-secret-t179", Timeout: time.Second, ProjectionStore: &t179ScoreLookup{records: []localeval.ScoreProjectionSnapshot{snapshot}}})
			if err != nil {
				t.Fatal(err)
			}
			observations, err := backend.ProjectionStates(context.Background(), target)
			if err != nil || len(observations) != 1 || observations[0].Status != string(status) || observations[0].Attempt != 1 || calls != 0 {
				t.Fatalf("status=%s observations=%#v error=%v calls=%d", status, observations, err, calls)
			}
		})
	}
}

func TestLangfuseScoreSmokeBackendBoundsResponsesAndHidesDiagnostics(t *testing.T) {
	target := scoreSmokeTargetForT179()
	local := scoreSnapshotForT179(target, langfuse.ScoreProjectionStatusSent, 1)
	tests := []struct {
		name            string
		status          int
		body, wantClass string
	}{
		{name: "authentication", status: http.StatusUnauthorized, body: "raw-t179-auth", wantClass: "authentication_failed"},
		{name: "upstream", status: http.StatusBadGateway, body: "raw-t179-upstream", wantClass: "backend_unavailable"},
		{name: "malformed", status: http.StatusOK, body: "{", wantClass: "malformed_response"},
		{name: "oversized", status: http.StatusOK, body: strings.Repeat("x", maximumBackendResponseSize+1), wantClass: "malformed_response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(tt.status)
				_, _ = writer.Write([]byte(tt.body))
			}))
			defer server.Close()
			backend, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: server.URL, Credential: "read-secret-t179", Timeout: time.Second, ProjectionStore: &t179ScoreLookup{records: []localeval.ScoreProjectionSnapshot{local}}})
			if err != nil {
				t.Fatal(err)
			}
			observations, err := backend.ProjectionStates(context.Background(), target)
			if observations != nil || errorClass(err) != tt.wantClass {
				t.Fatalf("result=(%#v,%v) class=%q", observations, err, errorClass(err))
			}
			for _, forbidden := range []string{"raw-t179", server.URL, "read-secret-t179", target.ProjectionID, target.PlatformTraceID} {
				if strings.Contains(fmt.Sprint(err), forbidden) {
					t.Fatalf("error leaked %q", forbidden)
				}
			}
		})
	}
}

func TestLangfuseScoreSmokeBackendRejectsUnsafeConfigurationAndTargetBeforeNetwork(t *testing.T) {
	if backend, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: "https://example.com:443", Credential: "read-secret-t179", Timeout: time.Second, ProjectionStore: &t179ScoreLookup{}}); backend != nil || errorClass(err) != "backend_unavailable" {
		t.Fatalf("unsafe endpoint=(%#v,%v)", backend, err)
	}
	if backend, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: "http://127.0.0.1:3000", Timeout: time.Second, ProjectionStore: &t179ScoreLookup{}}); backend != nil || errorClass(err) != "authentication_failed" {
		t.Fatalf("missing credential=(%#v,%v)", backend, err)
	}

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { calls++ }))
	defer server.Close()
	valid := scoreSmokeTargetForT179()
	local := scoreSnapshotForT179(valid, langfuse.ScoreProjectionStatusSent, 1)
	backend, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: server.URL, Credential: "read-secret-t179", Timeout: time.Second, ProjectionStore: &t179ScoreLookup{records: []localeval.ScoreProjectionSnapshot{local}}})
	if err != nil {
		t.Fatal(err)
	}
	tests := []smoke.ScoreSmokeProjectionTarget{
		func() smoke.ScoreSmokeProjectionTarget { v := valid; v.ProjectionID = ""; return v }(),
		func() smoke.ScoreSmokeProjectionTarget { v := valid; v.PlatformTraceID = ""; return v }(),
		func() smoke.ScoreSmokeProjectionTarget {
			v := valid
			v.Deadline = v.StartedAt.Add(121 * time.Second)
			return v
		}(),
		func() smoke.ScoreSmokeProjectionTarget { v := valid; v.Limit = 101; return v }(),
	}
	for _, invalid := range tests {
		if _, err := backend.ProjectionStates(context.Background(), invalid); errorClass(err) != "invalid_query" {
			t.Fatalf("invalid target error=%v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("unsafe targets sent %d requests", calls)
	}
}

type t179ScoreLookup struct {
	records []localeval.ScoreProjectionSnapshot
	runIDs  []string
}

func (store *t179ScoreLookup) FindByRunID(_ context.Context, runID string) ([]localeval.ScoreProjectionSnapshot, error) {
	store.runIDs = append(store.runIDs, runID)
	return append([]localeval.ScoreProjectionSnapshot(nil), store.records...), nil
}

func scoreSmokeTargetForT179() smoke.ScoreSmokeProjectionTarget {
	started := time.Now().UTC().Add(-10 * time.Second)
	return smoke.ScoreSmokeProjectionTarget{RunID: "score-run-t179", Marker: "score-marker-t179", ProjectionID: "projection-t179", EvalRunID: "eval-run-t179", RequestID: "request-t179", AITraceID: "ai-trace-t179", PlatformTraceID: "0123456789abcdef0123456789abcdef", PlatformObservationID: "0123456789abcdef", StartedAt: started, Deadline: started.Add(2 * time.Minute), Limit: 100}
}

func scoreSnapshotForT179(target smoke.ScoreSmokeProjectionTarget, status langfuse.ScoreProjectionStatus, attempt int) localeval.ScoreProjectionSnapshot {
	return localeval.ScoreProjectionSnapshot{RunID: target.RunID, EvalRunID: target.EvalRunID, ProjectionID: target.ProjectionID, RequestID: target.RequestID, AITraceID: target.AITraceID, PlatformTraceID: target.PlatformTraceID, PlatformObservationID: target.PlatformObservationID, Status: status, Attempt: attempt, CreatedAt: target.StartedAt.Add(-time.Second), ObservedAt: target.StartedAt}
}

func scoreResultsForT179(target smoke.ScoreSmokeProjectionTarget, count int) string {
	data := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		data = append(data, map[string]any{"id": target.ProjectionID, "timestamp": target.StartedAt.Add(time.Second).Format(time.RFC3339Nano), "subject": map[string]string{"kind": "observation", "id": target.PlatformObservationID, "traceId": target.PlatformTraceID}})
	}
	encoded, _ := json.Marshal(map[string]any{"data": data, "meta": map[string]any{"cursor": nil}})
	return string(encoded)
}

// T130d：ScoreCountByID 是 score worker 故障场景的幂等断言事实源——统计
// 平台上该投影 ID 的 score 数量。只读封闭查询：精确 id 过滤 + 受限窗口 +
// 有界 limit；畸形文档拒绝。
func TestLangfuseScoreSmokeBackendScoreCountByIDQueriesExactIdentity(t *testing.T) {
	target := scoreSmokeTargetForT179()
	store := &t179ScoreLookup{records: []localeval.ScoreProjectionSnapshot{scoreSnapshotForT179(target, langfuse.ScoreProjectionStatusSent, 2)}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("id") != target.ProjectionID || query.Get("fields") != "subject" || query.Get("limit") != "8" ||
			query.Get("fromTimestamp") != target.StartedAt.Format(time.RFC3339Nano) || query.Get("toTimestamp") != target.Deadline.Format(time.RFC3339Nano) {
			t.Fatalf("query=%v, want exact id/window and bounded fields", query)
		}
		_, _ = writer.Write([]byte(`{"data":[{"id":"` + target.ProjectionID + `"},{"id":"` + target.ProjectionID + `"}],"meta":{"limit":8,"cursor":null}}`))
	}))
	defer server.Close()

	backend, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: server.URL, Credential: "cHVibGljOnJlYWQtc2VjcmV0", Timeout: time.Second, ProjectionStore: store})
	if err != nil {
		t.Fatalf("constructor error = %v", err)
	}
	count, err := backend.ScoreCountByID(context.Background(), target.ProjectionID, target.StartedAt, target.Deadline, 8)
	if err != nil || count != 2 {
		t.Fatalf("ScoreCountByID() = (%d, %v), want 2（重复投递必须可计数）", count, err)
	}
	if _, err := backend.ScoreCountByID(context.Background(), "", target.StartedAt, target.Deadline, 8); err == nil {
		t.Fatal("empty projection id must fail closed")
	}
	if _, err := backend.ScoreCountByID(context.Background(), target.ProjectionID, target.Deadline, target.StartedAt, 8); err == nil {
		t.Fatal("reversed window must fail closed")
	}
}

func TestLangfuseScoreSmokeBackendScoreCountByIDRejectsMalformedDocument(t *testing.T) {
	target := scoreSmokeTargetForT179()
	store := &t179ScoreLookup{records: []localeval.ScoreProjectionSnapshot{scoreSnapshotForT179(target, langfuse.ScoreProjectionStatusSent, 1)}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"data":[`))
	}))
	defer server.Close()
	backend, err := NewLangfuseScoreSmokeBackend(LangfuseScoreSmokeBackendConfig{BaseURL: server.URL, Credential: "cHVibGljOnJlYWQtc2VjcmV0", Timeout: time.Second, ProjectionStore: store})
	if err != nil {
		t.Fatalf("constructor error = %v", err)
	}
	if _, err := backend.ScoreCountByID(context.Background(), target.ProjectionID, target.StartedAt, target.Deadline, 8); err == nil {
		t.Fatal("malformed document must be rejected, never counted as zero")
	}
}
