//go:build !windows

package quick

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/RC-CHN/wg-quic/internal/platform"
)

// DeleteDesktopConfig removes one installed configuration. Desktop shells
// execute this narrow operation through pkexec instead of running their whole
// webview process with root privileges.
func DeleteDesktopConfig(name string) error {
	host := platform.Current()
	if err := host.ValidateInterfaceName(name); err != nil {
		return err
	}
	// Best-effort stop before removal so a running tunnel does not outlive
	// its configuration. A tunnel that is not running makes down a no-op.
	_ = Manage(context.Background(), "down", name)
	if err := os.Remove(host.ConfigPath(name)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("tunnel %s is not configured", name)
		}
		return fmt.Errorf("remove desktop configuration: %w", err)
	}
	return nil
}
