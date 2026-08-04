package core

import (
	"context"
	"log"
	"path/filepath"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/platform"
)

type RunOptions struct {
	Name   string
	FwMark *uint32
}

// Run starts only the userspace data plane. It does not assign addresses,
// install routes or DNS, execute hooks, or invoke a service manager.
func Run(ctx context.Context, configPath string, options RunOptions) error {
	cfg, err := config.ParseFile(configPath)
	if err != nil {
		return err
	}
	if options.FwMark != nil {
		cfg.Interface.FwMark = *options.FwMark
	}
	name := InterfaceName(configPath, options.Name)
	instance, err := New(cfg, name, platform.Current())
	if err != nil {
		return err
	}
	defer instance.Close()
	if err := instance.Up(ctx); err != nil {
		return err
	}
	log.Printf("wg-quic core interface %s is up on UDP port %d", name, instance.ListenPort())
	return instance.Wait(ctx)
}

func InterfaceName(configPath, requestedName string) string {
	if requestedName != "" {
		return requestedName
	}
	return strings.TrimSuffix(filepath.Base(configPath), filepath.Ext(configPath))
}
