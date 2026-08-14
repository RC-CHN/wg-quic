package quick

import (
	"fmt"
	"os"

	"github.com/RC-CHN/wg-quic/internal/platform"
)

// maxDesktopConfigSize bounds one desktop-managed configuration file.
const maxDesktopConfigSize = 1024 * 1024

// ReadDesktopConfig returns the installed configuration text for one tunnel.
// Desktop shells execute this narrow operation through pkexec instead of
// running their whole webview process with root privileges.
func ReadDesktopConfig(name string) (string, error) {
	host := platform.Current()
	if err := host.ValidateInterfaceName(name); err != nil {
		return "", err
	}
	path := host.ConfigPath(name)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect desktop configuration: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("desktop configuration %q is not a regular file", name)
	}
	if info.Size() > maxDesktopConfigSize {
		return "", fmt.Errorf(
			"desktop configuration is %d bytes; maximum is %d",
			info.Size(),
			maxDesktopConfigSize,
		)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read desktop configuration: %w", err)
	}
	return string(contents), nil
}
