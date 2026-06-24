package apperror

import (
	"errors"
	"testing"

	"github.com/jazzash/ashjazz-aiagent/pkg/ai/llm"
)

func TestClassifiedErrorSupportsErrorsIs(t *testing.T) {
	t.Parallel()

	err := New(ClassUpstream, "provider timeout", errors.New("deadline exceeded"))

	if !errors.Is(err, ErrUpstream) {
		t.Fatal("errors.Is(err, ErrUpstream) = false, want true")
	}
	if errors.Is(err, ErrCaller) {
		t.Fatal("errors.Is(err, ErrCaller) = true, want false")
	}
}

func TestClassifiedErrorSupportsErrorsAs(t *testing.T) {
	t.Parallel()

	err := New(ClassCaller, "invalid request", errors.New("missing model"))

	var classified *Error
	if !errors.As(err, &classified) {
		t.Fatal("errors.As(err, *Error) = false, want true")
	}
	if classified.Class != ClassCaller {
		t.Fatalf("Class = %q, want %q", classified.Class, ClassCaller)
	}
	if classified.Message != "invalid request" {
		t.Fatalf("Message = %q, want invalid request", classified.Message)
	}
}

func TestClassifiedErrorPreservesPublicContractCause(t *testing.T) {
	t.Parallel()

	err := New(ClassUpstream, "provider unavailable", llm.ErrUpstream)

	if !errors.Is(err, llm.ErrUpstream) {
		t.Fatal("errors.Is(err, llm.ErrUpstream) = false, want true")
	}
	if !errors.Is(err, ErrUpstream) {
		t.Fatal("errors.Is(err, apperror.ErrUpstream) = false, want true")
	}
}

func TestWrapNilReturnsNil(t *testing.T) {
	t.Parallel()

	if got := Wrap(ClassProtocol, "ignored", nil); got != nil {
		t.Fatalf("Wrap(nil) = %v, want nil", got)
	}
}

func TestClassifiedErrorCoversAllClassesAndNilReceiver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		class  Class
		target error
	}{
		{name: "caller", class: ClassCaller, target: ErrCaller},
		{name: "upstream", class: ClassUpstream, target: ErrUpstream},
		{name: "protocol", class: ClassProtocol, target: ErrProtocol},
		{name: "internal", class: ClassInternal, target: ErrInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New(tt.class, "classified", nil)
			if !errors.Is(err, tt.target) {
				t.Fatalf("errors.Is(%v) = false, want true", tt.target)
			}
			if err.Error() != string(tt.class)+": classified" {
				t.Fatalf("Error() = %q, want class and message", err.Error())
			}
		})
	}

	var nilErr *Error
	if nilErr.Error() != "<nil>" {
		t.Fatalf("nil Error() = %q, want <nil>", nilErr.Error())
	}
	if nilErr.Unwrap() != nil {
		t.Fatal("nil Unwrap() != nil")
	}
	if nilErr.Is(ErrInternal) {
		t.Fatal("nil Is() = true, want false")
	}
	if errors.Is(New(Class("unknown"), "unknown", nil), ErrInternal) {
		t.Fatal("unknown class unexpectedly matched ErrInternal")
	}
}
