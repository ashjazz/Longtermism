package eval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	aieval "github.com/ashjazz/Longtermism/pkg/ai/eval"
	"github.com/ashjazz/Longtermism/pkg/ai/obs"
	"golang.org/x/sys/unix"
)

const (
	// DefaultLocalEvidenceRetention 是低敏本地 evidence 的数据生命周期上限。
	//
	// T093 只固定配置边界和每条记录的 persisted_at；真正的过期清理由 T119
	// 统一实现，避免在每次 Append 时扫描并重写整个事实文件。
	DefaultLocalEvidenceRetention = 90 * 24 * time.Hour

	localEvidenceSchemaVersion  = 1
	maxLocalEvidenceRecordBytes = 64 * 1024
	maxLocalEvidenceFactBytes   = 256
	maxLocalEvidenceFileBytes   = 64 * 1024 * 1024
	evidenceLockRetryInterval   = 5 * time.Millisecond
)

var (
	ErrEvidenceStorageUnavailable = errors.New("local evidence storage unavailable")
	ErrEvidenceStoreConfiguration = errors.New("local evidence store configuration is invalid")
	ErrEvidenceInvalid            = errors.New("local evidence is invalid")
	ErrEvidenceStoreClosed        = errors.New("local evidence store is closed")
	ErrEvidenceStoreCorrupt       = errors.New("local evidence store is corrupt")
	ErrEvidenceStoreCapacity      = errors.New("local evidence store capacity exceeded")
)

// LocalEvidenceStoreConfig 声明本地事实文件及其最长保留边界。
//
// Retention 为零时使用 90 天默认值；允许更短的最小化策略，但禁止超过项目的数据
// 生命周期基线。这里不包含任何 Langfuse 或平台配置，本地 evidence 始终是事实源。
type LocalEvidenceStoreConfig struct {
	Path      string
	Retention time.Duration
}

// LocalEvidenceStore 是并发安全的低敏 JSONL evidence store。
//
// 同一 handle 由可取消的 operationGate 串行化；不同 handle 或不同进程通过相同 sidecar lock
// 文件协调。数据文件每次操作重新打开，使后续 retention compaction 原子替换 inode
// 后，旧 handle 也不会继续写入已经被替换的文件。
type LocalEvidenceStore struct {
	dataName  string
	lockName  string
	retention time.Duration
	directory *os.File
	lockFile  *os.File

	stateMu       sync.RWMutex
	operationGate chan struct{}
	isClosed      bool
}

type localEvidenceRecord struct {
	SchemaVersion int                   `json:"schema_version"`
	PersistedAt   time.Time             `json:"persisted_at"`
	Evidence      localEvidenceSnapshot `json:"evidence"`
}

// localEvidenceSnapshot 是稳定的磁盘 schema，不依赖领域 struct 的 Go 字段名。
// 它只投影 EvaluationEvidence 已有的低敏事实，不引入平台状态或原始内容。
type localEvidenceSnapshot struct {
	EvalRunID        string                  `json:"eval_run_id"`
	RequestID        string                  `json:"request_id"`
	AITraceID        string                  `json:"ai_trace_id"`
	ServiceTraceID   string                  `json:"service_trace_id"`
	SpanID           string                  `json:"span_id"`
	Dataset          aieval.DatasetIdentity  `json:"dataset"`
	SampleID         string                  `json:"sample_id"`
	MetricName       string                  `json:"metric_name"`
	Score            float64                 `json:"score"`
	Threshold        *float64                `json:"threshold,omitempty"`
	RegressionStatus aieval.RegressionStatus `json:"regression_status"`
	FailureSummary   string                  `json:"failure_summary,omitempty"`
}

type evidenceStoreError struct {
	category  error
	operation string
	cause     error
}

func (err *evidenceStoreError) Error() string {
	return err.category.Error() + ": " + err.operation
}

func (err *evidenceStoreError) Unwrap() []error {
	if err.cause == nil {
		return []error{err.category}
	}
	return []error{err.category, err.cause}
}

// OpenLocalEvidenceStore 打开或创建私有 evidence JSONL 文件。
func OpenLocalEvidenceStore(config LocalEvidenceStoreConfig) (*LocalEvidenceStore, error) {
	path, retention, err := normalizeLocalEvidenceStoreConfig(config)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, newEvidenceStoreError(ErrEvidenceStorageUnavailable, "create_parent", err)
	}

	directory, err := openPrivateEvidenceDirectory(filepath.Dir(path))
	if err != nil {
		return nil, newEvidenceStoreError(ErrEvidenceStorageUnavailable, "open_parent", err)
	}
	dataName := filepath.Base(path)
	lockName := dataName + ".lock"
	lockFile, err := openPrivateEvidenceFileAt(directory, lockName, unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		_ = directory.Close()
		return nil, newEvidenceStoreError(ErrEvidenceStorageUnavailable, "open_lock", err)
	}
	store := &LocalEvidenceStore{
		dataName:      dataName,
		lockName:      lockName,
		retention:     retention,
		directory:     directory,
		lockFile:      lockFile,
		operationGate: make(chan struct{}, 1),
	}
	if err := store.withFileLock(context.Background(), unix.LOCK_EX, func() error {
		file, openErr := openPrivateEvidenceFileAt(directory, dataName, unix.O_RDWR|unix.O_CREAT)
		if openErr != nil {
			return newEvidenceStoreError(ErrEvidenceStorageUnavailable, "open_data", openErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return newEvidenceStoreError(ErrEvidenceStorageUnavailable, "close_data", closeErr)
		}
		if syncErr := retryUnixError(func() error { return unix.Fsync(int(directory.Fd())) }); syncErr != nil {
			return newEvidenceStoreError(ErrEvidenceStorageUnavailable, "sync_parent", syncErr)
		}
		return nil
	}); err != nil {
		_ = lockFile.Close()
		_ = directory.Close()
		return nil, err
	}
	return store, nil
}

// Retention 返回已经规范化的本地保留边界。
func (store *LocalEvidenceStore) Retention() time.Duration {
	if store == nil {
		return 0
	}
	store.stateMu.RLock()
	defer store.stateMu.RUnlock()
	return store.retention
}

// Append 在返回成功前完成单条 JSONL 追加和 fsync。
//
// 先校验、复制并编码，再取得文件锁，确保非法 evidence 或 marshal 失败不会在磁盘
// 留下半条记录。Sync 失败会尝试回滚本次追加，但调用方仍应把它视为 durability 未确认。
func (store *LocalEvidenceStore) Append(ctx context.Context, evidence aieval.EvaluationEvidence) error {
	if err := validateEvidenceContext(ctx); err != nil {
		return err
	}
	canonical, err := validateAndCloneEvidence(evidence)
	if err != nil {
		return err
	}
	record := localEvidenceRecord{
		SchemaVersion: localEvidenceSchemaVersion,
		PersistedAt:   time.Now().UTC(),
		Evidence:      snapshotFromEvidence(canonical),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return newEvidenceStoreError(ErrEvidenceInvalid, "encode_record", err)
	}
	if len(encoded)+1 > maxLocalEvidenceRecordBytes {
		return newEvidenceStoreError(ErrEvidenceInvalid, "record_too_large", nil)
	}
	payload := append(encoded, '\n')

	return store.withFileLock(ctx, unix.LOCK_EX, func() error {
		return appendEvidencePayload(store.directory, store.dataName, payload)
	})
}

// ReadAll 返回当前完整、低敏的 evidence 快照。
//
// 每次调用都重新从 JSON 解码，因此返回值中的 Threshold 指针和调用方此前读到的值
// 不共享内存。损坏或未知版本 fail closed，不能静默跳过事实。
func (store *LocalEvidenceStore) ReadAll(ctx context.Context) ([]aieval.EvaluationEvidence, error) {
	if err := validateEvidenceContext(ctx); err != nil {
		return nil, err
	}

	var evidence []aieval.EvaluationEvidence
	err := store.withFileLock(ctx, unix.LOCK_SH, func() error {
		file, err := openPrivateEvidenceFileAt(store.directory, store.dataName, unix.O_RDONLY)
		if err != nil {
			return newEvidenceStoreError(ErrEvidenceStorageUnavailable, "open_data_for_read", err)
		}
		defer file.Close()
		info, statErr := file.Stat()
		if statErr != nil {
			return newEvidenceStoreError(ErrEvidenceStorageUnavailable, "stat_data_for_read", statErr)
		}
		if info.Size() > maxLocalEvidenceFileBytes {
			return ErrEvidenceStoreCapacity
		}

		decoded, decodeErr := decodeLocalEvidence(ctx, file)
		if decodeErr != nil {
			return decodeErr
		}
		evidence = decoded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return evidence, nil
}

// Find returns defensive copies for one exact eval-run identity. The lifecycle
// deliberately receives a slice so zero/duplicate facts remain distinguishable
// and are rejected before any platform projection is admitted.
func (store *LocalEvidenceStore) Find(ctx context.Context, evalRunID string) ([]aieval.EvaluationEvidence, error) {
	if strings.TrimSpace(evalRunID) == "" {
		return nil, ErrEvidenceInvalid
	}
	records, err := store.ReadAll(ctx)
	if err != nil {
		return nil, err
	}
	matches := make([]aieval.EvaluationEvidence, 0, 1)
	for _, record := range records {
		if record.EvalRunID == evalRunID {
			cloned := record
			cloned.Threshold = cloneEvidenceThreshold(record.Threshold)
			matches = append(matches, cloned)
		}
	}
	return matches, nil
}

// Close 幂等关闭 sidecar lock。正在进行的读写持有 stateMu 的读锁，因此 Close
// 会等待它们完成，不会在 Flock 或文件操作中途关闭 descriptor。
func (store *LocalEvidenceStore) Close() error {
	if store == nil {
		return nil
	}
	store.stateMu.Lock()
	defer store.stateMu.Unlock()
	if store.isClosed {
		return nil
	}
	store.isClosed = true
	var closeErr error
	if store.lockFile != nil {
		closeErr = store.lockFile.Close()
	}
	if store.directory != nil {
		if err := store.directory.Close(); closeErr == nil {
			closeErr = err
		}
	}
	if closeErr != nil {
		return newEvidenceStoreError(ErrEvidenceStorageUnavailable, "close_files", closeErr)
	}
	return nil
}

func normalizeLocalEvidenceStoreConfig(config LocalEvidenceStoreConfig) (string, time.Duration, error) {
	if strings.TrimSpace(config.Path) == "" {
		return "", 0, newEvidenceStoreError(ErrEvidenceStoreConfiguration, "path_required", nil)
	}
	retention := config.Retention
	if retention == 0 {
		retention = DefaultLocalEvidenceRetention
	}
	if retention < 0 || retention > DefaultLocalEvidenceRetention {
		return "", 0, newEvidenceStoreError(ErrEvidenceStoreConfiguration, "retention_out_of_range", nil)
	}
	path := filepath.Clean(config.Path)
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "", 0, newEvidenceStoreError(ErrEvidenceStoreConfiguration, "file_name_required", nil)
	}
	return path, retention, nil
}

func openPrivateEvidenceDirectory(path string) (*os.File, error) {
	descriptor, err := retryUnixInt(func() (int, error) {
		return unix.Open(
			path,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
			0,
		)
	})
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := retryUnixError(func() error { return unix.Fstat(descriptor, &stat) }); err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o022 != 0 {
		_ = unix.Close(descriptor)
		return nil, errors.New("evidence parent directory is not private")
	}
	directory := os.NewFile(uintptr(descriptor), "evidence-directory")
	if directory == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("wrap evidence directory descriptor")
	}
	return directory, nil
}

func openPrivateEvidenceFileAt(directory *os.File, name string, flags int) (*os.File, error) {
	if directory == nil || name == "" || name != filepath.Base(name) {
		return nil, errors.New("evidence file name is invalid")
	}
	// O_NONBLOCK prevents a replaced FIFO/device from hanging before fstat can reject it.
	// It has no effect on regular-file reads and writes.
	descriptor, err := retryUnixInt(func() (int, error) {
		return unix.Openat(
			int(directory.Fd()),
			name,
			flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
			0o600,
		)
	})
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	if err := retryUnixError(func() error { return unix.Fstat(descriptor, &stat) }); err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = unix.Close(descriptor)
		return nil, errors.New("evidence path is not a regular file")
	}
	if err := retryUnixError(func() error { return unix.Fchmod(descriptor, 0o600) }); err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), name)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("wrap evidence file descriptor")
	}
	return file, nil
}

func appendEvidencePayload(directory *os.File, dataName string, payload []byte) error {
	file, err := openPrivateEvidenceFileAt(directory, dataName, unix.O_WRONLY|unix.O_APPEND)
	if err != nil {
		return newEvidenceStoreError(ErrEvidenceStorageUnavailable, "open_data_for_append", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return newEvidenceStoreError(ErrEvidenceStorageUnavailable, "stat_data", err)
	}
	originalSize := info.Size()
	if originalSize > maxLocalEvidenceFileBytes-int64(len(payload)) {
		return ErrEvidenceStoreCapacity
	}
	written, writeErr := file.Write(payload)
	if writeErr != nil || written != len(payload) {
		rollbackEvidenceAppend(file, originalSize)
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		return newEvidenceStoreError(ErrEvidenceStorageUnavailable, "append_record", writeErr)
	}
	if err := file.Sync(); err != nil {
		rollbackEvidenceAppend(file, originalSize)
		return newEvidenceStoreError(ErrEvidenceStorageUnavailable, "sync_record", err)
	}
	return nil
}

func rollbackEvidenceAppend(file *os.File, originalSize int64) {
	if err := file.Truncate(originalSize); err == nil {
		_ = file.Sync()
	}
}

func decodeLocalEvidence(ctx context.Context, reader io.Reader) ([]aieval.EvaluationEvidence, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxLocalEvidenceRecordBytes)
	evidence := make([]aieval.EvaluationEvidence, 0)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			return nil, corruptEvidenceStoreError(lineNumber, "empty_record")
		}
		record, err := decodeLocalEvidenceRecord(line)
		if err != nil {
			return nil, corruptEvidenceStoreError(lineNumber, "invalid_record")
		}
		if record.SchemaVersion != localEvidenceSchemaVersion || record.PersistedAt.IsZero() {
			return nil, corruptEvidenceStoreError(lineNumber, "unsupported_record")
		}
		canonical, err := validateAndCloneEvidence(record.Evidence.toEvidence())
		if err != nil {
			return nil, corruptEvidenceStoreError(lineNumber, "invalid_evidence")
		}
		evidence = append(evidence, canonical)
	}
	if err := scanner.Err(); err != nil {
		return nil, newEvidenceStoreError(ErrEvidenceStoreCorrupt, "record_scan_failed", err)
	}
	return evidence, nil
}

func decodeLocalEvidenceRecord(line []byte) (localEvidenceRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var record localEvidenceRecord
	if err := decoder.Decode(&record); err != nil {
		return localEvidenceRecord{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return localEvidenceRecord{}, errors.New("record contains trailing JSON")
	}
	return record, nil
}

func validateAndCloneEvidence(evidence aieval.EvaluationEvidence) (aieval.EvaluationEvidence, error) {
	textFacts := map[string]string{
		"eval_run_id":       evidence.EvalRunID,
		"request_id":        evidence.RequestID,
		"ai_trace_id":       evidence.AITraceID,
		"service_trace_id":  evidence.ServiceTraceID,
		"span_id":           evidence.SpanID,
		"dataset_name":      evidence.Dataset.Name,
		"dataset_version":   evidence.Dataset.Version,
		"sample_id":         evidence.SampleID,
		"metric_name":       evidence.MetricName,
		"regression_status": string(evidence.RegressionStatus),
		"failure_summary":   evidence.FailureSummary,
	}
	for key, value := range textFacts {
		if len(value) > maxLocalEvidenceFactBytes {
			return aieval.EvaluationEvidence{}, newEvidenceStoreError(ErrEvidenceInvalid, key+"_too_large", nil)
		}
	}
	if len(obs.ScanForbiddenPayloadFields(textFacts)) > 0 {
		return aieval.EvaluationEvidence{}, newEvidenceStoreError(ErrEvidenceInvalid, "unsafe_text_fact", nil)
	}

	canonical, err := aieval.NewEvaluationEvidence(aieval.EvaluationEvidenceInput{
		Identity: obs.NewCorrelationIdentity(
			evidence.RequestID,
			obs.WithServiceSpan(evidence.ServiceTraceID, evidence.SpanID),
			obs.WithAITraceID(evidence.AITraceID),
			obs.WithEvalRunID(evidence.EvalRunID),
		),
		Dataset:    evidence.Dataset,
		SampleID:   evidence.SampleID,
		MetricName: evidence.MetricName,
		Score:      evidence.Score,
		Threshold:  cloneEvidenceThreshold(evidence.Threshold),
	})
	if err != nil || !evidenceMatchesCanonical(evidence, canonical) {
		return aieval.EvaluationEvidence{}, newEvidenceStoreError(ErrEvidenceInvalid, "domain_invariant_failed", err)
	}
	return canonical, nil
}

func evidenceMatchesCanonical(input, canonical aieval.EvaluationEvidence) bool {
	return input.EvalRunID == canonical.EvalRunID &&
		input.RequestID == canonical.RequestID &&
		input.AITraceID == canonical.AITraceID &&
		input.ServiceTraceID == canonical.ServiceTraceID &&
		input.SpanID == canonical.SpanID &&
		input.Dataset == canonical.Dataset &&
		input.SampleID == canonical.SampleID &&
		input.MetricName == canonical.MetricName &&
		input.Score == canonical.Score &&
		evidenceThresholdsEqual(input.Threshold, canonical.Threshold) &&
		input.RegressionStatus == canonical.RegressionStatus &&
		input.FailureSummary == canonical.FailureSummary
}

func evidenceThresholdsEqual(first, second *float64) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return *first == *second
}

func cloneEvidenceThreshold(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func snapshotFromEvidence(evidence aieval.EvaluationEvidence) localEvidenceSnapshot {
	return localEvidenceSnapshot{
		EvalRunID:        evidence.EvalRunID,
		RequestID:        evidence.RequestID,
		AITraceID:        evidence.AITraceID,
		ServiceTraceID:   evidence.ServiceTraceID,
		SpanID:           evidence.SpanID,
		Dataset:          evidence.Dataset,
		SampleID:         evidence.SampleID,
		MetricName:       evidence.MetricName,
		Score:            evidence.Score,
		Threshold:        cloneEvidenceThreshold(evidence.Threshold),
		RegressionStatus: evidence.RegressionStatus,
		FailureSummary:   evidence.FailureSummary,
	}
}

func (snapshot localEvidenceSnapshot) toEvidence() aieval.EvaluationEvidence {
	return aieval.EvaluationEvidence{
		EvalRunID:        snapshot.EvalRunID,
		RequestID:        snapshot.RequestID,
		AITraceID:        snapshot.AITraceID,
		ServiceTraceID:   snapshot.ServiceTraceID,
		SpanID:           snapshot.SpanID,
		Dataset:          snapshot.Dataset,
		SampleID:         snapshot.SampleID,
		MetricName:       snapshot.MetricName,
		Score:            snapshot.Score,
		Threshold:        cloneEvidenceThreshold(snapshot.Threshold),
		RegressionStatus: snapshot.RegressionStatus,
		FailureSummary:   snapshot.FailureSummary,
	}
}

func (store *LocalEvidenceStore) withFileLock(ctx context.Context, lockType int, operation func() error) (resultErr error) {
	if store == nil {
		return ErrEvidenceStoreClosed
	}
	if err := validateEvidenceContext(ctx); err != nil {
		return err
	}

	store.stateMu.RLock()
	defer store.stateMu.RUnlock()
	if store.isClosed || store.directory == nil || store.lockFile == nil || store.operationGate == nil {
		return ErrEvidenceStoreClosed
	}
	select {
	case store.operationGate <- struct{}{}:
		defer func() { <-store.operationGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateEvidenceLockBinding(store.directory, store.lockName, store.lockFile); err != nil {
		return err
	}

	if err := acquireEvidenceFileLock(ctx, store.lockFile, lockType); err != nil {
		return err
	}
	defer func() {
		if err := retryUnixError(func() error {
			return unix.Flock(int(store.lockFile.Fd()), unix.LOCK_UN)
		}); err != nil && resultErr == nil {
			resultErr = newEvidenceStoreError(ErrEvidenceStorageUnavailable, "unlock", err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateEvidenceLockBinding(store.directory, store.lockName, store.lockFile); err != nil {
		return err
	}
	return operation()
}

func validateEvidenceLockBinding(directory *os.File, lockName string, lockFile *os.File) error {
	var held unix.Stat_t
	if err := retryUnixError(func() error { return unix.Fstat(int(lockFile.Fd()), &held) }); err != nil {
		return newEvidenceStoreError(ErrEvidenceStorageUnavailable, "stat_held_lock", err)
	}
	var current unix.Stat_t
	if err := retryUnixError(func() error {
		return unix.Fstatat(int(directory.Fd()), lockName, &current, unix.AT_SYMLINK_NOFOLLOW)
	}); err != nil {
		return newEvidenceStoreError(ErrEvidenceStorageUnavailable, "stat_current_lock", err)
	}
	if held.Dev != current.Dev || held.Ino != current.Ino || current.Mode&unix.S_IFMT != unix.S_IFREG || current.Nlink != 1 {
		return newEvidenceStoreError(ErrEvidenceStorageUnavailable, "lock_binding_changed", nil)
	}
	return nil
}

func acquireEvidenceFileLock(ctx context.Context, file *os.File, lockType int) error {
	for {
		err := unix.Flock(int(file.Fd()), lockType|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return newEvidenceStoreError(ErrEvidenceStorageUnavailable, "lock", err)
		}

		timer := time.NewTimer(evidenceLockRetryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func retryUnixInt(operation func() (int, error)) (int, error) {
	for {
		value, err := operation()
		if !errors.Is(err, unix.EINTR) {
			return value, err
		}
	}
}

func retryUnixError(operation func() error) error {
	for {
		err := operation()
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func validateEvidenceContext(ctx context.Context) error {
	if ctx == nil {
		return newEvidenceStoreError(ErrEvidenceInvalid, "context_required", nil)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func newEvidenceStoreError(category error, operation string, cause error) error {
	return &evidenceStoreError{
		category:  category,
		operation: operation,
		cause:     cause,
	}
}

func corruptEvidenceStoreError(lineNumber int, operation string) error {
	return newEvidenceStoreError(
		ErrEvidenceStoreCorrupt,
		fmt.Sprintf("%s_at_line_%d", operation, lineNumber),
		nil,
	)
}
