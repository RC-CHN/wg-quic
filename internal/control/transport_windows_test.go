//go:build windows

package control

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestNormalizeNamedPipeMissingErrors(t *testing.T) {
	for _, nativeError := range []error{
		windows.ERROR_FILE_NOT_FOUND,
		windows.ERROR_PATH_NOT_FOUND,
	} {
		original := &os.PathError{
			Op:   "open",
			Path: `\\.\pipe\wg-quic-missing`,
			Err:  nativeError,
		}
		normalized := normalizeNamedPipeDialError(original)
		if !errors.Is(normalized, os.ErrNotExist) {
			t.Fatalf("%v was not normalized to os.ErrNotExist: %v", nativeError, normalized)
		}
		if !errors.Is(normalized, nativeError) {
			t.Fatalf("normalization did not preserve native error %v: %v", nativeError, normalized)
		}
		if normalized.Error() != original.Error() {
			t.Fatalf("normalization changed diagnostic text: %q != %q", normalized, original)
		}
	}
}

func TestNormalizeNamedPipeDoesNotHideAccessDenied(t *testing.T) {
	original := &os.PathError{
		Op:   "open",
		Path: `\\.\pipe\wg-quic-protected`,
		Err:  windows.ERROR_ACCESS_DENIED,
	}
	normalized := normalizeNamedPipeDialError(original)
	if normalized != original {
		t.Fatalf("access-denied error was unexpectedly wrapped: %v", normalized)
	}
	if errors.Is(normalized, os.ErrNotExist) {
		t.Fatalf("access-denied error was classified as missing: %v", normalized)
	}
}
