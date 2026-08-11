//go:build windows

package app

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/control"
)

func TestMissingWindowsStatusPipeIsMarkedInactive(t *testing.T) {
	name := fmt.Sprintf("wg-quic-missing-status-%d", os.Getpid())
	path := `\\.\pipe\` + name
	_, readErr := control.Read(path)
	if readErr == nil {
		t.Fatal("unexpectedly read status from an absent named pipe")
	}
	err := namedStatusReadError(name, path, readErr)
	if !strings.Contains(err.Error(), "interface is inactive:") {
		t.Fatalf("missing named-pipe status error was not marked inactive: %v", err)
	}
}
