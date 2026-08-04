// Package platformenv owns platform-specific interface-name and local-path
// semantics shared by the data-plane device host and host-policy backends.
package platformenv

import "errors"

var ErrUnsupported = errors.New("wg-quic host integration is not implemented on this operating system")

// Paths is a zero-state platform path and interface-name provider.
type Paths struct{}
