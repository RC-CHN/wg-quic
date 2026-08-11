package app

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestNamedStatusReadErrorMarksMissingRuntimeInactive(t *testing.T) {
	err := namedStatusReadError(
		"office",
		`\\.\pipe\wg-quic-office`,
		fmt.Errorf("open control endpoint: %w", os.ErrNotExist),
	)
	if !strings.Contains(err.Error(), `interface is inactive: "office"`) {
		t.Fatalf("missing runtime error = %q", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing runtime error does not preserve os.ErrNotExist: %v", err)
	}
}

func TestNamedStatusReadErrorPreservesUnexpectedFailure(t *testing.T) {
	err := namedStatusReadError("office", "control", errors.New("access denied"))
	if strings.Contains(err.Error(), "interface is inactive:") {
		t.Fatalf("unexpected failure was marked inactive: %v", err)
	}
	if got, want := err.Error(), "control: access denied"; got != want {
		t.Fatalf("unexpected failure = %q, want %q", got, want)
	}
}
