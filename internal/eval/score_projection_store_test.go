package eval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/langfuse"
	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
)

// TestScoreProjectionStorePersistsOneCurrentSnapshotPerRun 固定本地 score projection
// 索引的职责：它保存当前不可变状态，而不是把每次 transition 追加成多个“当前事实”。
// 重开应用后仍应按受控 run ID 精确找到同一个 ProjectionID。
func TestScoreProjectionStorePersistsOneCurrentSnapshotPerRun(t *testing.T) {
	path := t.TempDir() + "/score-projections.json"
	runID := "score-run-t179"
	projection := newT179Projection(t)
	store, err := OpenScoreProjectionStore(ScoreProjectionStoreConfig{Path: path})
	if err != nil {
		t.Fatalf("OpenScoreProjectionStore() error = %v", err)
	}
	if err := store.SaveInitial(context.Background(), runID, projection, 2); err != nil {
		t.Fatalf("SaveInitial() error = %v", err)
	}
	sending := transitionT179(t, projection, langfuse.ScoreProjectionStatusSending)
	if err := store.Update(context.Background(), runID, sending); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := OpenScoreProjectionStore(ScoreProjectionStoreConfig{Path: path})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	records, err := reopened.FindByRunID(context.Background(), runID)
	if err != nil || len(records) != 1 {
		t.Fatalf("FindByRunID() = (%#v, %v), want one current snapshot", records, err)
	}
	assertT179SnapshotMatches(t, records[0], runID, sending)
	pending, err := reopened.LoadPending(context.Background())
	if err != nil || len(pending) != 1 || pending[0].RunID != runID || pending[0].ProjectionID != projection.ProjectionID || pending[0].Status != langfuse.ScoreProjectionStatusSending || pending[0].MaxAttempts != 2 || pending[0].TargetKind != projection.Target.Kind() || pending[0].PlatformTraceID != projection.Target.PlatformTraceID() || pending[0].PlatformObservationID != projection.Target.PlatformObservationID() || !reflect.DeepEqual(pending[0].Evidence, projection.Evidence) {
		t.Fatalf("LoadPending() = (%#v,%v), want serializable recovery snapshot", pending, err)
	}
	if foreign, err := reopened.FindByRunID(context.Background(), "foreign-run-t179"); err != nil || len(foreign) != 0 {
		t.Fatalf("foreign lookup = (%#v, %v), want empty", foreign, err)
	}
}

func TestScoreProjectionStoreRejectsIdentityOrStateRewrites(t *testing.T) {
	runID := "score-run-t179"
	projection := newT179Projection(t)
	tests := []struct {
		name string
		act  func(*ScoreProjectionStore) error
	}{
		{name: "same run cannot point to another projection", act: func(store *ScoreProjectionStore) error {
			other := newT179ProjectionWithMetric(t, "faithfulness")
			return store.SaveInitial(context.Background(), runID, other, 2)
		}},
		{name: "attempt cannot move backward", act: func(store *ScoreProjectionStore) error {
			sending := transitionT179(t, projection, langfuse.ScoreProjectionStatusSending)
			retry := transitionT179(t, sending, langfuse.ScoreProjectionStatusRetryWait)
			if err := store.Update(context.Background(), runID, sending); err != nil {
				return err
			}
			if err := store.Update(context.Background(), runID, retry); err != nil {
				return err
			}
			return store.Update(context.Background(), runID, sending)
		}},
		{name: "foreign run cannot update projection", act: func(store *ScoreProjectionStore) error {
			return store.Update(context.Background(), "foreign-run-t179", transitionT179(t, projection, langfuse.ScoreProjectionStatusSending))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := OpenScoreProjectionStore(ScoreProjectionStoreConfig{Path: t.TempDir() + "/projection.json"})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.SaveInitial(context.Background(), runID, projection, 2); err != nil {
				t.Fatal(err)
			}
			err = tt.act(store)
			if !errors.Is(err, ErrScoreProjectionStoreConflict) {
				t.Fatalf("error = %v, want conflict", err)
			}
			for _, forbidden := range []string{runID, projection.ProjectionID, projection.Evidence.EvalRunID} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error leaked identity %q", forbidden)
				}
			}
		})
	}
}

func TestScoreProjectionStoreReturnsDefensiveSnapshots(t *testing.T) {
	store, err := OpenScoreProjectionStore(ScoreProjectionStoreConfig{Path: t.TempDir() + "/projection.json"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projection := newT179Projection(t)
	if err := store.SaveInitial(context.Background(), "score-run-t179", projection, 2); err != nil {
		t.Fatal(err)
	}
	first, _ := store.FindByRunID(context.Background(), "score-run-t179")
	first[0].Status = langfuse.ScoreProjectionStatusSent
	if first[0].Threshold != nil {
		*first[0].Threshold = 0.1
	}
	second, _ := store.FindByRunID(context.Background(), "score-run-t179")
	if second[0].Status != langfuse.ScoreProjectionStatusQueued || second[0].Threshold == nil || *second[0].Threshold != 0.8 {
		t.Fatalf("stored status changed through caller snapshot: %q", second[0].Status)
	}
}

func TestScoreProjectionStoreUsesPrivateRegularFilesAndFailsClosedOnCorruption(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "projection.json")
	store, err := OpenScoreProjectionStore(ScoreProjectionStoreConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	projection := newT179Projection(t)
	if err := store.SaveInitial(context.Background(), "score-run-t179", projection, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("projection file mode=%o regular=%v", info.Mode().Perm(), info.Mode().IsRegular())
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"partial":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenScoreProjectionStore(ScoreProjectionStoreConfig{Path: path}); reopened != nil || !errors.Is(err, ErrScoreProjectionStoreCorrupt) {
		t.Fatalf("corrupt reopen=(%#v,%v)", reopened, err)
	}

	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte("protected"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenScoreProjectionStore(ScoreProjectionStoreConfig{Path: link}); opened != nil || !errors.Is(err, ErrScoreProjectionStoreConfiguration) {
		t.Fatalf("symlink open=(%#v,%v)", opened, err)
	}
	content, _ := os.ReadFile(target)
	if string(content) != "protected" {
		t.Fatal("symlink target was modified")
	}
}

func newT179Projection(t *testing.T) langfuse.ScoreProjection {
	return newT179ProjectionWithMetric(t, "answer_relevance")
}

func newT179ProjectionWithMetric(t *testing.T, metric string) langfuse.ScoreProjection {
	t.Helper()
	traceID, spanID := "0123456789abcdef0123456789abcdef", "0123456789abcdef"
	threshold := 0.8
	evidence, err := aieval.NewEvaluationEvidence(aieval.EvaluationEvidenceInput{Identity: obs.NewCorrelationIdentity("request-t179", obs.WithServiceSpan(traceID, spanID), obs.WithAITraceID("ai-trace-t179"), obs.WithEvalRunID("eval-run-t179")), Dataset: aieval.DatasetIdentity{Name: "chat-golden", Version: "v1"}, SampleID: "sample-t179", MetricName: metric, Score: 0.91, Threshold: &threshold})
	if err != nil {
		t.Fatal(err)
	}
	trace, err := langfuse.MapTraceToProjection(langfuse.TraceMapperInput{Span: langfuse.OTLPSpanSnapshot{TraceID: traceID, SpanID: spanID, Name: "ai.generation", ObservationType: obs.ObservationTypeGeneration}, PayloadMode: obs.PayloadModeMetadataOnly})
	if err != nil {
		t.Fatal(err)
	}
	target, err := langfuse.NewScoreTarget(trace, langfuse.ScoreTargetKindObservation)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := langfuse.NewScoreProjection(langfuse.ScoreProjectionInput{Target: target, Evidence: evidence, MaxAttempts: 2, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

func transitionT179(t *testing.T, projection langfuse.ScoreProjection, status langfuse.ScoreProjectionStatus) langfuse.ScoreProjection {
	t.Helper()
	updated, err := projection.Transition(status)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func assertT179SnapshotMatches(t *testing.T, snapshot ScoreProjectionSnapshot, runID string, projection langfuse.ScoreProjection) {
	t.Helper()
	if snapshot.RunID != runID || snapshot.ProjectionID != projection.ProjectionID || snapshot.EvalRunID != projection.Evidence.EvalRunID || snapshot.RequestID != projection.Evidence.RequestID || snapshot.AITraceID != projection.Evidence.AITraceID || snapshot.PlatformTraceID != projection.Target.PlatformTraceID() || snapshot.PlatformObservationID != projection.Target.PlatformObservationID() || snapshot.Status != projection.Status || snapshot.Attempt != projection.Attempt || !snapshot.CreatedAt.Equal(projection.CreatedAt) {
		t.Fatalf("snapshot = %#v, want projection identity/status", snapshot)
	}
	encoded, _ := json.Marshal(snapshot)
	for _, forbidden := range []string{"failure_summary", "endpoint", "credential", "secret", "raw_payload"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("snapshot leaked forbidden field %q", forbidden)
		}
	}
}
