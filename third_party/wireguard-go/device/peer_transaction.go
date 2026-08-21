package device

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"
)

type PeerMutationOperation uint8

const (
	PeerMutationAdd PeerMutationOperation = iota + 1
	PeerMutationUpdate
	PeerMutationRemove
)

// RuntimePeerConfig is the complete mutable WireGuard projection used by one
// peer transaction. Endpoint selection is intentionally owned by the separate
// endpoint-generation transaction.
type RuntimePeerConfig struct {
	PublicKey           NoisePublicKey
	PresharedKey        NoisePresharedKey
	AllowedIPs          []netip.Prefix
	PersistentKeepalive uint16
}

type PeerMutation struct {
	Operation PeerMutationOperation
	Peer      RuntimePeerConfig
}

type preparedPeerSetState uint8

const (
	preparedPeerSetReady preparedPeerSetState = iota
	preparedPeerSetCommitted
	preparedPeerSetRolledBack
	preparedPeerSetFinalized
)

// PreparedPeerSet contains a prevalidated before/after projection. Prepare
// performs no mutation. Commit holds the device IPC mutex, verifies the entire
// peer projection still matches the snapshot, and then performs only
// non-failing in-memory ownership changes after all peer allocations succeed.
type PreparedPeerSet struct {
	mu        sync.Mutex
	device    *Device
	mutations []PeerMutation
	before    map[NoisePublicKey]RuntimePeerConfig
	committed map[NoisePublicKey]RuntimePeerConfig
	after     map[NoisePublicKey]RuntimePeerConfig
	state     preparedPeerSetState
}

func (device *Device) PreparePeerSet(mutations []PeerMutation) (*PreparedPeerSet, error) {
	if len(mutations) == 0 {
		return nil, errors.New("peer transaction requires at least one mutation")
	}
	cloned, err := validatePeerMutations(mutations)
	if err != nil {
		return nil, err
	}
	device.ipcMutex.Lock()
	defer device.ipcMutex.Unlock()
	if device.isClosed() {
		return nil, errors.New("device is closed")
	}
	before := device.runtimePeerProjectionLocked()
	after, err := applyPeerProjection(before, cloned)
	if err != nil {
		return nil, err
	}
	if len(after) > MaxPeers {
		return nil, fmt.Errorf("peer transaction exceeds maximum peer count %d", MaxPeers)
	}
	device.staticIdentity.RLock()
	localPublicKey := device.staticIdentity.publicKey
	device.staticIdentity.RUnlock()
	for _, mutation := range cloned {
		_, exists := before[mutation.Peer.PublicKey]
		switch mutation.Operation {
		case PeerMutationAdd:
			if exists {
				return nil, errors.New("peer add targets an existing public key")
			}
			if mutation.Peer.PublicKey.Equals(localPublicKey) {
				return nil, errors.New("peer public key matches the interface public key")
			}
		case PeerMutationUpdate:
			current, ok := before[mutation.Peer.PublicKey]
			if !ok {
				return nil, errors.New("peer update targets a missing public key")
			}
			if current.PresharedKey != mutation.Peer.PresharedKey {
				return nil, errors.New("active peer preshared-key rotation requires restart")
			}
		case PeerMutationRemove:
			if !exists {
				return nil, errors.New("peer removal targets a missing public key")
			}
		}
	}
	if err := validateAllowedIPOwnership(after); err != nil {
		return nil, err
	}
	committed := clonePeerProjection(after)
	for _, mutation := range cloned {
		if mutation.Operation != PeerMutationRemove {
			continue
		}
		removed := cloneRuntimePeerConfig(before[mutation.Peer.PublicKey])
		removed.AllowedIPs = nil
		removed.PersistentKeepalive = 0
		committed[mutation.Peer.PublicKey] = removed
	}
	return &PreparedPeerSet{
		device: device, mutations: cloned,
		before: clonePeerProjection(before), committed: committed,
		after: clonePeerProjection(after),
		state: preparedPeerSetReady,
	}, nil
}

func validatePeerMutations(mutations []PeerMutation) ([]PeerMutation, error) {
	result := make([]PeerMutation, len(mutations))
	seenPeers := make(map[NoisePublicKey]struct{}, len(mutations))
	for index, mutation := range mutations {
		if mutation.Peer.PublicKey.IsZero() {
			return nil, fmt.Errorf("peer mutation %d has a zero public key", index+1)
		}
		if _, exists := seenPeers[mutation.Peer.PublicKey]; exists {
			return nil, fmt.Errorf("peer mutation %d duplicates a public key", index+1)
		}
		seenPeers[mutation.Peer.PublicKey] = struct{}{}
		switch mutation.Operation {
		case PeerMutationAdd, PeerMutationUpdate, PeerMutationRemove:
		default:
			return nil, fmt.Errorf("peer mutation %d has an invalid operation", index+1)
		}
		result[index] = mutation
		result[index].Peer.AllowedIPs = append([]netip.Prefix(nil), mutation.Peer.AllowedIPs...)
		seenPrefixes := make(map[netip.Prefix]struct{}, len(mutation.Peer.AllowedIPs))
		for prefixIndex, prefix := range result[index].Peer.AllowedIPs {
			if !prefix.IsValid() {
				return nil, fmt.Errorf("peer mutation %d AllowedIP %d is invalid", index+1, prefixIndex+1)
			}
			prefix = prefix.Masked()
			result[index].Peer.AllowedIPs[prefixIndex] = prefix
			if _, exists := seenPrefixes[prefix]; exists {
				return nil, fmt.Errorf("peer mutation %d AllowedIP %s is duplicated", index+1, prefix)
			}
			seenPrefixes[prefix] = struct{}{}
		}
		sortRuntimePrefixes(result[index].Peer.AllowedIPs)
	}
	return result, nil
}

func applyPeerProjection(
	before map[NoisePublicKey]RuntimePeerConfig,
	mutations []PeerMutation,
) (map[NoisePublicKey]RuntimePeerConfig, error) {
	after := clonePeerProjection(before)
	for _, mutation := range mutations {
		switch mutation.Operation {
		case PeerMutationAdd:
			if _, exists := after[mutation.Peer.PublicKey]; exists {
				return nil, errors.New("peer add targets an existing public key")
			}
			after[mutation.Peer.PublicKey] = cloneRuntimePeerConfig(mutation.Peer)
		case PeerMutationUpdate:
			if _, exists := after[mutation.Peer.PublicKey]; !exists {
				return nil, errors.New("peer update targets a missing public key")
			}
			after[mutation.Peer.PublicKey] = cloneRuntimePeerConfig(mutation.Peer)
		case PeerMutationRemove:
			if _, exists := after[mutation.Peer.PublicKey]; !exists {
				return nil, errors.New("peer removal targets a missing public key")
			}
			delete(after, mutation.Peer.PublicKey)
		}
	}
	return after, nil
}

func validateAllowedIPOwnership(peers map[NoisePublicKey]RuntimePeerConfig) error {
	owners := make(map[netip.Prefix]NoisePublicKey)
	for publicKey, peer := range peers {
		for _, prefix := range peer.AllowedIPs {
			if owner, exists := owners[prefix]; exists && owner != publicKey {
				return fmt.Errorf("AllowedIP %s has multiple peer owners", prefix)
			}
			owners[prefix] = publicKey
		}
	}
	return nil
}

func (transaction *PreparedPeerSet) Commit() error {
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	switch transaction.state {
	case preparedPeerSetCommitted, preparedPeerSetFinalized:
		return nil
	case preparedPeerSetRolledBack:
		return errors.New("peer transaction was rolled back")
	}
	device := transaction.device
	device.ipcMutex.Lock()
	defer device.ipcMutex.Unlock()
	if device.isClosed() {
		return errors.New("device is closed")
	}
	if !equalPeerProjection(device.runtimePeerProjectionLocked(), transaction.before) {
		return errors.New("peer configuration changed after transaction preparation")
	}

	created := make(map[NoisePublicKey]*Peer)
	for _, mutation := range transaction.mutations {
		if mutation.Operation != PeerMutationAdd {
			continue
		}
		peer, err := device.NewPeer(mutation.Peer.PublicKey)
		if err != nil {
			for publicKey := range created {
				device.RemovePeer(publicKey)
			}
			return fmt.Errorf("create prepared peer: %w", err)
		}
		created[mutation.Peer.PublicKey] = peer
	}

	for _, mutation := range transaction.mutations {
		if mutation.Operation == PeerMutationAdd {
			continue
		}
		peer := device.LookupPeer(mutation.Peer.PublicKey)
		device.allowedips.RemoveByPeer(peer)
		if mutation.Operation == PeerMutationRemove {
			peer.persistentKeepaliveInterval.Store(0)
		}
	}

	for _, mutation := range transaction.mutations {
		if mutation.Operation == PeerMutationRemove {
			continue
		}
		peer := device.LookupPeer(mutation.Peer.PublicKey)
		if mutation.Operation == PeerMutationAdd {
			peer.handshake.mutex.Lock()
			peer.handshake.presharedKey = mutation.Peer.PresharedKey
			peer.handshake.mutex.Unlock()
		}
		oldKeepalive := peer.persistentKeepaliveInterval.Swap(
			uint32(mutation.Peer.PersistentKeepalive),
		)
		for _, prefix := range mutation.Peer.AllowedIPs {
			device.allowedips.Insert(prefix, peer)
		}
		if device.isUp() {
			peer.Start()
			if oldKeepalive == 0 && mutation.Peer.PersistentKeepalive != 0 {
				peer.SendKeepalive()
			}
			peer.SendStagedPackets()
		}
	}
	transaction.state = preparedPeerSetCommitted
	return nil
}

func (transaction *PreparedPeerSet) Rollback() error {
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	switch transaction.state {
	case preparedPeerSetReady:
		transaction.state = preparedPeerSetRolledBack
		transaction.clearSecrets()
		return nil
	case preparedPeerSetRolledBack:
		return nil
	case preparedPeerSetFinalized:
		return errors.New("finalized peer transaction cannot be rolled back")
	}
	device := transaction.device
	device.ipcMutex.Lock()
	defer device.ipcMutex.Unlock()
	if device.isClosed() {
		return errors.New("device is closed")
	}
	if !equalPeerProjection(device.runtimePeerProjectionLocked(), transaction.committed) {
		return errors.New("peer configuration changed after transaction commit")
	}
	for _, mutation := range transaction.mutations {
		if peer := device.LookupPeer(mutation.Peer.PublicKey); peer != nil {
			device.allowedips.RemoveByPeer(peer)
		}
	}
	for _, mutation := range transaction.mutations {
		if mutation.Operation == PeerMutationAdd {
			device.RemovePeer(mutation.Peer.PublicKey)
		}
	}
	for _, mutation := range transaction.mutations {
		before, existed := transaction.before[mutation.Peer.PublicKey]
		if !existed {
			continue
		}
		peer := device.LookupPeer(mutation.Peer.PublicKey)
		peer.handshake.mutex.Lock()
		peer.handshake.presharedKey = before.PresharedKey
		peer.handshake.mutex.Unlock()
		oldKeepalive := peer.persistentKeepaliveInterval.Swap(uint32(before.PersistentKeepalive))
		for _, prefix := range before.AllowedIPs {
			device.allowedips.Insert(prefix, peer)
		}
		if device.isUp() && oldKeepalive == 0 && before.PersistentKeepalive != 0 {
			peer.SendKeepalive()
		}
	}
	transaction.state = preparedPeerSetRolledBack
	transaction.clearSecrets()
	return nil
}

func (transaction *PreparedPeerSet) Finalize() error {
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	switch transaction.state {
	case preparedPeerSetFinalized:
		return nil
	case preparedPeerSetReady:
		return errors.New("uncommitted peer transaction cannot be finalized")
	case preparedPeerSetRolledBack:
		return errors.New("rolled-back peer transaction cannot be finalized")
	}
	device := transaction.device
	device.ipcMutex.Lock()
	defer device.ipcMutex.Unlock()
	if device.isClosed() {
		return errors.New("device is closed")
	}
	if !equalPeerProjection(device.runtimePeerProjectionLocked(), transaction.committed) {
		return errors.New("peer configuration changed before transaction finalization")
	}
	for _, mutation := range transaction.mutations {
		if mutation.Operation == PeerMutationRemove {
			device.RemovePeer(mutation.Peer.PublicKey)
		}
	}
	transaction.state = preparedPeerSetFinalized
	transaction.clearSecrets()
	return nil
}

// runtimePeerProjectionLocked requires device.ipcMutex. Endpoint changes and
// counters are excluded so DDNS/roaming can proceed between prepare and commit.
func (device *Device) runtimePeerProjectionLocked() map[NoisePublicKey]RuntimePeerConfig {
	device.peers.RLock()
	peers := make(map[NoisePublicKey]*Peer, len(device.peers.keyMap))
	for publicKey, peer := range device.peers.keyMap {
		peers[publicKey] = peer
	}
	device.peers.RUnlock()
	result := make(map[NoisePublicKey]RuntimePeerConfig, len(peers))
	for publicKey, peer := range peers {
		peer.handshake.mutex.RLock()
		presharedKey := peer.handshake.presharedKey
		peer.handshake.mutex.RUnlock()
		config := RuntimePeerConfig{
			PublicKey: publicKey, PresharedKey: presharedKey,
			PersistentKeepalive: uint16(peer.persistentKeepaliveInterval.Load()),
		}
		device.allowedips.EntriesForPeer(peer, func(prefix netip.Prefix) bool {
			config.AllowedIPs = append(config.AllowedIPs, prefix.Masked())
			return true
		})
		sortRuntimePrefixes(config.AllowedIPs)
		result[publicKey] = config
	}
	return result
}

func cloneRuntimePeerConfig(peer RuntimePeerConfig) RuntimePeerConfig {
	peer.AllowedIPs = append([]netip.Prefix(nil), peer.AllowedIPs...)
	return peer
}

func clonePeerProjection(peers map[NoisePublicKey]RuntimePeerConfig) map[NoisePublicKey]RuntimePeerConfig {
	result := make(map[NoisePublicKey]RuntimePeerConfig, len(peers))
	for publicKey, peer := range peers {
		result[publicKey] = cloneRuntimePeerConfig(peer)
	}
	return result
}

func equalPeerProjection(left, right map[NoisePublicKey]RuntimePeerConfig) bool {
	if len(left) != len(right) {
		return false
	}
	for publicKey, leftPeer := range left {
		rightPeer, exists := right[publicKey]
		if !exists || leftPeer.PublicKey != rightPeer.PublicKey ||
			leftPeer.PresharedKey != rightPeer.PresharedKey ||
			leftPeer.PersistentKeepalive != rightPeer.PersistentKeepalive ||
			!slices.Equal(leftPeer.AllowedIPs, rightPeer.AllowedIPs) {
			return false
		}
	}
	return true
}

func sortRuntimePrefixes(prefixes []netip.Prefix) {
	slices.SortFunc(prefixes, func(left, right netip.Prefix) int {
		if left.Addr().Is4() != right.Addr().Is4() {
			if left.Addr().Is4() {
				return -1
			}
			return 1
		}
		if left.Bits() != right.Bits() {
			return left.Bits() - right.Bits()
		}
		return left.Addr().Compare(right.Addr())
	})
}

func (transaction *PreparedPeerSet) clearSecrets() {
	for publicKey, peer := range transaction.before {
		peer.PresharedKey = NoisePresharedKey{}
		transaction.before[publicKey] = peer
	}
	for publicKey, peer := range transaction.after {
		peer.PresharedKey = NoisePresharedKey{}
		transaction.after[publicKey] = peer
	}
	for publicKey, peer := range transaction.committed {
		peer.PresharedKey = NoisePresharedKey{}
		transaction.committed[publicKey] = peer
	}
	for index := range transaction.mutations {
		transaction.mutations[index].Peer.PresharedKey = NoisePresharedKey{}
	}
}
