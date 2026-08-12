package observability

import (
	"context"
	"errors"
	"testing"
)

func TestHTTPCompletionLogFanoutWriterContinuesAfterSinkFailure(t *testing.T) {
	wantErr := errors.New("synthetic sink rejection")
	first := &completionLogWriterStub{err: wantErr}
	second := &completionLogWriterStub{}
	writer := NewHTTPCompletionLogFanoutWriter(nil, first, second)

	entry := HTTPCompletionLog{}
	err := writer.Write(context.Background(), entry)

	if !errors.Is(err, wantErr) {
		t.Fatalf("Write() error = %v, want %v", err, wantErr)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("sink calls = (%d, %d), want (1, 1)", first.calls, second.calls)
	}
}

func TestHTTPCompletionLogFanoutWriterReturnsNilWithoutSinks(t *testing.T) {
	if writer := NewHTTPCompletionLogFanoutWriter(nil); writer != nil {
		t.Fatalf("NewHTTPCompletionLogFanoutWriter(nil) = %#v, want nil", writer)
	}
}

type completionLogWriterStub struct {
	calls int
	err   error
}

func (w *completionLogWriterStub) Write(context.Context, HTTPCompletionLog) error {
	w.calls++
	return w.err
}
