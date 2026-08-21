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

func (Paths) ManagementPath(name string) string {
	return `\\.\pipe\wg-quic-quick-` + name
}

func (Paths) ConfigPath(name string) string {
	root := os.Getenv("ProgramData")
	if root == "" {
		root = `C:\ProgramData`
	}
	return filepath.Join(root, "wg-quic", "interfaces", name+".conf")
}

func (paths Paths) ControlPaths() ([]string, error) {
	configPattern := filepath.Join(
		filepath.Dir(paths.ConfigPath("placeholder")),
		"*.conf",
	)
	configs, err := filepath.Glob(configPattern)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(configs))
	for _, config := range configs {
		name := strings.TrimSuffix(filepath.Base(config), filepath.Ext(config))
		if paths.ValidateInterfaceName(name) == nil {
			result = append(result, paths.ControlPath(name))
		}
	}
	return result, nil
}
