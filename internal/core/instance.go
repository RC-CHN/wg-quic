// Package core owns the portable wg-quic userspace data plane.
//
// It creates the TUN-backed WireGuard device, QUIC/FEC/obfuscation transport,
// and local status endpoint. Address assignment, routes, DNS, hooks, and
// service management intentionally live in package quick.
package core

import (
	"context"
	"errors"
	"fmt"
	"sync"

	armorbind "github.com/RC-CHN/wg-quic/internal/bind"
	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/platform"
	"github.com/RC-CHN/wg-quic/internal/wgdevice"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/device"
)

// Instance is one configured userspace WireGuard device. New creates the TUN
// and configures the cryptographic peer state; Up opens the carrier and status
// socket after an optional quick layer has configured the host interface.
type Instance struct {
	name        string
	cfg         *config.Config
	bind        *armorbind.Bind
	device      *device.Device
	controlPath string

	mu            sync.Mutex
	up            bool
	controlServer *control.Server
	closeOnce     sync.Once
	closeErr      error
}

func New(cfg *config.Config, name string, host platform.DeviceHost) (*Instance, error) {
	if cfg == nil {
		return nil, errors.New("configuration is required")
	}
	if host == nil {
		return nil, errors.New("device host is required")
	}
	if err := host.ValidateInterfaceName(name); err != nil {
		return nil, err
	}
	bindConfig, err := transportBindConfig(cfg)
	if err != nil {
		return nil, err
	}
	tdev, err := host.CreateTUN(name, InterfaceMTU(cfg))
	if err != nil {
		return nil, fmt.Errorf("create TUN %s: %w", name, err)
	}
	bind := armorbind.New(bindConfig)
	logger := device.NewLogger(device.LogLevelError, fmt.Sprintf("(%s) ", name))
	dev := device.NewDevice(tdev, bind, logger)
	if err := wgdevice.Configure(dev, cfg); err != nil {
		dev.Close()
		return nil, fmt.Errorf("configure WireGuard device: %w", err)
	}
	return &Instance{
		name: name, cfg: cfg, bind: bind, device: dev,
		controlPath: host.ControlPath(name),
	}, nil
}

func (i *Instance) Up(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.up {
		return errors.New("wg-quic core instance is already up")
	}
	if err := i.device.Up(); err != nil {
		return fmt.Errorf("bring WireGuard device up: %w", err)
	}
	server, err := control.Start(ctx, i.controlPath, func() control.Status {
		return control.Status{
			Interface: i.name, State: "up", ListenPort: i.bind.Port(),
			Carrier: i.cfg.Transport.Carrier, FECMode: i.cfg.Transport.FEC,
			ObfsMode: i.cfg.Transport.Obfs, Stats: i.bind.Stats(),
		}
	})
	if err != nil {
		_ = i.device.Down()
		return fmt.Errorf("start local control socket: %w", err)
	}
	i.controlServer = server
	i.up = true
	return nil
}

// Wait returns nil when ctx requests an orderly shutdown. A closed WireGuard
// device without cancellation is treated as an unexpected core failure.
func (i *Instance) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return nil
	case <-i.device.Wait():
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("WireGuard device stopped unexpectedly")
	}
}

func (i *Instance) Close() error {
	i.closeOnce.Do(func() {
		i.mu.Lock()
		server := i.controlServer
		i.controlServer = nil
		i.mu.Unlock()
		if server != nil {
			i.closeErr = server.Close()
		}
		i.device.Close()
	})
	return i.closeErr
}

func (i *Instance) ListenPort() uint16 {
	return i.bind.Port()
}

func InterfaceMTU(cfg *config.Config) int {
	if cfg.Interface.MTU != 0 {
		return cfg.Interface.MTU
	}
	return 1380
}
