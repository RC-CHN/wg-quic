//go:build !linux && !freebsd && !windows

package platformenv

func (Paths) ValidateInterfaceName(string) error {
	return ErrUnsupported
}

func (Paths) ControlPath(string) string {
	return ""
}

func (Paths) ConfigPath(string) string {
	return ""
}
