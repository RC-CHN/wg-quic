//go:build windows

package control

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func testControlPath(t *testing.T, name string) string {
	t.Helper()
	return fmt.Sprintf(`\\.\pipe\wg-quic-test-%s-%d-%d`, name, os.Getpid(), time.Now().UnixNano())
}
