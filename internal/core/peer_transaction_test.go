package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun/tuntest"
	"golang.org/x/crypto/curve25519"
)

func coreTransactionPublicKey(t *testing.T, value byte) string {
	t.Helper()
	publicKey, err := curve25519.X25519(bytes.Repeat([]byte{value}, 32), curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(publicKey)
}

func newCoreTransactionInstance(t *testing.T) (*Instance, []string) {
	t.Helper()
	keys := []string{
		coreTransactionPublicKey(t, 0x41),
		coreTransactionPublicKey(t, 0x42),
		coreTransactionPublicKey(t, 0x43),
	}
	host := &testDeviceHost{
		tunnel: tuntest.NewChannelTUN(), controlPath: testControlPath(t, "transactions"),
	}
	instance, err := New(&config.Config{
		Interface: config.Interface{
			PrivateKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32)),
		},
		Peers: []config.Peer{
			{PublicKey: keys[0], AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}},
			{PublicKey: keys[1], AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.2.0.0/16")}},
		},
		Transport: config.DefaultTransport(),
	}, "wg0", host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return instance, keys
}

func corePeerSetRequest(keys []string) control.PeerSetRequest {
	return control.PeerSetRequest{
		TransactionID: "transaction-1",
		Mutations: []control.PeerMutation{
			{
				Operation: "update", PublicKey: keys[0],
				AllowedIPs: []string{"10.10.0.0/16"}, PersistentKeepalive: 15,
				FECPolicy: "balanced",
			},
			{Operation: "remove", PublicKey: keys[1]},
			{
				Operation: "add", PublicKey: keys[2],
				AllowedIPs: []string{"10.3.0.0/16"}, PersistentKeepalive: 25,
				FECPolicy: "balanced",
			},
		},
	}
}

func TestCorePeerTransactionCommitAndFinalizePreserveUnrelatedState(t *testing.T) {
	instance, keys := newCoreTransactionInstance(t)
	peer1, peer2 := instance.peers[keys[0]], instance.peers[keys[1]]
	request := corePeerSetRequest(keys)
	if err := instance.preparePeerSet(request); err != nil {
		t.Fatal(err)
	}
	if _, exists := instance.peers[keys[2]]; exists {
		t.Fatal("prepare published added peer runtime")
	}
	if err := instance.commitPeerSet(request.TransactionID); err != nil {
		t.Fatal(err)
	}
	if instance.peers[keys[0]] != peer1 || instance.peers[keys[1]] != peer2 || instance.peers[keys[2]] == nil {
		t.Fatal("commit replaced existing runtime identity or failed to add runtime")
	}
	if err := instance.finalizePeerSet(request.TransactionID); err != nil {
		t.Fatal(err)
	}
	if instance.peers[keys[0]] != peer1 || instance.peers[keys[1]] != nil || instance.peers[keys[2]] == nil {
		t.Fatal("finalize did not remove only the requested runtime")
	}
	if !slices.Equal(instance.peerOrder, []string{keys[0], keys[2]}) {
		t.Fatalf("peer order = %#v", instance.peerOrder)
	}
	if got := instance.cfg.Peers; len(got) != 2 || got[0].AllowedIPs[0].String() != "10.10.0.0/16" {
		t.Fatalf("committed core config = %#v", got)
	}
	if err := instance.finalizePeerSet(request.TransactionID); err != nil {
		t.Fatalf("finalize is not idempotent: %v", err)
	}
	status := instance.status()
	if !slices.Contains(status.Capabilities, "typed_peer_transactions_v1") ||
		!slices.Contains(status.Capabilities, "dynamic_obfs_keys") ||
		!slices.Contains(status.Capabilities, "dynamic_peer_fec_policy") {
		t.Fatalf("core capabilities = %#v", status.Capabilities)
	}
}

func TestCorePeerTransactionRollbackRemovesPreparedRuntimeAndKeys(t *testing.T) {
	instance, keys := newCoreTransactionInstance(t)
	peer1, peer2 := instance.peers[keys[0]], instance.peers[keys[1]]
	request := corePeerSetRequest(keys)
	if err := instance.preparePeerSet(request); err != nil {
		t.Fatal(err)
	}
	if err := instance.commitPeerSet(request.TransactionID); err != nil {
		t.Fatal(err)
	}
	if err := instance.rollbackPeerSet(request.TransactionID); err != nil {
		t.Fatal(err)
	}
	if instance.peers[keys[0]] != peer1 || instance.peers[keys[1]] != peer2 || instance.peers[keys[2]] != nil {
		t.Fatal("rollback did not restore runtime peer identity")
	}
	if !slices.Equal(instance.peerOrder, []string{keys[0], keys[1]}) {
		t.Fatalf("peer order after rollback = %#v", instance.peerOrder)
	}
	if err := instance.rollbackPeerSet(request.TransactionID); err != nil {
		t.Fatalf("rollback is not idempotent: %v", err)
	}
}

func TestCorePeerTransactionRequestIDCollisionAndSerialization(t *testing.T) {
	instance, keys := newCoreTransactionInstance(t)
	request := corePeerSetRequest(keys)
	if err := instance.preparePeerSet(request); err != nil {
		t.Fatal(err)
	}
	if err := instance.preparePeerSet(request); err != nil {
		t.Fatalf("same prepare was not idempotent: %v", err)
	}
	conflict := request
	conflict.Mutations = slices.Clone(request.Mutations)
	conflict.Mutations[0].PersistentKeepalive = 99
	if err := instance.preparePeerSet(conflict); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("request-ID collision error = %v", err)
	}
	other := request
	other.TransactionID = "transaction-2"
	if err := instance.preparePeerSet(other); err == nil || !strings.Contains(err.Error(), "another") {
		t.Fatalf("concurrent transaction error = %v", err)
	}
	if err := instance.rollbackPeerSet(request.TransactionID); err != nil {
		t.Fatal(err)
	}
	if err := instance.preparePeerSet(other); err != nil {
		t.Fatalf("new transaction after rollback failed: %v", err)
	}
	if err := instance.rollbackPeerSet(other.TransactionID); err != nil {
		t.Fatal(err)
	}
}

func TestCorePeerTransactionControlRoundTrip(t *testing.T) {
	instance, keys := newCoreTransactionInstance(t)
	if err := instance.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	client := control.NewClient(instance.controlPath)
	request := corePeerSetRequest(keys)
	request.TransactionID = "through-control"
	if err := client.PreparePeerSet(request); err != nil {
		t.Fatal(err)
	}
	if err := client.CommitPeerSet(request.TransactionID); err != nil {
		t.Fatal(err)
	}
	if err := client.RollbackPeerSet(request.TransactionID); err != nil {
		t.Fatal(err)
	}
}

func TestCorePeerFECPolicyCommitAndRollback(t *testing.T) {
	instance, keys := newCoreTransactionInstance(t)
	endpoint := netip.MustParseAddrPort("192.0.2.10:443")
	if err := instance.installPeerEndpoint(keys[0], endpoint, 1); err != nil {
		t.Fatal(err)
	}
	request := control.PeerSetRequest{
		TransactionID: "fec-policy",
		Mutations: []control.PeerMutation{{
			Operation: "update", PublicKey: keys[0],
			AllowedIPs: []string{"10.1.0.0/16"}, FECPolicy: "throughput",
		}},
	}
	if err := instance.preparePeerSet(request); err != nil {
		t.Fatal(err)
	}
	if got := instance.peers[keys[0]].fecPolicy; got != "balanced" {
		t.Fatalf("prepare published FEC policy %q", got)
	}
	if err := instance.commitPeerSet(request.TransactionID); err != nil {
		t.Fatal(err)
	}
	if got := instance.peers[keys[0]].fecPolicy; got != "throughput" {
		t.Fatalf("committed FEC policy = %q", got)
	}
	if err := instance.rollbackPeerSet(request.TransactionID); err != nil {
		t.Fatal(err)
	}
	if got := instance.peers[keys[0]].fecPolicy; got != "balanced" {
		t.Fatalf("rolled-back FEC policy = %q", got)
	}
}
