//go:build !windows

package quick

import "path/filepath"

func testControlPath(root, name string) string {
	return filepath.Join(root, name+".sock")
}
