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
	"net/netip"
	"sync"

	armorbind "github.com/RC-CHN/wg-quic/internal/bind"
	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/platform"
	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
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

	endpointMu sync.RWMutex
	peers      map[string]*peerRuntime
	peerOrder  []string

	mu            sync.Mutex
	prepared      bool
	up            bool
	controlServer *control.Server
	closeOnce     sync.Once
	closeErr      error
}

type peerRuntime struct {
	status             control.PeerStatus
	obfsKey            obfs.Key
	hasObfsKey         bool
	releaseAssociation func()
}

func New(cfg *config.Config, name string, host platform.DeviceHost) (*Instance, error) {
	return newInstance(cfg, name, host, false)
}

func newInstance(cfg *config.Config, name string, host platform.DeviceHost, debug bool) (*Instance, error) {
	if cfg == nil {
		return nil, errors.New("configuration is required")
	}
	if host == nil {
		return nil, errors.New("device host is required")
	}
	if err := host.ValidateInterfaceName(name); err != nil {
		return nil, err
	}
	transportConfig, err := buildTransportConfiguration(cfg)
	if err != nil {
		return nil, err
	}
	if debug {
		transportLogger := device.NewLogger(device.LogLevelVerbose, fmt.Sprintf("(%s/transport) ", name))
		transportConfig.Bind.Debugf = transportLogger.Verbosef
	}
	tdev, err := host.CreateTUN(name, InterfaceMTU(cfg))
	if err != nil {
		return nil, fmt.Errorf("create TUN %s: %w", name, err)
	}
	bind := armorbind.New(transportConfig.Bind)
	logLevel := device.LogLevelError
	if debug {
		logLevel = device.LogLevelVerbose
	}
	logger := device.NewLogger(logLevel, fmt.Sprintf("(%s) ", name))
	dev := device.NewDeviceWithOptions(tdev, bind, logger, device.Options{
		DisableTUNEventStateTransitions: true,
	})
	if err := wgdevice.Configure(dev, cfg); err != nil {
		dev.Close()
		return nil, fmt.Errorf("configure WireGuard device: %w", err)
	}
	instance := &Instance{
		name: name, cfg: cfg, bind: bind, device: dev,
		controlPath: host.ControlPath(name),
		peers:       make(map[string]*peerRuntime, len(cfg.Peers)),
		peerOrder:   make([]string, 0, len(cfg.Peers)),
	}
	for _, peer := range cfg.Peers {
		runtime := &peerRuntime{
			status: control.PeerStatus{PublicKey: peer.PublicKey},
		}
		if endpoint, err := netip.ParseAddrPort(peer.Endpoint); err == nil {
			runtime.status.Endpoint = canonicalEndpoint(endpoint).String()
		}
		runtime.obfsKey, runtime.hasObfsKey = transportConfig.PeerKeys[peer.PublicKey]
		instance.peers[peer.PublicKey] = runtime
		instance.peerOrder = append(instance.peerOrder, peer.PublicKey)
	}
	return instance, nil
}

func (i *Instance) Up(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.up {
		return errors.New("wg-quic core instance is already up")
	}
	if err := i.prepareLocked(ctx); err != nil {
		return err
	}
	if err := i.activateLocked(); err != nil {
		server := i.controlServer
		i.controlServer = nil
		i.prepared = false
		if server != nil {
			_ = server.Close()
		}
		return err
	}
	return nil
}

// Prepare exposes the local control plane after creating the TUN, but leaves
// the WireGuard device down. quick uses this state to install endpoint route
// leases and numeric endpoints before any outer packet can be sent.
func (i *Instance) Prepare(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.prepareLocked(ctx)
}

func (i *Instance) prepareLocked(ctx context.Context) error {
	if i.prepared {
		return nil
	}
	server, err := control.StartHandler(ctx, i.controlPath, control.Handler{
		Status:          i.status,
		SetPeerEndpoint: i.setPeerEndpoint,
		RedialPeer:      i.redialPeer,
		Activate:        i.activate,
	})
	if err != nil {
		return fmt.Errorf("start local control socket: %w", err)
	}
	i.controlServer = server
	i.prepared = true
	return nil
}

func (i *Instance) activate() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.activateLocked()
}

func (i *Instance) activateLocked() error {
	if !i.prepared {
		return errors.New("wg-quic core instance is not prepared")
	}
	if i.up {
		return nil
	}
	if err := i.device.Up(); err != nil {
		return fmt.Errorf("bring WireGuard device up: %w", err)
	}
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
		i.prepared = false
		i.up = false
		i.mu.Unlock()
		if server != nil {
			i.closeErr = server.Close()
		}
		i.endpointMu.Lock()
		for _, peer := range i.peers {
			if peer.releaseAssociation != nil {
				peer.releaseAssociation()
				peer.releaseAssociation = nil
			}
		}
		i.endpointMu.Unlock()
		i.device.Close()
	})
	return i.closeErr
}

func (i *Instance) ListenPort() uint16 {
	return i.bind.Port()
}

func (i *Instance) Stats() armorbind.Stats {
	return i.bind.Stats()
}

func (i *Instance) status() control.Status {
	i.mu.Lock()
	state := "down"
	if i.prepared {
		state = "prepared"
	}
	if i.up {
		state = "up"
	}
	i.mu.Unlock()
	i.endpointMu.RLock()
	peers := make([]control.PeerStatus, 0, len(i.peerOrder))
	for _, publicKey := range i.peerOrder {
		peers = append(peers, i.peers[publicKey].status)
	}
	i.endpointMu.RUnlock()
	return control.Status{
		Interface: i.name, State: state, ListenPort: i.bind.Port(),
		Carrier: i.cfg.Transport.Carrier, FECMode: i.cfg.Transport.FEC,
		ObfsMode: i.cfg.Transport.Obfs, Peers: peers, Stats: i.bind.Stats(),
	}
}

func (i *Instance) setPeerEndpoint(update control.SetPeerEndpointRequest) error {
	endpoint, err := netip.ParseAddrPort(update.Endpoint)
	if err != nil {
		return fmt.Errorf("parse numeric peer endpoint: %w", err)
	}
	endpoint = canonicalEndpoint(endpoint)
	if endpoint.Port() == 0 {
		return errors.New("peer endpoint port must not be zero")
	}
	if update.Generation == 0 {
		return errors.New("peer endpoint generation must be greater than zero")
	}

	i.endpointMu.Lock()
	defer i.endpointMu.Unlock()
	peer, ok := i.peers[update.PublicKey]
	if !ok {
		return errors.New("peer public key is not configured")
	}
	if update.Generation < peer.status.Generation {
		return fmt.Errorf(
			"stale peer endpoint generation %d; active generation is %d",
			update.Generation, peer.status.Generation,
		)
	}
	if update.Generation == peer.status.Generation {
		if peer.status.Endpoint == endpoint.String() {
			return nil
		}
		return fmt.Errorf("peer endpoint generation %d conflicts with the active endpoint", update.Generation)
	}

	var release func()
	if peer.hasObfsKey {
		release, err = i.bind.AcquireEndpointKey(endpoint, peer.obfsKey)
		if err != nil {
			return err
		}
	}
	if err := wgdevice.SetPeerEndpoint(i.device, update.PublicKey, endpoint); err != nil {
		if release != nil {
			release()
		}
		return fmt.Errorf("update WireGuard peer endpoint: %w", err)
	}

	oldEndpoint, oldEndpointValid := parseCanonicalEndpoint(peer.status.Endpoint)
	oldRelease := peer.releaseAssociation
	peer.releaseAssociation = release
	peer.status.Endpoint = endpoint.String()
	peer.status.Generation = update.Generation
	if oldEndpointValid && oldEndpoint != endpoint {
		i.bind.RetireEndpoint(oldEndpoint)
	}
	if oldRelease != nil {
		oldRelease()
	}
	return nil
}

func (i *Instance) redialPeer(publicKey string) error {
	i.endpointMu.RLock()
	peer, ok := i.peers[publicKey]
	if !ok {
		i.endpointMu.RUnlock()
		return errors.New("peer public key is not configured")
	}
	endpoint, valid := parseCanonicalEndpoint(peer.status.Endpoint)
	i.endpointMu.RUnlock()
	if !valid {
		return errors.New("peer does not have an active numeric endpoint")
	}
	i.bind.RedialEndpoint(endpoint)
	return nil
}

func canonicalEndpoint(endpoint netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
}

func parseCanonicalEndpoint(value string) (netip.AddrPort, bool) {
	endpoint, err := netip.ParseAddrPort(value)
	if err != nil {
		return netip.AddrPort{}, false
	}
	return canonicalEndpoint(endpoint), true
}

func InterfaceMTU(cfg *config.Config) int {
	if cfg.Interface.MTU != 0 {
		return cfg.Interface.MTU
	}
	return 1380
}
