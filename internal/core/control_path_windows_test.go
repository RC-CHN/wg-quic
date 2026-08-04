//go:build windows

package core

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func testControlPath(t *testing.T, name string) string {
	t.Helper()
	return fmt.Sprintf(`\\.\pipe\wg-quic-core-test-%s-%d-%d`, name, os.Getpid(), time.Now().UnixNano())
}
