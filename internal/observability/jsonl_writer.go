package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// JSONLHTTPCompletionLogWriter is a narrow sink: it accepts only the already allowlisted
// HTTPCompletionLog value, so a caller cannot turn the shared file into a raw-payload channel.
type JSONLHTTPCompletionLogWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func NewJSONLHTTPCompletionLogWriter(writer io.Writer) (*JSONLHTTPCompletionLogWriter, error) {
	if writer == nil {
		return nil, fmt.Errorf("JSONL HTTP completion writer: writer is required")
	}
	return &JSONLHTTPCompletionLogWriter{writer: writer}, nil
}

func (w *JSONLHTTPCompletionLogWriter) Write(_ context.Context, entry HTTPCompletionLog) error {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal HTTP completion JSONL: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.writer.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write HTTP completion JSONL: %w", err)
	}
	return nil
}
