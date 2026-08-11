//go:build windows

package quick

import (
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWindowsServiceDiagnosticsRetainsBoundedTail(t *testing.T) {
	diagnostics := &windowsServiceDiagnostics{}
	prefix := strings.Repeat("a", windowsServiceDiagnosticOutputSize)
	if written, err := diagnostics.Write([]byte(prefix)); err != nil ||
		written != len(prefix) {
		t.Fatalf("initial diagnostic write = %d, %v", written, err)
	}
	const tail = "final startup failure"
	if written, err := diagnostics.Write([]byte(tail)); err != nil ||
		written != len(tail) {
		t.Fatalf("tail diagnostic write = %d, %v", written, err)
	}
	output := diagnostics.String()
	if !strings.HasPrefix(output, "[earlier service output truncated]\n") {
		t.Fatalf("bounded diagnostics omitted truncation marker: %q", output[:64])
	}
	if !strings.HasSuffix(output, tail) {
		t.Fatal("bounded diagnostics did not retain the newest failure output")
	}
}

func TestWindowsServiceDiagnosticsPreservesWriteContract(t *testing.T) {
	diagnostics := &windowsServiceDiagnostics{}
	contents := strings.Repeat("x", windowsServiceDiagnosticOutputSize+128)
	written, err := diagnostics.Write([]byte(contents))
	if err != nil {
		t.Fatal(err)
	}
	if written != len(contents) {
		t.Fatalf("diagnostic writer reported %d bytes, want %d", written, len(contents))
	}
	if !strings.HasSuffix(diagnostics.String(), strings.Repeat("x", 128)) {
		t.Fatal("oversized diagnostic write did not retain its tail")
	}
}

func TestWindowsServiceDiagnosticsRecordsPreReadyFailure(t *testing.T) {
	diagnostics := &windowsServiceDiagnostics{}
	var recorded string
	diagnostics.writeFile = func(message string) error {
		recorded = message
		return nil
	}
	_, _ = diagnostics.Write([]byte("core output"))
	want := errors.New("startup failed")
	err := diagnostics.recordFailure(want)
	if !errors.Is(err, want) {
		t.Fatalf("recorded failure = %v", err)
	}
	if !strings.Contains(recorded, "startup failed") ||
		!strings.Contains(recorded, "core output") {
		t.Fatalf("failure record = %q", recorded)
	}
}

func TestWindowsServiceDiagnosticsPreservesWriteFailure(t *testing.T) {
	diagnostics := &windowsServiceDiagnostics{
		writeFile: func(string) error { return os.ErrPermission },
	}
	want := errors.New("startup failed")
	err := diagnostics.recordFailure(want)
	if !errors.Is(err, want) || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("failure record write error = %v", err)
	}
}

func TestSanitizeWindowsServiceDiagnosticBoundsUTF8AndControls(t *testing.T) {
	message := "prefix\x00\x01\n" + strings.Repeat("界", 32) + "tail"
	const limit = 48
	got := sanitizeWindowsServiceDiagnostic(message, limit)
	if len(got) > limit {
		t.Fatalf("sanitized diagnostic size = %d, want <= %d", len(got), limit)
	}
	if !strings.HasSuffix(got, "tail") {
		t.Fatalf("sanitized diagnostic lost its tail: %q", got)
	}
	if !strings.HasPrefix(got, "[earlier diagnostic text truncated]\n") {
		t.Fatalf("sanitized diagnostic omitted truncation marker: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("sanitized diagnostic is not UTF-8: %q", got)
	}
	control := sanitizeWindowsServiceDiagnostic("a\x00b\x7fc\u009b", 32)
	if strings.ContainsAny(control, "\x00\x7f\u009b") {
		t.Fatalf("sanitized diagnostic retained controls: %q", control)
	}
}
