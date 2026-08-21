//go:build linux

package platformenv

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
)

var interfaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_=+.-]{1,15}$`)

func (Paths) ValidateInterfaceName(name string) error {
	if !interfaceNamePattern.MatchString(name) {
		return fmt.Errorf("invalid Linux interface name %q", name)
	}
	return nil
}

func (Paths) ControlPath(name string) string {
	return "/run/wg-quic/" + name + ".sock"
}

func (Paths) ManagementPath(name string) string {
	return "/run/wg-quic/" + name + ".manage.sock"
}

func (Paths) ConfigPath(name string) string {
	return "/etc/wg-quic/" + name + ".conf"
}

func (Paths) ControlPaths() ([]string, error) {
	paths, err := filepath.Glob("/run/wg-quic/*.sock")
	sort.Strings(paths)
	return paths, err
}
