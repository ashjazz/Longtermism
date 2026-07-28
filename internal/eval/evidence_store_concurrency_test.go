package eval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
)

func TestLocalEvidenceStoreCoordinatesAppendsAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.jsonl")
	const recordsPerProcess = 12
	commands := make([]*exec.Cmd, 2)
	outputs := make([]*bytes.Buffer, len(commands))
	for processIndex := range commands {
		outputs[processIndex] = &bytes.Buffer{}
		command := exec.Command(os.Args[0], "-test.run=^TestLocalEvidenceStoreSubprocessWriter$")
		command.Env = append(
			os.Environ(),
			"LONGTERMISM_EVIDENCE_SUBPROCESS=1",
			"LONGTERMISM_EVIDENCE_PATH="+path,
			fmt.Sprintf("LONGTERMISM_EVIDENCE_PREFIX=sample-process-%d-", processIndex),
			fmt.Sprintf("LONGTERMISM_EVIDENCE_COUNT=%d", recordsPerProcess),
		)
		command.Stdout = outputs[processIndex]
		command.Stderr = outputs[processIndex]
		commands[processIndex] = command
		if err := command.Start(); err != nil {
			t.Fatalf("subprocess %d Start() error = %v", processIndex, err)
		}
	}
	for processIndex, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("subprocess %d Wait() error = %v; output=%s", processIndex, err, outputs[processIndex].String())
		}
	}

	store := openLocalEvidenceStore(t, path)
	got := readAllLocalEvidence(t, store)
	if len(got) != len(commands)*recordsPerProcess {
		t.Fatalf("ReadAll() count = %d, want %d cross-process records", len(got), len(commands)*recordsPerProcess)
	}
	seen := make(map[string]struct{}, len(got))
	for _, evidence := range got {
		if _, exists := seen[evidence.SampleID]; exists {
			t.Fatalf("ReadAll() duplicated cross-process sample %q", evidence.SampleID)
		}
		seen[evidence.SampleID] = struct{}{}
	}
}

func TestLocalEvidenceStoreSubprocessWriter(t *testing.T) {
	if os.Getenv("LONGTERMISM_EVIDENCE_SUBPROCESS") != "1" {
		return
	}
	path := os.Getenv("LONGTERMISM_EVIDENCE_PATH")
	prefix := os.Getenv("LONGTERMISM_EVIDENCE_PREFIX")
	count := 0
	if _, err := fmt.Sscanf(os.Getenv("LONGTERMISM_EVIDENCE_COUNT"), "%d", &count); err != nil || count <= 0 {
		t.Fatal("subprocess evidence count is invalid")
	}
	store := openLocalEvidenceStore(t, path)
	for recordIndex := range count {
		evidence := newStoredEvidence(t, prefix+twoDigit(recordIndex), 0.86)
		if err := store.Append(context.Background(), evidence); err != nil {
			t.Fatalf("subprocess Append(%d) error = %v", recordIndex, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("subprocess Close() error = %v", err)
	}
}

func TestLocalEvidenceStoreCloseRacesSafelyWithReadsAndAppends(t *testing.T) {
	store := openLocalEvidenceStore(t, filepath.Join(t.TempDir(), "evidence.jsonl"))
	const operations = 48
	start := make(chan struct{})
	results := make(chan error, operations+1)
	var workers sync.WaitGroup
	evidenceByOperation := make([]aieval.EvaluationEvidence, operations)
	for index := range operations {
		if index%2 == 0 {
			evidenceByOperation[index] = newStoredEvidence(t, "sample-close-race-"+twoDigit(index), 0.93)
		}
	}

	for index := range operations {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			if index%2 == 0 {
				results <- store.Append(context.Background(), evidenceByOperation[index])
				return
			}
			_, err := store.ReadAll(context.Background())
			results <- err
		}(index)
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		results <- store.Close()
	}()
	close(start)
	workers.Wait()
	close(results)

	for err := range results {
		if err != nil && !errors.Is(err, ErrEvidenceStoreClosed) {
			t.Fatalf("concurrent operation error = %v, want nil or ErrEvidenceStoreClosed", err)
		}
	}
}
