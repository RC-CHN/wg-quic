package armorbind

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/device"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun/tuntest"
	"golang.org/x/crypto/curve25519"
)

// BenchmarkTransportThroughput pumps full-size inner packets through two
// in-process WireGuard devices joined by the real QUIC carrier over loopback
// UDP. It exists for CPU profiling (-cpuprofile) and relative before/after
// comparisons; the container fixture remains the absolute reference.
func BenchmarkTransportThroughput(b *testing.B) {
	for _, mode := range []string{"salamander", "none"} {
		b.Run(mode, func(b *testing.B) {
			benchmarkThroughput(b, mode)
		})
	}
}

func benchmarkThroughput(b *testing.B, obfsMode string) {
	var privateKeys [2][32]byte
	var publicKeys [2][]byte
	var presharedKey [32]byte
	if _, err := rand.Read(presharedKey[:]); err != nil {
		b.Fatal(err)
	}
	for i := range privateKeys {
		if _, err := rand.Read(privateKeys[i][:]); err != nil {
			b.Fatal(err)
		}
		public, err := curve25519.X25519(privateKeys[i][:], curve25519.Basepoint)
		if err != nil {
			b.Fatal(err)
		}
		publicKeys[i] = public
	}

	var peers [2]testDevice
	for i := range peers {
		peers[i].ip = netip.AddrFrom4([4]byte{10, 66, 0, byte(i + 1)})
	}
	for i := range peers {
		bindConfig := DefaultConfig()
		bindConfig.ObfsMode = obfsMode
		if obfsMode == "salamander" {
			obfsKey, err := obfs.DeriveWireGuardKey(privateKeys[i][:], publicKeys[i^1], presharedKey[:])
			if err != nil {
				b.Fatal(err)
			}
			bindConfig.ObfsKeys = []obfs.Key{obfsKey}
		}
		peers[i].bind = New(bindConfig)
		peers[i].tun = tuntest.NewChannelTUN()
		peers[i].dev = device.NewDeviceWithOptions(
			peers[i].tun.TUN(),
			peers[i].bind,
			device.NewLogger(device.LogLevelError, fmt.Sprintf("wgq-bench-%d: ", i)),
			device.Options{DisableTUNEventStateTransitions: true},
		)
		cfg := fmt.Sprintf(
			"private_key=%s\nlisten_port=0\nreplace_peers=true\npublic_key=%s\npreshared_key=%s\nprotocol_version=1\nreplace_allowed_ips=true\nallowed_ip=%s/32\n",
			hex.EncodeToString(privateKeys[i][:]),
			hex.EncodeToString(publicKeys[i^1]),
			hex.EncodeToString(presharedKey[:]),
			peers[i^1].ip,
		)
		if err := peers[i].dev.IpcSet(cfg); err != nil {
			b.Fatal(err)
		}
		if err := peers[i].dev.Up(); err != nil {
			b.Fatal(err)
		}
		b.Cleanup(peers[i].dev.Close)
	}
	for i := range peers {
		cfg := fmt.Sprintf(
			"public_key=%s\nendpoint=127.0.0.1:%d\n",
			hex.EncodeToString(publicKeys[i^1]),
			peers[i^1].bind.Port(),
		)
		if err := peers[i].dev.IpcSet(cfg); err != nil {
			b.Fatal(err)
		}
	}

	// Warmup: one packet through the full path so the timed section starts
	// with an established session and keypair.
	warmup := tuntest.Ping(peers[1].ip, peers[0].ip)
	peers[0].tun.Outbound <- warmup
	select {
	case <-peers[1].tun.Inbound:
	case <-time.After(5 * time.Second):
		b.Fatal("warmup packet did not traverse the transport")
	}

	const innerSize = 1280
	packet := make([]byte, innerSize)
	packet[0] = 0x45 // IPv4, IHL=5
	binary.BigEndian.PutUint16(packet[2:4], uint16(innerSize))
	packet[8] = 64   // TTL
	packet[9] = 0x11 // UDP (contents irrelevant to the tunnel)
	// The source must match the sending peer's AllowedIPs or the receiver's
	// cryptokey routing check drops the packet after decryption.
	srcIP := peers[0].ip.As4()
	copy(packet[12:16], srcIP[:])
	dstIP := peers[1].ip.As4()
	copy(packet[16:20], dstIP[:])

	stop := make(chan struct{})
	var sent atomic.Int64
	go func() {
		for {
			select {
			case peers[0].tun.Outbound <- packet:
				sent.Add(1)
			case <-stop:
				return
			}
		}
	}()

	var received atomic.Int64
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for {
			select {
			case msg, ok := <-peers[1].tun.Inbound:
				if !ok {
					return
				}
				received.Add(int64(len(msg)))
			case <-stop:
				return
			}
		}
	}()

	const pump = 3 * time.Second
	time.Sleep(pump)
	close(stop)
	<-drainDone
	delivered := received.Load()
	mbps := float64(delivered) * 8 / pump.Seconds() / 1e6
	b.ReportMetric(mbps, "Mbps")
	b.Logf("mode=%s sent=%d packets delivered=%d bytes in %v", obfsMode, sent.Load(), delivered, pump)
}
