package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	armorbind "github.com/RC-CHN/wg-quic/internal/bind"
	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/platform"
	"github.com/RC-CHN/wg-quic/internal/wgdevice"
	"golang.zx2c4.com/wireguard/device"
)

func Run(ctx context.Context, configPath, requestedName string) error {
	return runWithHost(ctx, configPath, requestedName, platform.Current())
}

func runWithHost(ctx context.Context, configPath, requestedName string, host platform.Host) error {
	cfg, err := config.ParseFile(configPath)
	if err != nil {
		return err
	}
	name := requestedName
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(configPath), filepath.Ext(configPath))
	}
	if err := host.ValidateInterfaceName(name); err != nil {
		return err
	}
	if err := host.Prepare(ctx, cfg); err != nil {
		return err
	}
	bindConfig, err := transportBindConfig(cfg)
	if err != nil {
		return err
	}
	for _, hook := range cfg.Interface.PreUp {
		if err := host.RunHook(ctx, hook, name); err != nil {
			return fmt.Errorf("PreUp: %w", err)
		}
	}
	tdev, err := host.CreateTUN(name, interfaceMTU(cfg))
	if err != nil {
		return fmt.Errorf("create TUN %s: %w", name, err)
	}
	bind := armorbind.New(bindConfig)
	logger := device.NewLogger(device.LogLevelError, fmt.Sprintf("(%s) ", name))
	dev := device.NewDevice(tdev, bind, logger)
	defer dev.Close()
	if err := wgdevice.Configure(dev, cfg); err != nil {
		return fmt.Errorf("configure WireGuard device: %w", err)
	}
	networkCleanup, err := host.ConfigureNetwork(ctx, name, cfg)
	defer cleanupHost(context.Background(), host, name, cfg, &networkCleanup)
	if err != nil {
		return err
	}
	if err := dev.Up(); err != nil {
		return fmt.Errorf("bring WireGuard device up: %w", err)
	}
	controlServer, err := control.Start(ctx, host.ControlPath(name), func() control.Status {
		return control.Status{
			Interface: name, State: "up", ListenPort: bind.Port(),
			Carrier: cfg.Transport.Carrier, FECMode: cfg.Transport.FEC, ObfsMode: cfg.Transport.Obfs, Stats: bind.Stats(),
		}
	})
	if err != nil {
		return fmt.Errorf("start local control socket: %w", err)
	}
	defer controlServer.Close()
	for _, hook := range cfg.Interface.PostUp {
		if err := host.RunHook(ctx, hook, name); err != nil {
			return fmt.Errorf("PostUp: %w", err)
		}
	}
	log.Printf("wg-quic interface %s is up on UDP port %d", name, bind.Port())
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-signalCtx.Done():
		return nil
	case <-dev.Wait():
		return errors.New("WireGuard device stopped unexpectedly")
	}
}

func cleanupHost(ctx context.Context, host platform.Host, name string, cfg *config.Config, cleanup *platform.Cleanup) {
	for _, hook := range cfg.Interface.PreDown {
		if err := host.RunHook(ctx, hook, name); err != nil {
			log.Printf("PreDown: %v", err)
		}
	}
	if cleanup != nil && *cleanup != nil {
		if err := (*cleanup)(ctx); err != nil {
			log.Printf("platform network cleanup: %v", err)
		}
	}
	for _, hook := range cfg.Interface.PostDown {
		if err := host.RunHook(ctx, hook, name); err != nil {
			log.Printf("PostDown: %v", err)
		}
	}
}

func interfaceMTU(cfg *config.Config) int {
	if cfg.Interface.MTU != 0 {
		return cfg.Interface.MTU
	}
	return 1380
}
