package eval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
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
