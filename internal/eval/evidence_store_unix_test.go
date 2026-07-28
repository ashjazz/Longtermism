package eval

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEvidenceStoreRetriesInterruptibleUnixOperations(t *testing.T) {
	intCalls := 0
	value, err := retryUnixInt(func() (int, error) {
		intCalls++
		if intCalls < 3 {
			return 0, unix.EINTR
		}
		return 42, nil
	})
	if err != nil || value != 42 || intCalls != 3 {
		t.Fatalf("retryUnixInt() = (%d, %v, calls:%d), want (42, nil, calls:3)", value, err, intCalls)
	}

	errorCalls := 0
	err = retryUnixError(func() error {
		errorCalls++
		if errorCalls < 3 {
			return unix.EINTR
		}
		return nil
	})
	if err != nil || errorCalls != 3 {
		t.Fatalf("retryUnixError() = (%v, calls:%d), want (nil, calls:3)", err, errorCalls)
	}

	sentinel := errors.New("terminal")
	if _, err := retryUnixInt(func() (int, error) { return 0, sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("retryUnixInt() error = %v, want terminal error", err)
	}
}
