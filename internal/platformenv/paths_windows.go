//go:build windows

package platformenv

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var interfaceNamePattern = regexp.MustCompile(`^[^\\/:*?"<>|\x00-\x1f]{1,128}$`)

func (Paths) ValidateInterfaceName(name string) error {
	if !interfaceNamePattern.MatchString(name) || strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("invalid Windows interface name %q", name)
	}
	return nil
}

func (Paths) ControlPath(name string) string {
	return `\\.\pipe\wg-quic-` + name
}

func (Paths) ConfigPath(name string) string {
	root := os.Getenv("ProgramData")
	if root == "" {
		root = `C:\ProgramData`
	}
	return filepath.Join(root, "wg-quic", "interfaces", name+".conf")
}
