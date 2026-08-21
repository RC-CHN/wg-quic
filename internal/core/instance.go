// Package core owns the portable wg-quic userspace data plane.
//
// It creates the TUN-backed WireGuard device, QUIC/FEC/obfuscation transport,
// and local status endpoint. Address assignment, routes, DNS, hooks, and
// service management intentionally live in package quick.
package core

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/netip"
	runtimemetrics "runtime/metrics"
	"sync"

	armorbind "github.com/RC-CHN/wg-quic/internal/bind"
	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/devicehost"
	"github.com/RC-CHN/wg-quic/internal/peerendpoint"
	"github.com/RC-CHN/wg-quic/internal/telemetry"
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

	peerTransactionMu     sync.Mutex
	peerTransactions      map[string]*corePeerTransaction
	peerTransactionOrder  []string
	activePeerTransaction string
	peerFingerprintKey    [32]byte

	mu            sync.Mutex
	prepared      bool
	up            bool
	controlServer *control.Server
	statusServer  *control.Server
	closeOnce     sync.Once
	closeErr      error
}

type peerRuntime struct {
	status                    control.PeerStatus
	activationReceiveSequence uint64
	obfsKey                   obfs.Key
	hasObfsKey                bool
	releaseReceiveKey         func()
	releaseAssociation        func()
	fecPolicy                 string
	releaseFECPolicy          func()
	obsoleteEndpoints         []peerEndpointResources
}

type peerEndpointResources struct {
	endpoint           netip.AddrPort
	releaseAssociation func()
	releaseFECPolicy   func()
}

func New(cfg *config.Config, name string, host devicehost.Host) (*Instance, error) {
	return newInstance(cfg, name, host, false)
}

func newInstance(cfg *config.Config, name string, host devicehost.Host, debug bool) (*Instance, error) {
	if cfg == nil {
		return nil, errors.New("configuration is required")
	}
	if host == nil {
		return nil, errors.New("device host is required")
	}
	if err := host.ValidateInterfaceName(name); err != nil {
		return nil, err
	}
	configuredEndpoints := make(map[string]netip.AddrPort, len(cfg.Peers))
	for index, peer := range cfg.Peers {
		if peer.Endpoint == "" {
			continue
		}
		spec, err := peerendpoint.Parse(peer.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("Peer %d Endpoint: %w", index+1, err)
		}
		endpoint, numeric := spec.AddrPort()
		if !numeric {
			return nil, fmt.Errorf(
				"Peer %d Endpoint %q is a hostname; run wg-quic-quick to resolve and supervise hostnames",
				index+1, peer.Endpoint,
			)
		}
		configuredEndpoints[peer.PublicKey] = endpoint
	}
	transportConfig, err := buildTransportConfiguration(cfg)
	if err != nil {
		return nil, err
	}
	var peerFingerprintKey [32]byte
	if _, err := rand.Read(peerFingerprintKey[:]); err != nil {
		return nil, fmt.Errorf("generate peer transaction fingerprint key: %w", err)
	}
	transportConfig.Bind.Eventf = func(format string, args ...any) {
		log.Printf(
			"wg-quic transport %s: "+format,
			append([]any{name}, args...)...,
		)
	}
	if debug {
		transportLogger := device.NewLogger(device.LogLevelVerbose, fmt.Sprintf("(%s/transport) ", name))
		transportConfig.Bind.Debugf = transportLogger.Verbosef
	}
	tdev, err := host.CreateTUN(name, cfg.EffectiveMTU())
	if err != nil {
		return nil, fmt.Errorf("create TUN %s: %w", name, err)
	}
	bind := armorbind.New(transportConfig.Bind)
	logLevel := device.LogLevelError
	if debug {
		logLevel = device.LogLevelVerbose
	}
	logger := device.NewLogger(logLevel, fmt.Sprintf("(%s) ", name))
	var instance *Instance
	dev := device.NewDeviceWithOptions(tdev, bind, logger, device.Options{
		DisableTUNEventStateTransitions: true,
		AuthenticatedReceive: func(event device.AuthenticatedReceive) {
			if instance != nil {
				instance.recordAuthenticatedReceive(event)
			}
		},
	})
	deviceConfig := cfg.Clone()
	for index := range deviceConfig.Peers {
		deviceConfig.Peers[index].Endpoint = ""
	}
	if err := wgdevice.Configure(dev, deviceConfig); err != nil {
		dev.Close()
		return nil, fmt.Errorf("configure WireGuard device: %w", err)
	}
	instance = &Instance{
		name: name, cfg: cfg, bind: bind, device: dev,
		controlPath:        host.ControlPath(name),
		peers:              make(map[string]*peerRuntime, len(cfg.Peers)),
		peerOrder:          make([]string, 0, len(cfg.Peers)),
		peerTransactions:   make(map[string]*corePeerTransaction),
		peerFingerprintKey: peerFingerprintKey,
	}
	bind.SetSessionRestored(instance.restorePeersAfterTransportReconnect)
	for _, peer := range cfg.Peers {
		runtime := &peerRuntime{
			status:    control.PeerStatus{PublicKey: peer.PublicKey},
			fecPolicy: canonicalPeerFECPolicy(peer.FECPolicy),
		}
		runtime.obfsKey, runtime.hasObfsKey = transportConfig.PeerKeys[peer.PublicKey]
		if runtime.hasObfsKey {
			runtime.releaseReceiveKey = bind.AcquireReceiveKey(runtime.obfsKey)
		}
		instance.peers[peer.PublicKey] = runtime
		instance.peerOrder = append(instance.peerOrder, peer.PublicKey)
	}
	for _, peer := range cfg.Peers {
		endpoint, ok := configuredEndpoints[peer.PublicKey]
		if !ok {
			continue
		}
		if err := instance.installPeerEndpoint(peer.PublicKey, endpoint, 1); err != nil {
			instance.Close()
			return nil, fmt.Errorf("install Peer endpoint: %w", err)
		}
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
		statusServer := i.statusServer
		i.controlServer = nil
		i.statusServer = nil
		i.prepared = false
		if server != nil {
			_ = server.Close()
		}
		if statusServer != nil {
			_ = statusServer.Close()
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
		Status:           i.status,
		SetPeerEndpoint:  i.setPeerEndpoint,
		RedialPeer:       i.redialPeer,
		Activate:         i.activate,
		PreparePeerSet:   i.preparePeerSet,
		CommitPeerSet:    i.commitPeerSet,
		RollbackPeerSet:  i.rollbackPeerSet,
		FinalizePeerSet:  i.finalizePeerSet,
		FinalizeEndpoint: i.finalizePeerEndpoint,
	})
	if err != nil {
		return fmt.Errorf("start local control socket: %w", err)
	}
	statusServer, err := control.StartReadOnlyStatus(
		ctx,
		i.controlPath,
		i.status,
	)
	if err != nil {
		_ = server.Close()
		return fmt.Errorf("start read-only status socket: %w", err)
	}
	i.controlServer = server
	i.statusServer = statusServer
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
		statusServer := i.statusServer
		i.controlServer = nil
		i.statusServer = nil
		i.prepared = false
		i.up = false
		i.mu.Unlock()
		if server != nil {
			i.closeErr = server.Close()
		}
		if statusServer != nil {
			i.closeErr = errors.Join(
				i.closeErr,
				statusServer.Close(),
			)
		}
		i.closePreparedPeerTransactions()
		i.endpointMu.Lock()
		for _, peer := range i.peers {
			i.releasePeerRuntimeLocked(peer)
		}
		i.endpointMu.Unlock()
		i.device.Close()
	})
	return i.closeErr
}

func (i *Instance) ListenPort() uint16 {
	return i.bind.Port()
}

func (i *Instance) Stats() telemetry.Stats {
	stats := i.bind.Stats()
	addRuntimeStats(&stats)
	return stats
}

func (i *Instance) status() control.Status {
	i.mu.Lock()
	cfg := i.cfg
	state := "down"
	if i.prepared {
		state = "prepared"
	}
	if i.up {
		state = "up"
	}
	i.mu.Unlock()
	runtimePeers, _ := wgdevice.PeerStatuses(i.device)
	i.endpointMu.RLock()
	peers := make([]control.PeerStatus, 0, len(i.peerOrder))
	for _, publicKey := range i.peerOrder {
		runtimePeer := i.peers[publicKey]
		peer := runtimePeer.status
		peer.FECPolicy = runtimePeer.fecPolicy
		if runtime, ok := runtimePeers[publicKey]; ok {
			peer.LatestHandshake = runtime.LatestHandshake
			peer.LastRx = runtime.LastRx
			peer.LastTx = runtime.LastTx
			peer.LastActivity, peer.LastDirection = latestPeerActivity(
				runtime.LastRx,
				runtime.LastTx,
			)
			peer.TransferRx = runtime.TransferRx
			peer.TransferTx = runtime.TransferTx
		}
		peer.Session = string(armorbind.EndpointSessionIdle)
		if endpoint, err := peerendpoint.ParseNumeric(peer.Endpoint); err == nil {
			peer.Session = string(i.bind.EndpointSessionState(endpoint))
			reconnect := i.bind.EndpointReconnectStatus(endpoint)
			peer.ReconnectAttempts = reconnect.Attempts
			peer.ReconnectFailures = reconnect.Failures
			peer.ConsecutiveReconnectFailures = reconnect.ConsecutiveFailures
			peer.NextReconnect = reconnect.NextReconnect
		}
		peers = append(peers, peer)
	}
	i.endpointMu.RUnlock()
	addresses := make([]string, 0, len(cfg.Interface.Addresses))
	for _, addr := range cfg.Interface.Addresses {
		addresses = append(addresses, addr.String())
	}
	return control.Status{
		Interface: i.name, State: state, ListenPort: i.bind.Port(),
		Carrier: cfg.Transport.Carrier, FECMode: cfg.Transport.FEC,
		ObfsMode: cfg.Transport.Obfs, Addresses: addresses,
		Peers: peers,
		Capabilities: []string{
			"core_control_v1",
			"typed_peer_transactions_v1",
			"dynamic_obfs_keys",
			"dynamic_peer_fec_policy",
			"authenticated_endpoint_generation",
		},
		Stats: i.Stats(),
	}
}

func latestPeerActivity(lastRx, lastTx int64) (int64, string) {
	if lastRx == 0 && lastTx == 0 {
		return 0, ""
	}
	if lastRx >= lastTx {
		return lastRx, "received"
	}
	return lastTx, "sent"
}

func addRuntimeStats(stats *telemetry.Stats) {
	samples := []runtimemetrics.Sample{
		{Name: "/gc/heap/allocs:bytes"},
		{Name: "/gc/heap/allocs:objects"},
		{Name: "/gc/heap/objects:objects"},
		{Name: "/gc/cycles/total:gc-cycles"},
		{Name: "/cpu/classes/gc/pause:cpu-seconds"},
	}
	runtimemetrics.Read(samples)
	stats.RuntimeAllocBytes = samples[0].Value.Uint64()
	stats.RuntimeAllocObjects = samples[1].Value.Uint64()
	stats.RuntimeHeapObjects = samples[2].Value.Uint64()
	stats.RuntimeGCCycles = samples[3].Value.Uint64()
	stats.RuntimeGCPauseCPUNanos = uint64(
		samples[4].Value.Float64() * 1_000_000_000,
	)
}

func (i *Instance) setPeerEndpoint(update control.SetPeerEndpointRequest) error {
	if update.Endpoint == "" {
		return i.clearPeerEndpoint(update.PublicKey, update.Generation)
	}
	endpoint, err := peerendpoint.ParseNumeric(update.Endpoint)
	if err != nil {
		return fmt.Errorf("parse numeric peer endpoint: %w", err)
	}
	return i.installPeerEndpoint(update.PublicKey, endpoint, update.Generation)
}

func (i *Instance) clearPeerEndpoint(publicKey string, generation uint64) error {
	if generation == 0 {
		return errors.New("peer endpoint generation must be greater than zero")
	}
	i.endpointMu.Lock()
	defer i.endpointMu.Unlock()
	peer, ok := i.peers[publicKey]
	if !ok {
		return errors.New("peer public key is not configured")
	}
	if generation < peer.status.Generation {
		return fmt.Errorf(
			"stale peer endpoint generation %d; active generation is %d",
			generation, peer.status.Generation,
		)
	}
	if generation == peer.status.Generation {
		if peer.status.Endpoint == "" {
			return nil
		}
		return fmt.Errorf("peer endpoint generation %d conflicts with the active endpoint", generation)
	}
	if err := wgdevice.ClearPeerEndpoint(i.device, publicKey); err != nil {
		return fmt.Errorf("clear WireGuard peer endpoint: %w", err)
	}
	activationReceiveSequence := i.bind.ReceiveSequence()
	oldEndpoint, oldEndpointErr := peerendpoint.ParseNumeric(peer.status.Endpoint)
	i.retainPeerEndpointResourcesLocked(peer, oldEndpoint, oldEndpointErr)
	peer.releaseAssociation = nil
	peer.releaseFECPolicy = nil
	peer.status.Endpoint = ""
	peer.status.Generation = generation
	peer.status.AuthenticatedGeneration = 0
	peer.status.AuthenticatedEndpoint = ""
	peer.activationReceiveSequence = activationReceiveSequence
	return nil
}

func (i *Instance) installPeerEndpoint(publicKey string, endpoint netip.AddrPort, generation uint64) error {
	if generation == 0 {
		return errors.New("peer endpoint generation must be greater than zero")
	}
	i.mu.Lock()
	up := i.up
	i.mu.Unlock()

	i.endpointMu.Lock()
	defer i.endpointMu.Unlock()
	peer, ok := i.peers[publicKey]
	if !ok {
		return errors.New("peer public key is not configured")
	}
	if generation < peer.status.Generation {
		return fmt.Errorf(
			"stale peer endpoint generation %d; active generation is %d",
			generation, peer.status.Generation,
		)
	}
	if generation == peer.status.Generation {
		if peer.status.Endpoint == endpoint.String() {
			return nil
		}
		return fmt.Errorf("peer endpoint generation %d conflicts with the active endpoint", generation)
	}

	var (
		release    func()
		fecRelease func()
		err        error
	)
	fecRelease, err = i.bind.AcquireEndpointFECPolicy(endpoint, peer.fecPolicy)
	if err != nil {
		return err
	}
	if peer.hasObfsKey {
		release, err = i.bind.AcquireEndpointKey(endpoint, peer.obfsKey)
		if err != nil {
			fecRelease()
			return err
		}
	}
	if err := wgdevice.SetPeerEndpoint(i.device, publicKey, endpoint); err != nil {
		if release != nil {
			release()
		}
		fecRelease()
		return fmt.Errorf("update WireGuard peer endpoint: %w", err)
	}
	activationReceiveSequence := i.bind.ReceiveSequence()

	oldEndpoint, oldEndpointErr := peerendpoint.ParseNumeric(peer.status.Endpoint)
	i.retainPeerEndpointResourcesLocked(peer, oldEndpoint, oldEndpointErr)
	peer.releaseAssociation = release
	peer.releaseFECPolicy = fecRelease
	peer.status.Endpoint = endpoint.String()
	peer.status.Generation = generation
	peer.status.AuthenticatedGeneration = 0
	peer.status.AuthenticatedEndpoint = ""
	peer.activationReceiveSequence = activationReceiveSequence
	if up {
		// The endpoint update is already committed. Readiness is reported
		// separately through peer session status, so a probe failure must not
		// turn a successful update into an ambiguous control response.
		_ = wgdevice.ProbePeer(i.device, publicKey)
	}
	return nil
}

func (i *Instance) retainPeerEndpointResourcesLocked(
	peer *peerRuntime,
	endpoint netip.AddrPort,
	endpointErr error,
) {
	if endpointErr != nil && peer.releaseAssociation == nil && peer.releaseFECPolicy == nil {
		return
	}
	peer.obsoleteEndpoints = append(peer.obsoleteEndpoints, peerEndpointResources{
		endpoint: endpoint, releaseAssociation: peer.releaseAssociation,
		releaseFECPolicy: peer.releaseFECPolicy,
	})
}

func (i *Instance) finalizePeerEndpoint(publicKey string, generation uint64) error {
	if generation == 0 {
		return errors.New("peer endpoint generation must be greater than zero")
	}
	i.endpointMu.Lock()
	defer i.endpointMu.Unlock()
	peer := i.peers[publicKey]
	if peer == nil {
		return errors.New("peer public key is not configured")
	}
	if peer.status.Generation != generation {
		return fmt.Errorf(
			"peer endpoint generation is %d, cannot finalize generation %d",
			peer.status.Generation, generation,
		)
	}
	active, _ := peerendpoint.ParseNumeric(peer.status.Endpoint)
	for index := range peer.obsoleteEndpoints {
		resource := &peer.obsoleteEndpoints[index]
		if resource.endpoint.IsValid() && resource.endpoint != active {
			i.bind.RetireEndpoint(resource.endpoint)
		}
		if resource.releaseAssociation != nil {
			resource.releaseAssociation()
			resource.releaseAssociation = nil
		}
		if resource.releaseFECPolicy != nil {
			resource.releaseFECPolicy()
			resource.releaseFECPolicy = nil
		}
	}
	peer.obsoleteEndpoints = nil
	return nil
}

func (i *Instance) recordAuthenticatedReceive(event device.AuthenticatedReceive) {
	publicKey := base64.StdEncoding.EncodeToString(event.PublicKey[:])
	i.endpointMu.Lock()
	peer := i.peers[publicKey]
	policy := ""
	if peer != nil {
		policy = peer.fecPolicy
	}
	if event.ReceiveSequence == 0 || peer == nil || peer.status.Generation == 0 ||
		peer.status.Endpoint != event.Endpoint ||
		event.ReceiveSequence <= peer.activationReceiveSequence {
		i.endpointMu.Unlock()
		if policy != "" {
			_ = i.bind.SetAuthenticatedSessionFECPolicy(event.SessionID, policy)
		}
		return
	}
	peer.status.AuthenticatedGeneration = peer.status.Generation
	peer.status.AuthenticatedEndpoint = event.Endpoint
	i.endpointMu.Unlock()
	_ = i.bind.SetAuthenticatedSessionFECPolicy(event.SessionID, policy)
}

func canonicalPeerFECPolicy(policy string) string {
	switch policy {
	case "latency", "throughput":
		return policy
	default:
		return "balanced"
	}
}

func (i *Instance) redialPeer(publicKey string) error {
	i.endpointMu.RLock()
	peer, ok := i.peers[publicKey]
	if !ok {
		i.endpointMu.RUnlock()
		return errors.New("peer public key is not configured")
	}
	endpoint, err := peerendpoint.ParseNumeric(peer.status.Endpoint)
	i.endpointMu.RUnlock()
	if err != nil {
		return errors.New("peer does not have an active numeric endpoint")
	}
	i.bind.RedialEndpoint(endpoint)
	return nil
}

func (i *Instance) restorePeersAfterTransportReconnect(endpoint netip.AddrPort) {
	i.endpointMu.RLock()
	publicKeys := make([]string, 0, 1)
	for _, publicKey := range i.peerOrder {
		peer := i.peers[publicKey]
		configured, err := peerendpoint.ParseNumeric(peer.status.Endpoint)
		if err == nil && configured == endpoint {
			publicKeys = append(publicKeys, publicKey)
		}
	}
	i.endpointMu.RUnlock()

	for _, publicKey := range publicKeys {
		// An authenticated packet received on a simultaneously accepted QUIC
		// connection may have taught WireGuard a connection-scoped endpoint.
		// Restore the maintained endpoint before probing the fresh transport.
		if err := wgdevice.SetPeerEndpoint(i.device, publicKey, endpoint); err != nil {
			log.Printf(
				"wg-quic transport %s: restore peer endpoint after reconnect: %v",
				i.name,
				err,
			)
			continue
		}
		if err := wgdevice.ProbePeer(i.device, publicKey); err != nil {
			log.Printf(
				"wg-quic transport %s: probe peer after reconnect: %v",
				i.name,
				err,
			)
		}
	}
}
