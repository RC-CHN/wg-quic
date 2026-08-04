// Package quick owns wg-quick-style host policy and service orchestration.
// The shared data plane remains in package core.
package quick

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/platform"
)

func ResolveConfig(input, requestedName string, host platform.Host) (path, name string, err error) {
	if input == "" {
		return "", "", errors.New("interface or configuration path is required")
	}
	if filepath.IsAbs(input) || filepath.Dir(input) != "." || strings.EqualFold(filepath.Ext(input), ".conf") {
		path = input
	} else {
		if err := host.ValidateInterfaceName(input); err != nil {
			return "", "", err
		}
		path = host.ConfigPath(input)
	}
	name = interfaceName(path, requestedName)
	if err := host.ValidateInterfaceName(name); err != nil {
		return "", "", err
	}
	return path, name, nil
}

func interfaceName(configPath, requestedName string) string {
	if requestedName != "" {
		return requestedName
	}
	return strings.TrimSuffix(filepath.Base(configPath), filepath.Ext(configPath))
}

func Check(input string) error {
	host := platform.Current()
	path, _, err := ResolveConfig(input, "", host)
	if err != nil {
		return err
	}
	cfg, err := config.ParseFile(path)
	if err != nil {
		return err
	}
	return validateConfig(cfg)
}

func validateConfig(cfg *config.Config) error {
	if cfg.Interface.SaveConfig {
		return errors.New("SaveConfig=true is not supported because wg-quic has no runtime configuration mutation to persist")
	}
	return nil
}
