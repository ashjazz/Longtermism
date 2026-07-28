package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"golang.org/x/sys/unix"
)

func TestLocalEvidenceStorePersistsFactsBeforeAnyProjection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	store := openLocalEvidenceStore(t, path)
	evidence := newStoredEvidence(t, "sample-persist-before-projection", 0.91)

	// 本地 evidence 是事实源。投影器尚未运行或之后失败，都不能回滚已经成功的持久化。
	if err := store.Append(context.Background(), evidence); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	assertPrivateEvidenceFilePermissions(t, path)
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened := openLocalEvidenceStore(t, path)
	got := readAllLocalEvidence(t, reopened)
	if !reflect.DeepEqual(got, []aieval.EvaluationEvidence{evidence}) {
		t.Fatalf("ReadAll() = %#v, want persisted evidence %#v", got, evidence)
	}
}

func TestLocalEvidenceStoreDefensivelyCopiesEvidenceAcrossPersistenceBoundary(t *testing.T) {
	store := openLocalEvidenceStore(t, filepath.Join(t.TempDir(), "evidence.jsonl"))
	evidence := newStoredEvidence(t, "sample-defensive-copy", 0.88)
	wantThreshold := *evidence.Threshold

	if err := store.Append(context.Background(), evidence); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	*evidence.Threshold = 0.01

	first := readAllLocalEvidence(t, store)
	if len(first) != 1 || first[0].Threshold == nil || *first[0].Threshold != wantThreshold {
		t.Fatalf("first ReadAll() = %#v, want threshold %v", first, wantThreshold)
	}
	*first[0].Threshold = 0.02
	first[0].SampleID = "mutated-after-read"

	second := readAllLocalEvidence(t, store)
	if len(second) != 1 || second[0].Threshold == nil || *second[0].Threshold != wantThreshold || second[0].SampleID != "sample-defensive-copy" {
		t.Fatalf("second ReadAll() = %#v, want immutable persisted snapshot", second)
	}
}

func TestLocalEvidenceStorePreservesValidLowSensitiveUnicodeAndVersionFacts(t *testing.T) {
	store := openLocalEvidenceStore(t, filepath.Join(t.TempDir(), "evidence.jsonl"))
	threshold := 0.8
	evidence, err := aieval.NewEvaluationEvidence(aieval.EvaluationEvidenceInput{
		Identity: obs.NewCorrelationIdentity(
			"req-t093-unicode",
			obs.WithServiceSpan("service-trace-t093-unicode", "span-t093-unicode"),
			obs.WithAITraceID("ai-trace-t093-unicode"),
			obs.WithEvalRunID("eval-run-t093-unicode"),
		),
		Dataset:    aieval.DatasetIdentity{Name: "客服 对话", Version: "v1.0.0+build.1"},
		SampleID:   "样例 01",
		MetricName: "回答 相关性",
		Score:      0.91,
		Threshold:  &threshold,
	})
	if err != nil {
		t.Fatalf("NewEvaluationEvidence() error = %v", err)
	}

	if err := store.Append(context.Background(), evidence); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if got := readAllLocalEvidence(t, store); !reflect.DeepEqual(got, []aieval.EvaluationEvidence{evidence}) {
		t.Fatalf("ReadAll() = %#v, want valid low-sensitive Unicode evidence %#v", got, evidence)
	}
}

func TestLocalEvidenceStoreReopensCompleteOrderedEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	store := openLocalEvidenceStore(t, path)
	want := []aieval.EvaluationEvidence{
		newStoredEvidence(t, "sample-reopen-001", 0.91),
		newStoredEvidence(t, "sample-reopen-002", 0.74),
	}
	for _, evidence := range want {
		if err := store.Append(context.Background(), evidence); err != nil {
			t.Fatalf("Append(%q) error = %v", evidence.SampleID, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened := openLocalEvidenceStore(t, path)
	if got := readAllLocalEvidence(t, reopened); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadAll() after reopen = %#v, want %#v", got, want)
	}
}

func TestLocalEvidenceStoreAppendsConcurrentlyWithoutLosingOrTearingRecords(t *testing.T) {
	store := openLocalEvidenceStore(t, filepath.Join(t.TempDir(), "evidence.jsonl"))
	const writers = 32
	start := make(chan struct{})
	errorsByWriter := make(chan error, writers)
	var writersDone sync.WaitGroup
	evidenceByWriter := make([]aieval.EvaluationEvidence, writers)
	for writer := range writers {
		evidenceByWriter[writer] = newStoredEvidence(t, "sample-concurrent-"+twoDigit(writer), 0.81)
	}

	for writer := range writers {
		writersDone.Add(1)
		go func(writer int) {
			defer writersDone.Done()
			<-start
			errorsByWriter <- store.Append(context.Background(), evidenceByWriter[writer])
		}(writer)
	}
	close(start)
	writersDone.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatalf("concurrent Append() error = %v", err)
		}
	}

	got := readAllLocalEvidence(t, store)
	if len(got) != writers {
		t.Fatalf("ReadAll() count = %d, want %d", len(got), writers)
	}
	seen := make(map[string]struct{}, writers)
	for _, evidence := range got {
		if _, exists := seen[evidence.SampleID]; exists {
			t.Fatalf("ReadAll() duplicated sample %q", evidence.SampleID)
		}
		seen[evidence.SampleID] = struct{}{}
	}
	for writer := range writers {
		wantSampleID := "sample-concurrent-" + twoDigit(writer)
		if _, exists := seen[wantSampleID]; !exists {
			t.Fatalf("ReadAll() missed concurrent sample %q", wantSampleID)
		}
	}
}

func TestLocalEvidenceStoreAppendsAcrossIndependentHandlesWithoutLosingRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	first := openLocalEvidenceStore(t, path)
	second := openLocalEvidenceStore(t, path)
	const recordsPerHandle = 16
	start := make(chan struct{})
	appendErrors := make(chan error, recordsPerHandle*2)
	var writersDone sync.WaitGroup
	evidenceByHandle := [2][]aieval.EvaluationEvidence{}
	for handleIndex := range evidenceByHandle {
		evidenceByHandle[handleIndex] = make([]aieval.EvaluationEvidence, recordsPerHandle)
		for recordIndex := range recordsPerHandle {
			sampleID := fmt.Sprintf("sample-multi-handle-%d-%02d", handleIndex, recordIndex)
			evidenceByHandle[handleIndex][recordIndex] = newStoredEvidence(t, sampleID, 0.86)
		}
	}

	for handleIndex, store := range []*LocalEvidenceStore{first, second} {
		for recordIndex := range recordsPerHandle {
			writersDone.Add(1)
			go func(handleIndex, recordIndex int, store *LocalEvidenceStore) {
				defer writersDone.Done()
				<-start
				appendErrors <- store.Append(context.Background(), evidenceByHandle[handleIndex][recordIndex])
			}(handleIndex, recordIndex, store)
		}
	}
	close(start)
	writersDone.Wait()
	close(appendErrors)
	for err := range appendErrors {
		if err != nil {
			t.Fatalf("cross-handle Append() error = %v", err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}

	reopened := openLocalEvidenceStore(t, path)
	got := readAllLocalEvidence(t, reopened)
	if len(got) != recordsPerHandle*2 {
		t.Fatalf("ReadAll() count = %d, want %d", len(got), recordsPerHandle*2)
	}
	seen := make(map[string]struct{}, recordsPerHandle*2)
	for _, evidence := range got {
		if _, exists := seen[evidence.SampleID]; exists {
			t.Fatalf("ReadAll() duplicated cross-handle sample %q", evidence.SampleID)
		}
		seen[evidence.SampleID] = struct{}{}
	}
	for handleIndex := range 2 {
		for recordIndex := range recordsPerHandle {
			wantSampleID := fmt.Sprintf("sample-multi-handle-%d-%02d", handleIndex, recordIndex)
			if _, exists := seen[wantSampleID]; !exists {
				t.Fatalf("ReadAll() missed cross-handle sample %q", wantSampleID)
			}
		}
	}
}

func TestOpenLocalEvidenceStoreDiagnosesUnavailableStorage(t *testing.T) {
	directory := t.TempDir()
	const pathCanary = "path-canary-t073-private"
	parentFile := filepath.Join(directory, pathCanary)
	if err := os.WriteFile(parentFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile() setup error = %v", err)
	}

	_, err := OpenLocalEvidenceStore(LocalEvidenceStoreConfig{Path: filepath.Join(parentFile, "evidence.jsonl")})
	if !errors.Is(err, ErrEvidenceStorageUnavailable) {
		t.Fatalf("OpenLocalEvidenceStore() error = %v, want ErrEvidenceStorageUnavailable", err)
	}
	if strings.Contains(err.Error(), pathCanary) {
		t.Fatal("storage diagnostic must not reflect a sensitive local path")
	}
}

func TestOpenLocalEvidenceStoreEnforcesNinetyDayRetentionBoundary(t *testing.T) {
	tests := []struct {
		name          string
		retention     time.Duration
		wantRetention time.Duration
		wantErr       bool
	}{
		{name: "zero uses the ninety day default", wantRetention: DefaultLocalEvidenceRetention},
		{name: "shorter retention is allowed", retention: 30 * 24 * time.Hour, wantRetention: 30 * 24 * time.Hour},
		{name: "exactly ninety days is allowed", retention: DefaultLocalEvidenceRetention, wantRetention: DefaultLocalEvidenceRetention},
		{name: "negative retention is rejected", retention: -time.Hour, wantErr: true},
		{name: "retention above ninety days is rejected", retention: DefaultLocalEvidenceRetention + time.Nanosecond, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := OpenLocalEvidenceStore(LocalEvidenceStoreConfig{
				Path:      filepath.Join(t.TempDir(), "evidence.jsonl"),
				Retention: tt.retention,
			})
			if tt.wantErr {
				if !errors.Is(err, ErrEvidenceStoreConfiguration) {
					t.Fatalf("OpenLocalEvidenceStore() error = %v, want ErrEvidenceStoreConfiguration", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenLocalEvidenceStore() error = %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if got := store.Retention(); got != tt.wantRetention {
				t.Fatalf("Retention() = %v, want %v", got, tt.wantRetention)
			}
		})
	}
}

func TestLocalEvidenceStoreRestrictsExistingEvidenceFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("WriteFile() setup error = %v", err)
	}
	store := openLocalEvidenceStore(t, path)
	if err := store.Append(context.Background(), newStoredEvidence(t, "sample-file-mode", 0.92)); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	assertPrivateEvidenceFilePermissions(t, path)
}

func TestLocalEvidenceStoreRejectsRawContentBeforeItReachesDiskOrErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	store := openLocalEvidenceStore(t, path)
	valid := newStoredEvidence(t, "sample-safe-evidence", 0.93)
	if err := store.Append(context.Background(), valid); err != nil {
		t.Fatalf("Append(valid) error = %v", err)
	}

	const rawQuery = "raw query: t073-private"
	const rawOutput = "raw output: t073-private"
	const credential = "Authorization: Bearer t073-private-token"
	const email = "user-t073@example.test"
	tests := []struct {
		name   string
		mutate func(aieval.EvaluationEvidence) aieval.EvaluationEvidence
	}{
		{name: "eval run id", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.EvalRunID = rawQuery
			return evidence
		}},
		{name: "request id", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.RequestID = email
			return evidence
		}},
		{name: "AI trace id", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.AITraceID = credential
			return evidence
		}},
		{name: "service trace id", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.ServiceTraceID = rawOutput
			return evidence
		}},
		{name: "span id", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.SpanID = rawQuery
			return evidence
		}},
		{name: "dataset name", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.Dataset.Name = rawQuery
			return evidence
		}},
		{name: "dataset version", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.Dataset.Version = email
			return evidence
		}},
		{name: "sample id", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.SampleID = rawQuery
			return evidence
		}},
		{name: "metric name", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.MetricName = rawOutput
			return evidence
		}},
		{name: "failure summary", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.FailureSummary = credential
			return evidence
		}},
		{name: "regression status", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.RegressionStatus = aieval.RegressionStatus(rawOutput)
			return evidence
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.Append(context.Background(), tt.mutate(valid))
			if err == nil {
				t.Fatal("Append(raw content) error = nil, want rejection")
			}
			for _, forbidden := range []string{rawQuery, rawOutput, credential, email} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatal("Append() error reflected protected content")
				}
			}
		})
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, forbidden := range []string{rawQuery, rawOutput, credential, email, "prompt", "output", "authorization", "api_key"} {
		if strings.Contains(strings.ToLower(string(onDisk)), strings.ToLower(forbidden)) {
			t.Fatalf("evidence file leaked forbidden raw content %q", forbidden)
		}
	}
	if got := readAllLocalEvidence(t, store); !reflect.DeepEqual(got, []aieval.EvaluationEvidence{valid}) {
		t.Fatalf("ReadAll() after rejected records = %#v, want only original valid evidence", got)
	}
}

func TestLocalEvidenceStoreRejectsInvalidDomainFactsBeforeWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	store := openLocalEvidenceStore(t, path)
	valid := newStoredEvidence(t, "sample-invalid-domain", 0.93)
	tests := []struct {
		name   string
		mutate func(aieval.EvaluationEvidence) aieval.EvaluationEvidence
	}{
		{name: "score is NaN", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.Score = math.NaN()
			return evidence
		}},
		{name: "threshold is above one", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			threshold := 1.1
			evidence.Threshold = &threshold
			return evidence
		}},
		{name: "status contradicts score", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.RegressionStatus = aieval.RegressionStatusFailed
			return evidence
		}},
		{name: "failure summary is not derived", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.FailureSummary = "arbitrary summary"
			return evidence
		}},
		{name: "identifier exceeds export boundary", mutate: func(evidence aieval.EvaluationEvidence) aieval.EvaluationEvidence {
			evidence.SampleID = strings.Repeat("a", maxLocalEvidenceFactBytes+1)
			return evidence
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Append(context.Background(), tt.mutate(valid)); !errors.Is(err, ErrEvidenceInvalid) {
				t.Fatalf("Append() error = %v, want ErrEvidenceInvalid", err)
			}
		})
	}
	if got := readAllLocalEvidence(t, store); len(got) != 0 {
		t.Fatalf("ReadAll() = %#v, want no invalid evidence written", got)
	}
}

func TestLocalEvidenceStoreFailsClosedForCorruptOrUnknownRecords(t *testing.T) {
	valid := newStoredEvidence(t, "sample-corrupt-fixture", 0.93)
	baseRecord := localEvidenceRecord{
		SchemaVersion: localEvidenceSchemaVersion,
		PersistedAt:   time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC),
		Evidence:      snapshotFromEvidence(valid),
	}
	unknownSchema := baseRecord
	unknownSchema.SchemaVersion++
	missingTimestamp := baseRecord
	missingTimestamp.PersistedAt = time.Time{}
	invalidEvidence := baseRecord
	invalidEvidence.Evidence.MetricName = ""

	tests := []struct {
		name    string
		content []byte
	}{
		{name: "empty line", content: []byte("\n")},
		{name: "invalid JSON", content: []byte("{\n")},
		{name: "trailing JSON", content: []byte("{}{}\n")},
		{name: "unknown field", content: []byte(`{"schema_version":1,"persisted_at":"2026-07-28T12:00:00Z","evidence":{},"unexpected":true}` + "\n")},
		{name: "unknown schema", content: marshalEvidenceStoreFixture(t, unknownSchema)},
		{name: "missing persisted timestamp", content: marshalEvidenceStoreFixture(t, missingTimestamp)},
		{name: "invalid evidence", content: marshalEvidenceStoreFixture(t, invalidEvidence)},
		{name: "record exceeds size limit", content: append([]byte(strings.Repeat("x", maxLocalEvidenceRecordBytes)), '\n')},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "corrupt-path-canary-t093.jsonl")
			store := openLocalEvidenceStore(t, path)
			if err := os.WriteFile(path, tt.content, 0o600); err != nil {
				t.Fatalf("WriteFile() fixture error = %v", err)
			}

			_, err := store.ReadAll(context.Background())
			if !errors.Is(err, ErrEvidenceStoreCorrupt) {
				t.Fatalf("ReadAll() error = %v, want ErrEvidenceStoreCorrupt", err)
			}
			if strings.Contains(err.Error(), "corrupt-path-canary-t093") || strings.Contains(err.Error(), string(tt.content)) {
				t.Fatal("corruption error must not reflect the path or raw record")
			}
		})
	}
}

func TestLocalEvidenceStoreContextAndCloseBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	store := openLocalEvidenceStore(t, path)
	evidence := newStoredEvidence(t, "sample-lifecycle", 0.93)

	if err := store.Append(nil, evidence); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatalf("Append(nil context) error = %v, want ErrEvidenceInvalid", err)
	}
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Append(cancelledContext, evidence); !errors.Is(err, context.Canceled) {
		t.Fatalf("Append(cancelled context) error = %v, want context.Canceled", err)
	}
	if _, err := store.ReadAll(cancelledContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadAll(cancelled context) error = %v, want context.Canceled", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close() error = %v, want idempotent success", err)
	}
	if err := store.Append(context.Background(), evidence); !errors.Is(err, ErrEvidenceStoreClosed) {
		t.Fatalf("Append() after Close error = %v, want ErrEvidenceStoreClosed", err)
	}
	if _, err := store.ReadAll(context.Background()); !errors.Is(err, ErrEvidenceStoreClosed) {
		t.Fatalf("ReadAll() after Close error = %v, want ErrEvidenceStoreClosed", err)
	}

	var nilStore *LocalEvidenceStore
	if got := nilStore.Retention(); got != 0 {
		t.Fatalf("nil store Retention() = %v, want 0", got)
	}
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil store Close() error = %v", err)
	}
	if _, err := nilStore.ReadAll(context.Background()); !errors.Is(err, ErrEvidenceStoreClosed) {
		t.Fatalf("nil store ReadAll() error = %v, want ErrEvidenceStoreClosed", err)
	}
}

func TestLocalEvidenceStoreCancelsWhileWaitingForSameHandleOperation(t *testing.T) {
	store := openLocalEvidenceStore(t, filepath.Join(t.TempDir(), "evidence.jsonl"))
	store.operationGate <- struct{}{}
	t.Cleanup(func() { <-store.operationGate })

	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)
	evidence := newStoredEvidence(t, "sample-cancelled-waiter", 0.93)
	go func() {
		close(started)
		result <- store.Append(ctx, evidence)
	}()
	<-started
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Append() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Append() did not observe cancellation while waiting for the local operation gate")
	}
}

func TestDecodeLocalEvidenceObservesCancellationDuringScan(t *testing.T) {
	record := localEvidenceRecord{
		SchemaVersion: localEvidenceSchemaVersion,
		PersistedAt:   time.Now().UTC(),
		Evidence:      snapshotFromEvidence(newStoredEvidence(t, "sample-cancelled-scan", 0.93)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := decodeLocalEvidence(ctx, strings.NewReader(string(marshalEvidenceStoreFixture(t, record)))); !errors.Is(err, context.Canceled) {
		t.Fatalf("decodeLocalEvidence() error = %v, want context.Canceled", err)
	}
}

func TestOpenLocalEvidenceStoreRejectsSymlinkDataFile(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.jsonl")
	path := filepath.Join(directory, "evidence-path-canary-t093.jsonl")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("WriteFile() setup error = %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("Symlink() setup error = %v", err)
	}

	_, err := OpenLocalEvidenceStore(LocalEvidenceStoreConfig{Path: path})
	if !errors.Is(err, ErrEvidenceStorageUnavailable) {
		t.Fatalf("OpenLocalEvidenceStore() error = %v, want ErrEvidenceStorageUnavailable", err)
	}
	if strings.Contains(err.Error(), "evidence-path-canary-t093") {
		t.Fatal("symlink rejection must not reflect the configured path")
	}
}

func TestOpenLocalEvidenceStoreRejectsSymlinkParentAndHardlinkedData(t *testing.T) {
	t.Run("symlink parent", func(t *testing.T) {
		actualDirectory := t.TempDir()
		linkedDirectory := filepath.Join(t.TempDir(), "linked-parent-canary-t093")
		if err := os.Symlink(actualDirectory, linkedDirectory); err != nil {
			t.Fatalf("Symlink(parent) setup error = %v", err)
		}
		_, err := OpenLocalEvidenceStore(LocalEvidenceStoreConfig{
			Path: filepath.Join(linkedDirectory, "evidence.jsonl"),
		})
		if !errors.Is(err, ErrEvidenceStorageUnavailable) {
			t.Fatalf("OpenLocalEvidenceStore() error = %v, want ErrEvidenceStorageUnavailable", err)
		}
		if strings.Contains(err.Error(), "linked-parent-canary-t093") {
			t.Fatal("parent symlink rejection must not reflect the configured path")
		}
	})

	t.Run("hardlinked data", func(t *testing.T) {
		directory := t.TempDir()
		target := filepath.Join(directory, "target.jsonl")
		path := filepath.Join(directory, "hardlink-path-canary-t093.jsonl")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatalf("WriteFile() setup error = %v", err)
		}
		if err := os.Link(target, path); err != nil {
			t.Fatalf("Link() setup error = %v", err)
		}
		_, err := OpenLocalEvidenceStore(LocalEvidenceStoreConfig{Path: path})
		if !errors.Is(err, ErrEvidenceStorageUnavailable) {
			t.Fatalf("OpenLocalEvidenceStore() error = %v, want ErrEvidenceStorageUnavailable", err)
		}
		if strings.Contains(err.Error(), "hardlink-path-canary-t093") {
			t.Fatal("hardlink rejection must not reflect the configured path")
		}
	})
}

func TestLocalEvidenceStoreRejectsReplacedLockBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock-binding-path-canary-t093.jsonl")
	store := openLocalEvidenceStore(t, path)
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatalf("Remove(lock) setup error = %v", err)
	}
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatalf("WriteFile(lock) setup error = %v", err)
	}

	err := store.Append(context.Background(), newStoredEvidence(t, "sample-lock-replaced", 0.93))
	if !errors.Is(err, ErrEvidenceStorageUnavailable) {
		t.Fatalf("Append() error = %v, want ErrEvidenceStorageUnavailable", err)
	}
	if strings.Contains(err.Error(), "lock-binding-path-canary-t093") {
		t.Fatal("lock-binding error must not reflect the configured path")
	}
}

func TestLocalEvidenceStoreEnforcesTotalFileCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	store := openLocalEvidenceStore(t, path)
	if err := os.Truncate(path, maxLocalEvidenceFileBytes+1); err != nil {
		t.Fatalf("Truncate() setup error = %v", err)
	}

	if _, err := store.ReadAll(context.Background()); !errors.Is(err, ErrEvidenceStoreCapacity) {
		t.Fatalf("ReadAll() error = %v, want ErrEvidenceStoreCapacity", err)
	}
	if err := store.Append(context.Background(), newStoredEvidence(t, "sample-capacity", 0.93)); !errors.Is(err, ErrEvidenceStoreCapacity) {
		t.Fatalf("Append() error = %v, want ErrEvidenceStoreCapacity", err)
	}
}

func TestOpenLocalEvidenceStoreRejectsSpecialDataAndLockFiles(t *testing.T) {
	tests := []struct {
		name       string
		createPath func(string) string
	}{
		{
			name: "FIFO data file",
			createPath: func(path string) string {
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatalf("Mkfifo(data) error = %v", err)
				}
				return path
			},
		},
		{
			name: "FIFO lock file",
			createPath: func(path string) string {
				if err := unix.Mkfifo(path+".lock", 0o600); err != nil {
					t.Fatalf("Mkfifo(lock) error = %v", err)
				}
				return path
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.createPath(filepath.Join(t.TempDir(), "special-path-canary-t093.jsonl"))
			_, err := OpenLocalEvidenceStore(LocalEvidenceStoreConfig{Path: path})
			if !errors.Is(err, ErrEvidenceStorageUnavailable) {
				t.Fatalf("OpenLocalEvidenceStore() error = %v, want ErrEvidenceStorageUnavailable", err)
			}
			if strings.Contains(err.Error(), "special-path-canary-t093") {
				t.Fatal("special-file rejection must not reflect the configured path")
			}
		})
	}
}

func marshalEvidenceStoreFixture(t *testing.T, record localEvidenceRecord) []byte {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal() fixture error = %v", err)
	}
	return append(encoded, '\n')
}

func openLocalEvidenceStore(t *testing.T, path string) *LocalEvidenceStore {
	t.Helper()
	store, err := OpenLocalEvidenceStore(LocalEvidenceStoreConfig{Path: path})
	if err != nil {
		t.Fatalf("OpenLocalEvidenceStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func assertPrivateEvidenceFilePermissions(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("evidence file permissions = %04o, want no group/other access", info.Mode().Perm())
	}
}

func readAllLocalEvidence(t *testing.T, store *LocalEvidenceStore) []aieval.EvaluationEvidence {
	t.Helper()
	evidence, err := store.ReadAll(context.Background())
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return evidence
}

func newStoredEvidence(t *testing.T, sampleID string, score float64) aieval.EvaluationEvidence {
	t.Helper()
	threshold := 0.8
	evidence, err := aieval.NewEvaluationEvidence(aieval.EvaluationEvidenceInput{
		Identity: obs.NewCorrelationIdentity(
			"req-t073-"+sampleID,
			obs.WithServiceSpan("service-trace-t073-"+sampleID, "span-t073-"+sampleID),
			obs.WithAITraceID("ai-trace-t073-"+sampleID),
			obs.WithEvalRunID("eval-run-t073-"+sampleID),
		),
		Dataset:    aieval.DatasetIdentity{Name: "chat-golden", Version: "v1"},
		SampleID:   sampleID,
		MetricName: "answer_relevance",
		Score:      score,
		Threshold:  &threshold,
	})
	if err != nil {
		t.Fatalf("NewEvaluationEvidence() error = %v", err)
	}
	return evidence
}

func twoDigit(value int) string {
	return fmt.Sprintf("%02d", value)
}
