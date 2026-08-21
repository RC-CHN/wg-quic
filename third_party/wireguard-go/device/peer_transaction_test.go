package device

import (
	"encoding/hex"
	"net/netip"
	"strings"
	"testing"
)

func runtimeTestPublicKey(value byte) NoisePublicKey {
	var key NoisePublicKey
	for index := range key {
		key[index] = value
	}
	return key
}

func runtimeTestPresharedKey(value byte) NoisePresharedKey {
	var key NoisePresharedKey
	for index := range key {
		key[index] = value
	}
	return key
}

func addRuntimeTestPeer(
	t *testing.T,
	device *Device,
	publicKey NoisePublicKey,
	presharedKey NoisePresharedKey,
	keepalive uint16,
	prefixes ...string,
) *Peer {
	t.Helper()
	peer, err := device.NewPeer(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	peer.handshake.mutex.Lock()
	peer.handshake.presharedKey = presharedKey
	peer.handshake.mutex.Unlock()
	peer.persistentKeepaliveInterval.Store(uint32(keepalive))
	for _, value := range prefixes {
		device.allowedips.Insert(netip.MustParsePrefix(value), peer)
	}
	return peer
}

func runtimePeerConfig(
	publicKey NoisePublicKey,
	presharedKey NoisePresharedKey,
	keepalive uint16,
	prefixes ...string,
) RuntimePeerConfig {
	result := RuntimePeerConfig{
		PublicKey: publicKey, PresharedKey: presharedKey,
		PersistentKeepalive: keepalive,
	}
	for _, value := range prefixes {
		result.AllowedIPs = append(result.AllowedIPs, netip.MustParsePrefix(value))
	}
	return result
}

func TestPreparedPeerSetCommitPreservesUnrelatedObjectsAndDefersRemoval(t *testing.T) {
	device := randDevice(t)
	defer device.Close()
	key1, key2 := runtimeTestPublicKey(1), runtimeTestPublicKey(2)
	key3, key4 := runtimeTestPublicKey(3), runtimeTestPublicKey(4)
	psk1, psk2 := runtimeTestPresharedKey(11), runtimeTestPresharedKey(12)
	peer1 := addRuntimeTestPeer(t, device, key1, psk1, 0, "10.1.0.0/16")
	peer2 := addRuntimeTestPeer(t, device, key2, psk2, 25, "10.2.0.0/16")
	peer3 := addRuntimeTestPeer(t, device, key3, NoisePresharedKey{}, 0, "10.3.0.0/16")

	transaction, err := device.PreparePeerSet([]PeerMutation{
		{Operation: PeerMutationUpdate, Peer: runtimePeerConfig(key1, psk1, 15, "10.10.0.0/16")},
		{Operation: PeerMutationRemove, Peer: RuntimePeerConfig{PublicKey: key2}},
		{Operation: PeerMutationAdd, Peer: runtimePeerConfig(key4, runtimeTestPresharedKey(14), 20, "10.4.0.0/16")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if device.LookupPeer(key1) != peer1 || device.LookupPeer(key2) != peer2 || device.LookupPeer(key4) != nil {
		t.Fatal("prepare mutated peer objects")
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if device.LookupPeer(key1) != peer1 || device.LookupPeer(key3) != peer3 {
		t.Fatal("commit replaced an existing or unrelated peer object")
	}
	if device.LookupPeer(key2) != peer2 {
		t.Fatal("commit finalized removal before host transaction completed")
	}
	if device.LookupPeer(key4) == nil {
		t.Fatal("commit did not add peer")
	}
	projection := device.runtimePeerProjectionLocked()
	if got := projection[key2]; len(got.AllowedIPs) != 0 || got.PersistentKeepalive != 0 {
		t.Fatalf("removed peer was not detached at commit: %#v", got)
	}
	if got := projection[key1]; got.PersistentKeepalive != 15 ||
		len(got.AllowedIPs) != 1 || got.AllowedIPs[0] != netip.MustParsePrefix("10.10.0.0/16") {
		t.Fatalf("updated peer projection = %#v", got)
	}
	if err := transaction.Finalize(); err != nil {
		t.Fatal(err)
	}
	if device.LookupPeer(key2) != nil || device.LookupPeer(key3) != peer3 {
		t.Fatal("finalization did not remove only the selected peer")
	}
	if err := transaction.Finalize(); err != nil {
		t.Fatalf("finalization is not idempotent: %v", err)
	}
}

func TestPreparedPeerSetRollbackRestoresPriorProjection(t *testing.T) {
	device := randDevice(t)
	defer device.Close()
	key1, key2, key3 := runtimeTestPublicKey(21), runtimeTestPublicKey(22), runtimeTestPublicKey(23)
	psk1, psk2 := runtimeTestPresharedKey(31), runtimeTestPresharedKey(32)
	peer1 := addRuntimeTestPeer(t, device, key1, psk1, 5, "10.1.0.0/16")
	peer2 := addRuntimeTestPeer(t, device, key2, psk2, 25, "10.2.0.0/16")
	before := device.runtimePeerProjectionLocked()
	transaction, err := device.PreparePeerSet([]PeerMutation{
		{Operation: PeerMutationUpdate, Peer: runtimePeerConfig(key1, psk1, 30, "10.9.0.0/16")},
		{Operation: PeerMutationRemove, Peer: RuntimePeerConfig{PublicKey: key2}},
		{Operation: PeerMutationAdd, Peer: runtimePeerConfig(key3, NoisePresharedKey{}, 0, "10.3.0.0/16")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !equalPeerProjection(device.runtimePeerProjectionLocked(), before) {
		t.Fatal("rollback did not restore the prior projection")
	}
	if device.LookupPeer(key1) != peer1 || device.LookupPeer(key2) != peer2 || device.LookupPeer(key3) != nil {
		t.Fatal("rollback did not preserve prior peer identity")
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("rollback is not idempotent: %v", err)
	}
}

func TestPreparePeerSetRejectsConflictsWithoutMutation(t *testing.T) {
	device := randDevice(t)
	defer device.Close()
	key1, key2 := runtimeTestPublicKey(41), runtimeTestPublicKey(42)
	psk := runtimeTestPresharedKey(51)
	addRuntimeTestPeer(t, device, key1, psk, 0, "10.1.0.0/16")
	before := device.runtimePeerProjectionLocked()
	_, err := device.PreparePeerSet([]PeerMutation{{
		Operation: PeerMutationAdd,
		Peer:      runtimePeerConfig(key2, NoisePresharedKey{}, 0, "10.1.0.0/16"),
	}})
	if err == nil || !strings.Contains(err.Error(), "multiple peer owners") {
		t.Fatalf("AllowedIP conflict error = %v", err)
	}
	_, err = device.PreparePeerSet([]PeerMutation{{
		Operation: PeerMutationUpdate,
		Peer:      runtimePeerConfig(key1, runtimeTestPresharedKey(52), 0, "10.1.0.0/16"),
	}})
	if err == nil || !strings.Contains(err.Error(), "preshared-key rotation") {
		t.Fatalf("PSK rotation error = %v", err)
	}
	if !equalPeerProjection(device.runtimePeerProjectionLocked(), before) {
		t.Fatal("failed preparation mutated device")
	}
}

func TestPreparedPeerSetDetectsPeerConfigDriftButAllowsEndpointChange(t *testing.T) {
	device := randDevice(t)
	defer device.Close()
	key := runtimeTestPublicKey(61)
	psk := runtimeTestPresharedKey(62)
	peer := addRuntimeTestPeer(t, device, key, psk, 0, "10.1.0.0/16")
	transaction, err := device.PreparePeerSet([]PeerMutation{{
		Operation: PeerMutationUpdate,
		Peer:      runtimePeerConfig(key, psk, 10, "10.2.0.0/16"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	endpointUpdate := "public_key=" + hex.EncodeToString(key[:]) + "\n" +
		"update_only=true\nendpoint=192.0.2.10:443\n"
	if err := device.IpcSet(endpointUpdate); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("endpoint-only drift blocked peer transaction: %v", err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}

	second, err := device.PreparePeerSet([]PeerMutation{{
		Operation: PeerMutationUpdate,
		Peer:      runtimePeerConfig(key, psk, 10, "10.2.0.0/16"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	peer.persistentKeepaliveInterval.Store(99)
	if err := second.Commit(); err == nil || !strings.Contains(err.Error(), "changed after") {
		t.Fatalf("config drift commit error = %v", err)
	}
}
