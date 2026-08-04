// Package platformenv owns platform-specific interface-name and local-path
// semantics shared by the data-plane device host and host-policy backends.
package platformenv

import (
	"errors"
	"path/filepath"
	"strings"
)

var ErrUnsupported = errors.New("wg-quic host integration is not implemented on this operating system")

// Paths is a zero-state platform path and interface-name provider.
type Paths struct{}

// InterfaceName derives the default interface name from a configuration path
// using the target operating system's filepath semantics.
func InterfaceName(configPath, requestedName string) string {
	if requestedName != "" {
		return requestedName
	}
	return strings.TrimSuffix(filepath.Base(configPath), filepath.Ext(configPath))
}
