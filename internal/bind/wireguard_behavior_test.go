//go:build !windows

package armorbind

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/device"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun/tuntest"
	"golang.org/x/crypto/curve25519"
)

type behaviorPeer struct {
	bind *Bind
	tun  *tuntest.ChannelTUN
	dev  *device.Device
	ipv4 netip.Addr
	ipv6 netip.Addr
}

func TestWireGuardUpperLayerPacketsOverArmorBind(t *testing.T) {
	psk := randomTestKey(t)
	peers := newBehaviorPair(t, [2][32]byte{psk, psk}, psk[:])

	for i, payloadSize := range []int{0, 1, 128, 1300, 5000, 12000} {
		source := peers[i%2]
		destination := peers[(i+1)%2]
		ipv4 := testIPv4Packet(source.ipv4, destination.ipv4, payloadSize, uint16(i))
		exchangeTUNPacket(t, source.tun, destination.tun, ipv4)

		ipv6 := testIPv6Packet(destination.ipv6, source.ipv6, payloadSize, uint16(i))
		exchangeTUNPacket(t, destination.tun, source.tun, ipv6)
	}

	for _, peer := range peers {
		assertFullTransportUsed(t, peer.bind)
	}
}

func TestWireGuardConcurrentBidirectionalTrafficOverArmorBind(t *testing.T) {
	psk := randomTestKey(t)
	peers := newBehaviorPair(t, [2][32]byte{psk, psk}, psk[:])
	exchangeTUNPacket(
		t,
		peers[0].tun,
		peers[1].tun,
		testIPv4Packet(peers[0].ipv4, peers[1].ipv4, 64, 0xffff),
	)

	const packetCount = 32
	leftToRight := make([][]byte, packetCount)
	rightToLeft := make([][]byte, packetCount)
	for i := 0; i < packetCount; i++ {
		leftToRight[i] = testIPv4Packet(
			peers[0].ipv4,
			peers[1].ipv4,
			32+(i%7)*401,
			uint16(i),
		)
		rightToLeft[i] = testIPv6Packet(
			peers[1].ipv6,
			peers[0].ipv6,
			64+(i%5)*509,
			uint16(0x8000+i),
		)
	}

	type batchResult struct {
		name    string
		packets [][]byte
		err     error
	}
	results := make(chan batchResult, 4)
	go func() {
		packets, err := receiveTUNBatch(peers[1].tun, packetCount, 15*time.Second)
		results <- batchResult{name: "left-to-right receive", packets: packets, err: err}
	}()
	go func() {
		packets, err := receiveTUNBatch(peers[0].tun, packetCount, 15*time.Second)
		results <- batchResult{name: "right-to-left receive", packets: packets, err: err}
	}()
	go func() {
		results <- batchResult{
			name: "left-to-right send",
			err:  sendTUNBatch(peers[0].tun, leftToRight, 15*time.Second),
		}
	}()
	go func() {
		results <- batchResult{
			name: "right-to-left send",
			err:  sendTUNBatch(peers[1].tun, rightToLeft, 15*time.Second),
		}
	}()

	var gotLeftToRight, gotRightToLeft [][]byte
	for range 4 {
		result := <-results
		if result.err != nil {
			t.Fatalf("%s: %v", result.name, result.err)
		}
		switch result.name {
		case "left-to-right receive":
			gotLeftToRight = result.packets
		case "right-to-left receive":
			gotRightToLeft = result.packets
		}
	}
	assertPacketSet(t, gotLeftToRight, leftToRight)
	assertPacketSet(t, gotRightToLeft, rightToLeft)
}

func TestWireGuardAllowedIPsRejectSpoofedSourceOverArmorBind(t *testing.T) {
	psk := randomTestKey(t)
	peers := newBehaviorPair(t, [2][32]byte{psk, psk}, psk[:])

	valid := testIPv4Packet(peers[0].ipv4, peers[1].ipv4, 64, 1)
	exchangeTUNPacket(t, peers[0].tun, peers[1].tun, valid)

	spoofed := testIPv4Packet(
		netip.MustParseAddr("10.55.99.1"),
		peers[1].ipv4,
		64,
		2,
	)
	injectTUNPacket(t, peers[0].tun, spoofed)
	expectNoTUNPacket(t, peers[1].tun, 500*time.Millisecond)

	afterDrop := testIPv4Packet(peers[0].ipv4, peers[1].ipv4, 64, 3)
	exchangeTUNPacket(t, peers[0].tun, peers[1].tun, afterDrop)
}

func TestWireGuardMismatchedPresharedKeyRejectedOverArmorBind(t *testing.T) {
	leftPSK := randomTestKey(t)
	rightPSK := randomTestKey(t)
	transportPSK := randomTestKey(t)
	peers := newBehaviorPair(
		t,
		[2][32]byte{leftPSK, rightPSK},
		transportPSK[:],
	)

	packet := testIPv4Packet(peers[0].ipv4, peers[1].ipv4, 64, 1)
	injectTUNPacket(t, peers[0].tun, packet)
	expectNoTUNPacket(t, peers[1].tun, 1500*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for {
		left := peers[0].bind.Stats()
		right := peers[1].bind.Stats()
		if left.WireTxPackets > 0 && right.WireRxPackets > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"mismatched WireGuard PSK did not reach the working transport: left=%+v right=%+v",
				left,
				right,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWireGuardMultiPeerCryptokeyRoutingOverArmorBind(t *testing.T) {
	const peerCount = 3
	var privateKeys [peerCount][32]byte
	var publicKeys [peerCount][]byte
	var peers [peerCount]*behaviorPeer
	psk := randomTestKey(t)
	sharedObfsKey := obfs.Key(randomTestKey(t))

	for i := range peerCount {
		privateKeys[i] = randomTestKey(t)
		publicKey, err := curve25519.X25519(privateKeys[i][:], curve25519.Basepoint)
		if err != nil {
			t.Fatal(err)
		}
		publicKeys[i] = publicKey
		peers[i] = &behaviorPeer{
			bind: New(func() Config {
				cfg := DefaultConfig()
				cfg.ObfsMode = "salamander"
				cfg.ObfsKeys = []obfs.Key{sharedObfsKey}
				return cfg
			}()),
			tun:  tuntest.NewChannelTUN(),
			ipv4: netip.AddrFrom4([4]byte{10, 56, 0, byte(i + 1)}),
		}
		peers[i].dev = device.NewDevice(
			peers[i].tun.TUN(),
			peers[i].bind,
			device.NewLogger(device.LogLevelError, fmt.Sprintf("wgq-multi-%d: ", i)),
		)
		t.Cleanup(peers[i].dev.Close)
	}

	topology := [peerCount][]int{{1, 2}, {0}, {0}}
	for i, peer := range peers {
		var cfg strings.Builder
		fmt.Fprintf(
			&cfg,
			"private_key=%s\nlisten_port=0\nreplace_peers=true\n",
			hex.EncodeToString(privateKeys[i][:]),
		)
		for _, remote := range topology[i] {
			fmt.Fprintf(
				&cfg,
				"public_key=%s\npreshared_key=%s\nprotocol_version=1\nreplace_allowed_ips=true\nallowed_ip=%s/32\n",
				hex.EncodeToString(publicKeys[remote]),
				hex.EncodeToString(psk[:]),
				peers[remote].ipv4,
			)
		}
		if err := peer.dev.IpcSet(cfg.String()); err != nil {
			t.Fatal(err)
		}
		if err := peer.dev.Up(); err != nil {
			t.Fatal(err)
		}
	}
	for i, peer := range peers {
		for _, remote := range topology[i] {
			cfg := fmt.Sprintf(
				"public_key=%s\nendpoint=127.0.0.1:%d\n",
				hex.EncodeToString(publicKeys[remote]),
				peers[remote].bind.Port(),
			)
			if err := peer.dev.IpcSet(cfg); err != nil {
				t.Fatal(err)
			}
		}
	}

	exchangeTUNPacket(
		t,
		peers[0].tun,
		peers[1].tun,
		testIPv4Packet(peers[0].ipv4, peers[1].ipv4, 128, 1),
	)
	exchangeTUNPacket(
		t,
		peers[0].tun,
		peers[2].tun,
		testIPv4Packet(peers[0].ipv4, peers[2].ipv4, 128, 2),
	)
	exchangeTUNPacket(
		t,
		peers[1].tun,
		peers[0].tun,
		testIPv4Packet(peers[1].ipv4, peers[0].ipv4, 128, 3),
	)
	exchangeTUNPacket(
		t,
		peers[2].tun,
		peers[0].tun,
		testIPv4Packet(peers[2].ipv4, peers[0].ipv4, 128, 4),
	)

	for _, peer := range peers {
		assertFullTransportUsed(t, peer.bind)
	}
}

func newBehaviorPair(
	t *testing.T,
	wireGuardPSKs [2][32]byte,
	obfsPSK []byte,
) [2]*behaviorPeer {
	t.Helper()
	var privateKeys [2][32]byte
	var publicKeys [2][]byte
	var peers [2]*behaviorPeer

	for i := range privateKeys {
		privateKeys[i] = randomTestKey(t)
		publicKey, err := curve25519.X25519(privateKeys[i][:], curve25519.Basepoint)
		if err != nil {
			t.Fatal(err)
		}
		publicKeys[i] = publicKey
		peers[i] = &behaviorPeer{
			tun:  tuntest.NewChannelTUN(),
			ipv4: netip.AddrFrom4([4]byte{10, 55, 0, byte(i + 1)}),
			ipv6: netip.AddrFrom16([16]byte{
				0xfd, 0x00, 0, 0, 0, 0, 0, 0,
				0, 0, 0, 0, 0, 0, 0, byte(i + 1),
			}),
		}
	}

	for i, peer := range peers {
		obfsKey, err := obfs.DeriveWireGuardKey(
			privateKeys[i][:],
			publicKeys[i^1],
			obfsPSK,
		)
		if err != nil {
			t.Fatal(err)
		}
		bindConfig := DefaultConfig()
		bindConfig.ObfsMode = "salamander"
		bindConfig.ObfsKeys = []obfs.Key{obfsKey}
		peer.bind = New(bindConfig)
		peer.dev = device.NewDevice(
			peer.tun.TUN(),
			peer.bind,
			device.NewLogger(device.LogLevelError, fmt.Sprintf("wgq-behavior-%d: ", i)),
		)
		t.Cleanup(peer.dev.Close)

		cfg := fmt.Sprintf(
			"private_key=%s\nlisten_port=0\nreplace_peers=true\npublic_key=%s\npreshared_key=%s\nprotocol_version=1\nreplace_allowed_ips=true\nallowed_ip=%s/32\nallowed_ip=%s/128\n",
			hex.EncodeToString(privateKeys[i][:]),
			hex.EncodeToString(publicKeys[i^1]),
			hex.EncodeToString(wireGuardPSKs[i][:]),
			peers[i^1].ipv4,
			peers[i^1].ipv6,
		)
		if err := peer.dev.IpcSet(cfg); err != nil {
			t.Fatal(err)
		}
		if err := peer.dev.Up(); err != nil {
			t.Fatal(err)
		}
	}
	for i, peer := range peers {
		cfg := fmt.Sprintf(
			"public_key=%s\nendpoint=127.0.0.1:%d\n",
			hex.EncodeToString(publicKeys[i^1]),
			peers[i^1].bind.Port(),
		)
		if err := peer.dev.IpcSet(cfg); err != nil {
			t.Fatal(err)
		}
	}
	return peers
}

func randomTestKey(t *testing.T) [32]byte {
	t.Helper()
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	return key
}

func testIPv4Packet(src, dst netip.Addr, payloadSize int, tag uint16) []byte {
	const headerSize = 20
	packet := make([]byte, headerSize+payloadSize)
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	binary.BigEndian.PutUint16(packet[4:6], tag)
	packet[8] = 64
	packet[9] = 253
	src4 := src.As4()
	dst4 := dst.As4()
	copy(packet[12:16], src4[:])
	copy(packet[16:20], dst4[:])
	for i := headerSize; i < len(packet); i++ {
		packet[i] = byte(int(tag) + i)
	}
	binary.BigEndian.PutUint16(packet[10:12], internetChecksum(packet[:headerSize]))
	return packet
}

func testIPv6Packet(src, dst netip.Addr, payloadSize int, tag uint16) []byte {
	const headerSize = 40
	packet := make([]byte, headerSize+payloadSize)
	packet[0] = 0x60
	binary.BigEndian.PutUint16(packet[4:6], uint16(payloadSize))
	packet[6] = 59
	packet[7] = 64
	src16 := src.As16()
	dst16 := dst.As16()
	copy(packet[8:24], src16[:])
	copy(packet[24:40], dst16[:])
	for i := headerSize; i < len(packet); i++ {
		packet[i] = byte(int(tag) + i)
	}
	return packet
}

func internetChecksum(packet []byte) uint16 {
	var sum uint32
	for len(packet) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(packet))
		packet = packet[2:]
	}
	if len(packet) == 1 {
		sum += uint32(packet[0]) << 8
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}

func exchangeTUNPacket(
	t *testing.T,
	source, destination *tuntest.ChannelTUN,
	packet []byte,
) {
	t.Helper()
	injectTUNPacket(t, source, packet)
	select {
	case got := <-destination.Inbound:
		if !bytes.Equal(got, packet) {
			t.Fatal("WireGuard plaintext changed across ArmorBind")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for WireGuard plaintext")
	}
}

func injectTUNPacket(t *testing.T, source *tuntest.ChannelTUN, packet []byte) {
	t.Helper()
	select {
	case source.Outbound <- packet:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out injecting packet into TUN")
	}
}

func expectNoTUNPacket(t *testing.T, destination *tuntest.ChannelTUN, wait time.Duration) {
	t.Helper()
	select {
	case packet := <-destination.Inbound:
		t.Fatalf("unexpected WireGuard plaintext delivery: %x", packet)
	case <-time.After(wait):
	}
}

func sendTUNBatch(tun *tuntest.ChannelTUN, packets [][]byte, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	pacer := time.NewTicker(2 * time.Millisecond)
	defer pacer.Stop()
	for i, packet := range packets {
		if i != 0 {
			select {
			case <-pacer.C:
			case <-timer.C:
				return fmt.Errorf("timed out pacing packets")
			}
		}
		select {
		case tun.Outbound <- packet:
		case <-timer.C:
			return fmt.Errorf("timed out after sending packets")
		}
	}
	return nil
}

func receiveTUNBatch(
	tun *tuntest.ChannelTUN,
	count int,
	timeout time.Duration,
) ([][]byte, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	packets := make([][]byte, 0, count)
	for len(packets) < count {
		select {
		case packet := <-tun.Inbound:
			packets = append(packets, packet)
		case <-timer.C:
			return nil, fmt.Errorf("timed out after receiving %d of %d packets", len(packets), count)
		}
	}
	return packets, nil
}

func assertPacketSet(t *testing.T, got, want [][]byte) {
	t.Helper()
	counts := make(map[string]int, len(want))
	for _, packet := range want {
		counts[string(packet)]++
	}
	for _, packet := range got {
		key := string(packet)
		if counts[key] == 0 {
			t.Fatalf("received unexpected or duplicate packet: %x", packet)
		}
		counts[key]--
	}
	for packet, count := range counts {
		if count != 0 {
			t.Fatalf("packet delivered %d fewer times than expected: %x", count, []byte(packet))
		}
	}
}
