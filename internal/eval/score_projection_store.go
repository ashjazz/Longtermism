package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ashjazz/Longtermism/internal/observability/langfuse"
	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"golang.org/x/sys/unix"
)

const (
	scoreProjectionStoreSchemaVersion = 1
	maximumScoreProjectionStoreBytes  = 8 << 20
	maximumTerminalScoreProjections   = 1024
)

var (
	ErrScoreProjectionStoreConflict      = errors.New("score projection store conflict")
	ErrScoreProjectionStoreCorrupt       = errors.New("score projection store is corrupt")
	ErrScoreProjectionStoreConfiguration = errors.New("score projection store configuration is invalid")
	ErrScoreProjectionStoreUnavailable   = errors.New("score projection store is unavailable")
	ErrScoreProjectionStoreClosed        = errors.New("score projection store is closed")
)

type ScoreProjectionStoreConfig struct{ Path string }

// ScoreProjectionSnapshot is the low-sensitivity lookup DTO consumed by smoke.
// It intentionally omits FailureSummary and the remaining evaluation payload.
type ScoreProjectionSnapshot struct {
	RunID, EvalRunID, ProjectionID, RequestID, AITraceID string
	PlatformTraceID, PlatformObservationID               string
	Status                                               langfuse.ScoreProjectionStatus
	Attempt                                              int
	CreatedAt, ObservedAt                                time.Time
	Threshold                                            *float64
}

// StoredScoreProjection embeds the validated recovery snapshot so lifecycle code can
// reconstruct the same value object without reaching into private Langfuse fields.
type StoredScoreProjection struct {
	RunID string
	langfuse.ScoreProjectionRecoverySnapshot
}

type ScoreProjectionStore struct {
	directory *os.File
	lockFile  *os.File
	dataName  string
	lockName  string
	gate      chan struct{}
	mu        sync.RWMutex
	closed    bool
}

type scoreProjectionDiskState struct {
	SchemaVersion int                         `json:"schema_version"`
	Records       []scoreProjectionDiskRecord `json:"records"`
}

type scoreProjectionDiskRecord struct {
	RunID                 string                         `json:"run_id"`
	ProjectionID          string                         `json:"projection_id"`
	Evidence              aieval.EvaluationEvidence      `json:"evidence"`
	TargetKind            langfuse.ScoreTargetKind       `json:"target_kind"`
	PlatformTraceID       string                         `json:"platform_trace_id"`
	PlatformObservationID string                         `json:"platform_observation_id,omitempty"`
	Status                langfuse.ScoreProjectionStatus `json:"status"`
	Attempt               int                            `json:"attempt"`
	CreatedAt             time.Time                      `json:"created_at"`
	ObservedAt            time.Time                      `json:"observed_at"`
	MaxAttempts           int                            `json:"max_attempts"`
}

func OpenScoreProjectionStore(config ScoreProjectionStoreConfig) (*ScoreProjectionStore, error) {
	path := filepath.Clean(strings.TrimSpace(config.Path))
	if path == "." || filepath.Base(path) == "." || filepath.Base(path) == string(filepath.Separator) {
		return nil, ErrScoreProjectionStoreConfiguration
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrScoreProjectionStoreConfiguration
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, ErrScoreProjectionStoreConfiguration
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, ErrScoreProjectionStoreUnavailable
	}
	directory, err := openPrivateEvidenceDirectory(filepath.Dir(path))
	if err != nil {
		return nil, ErrScoreProjectionStoreConfiguration
	}
	dataName, lockName := filepath.Base(path), filepath.Base(path)+".lock"
	lockFile, err := openPrivateEvidenceFileAt(directory, lockName, unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		_ = directory.Close()
		return nil, ErrScoreProjectionStoreConfiguration
	}
	store := &ScoreProjectionStore{directory: directory, lockFile: lockFile, dataName: dataName, lockName: lockName, gate: make(chan struct{}, 1)}
	err = store.withLock(context.Background(), unix.LOCK_EX, func() error {
		file, openErr := openPrivateEvidenceFileAt(directory, dataName, unix.O_RDWR|unix.O_CREAT)
		if openErr != nil {
			return ErrScoreProjectionStoreConfiguration
		}
		_ = file.Close()
		_, readErr := store.readState()
		return readErr
	})
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func (store *ScoreProjectionStore) SaveInitial(ctx context.Context, runID string, projection langfuse.ScoreProjection, maxAttempts int) error {
	if strings.TrimSpace(runID) == "" || runID != strings.TrimSpace(runID) {
		return ErrScoreProjectionStoreConflict
	}
	record, err := diskRecord(runID, projection, maxAttempts, time.Now().UTC())
	if err != nil || projection.Status != langfuse.ScoreProjectionStatusQueued || projection.Attempt != 0 {
		return ErrScoreProjectionStoreConflict
	}
	return store.mutate(ctx, func(state scoreProjectionDiskState) (scoreProjectionDiskState, error) {
		for _, existing := range state.Records {
			if existing.RunID == runID || existing.ProjectionID == projection.ProjectionID {
				return state, ErrScoreProjectionStoreConflict
			}
		}
		state.Records = append(state.Records, record)
		state.Records = compactTerminalScoreProjections(state.Records, maximumTerminalScoreProjections)
		return state, nil
	})
}

func (store *ScoreProjectionStore) Update(ctx context.Context, runID string, projection langfuse.ScoreProjection) error {
	return store.mutate(ctx, func(state scoreProjectionDiskState) (scoreProjectionDiskState, error) {
		for index, current := range state.Records {
			if current.RunID != runID {
				continue
			}
			next, err := diskRecord(runID, projection, current.MaxAttempts, time.Now().UTC())
			if err != nil || !sameProjectionIdentity(current, next) || !validPersistedTransition(current, next) {
				return state, ErrScoreProjectionStoreConflict
			}
			state.Records[index] = next
			state.Records = compactTerminalScoreProjections(state.Records, maximumTerminalScoreProjections)
			return state, nil
		}
		return state, ErrScoreProjectionStoreConflict
	})
}

func (store *ScoreProjectionStore) FindByRunID(ctx context.Context, runID string) ([]ScoreProjectionSnapshot, error) {
	var result []ScoreProjectionSnapshot
	err := store.withLock(ctx, unix.LOCK_SH, func() error {
		state, err := store.readState()
		if err != nil {
			return err
		}
		for _, record := range state.Records {
			if record.RunID == runID {
				result = append(result, snapshotFromRecord(record))
			}
		}
		return nil
	})
	return result, err
}

func (store *ScoreProjectionStore) LoadPending(ctx context.Context) ([]StoredScoreProjection, error) {
	var result []StoredScoreProjection
	err := store.withLock(ctx, unix.LOCK_SH, func() error {
		state, err := store.readState()
		if err != nil {
			return err
		}
		for _, record := range state.Records {
			if record.Status != langfuse.ScoreProjectionStatusQueued && record.Status != langfuse.ScoreProjectionStatusSending && record.Status != langfuse.ScoreProjectionStatusRetryWait {
				continue
			}
			result = append(result, storedFromRecord(record))
		}
		sort.Slice(result, func(i, j int) bool { return result[i].RunID < result[j].RunID })
		return nil
	})
	return result, err
}

func (store *ScoreProjectionStore) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	var err error
	if store.lockFile != nil {
		err = store.lockFile.Close()
	}
	if store.directory != nil && store.directory.Close() != nil && err == nil {
		err = ErrScoreProjectionStoreUnavailable
	}
	return err
}

func (store *ScoreProjectionStore) mutate(ctx context.Context, fn func(scoreProjectionDiskState) (scoreProjectionDiskState, error)) error {
	return store.withLock(ctx, unix.LOCK_EX, func() error {
		state, err := store.readState()
		if err != nil {
			return err
		}
		next, err := fn(state)
		if err != nil {
			return err
		}
		return store.writeState(next)
	})
}

func (store *ScoreProjectionStore) withLock(ctx context.Context, kind int, fn func() error) (resultErr error) {
	if store == nil || ctx == nil {
		return ErrScoreProjectionStoreClosed
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.closed || store.lockFile == nil {
		return ErrScoreProjectionStoreClosed
	}
	select {
	case store.gate <- struct{}{}:
		defer func() { <-store.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if validateEvidenceLockBinding(store.directory, store.lockName, store.lockFile) != nil {
		return ErrScoreProjectionStoreUnavailable
	}
	if err := acquireEvidenceFileLock(ctx, store.lockFile, kind); err != nil {
		return ErrScoreProjectionStoreUnavailable
	}
	defer func() {
		if err := retryUnixError(func() error { return unix.Flock(int(store.lockFile.Fd()), unix.LOCK_UN) }); err != nil && resultErr == nil {
			resultErr = ErrScoreProjectionStoreUnavailable
		}
	}()
	if validateEvidenceLockBinding(store.directory, store.lockName, store.lockFile) != nil {
		return ErrScoreProjectionStoreUnavailable
	}
	return fn()
}

func (store *ScoreProjectionStore) readState() (scoreProjectionDiskState, error) {
	file, err := openPrivateEvidenceFileAt(store.directory, store.dataName, unix.O_RDONLY)
	if err != nil {
		return scoreProjectionDiskState{}, ErrScoreProjectionStoreUnavailable
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > maximumScoreProjectionStoreBytes {
		return scoreProjectionDiskState{}, ErrScoreProjectionStoreCorrupt
	}
	if info.Size() == 0 {
		return scoreProjectionDiskState{SchemaVersion: scoreProjectionStoreSchemaVersion}, nil
	}
	payload, err := io.ReadAll(io.LimitReader(file, maximumScoreProjectionStoreBytes+1))
	if err != nil || len(payload) > maximumScoreProjectionStoreBytes {
		return scoreProjectionDiskState{}, ErrScoreProjectionStoreCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var state scoreProjectionDiskState
	if decoder.Decode(&state) != nil || decoder.Decode(&struct{}{}) != io.EOF || state.SchemaVersion != scoreProjectionStoreSchemaVersion {
		return scoreProjectionDiskState{}, ErrScoreProjectionStoreCorrupt
	}
	seenRuns, seenIDs := map[string]bool{}, map[string]bool{}
	for _, record := range state.Records {
		if seenRuns[record.RunID] || seenIDs[record.ProjectionID] || validateDiskRecord(record) != nil {
			return scoreProjectionDiskState{}, ErrScoreProjectionStoreCorrupt
		}
		seenRuns[record.RunID], seenIDs[record.ProjectionID] = true, true
	}
	return state, nil
}

func (store *ScoreProjectionStore) writeState(state scoreProjectionDiskState) error {
	payload, err := json.Marshal(state)
	if err != nil || len(payload) > maximumScoreProjectionStoreBytes {
		return ErrScoreProjectionStoreUnavailable
	}
	tempName := store.dataName + ".tmp"
	_ = unix.Unlinkat(int(store.directory.Fd()), tempName, 0)
	temp, err := openPrivateEvidenceFileAt(store.directory, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return ErrScoreProjectionStoreUnavailable
	}
	cleanup := func() { _ = temp.Close(); _ = unix.Unlinkat(int(store.directory.Fd()), tempName, 0) }
	if _, err = temp.Write(payload); err != nil || temp.Sync() != nil || temp.Close() != nil {
		cleanup()
		return ErrScoreProjectionStoreUnavailable
	}
	if err = unix.Renameat(int(store.directory.Fd()), tempName, int(store.directory.Fd()), store.dataName); err != nil {
		cleanup()
		return ErrScoreProjectionStoreUnavailable
	}
	if err = unix.Fsync(int(store.directory.Fd())); err != nil {
		return ErrScoreProjectionStoreUnavailable
	}
	return nil
}

func diskRecord(runID string, projection langfuse.ScoreProjection, maxAttempts int, observedAt time.Time) (scoreProjectionDiskRecord, error) {
	recovery := langfuse.ScoreProjectionRecoverySnapshot{ProjectionID: projection.ProjectionID, Evidence: projection.Evidence, TargetKind: projection.Target.Kind(), PlatformTraceID: projection.Target.PlatformTraceID(), PlatformObservationID: projection.Target.PlatformObservationID(), Status: projection.Status, Attempt: projection.Attempt, CreatedAt: projection.CreatedAt, MaxAttempts: maxAttempts}
	restored, err := langfuse.RestoreScoreProjection(recovery)
	if err != nil || restored.ProjectionID != projection.ProjectionID {
		return scoreProjectionDiskRecord{}, ErrScoreProjectionStoreConflict
	}
	return scoreProjectionDiskRecord{RunID: runID, ProjectionID: recovery.ProjectionID, Evidence: cloneStoreEvidence(recovery.Evidence), TargetKind: recovery.TargetKind, PlatformTraceID: recovery.PlatformTraceID, PlatformObservationID: recovery.PlatformObservationID, Status: recovery.Status, Attempt: recovery.Attempt, CreatedAt: recovery.CreatedAt.UTC(), ObservedAt: observedAt.UTC(), MaxAttempts: recovery.MaxAttempts}, nil
}

func validateDiskRecord(record scoreProjectionDiskRecord) error {
	if strings.TrimSpace(record.RunID) == "" || record.RunID != strings.TrimSpace(record.RunID) || record.ObservedAt.IsZero() {
		return ErrScoreProjectionStoreCorrupt
	}
	_, err := langfuse.RestoreScoreProjection(langfuse.ScoreProjectionRecoverySnapshot{ProjectionID: record.ProjectionID, Evidence: record.Evidence, TargetKind: record.TargetKind, PlatformTraceID: record.PlatformTraceID, PlatformObservationID: record.PlatformObservationID, Status: record.Status, Attempt: record.Attempt, CreatedAt: record.CreatedAt, MaxAttempts: record.MaxAttempts})
	return err
}

func sameProjectionIdentity(a, b scoreProjectionDiskRecord) bool {
	return a.RunID == b.RunID && a.ProjectionID == b.ProjectionID && a.TargetKind == b.TargetKind && a.PlatformTraceID == b.PlatformTraceID && a.PlatformObservationID == b.PlatformObservationID && a.CreatedAt.Equal(b.CreatedAt) && a.MaxAttempts == b.MaxAttempts && evidenceEqual(a.Evidence, b.Evidence)
}

func validPersistedTransition(current, next scoreProjectionDiskRecord) bool {
	if next.Attempt < current.Attempt {
		return false
	}
	// A process may stop after persisting "sending" but before learning whether the
	// idempotent platform create completed. Lifecycle recovery first normalizes that
	// snapshot to queued and persists it before re-admission. This is the sole durable
	// transition that is broader than the in-process state machine.
	if current.Status == langfuse.ScoreProjectionStatusSending && next.Status == langfuse.ScoreProjectionStatusQueued {
		return current.Attempt == next.Attempt
	}
	restored, err := langfuse.RestoreScoreProjection(langfuse.ScoreProjectionRecoverySnapshot{ProjectionID: current.ProjectionID, Evidence: current.Evidence, TargetKind: current.TargetKind, PlatformTraceID: current.PlatformTraceID, PlatformObservationID: current.PlatformObservationID, Status: current.Status, Attempt: current.Attempt, CreatedAt: current.CreatedAt, MaxAttempts: current.MaxAttempts})
	if err != nil {
		return false
	}
	want, err := restored.Transition(next.Status)
	return err == nil && want.Status == next.Status && want.Attempt == next.Attempt
}

func snapshotFromRecord(r scoreProjectionDiskRecord) ScoreProjectionSnapshot {
	return ScoreProjectionSnapshot{RunID: r.RunID, EvalRunID: r.Evidence.EvalRunID, ProjectionID: r.ProjectionID, RequestID: r.Evidence.RequestID, AITraceID: r.Evidence.AITraceID, PlatformTraceID: r.PlatformTraceID, PlatformObservationID: r.PlatformObservationID, Status: r.Status, Attempt: r.Attempt, CreatedAt: r.CreatedAt, ObservedAt: r.ObservedAt, Threshold: cloneThreshold(r.Evidence.Threshold)}
}
func storedFromRecord(r scoreProjectionDiskRecord) StoredScoreProjection {
	return StoredScoreProjection{RunID: r.RunID, ScoreProjectionRecoverySnapshot: langfuse.ScoreProjectionRecoverySnapshot{ProjectionID: r.ProjectionID, Evidence: cloneStoreEvidence(r.Evidence), TargetKind: r.TargetKind, PlatformTraceID: r.PlatformTraceID, PlatformObservationID: r.PlatformObservationID, Status: r.Status, Attempt: r.Attempt, CreatedAt: r.CreatedAt, MaxAttempts: r.MaxAttempts}}
}
func cloneStoreEvidence(v aieval.EvaluationEvidence) aieval.EvaluationEvidence {
	v.Threshold = cloneThreshold(v.Threshold)
	return v
}
func cloneThreshold(v *float64) *float64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}
func evidenceEqual(a, b aieval.EvaluationEvidence) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(aa, bb)
}

func compactTerminalScoreProjections(records []scoreProjectionDiskRecord, limit int) []scoreProjectionDiskRecord {
	if limit < 0 {
		limit = 0
	}
	terminalIndexes := make([]int, 0, len(records))
	for index, record := range records {
		if isTerminalStoredProjection(record.Status) {
			terminalIndexes = append(terminalIndexes, index)
		}
	}
	if len(terminalIndexes) <= limit {
		return records
	}
	sort.Slice(terminalIndexes, func(i, j int) bool {
		return records[terminalIndexes[i]].ObservedAt.After(records[terminalIndexes[j]].ObservedAt)
	})
	keep := make(map[int]struct{}, limit)
	for _, index := range terminalIndexes[:limit] {
		keep[index] = struct{}{}
	}
	compacted := make([]scoreProjectionDiskRecord, 0, len(records)-len(terminalIndexes)+limit)
	for index, record := range records {
		if !isTerminalStoredProjection(record.Status) {
			compacted = append(compacted, record)
			continue
		}
		if _, ok := keep[index]; ok {
			compacted = append(compacted, record)
		}
	}
	return compacted
}

func isTerminalStoredProjection(status langfuse.ScoreProjectionStatus) bool {
	switch status {
	case langfuse.ScoreProjectionStatusSent,
		langfuse.ScoreProjectionStatusDroppedQueueFull,
		langfuse.ScoreProjectionStatusFailedPermanent,
		langfuse.ScoreProjectionStatusFailedShutdownTimeout,
		langfuse.ScoreProjectionStatusNotConfigured:
		return true
	default:
		return false
	}
}
