package quick

import (
	"encoding/json"

	"github.com/RC-CHN/wg-quic/internal/app"
)

// DesktopKeys is one freshly generated WireGuard keypair for a new tunnel.
type DesktopKeys struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

// GenerateDesktopKeys creates one WireGuard keypair for a new desktop tunnel.
// It runs unprivileged; only the resulting private key must be protected.
func GenerateDesktopKeys() (string, error) {
	private, err := app.GeneratePrivateKey()
	if err != nil {
		return "", err
	}
	public, err := app.PublicKey(private)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(DesktopKeys{PrivateKey: private, PublicKey: public})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
