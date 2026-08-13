//go:build windows

package quick

import (
	"errors"
	"fmt"
	"os"

	"github.com/RC-CHN/wg-quic/internal/platform"
)

// DeleteDesktopConfig removes one installed configuration. The desktop broker
// runs with the LocalSystem privileges required to delete the stored file.
func DeleteDesktopConfig(name string) error {
	host := platform.Current()
	if err := host.ValidateInterfaceName(name); err != nil {
		return err
	}
	if err := os.Remove(host.ConfigPath(name)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("tunnel %s is not configured", name)
		}
		return fmt.Errorf("remove desktop configuration: %w", err)
	}
	return nil
}
