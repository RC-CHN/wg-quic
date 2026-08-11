//go:build windows

package namedpipe

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDialSecurityQualityOfService(t *testing.T) {
	tests := []struct {
		name  string
		level uint32
		want  uint32
	}{
		{
			name:  "zero value remains anonymous",
			level: 0,
			want:  windows.SECURITY_SQOS_PRESENT | windows.SECURITY_ANONYMOUS,
		},
		{
			name:  "identification",
			level: windows.SECURITY_IDENTIFICATION,
			want:  windows.SECURITY_SQOS_PRESENT | windows.SECURITY_IDENTIFICATION,
		},
		{
			name:  "impersonation",
			level: windows.SECURITY_IMPERSONATION,
			want:  windows.SECURITY_SQOS_PRESENT | windows.SECURITY_IMPERSONATION,
		},
		{
			name:  "delegation",
			level: windows.SECURITY_DELEGATION,
			want:  windows.SECURITY_SQOS_PRESENT | windows.SECURITY_DELEGATION,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := dialSecurityQualityOfService(test.level)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("security QoS = %#x, want %#x", got, test.want)
			}
		})
	}
}

func TestDialSecurityQualityOfServiceRejectsInvalidLevel(t *testing.T) {
	_, err := dialSecurityQualityOfService(windows.SECURITY_EFFECTIVE_ONLY)
	if !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		t.Fatalf("error = %v, want ERROR_INVALID_PARAMETER", err)
	}
}

func TestDialConfigZeroDesiredAccessPreservesGenericDuplexDefault(t *testing.T) {
	if got, want := dialDesiredAccess(0), uint32(
		windows.GENERIC_READ|windows.GENERIC_WRITE,
	); got != want {
		t.Fatalf("zero desired access = %#x, want %#x", got, want)
	}
	const exact = uint32(0x12019b)
	if got := dialDesiredAccess(exact); got != exact {
		t.Fatalf("exact desired access = %#x, want %#x", got, exact)
	}
}
