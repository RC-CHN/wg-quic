package quick

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/RC-CHN/wg-quic/internal/config"
)

const (
	maxSecureConfigBytes      = 1 << 20
	maxSecureConfigPeers      = 1 << 16
	maxSecureConfigAllowedIPs = 1 << 16
)

func parseSecureConfigReader(reader io.Reader) (*config.Config, error) {
	if reader == nil {
		return nil, errors.New("configuration reader is required")
	}
	contents, err := io.ReadAll(io.LimitReader(reader, maxSecureConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read secure configuration snapshot: %w", err)
	}
	if len(contents) > maxSecureConfigBytes {
		return nil, fmt.Errorf("configuration snapshot exceeds %d bytes", maxSecureConfigBytes)
	}
	cfg, err := config.Parse(bytes.NewReader(contents))
	clear(contents)
	if err != nil {
		return nil, err
	}
	if err := validateSecureConfigLimits(cfg); err != nil {
		return nil, err
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func validateSecureConfigLimits(cfg *config.Config) error {
	if cfg == nil {
		return errors.New("configuration is required")
	}
	if len(cfg.Peers) > maxSecureConfigPeers {
		return fmt.Errorf(
			"configuration has %d peers; maximum is %d",
			len(cfg.Peers), maxSecureConfigPeers,
		)
	}
	totalAllowedIPs := 0
	for _, peer := range cfg.Peers {
		if len(peer.AllowedIPs) > maxSecureConfigAllowedIPs-totalAllowedIPs {
			return fmt.Errorf(
				"configuration has more than %d AllowedIPs",
				maxSecureConfigAllowedIPs,
			)
		}
		totalAllowedIPs += len(peer.AllowedIPs)
	}
	return nil
}
