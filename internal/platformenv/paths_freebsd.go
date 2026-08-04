//go:build freebsd

package platformenv

import (
	"fmt"
	"regexp"
)

var interfaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_=+.-]{1,15}$`)

func (Paths) ValidateInterfaceName(name string) error {
	if !interfaceNamePattern.MatchString(name) {
		return fmt.Errorf("invalid FreeBSD interface name %q", name)
	}
	return nil
}

func (Paths) ControlPath(name string) string {
	return "/var/run/wg-quic/" + name + ".sock"
}

func (Paths) ConfigPath(name string) string {
	return "/usr/local/etc/wg-quic/" + name + ".conf"
}
