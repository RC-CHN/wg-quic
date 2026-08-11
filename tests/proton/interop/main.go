// Command interop exercises the complete wg-quic userspace data plane without
// requiring a kernel TUN driver. It is intentionally built once for Linux and
// once for Windows so Proton can verify cross-OS wire compatibility.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"runtime"
	"time"

	armorbind "github.com/RC-CHN/wg-quic/internal/bind"
	"github.com/RC-CHN/wg-quic/internal/telemetry"
	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/device"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun/tuntest"
	"golang.org/x/crypto/curve25519"
)

const (
	defaultPackets = 64
	packetTimeout  = 10 * time.Second
	statsTimeout   = 3 * time.Second
)

var peerIPs = [2]netip.Addr{
	netip.MustParseAddr("10.254.77.1"),
	netip.MustParseAddr("10.254.77.2"),
}

type testPeer struct {
	bind *armorbind.Bind
	tun  *tuntest.ChannelTUN
	dev  *device.Device
}

type result struct {
	Role         string          `json:"role"`
	GOOS         string          `json:"goos"`
	GOARCH       string          `json:"goarch"`
	Packets      int             `json:"packets"`
	BytesChecked int             `json:"bytes_checked"`
	DurationMS   int64           `json:"duration_ms"`
	FEC          string          `json:"fec"`
	Obfs         string          `json:"obfs"`
	Congestion   string          `json:"congestion"`
	SocketCompat string          `json:"socket_compat,omitempty"`
	Stats        telemetry.Stats `json:"stats"`
}

var socketCompatibility string

func main() {
	var (
		role         = flag.String("role", "", "server or client")
		listenPort   = flag.Int("listen-port", 0, "UDP listen port (zero chooses a free port)")
		peerEndpoint = flag.String("peer-endpoint", "", "peer endpoint; required for the client")
		readyFile    = flag.String("ready-file", "", "write the selected listen port here when ready")
		packets      = flag.Int("packets", defaultPackets, "number of packets to verify in each direction")
	)
	flag.Parse()

	if err := run(*role, *listenPort, *peerEndpoint, *readyFile, *packets); err != nil {
		fmt.Fprintf(os.Stderr, "wg-quic Proton interop failed: %v\n", err)
		os.Exit(1)
	}
}

func run(role string, listenPort int, peerEndpoint, readyFile string, packets int) error {
	roleIndex := -1
	switch role {
	case "server":
		roleIndex = 0
	case "client":
		roleIndex = 1
	default:
		return errors.New("-role must be server or client")
	}
	if listenPort < 0 || listenPort > 65535 {
		return fmt.Errorf("invalid listen port %d", listenPort)
	}
	if role == "client" && peerEndpoint == "" {
		return errors.New("-peer-endpoint is required for the client")
	}
	if packets <= 0 || packets > 4096 {
		return fmt.Errorf("packet count %d is outside 1..4096", packets)
	}

	peer, err := newTestPeer(roleIndex, uint16(listenPort), peerEndpoint)
	if err != nil {
		return err
	}
	defer peer.dev.Close()

	if readyFile != "" {
		if err := os.WriteFile(readyFile, []byte(fmt.Sprintf("%d\n", peer.bind.Port())), 0o600); err != nil {
			return fmt.Errorf("write ready file: %w", err)
		}
	}

	started := time.Now()
	bytesChecked := 0
	if role == "server" {
		bytesChecked, err = runServer(peer, packets)
	} else {
		bytesChecked, err = runClient(peer, packets)
	}
	if err != nil {
		return err
	}

	stats, err := waitForFullTransport(peer.bind)
	if err != nil {
		return err
	}
	output := result{
		Role: role, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Packets: packets, BytesChecked: bytesChecked,
		DurationMS: time.Since(started).Milliseconds(),
		FEC:        "auto", Obfs: "salamander", Congestion: "model",
		SocketCompat: socketCompatibility, Stats: stats,
	}
	encoded, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	fmt.Println(string(encoded))
	return nil
}

func newTestPeer(index int, listenPort uint16, peerEndpoint string) (*testPeer, error) {
	privateKeys := [2][32]byte{
		sha256.Sum256([]byte("wg-quic/proton-interop/server-private/v1")),
		sha256.Sum256([]byte("wg-quic/proton-interop/client-private/v1")),
	}
	presharedKey := sha256.Sum256([]byte("wg-quic/proton-interop/preshared/v1"))
	var publicKeys [2][]byte
	for i := range privateKeys {
		public, err := curve25519.X25519(privateKeys[i][:], curve25519.Basepoint)
		if err != nil {
			return nil, fmt.Errorf("derive public key %d: %w", i, err)
		}
		publicKeys[i] = public
	}
	remote := index ^ 1
	obfsKey, err := obfs.DeriveWireGuardKey(privateKeys[index][:], publicKeys[remote], presharedKey[:])
	if err != nil {
		return nil, fmt.Errorf("derive Salamander key: %w", err)
	}

	bindConfig := armorbind.DefaultConfig()
	bindConfig.FECMode = "auto"
	bindConfig.ObfsMode = "salamander"
	bindConfig.ObfsKeys = []obfs.Key{obfsKey}
	bind := armorbind.New(bindConfig)
	tun := tuntest.NewChannelTUN()
	dev := device.NewDevice(
		tun.TUN(),
		bind,
		device.NewLogger(device.LogLevelError, fmt.Sprintf("interop-%d: ", index)),
	)

	configuration := fmt.Sprintf(
		"private_key=%s\nlisten_port=%d\nreplace_peers=true\npublic_key=%s\npreshared_key=%s\nprotocol_version=1\nreplace_allowed_ips=true\nallowed_ip=%s/32\n",
		hex.EncodeToString(privateKeys[index][:]),
		listenPort,
		hex.EncodeToString(publicKeys[remote]),
		hex.EncodeToString(presharedKey[:]),
		peerIPs[remote],
	)
	if peerEndpoint != "" {
		configuration += "endpoint=" + peerEndpoint + "\npersistent_keepalive_interval=1\n"
	}
	if err := dev.IpcSet(configuration); err != nil {
		dev.Close()
		return nil, fmt.Errorf("configure WireGuard device: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("bring WireGuard device up: %w", err)
	}
	return &testPeer{bind: bind, tun: tun, dev: dev}, nil
}

func runServer(peer *testPeer, packets int) (int, error) {
	bytesChecked := 0
	for sequence := 0; sequence < packets; sequence++ {
		expected := packet(peerIPs[0], peerIPs[1], sequence)
		got, err := receive(peer.tun.Inbound)
		if err != nil {
			return bytesChecked, fmt.Errorf("receive request %d: %w", sequence, err)
		}
		if !bytes.Equal(got, expected) {
			return bytesChecked, fmt.Errorf("request %d changed across the Windows-to-Linux data path", sequence)
		}
		bytesChecked += len(got)

		reply := packet(peerIPs[1], peerIPs[0], sequence)
		if err := send(peer.tun.Outbound, reply); err != nil {
			return bytesChecked, fmt.Errorf("send reply %d: %w", sequence, err)
		}
		bytesChecked += len(reply)
	}
	return bytesChecked, nil
}

func runClient(peer *testPeer, packets int) (int, error) {
	bytesChecked := 0
	for sequence := 0; sequence < packets; sequence++ {
		request := packet(peerIPs[0], peerIPs[1], sequence)
		if err := send(peer.tun.Outbound, request); err != nil {
			return bytesChecked, fmt.Errorf("send request %d: %w", sequence, err)
		}
		bytesChecked += len(request)

		expected := packet(peerIPs[1], peerIPs[0], sequence)
		got, err := receive(peer.tun.Inbound)
		if err != nil {
			return bytesChecked, fmt.Errorf("receive reply %d: %w", sequence, err)
		}
		if !bytes.Equal(got, expected) {
			return bytesChecked, fmt.Errorf("reply %d changed across the Linux-to-Windows data path", sequence)
		}
		bytesChecked += len(got)
	}
	return bytesChecked, nil
}

func packet(destination, source netip.Addr, sequence int) []byte {
	packet := tuntest.Ping(destination, source)
	binary.BigEndian.PutUint16(packet[len(packet)-2:], uint16(sequence))
	return packet
}

func send(channel chan<- []byte, packet []byte) error {
	select {
	case channel <- packet:
		return nil
	case <-time.After(packetTimeout):
		return errors.New("timed out injecting an inner IP packet")
	}
}

func receive(channel <-chan []byte) ([]byte, error) {
	select {
	case packet, ok := <-channel:
		if !ok {
			return nil, errors.New("in-memory TUN closed")
		}
		return packet, nil
	case <-time.After(packetTimeout):
		return nil, errors.New("timed out waiting for an inner IP packet")
	}
}

func waitForFullTransport(bind *armorbind.Bind) (telemetry.Stats, error) {
	deadline := time.Now().Add(statsTimeout)
	for {
		stats := bind.Stats()
		if stats.WGTxPackets > 0 && stats.WGRxPackets > 0 &&
			stats.WireTxPackets > 0 && stats.WireRxPackets > 0 &&
			stats.FECDataTx > 0 && stats.FECParityTx > 0 &&
			stats.QUICPacketsAcked > 0 && stats.ActiveSessions > 0 {
			return stats, nil
		}
		if time.Now().After(deadline) {
			return stats, fmt.Errorf("full WireGuard/QUIC/FEC path was not observed: %+v", stats)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
