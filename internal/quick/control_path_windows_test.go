//go:build windows

package quick

import (
	"crypto/sha256"
	"fmt"
)

func testControlPath(root, name string) string {
	hash := sha256.Sum256([]byte(root))
	return fmt.Sprintf(`\\.\pipe\wg-quic-quick-test-%s-%x`, name, hash[:8])
}
