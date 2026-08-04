package armorbind

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/tuntest"
)

type testDevice struct {
	bind *Bind
	tun  *tuntest.ChannelTUN
	dev  *device.Device
	ip   netip.Addr
}

func TestWireGuardDeviceBidirectionalOverQUIC(t *testing.T) {
	var privateKeys [2][32]byte
	var publicKeys [2][]byte
	for i := range privateKeys {
		if _, err := rand.Read(privateKeys[i][:]); err != nil {
			t.Fatal(err)
		}
		public, err := curve25519.X25519(privateKeys[i][:], curve25519.Basepoint)
		if err != nil {
			t.Fatal(err)
		}
		publicKeys[i] = public
	}

	var peers [2]testDevice
	for i := range peers {
		peers[i].ip = netip.AddrFrom4([4]byte{10, 55, 0, byte(i + 1)})
	}
	for i := range peers {
		peers[i].bind = New(DefaultConfig())
		peers[i].tun = tuntest.NewChannelTUN()
		peers[i].dev = device.NewDevice(
			peers[i].tun.TUN(),
			peers[i].bind,
			device.NewLogger(device.LogLevelError, fmt.Sprintf("wgq-test-%d: ", i)),
		)
		cfg := fmt.Sprintf(
			"private_key=%s\nlisten_port=0\nreplace_peers=true\npublic_key=%s\nprotocol_version=1\nreplace_allowed_ips=true\nallowed_ip=%s/32\n",
			hex.EncodeToString(privateKeys[i][:]),
			hex.EncodeToString(publicKeys[i^1]),
			peers[i^1].ip,
		)
		if err := peers[i].dev.IpcSet(cfg); err != nil {
			t.Fatal(err)
		}
		if err := peers[i].dev.Up(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(peers[i].dev.Close)
	}
	for i := range peers {
		cfg := fmt.Sprintf(
			"public_key=%s\nendpoint=127.0.0.1:%d\n",
			hex.EncodeToString(publicKeys[i^1]),
			peers[i^1].bind.Port(),
		)
		if err := peers[i].dev.IpcSet(cfg); err != nil {
			t.Fatal(err)
		}
	}

	sendTUNPacket(t, &peers[0], &peers[1])
	sendTUNPacket(t, &peers[1], &peers[0])
}

func sendTUNPacket(t *testing.T, destination, source *testDevice) {
	t.Helper()
	packet := tuntest.Ping(destination.ip, source.ip)
	select {
	case source.tun.Outbound <- packet:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out injecting packet into TUN")
	}
	select {
	case got := <-destination.tun.Inbound:
		if !bytes.Equal(got, packet) {
			t.Fatal("WireGuard plaintext changed across QUIC transport")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for WireGuard plaintext")
	}
}
