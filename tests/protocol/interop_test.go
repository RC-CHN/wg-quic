package protocol_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bind "github.com/RC-CHN/wg-quic/internal/bind"
	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/device"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun/tuntest"
	"golang.org/x/crypto/curve25519"
)

// The peer under test is a separate Rust process using standard Quinn/Rustls
// and its own implementation of the standalone document, with no Go imports.
func TestIndependentRustInterop(t *testing.T) {
	binary := os.Getenv("WG_QUIC_RUST_INTEROP")
	if binary == "" {
		t.Skip("set WG_QUIC_RUST_INTEROP to the compiled independent Rust probe")
	}
	for _, initiator := range []bool{true, false} {
		for _, mode := range []struct {
			name, obfs, fec string
			psk, drop       bool
		}{
			{"plain", "none", "off", false, false},
			{"default", "salamander", "auto", false, false},
			{"psk_fec_recovery", "salamander", "auto", true, true},
		} {
			role := "rust_responder"
			if initiator {
				role = "rust_initiator"
			}
			t.Run(role+"/"+mode.name, func(t *testing.T) {
				var goSecret, rustSecret, psk [32]byte
				for _, key := range []*[32]byte{&goSecret, &rustSecret, &psk} {
					if _, err := rand.Read(key[:]); err != nil {
						t.Fatal(err)
					}
				}
				goPublic, err := curve25519.X25519(goSecret[:], curve25519.Basepoint)
				if err != nil {
					t.Fatal(err)
				}
				rustPublic, err := curve25519.X25519(rustSecret[:], curve25519.Basepoint)
				if err != nil {
					t.Fatal(err)
				}
				cfg := bind.DefaultConfig()
				cfg.ObfsMode = mode.obfs
				cfg.FECMode = mode.fec
				if mode.obfs == "salamander" {
					var shared []byte
					if mode.psk {
						shared = psk[:]
					}
					key, err := obfs.DeriveWireGuardKey(goSecret[:], rustPublic, shared)
					if err != nil {
						t.Fatal(err)
					}
					cfg.ObfsKeys = []obfs.Key{key}
				}
				carrier := bind.New(cfg)
				tun := tuntest.NewChannelTUN()
				dev := device.NewDeviceWithOptions(tun.TUN(), carrier, device.NewLogger(device.LogLevelError, "go-interop: "), device.Options{DisableTUNEventStateTransitions: true})
				defer dev.Close()
				uapi := fmt.Sprintf("private_key=%x\nlisten_port=0\nreplace_peers=true\npublic_key=%x\nreplace_allowed_ips=true\nallowed_ip=10.200.0.2/32\n", goSecret, rustPublic)
				if mode.psk {
					uapi += fmt.Sprintf("preshared_key=%x\n", psk)
				}
				if err := dev.IpcSet(uapi); err != nil {
					t.Fatal(err)
				}
				if err := dev.Up(); err != nil {
					t.Fatal(err)
				}
				config := map[string]any{"private": hex.EncodeToString(rustSecret[:]), "peer": hex.EncodeToString(goPublic), "listen": "127.0.0.1:0", "remote": fmt.Sprintf("127.0.0.1:%d", carrier.Port()), "initiator": initiator, "obfs": mode.obfs, "drop_first_data": mode.drop}
				if mode.psk {
					config["psk"] = hex.EncodeToString(psk[:])
				}
				configPath := filepath.Join(t.TempDir(), "rust.json")
				encoded, _ := json.Marshal(config)
				if err := os.WriteFile(configPath, encoded, 0600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, configPath)
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = cmd.Process.Kill() }()
				events := make(chan map[string]any, 16)
				go func() {
					defer close(events)
					scanner := bufio.NewScanner(stdout)
					for scanner.Scan() {
						var event map[string]any
						if json.Unmarshal(scanner.Bytes(), &event) == nil {
							events <- event
						}
					}
				}()
				ready := <-events
				if ready["event"] != "ready" {
					t.Fatalf("Rust did not start: %s", stderr.String())
				}
				if !initiator {
					if err := dev.IpcSet(fmt.Sprintf("public_key=%x\nendpoint=127.0.0.1:%d\n", rustPublic, int(ready["port"].(float64)))); err != nil {
						t.Fatal(err)
					}
					select {
					case tun.Outbound <- goPacket():
					case <-ctx.Done():
						t.Fatal("Go injection timeout")
					}
				}
				gotInner := false
				var passed map[string]any
			loop:
				for {
					select {
					case packet := <-tun.Inbound:
						if len(packet) < 20 || !bytes.Equal(packet[12:16], []byte{10, 200, 0, 2}) || !bytes.Equal(packet[16:20], []byte{10, 200, 0, 1}) || string(packet[20:]) != "rust-to-go" {
							t.Fatalf("unexpected inner packet: length=%d", len(packet))
						}
						gotInner = true
						select {
						case tun.Outbound <- goPacket():
						case <-ctx.Done():
							t.Fatal("reply injection timeout")
						}
					case event, ok := <-events:
						if !ok {
							break loop
						}
						t.Log(event)
						if event["event"] == "passed" {
							passed = event
						}
					case <-ctx.Done():
						t.Fatal("Rust interoperability timed out")
					}
				}
				if err := cmd.Wait(); err != nil {
					t.Fatalf("Rust failed: %v\n%s", err, stderr.String())
				}
				if !gotInner || passed == nil {
					t.Fatalf("missing bidirectional authenticated delivery: inner=%v result=%v", gotInner, passed)
				}
				if mode.fec == "auto" {
					stats := carrier.Stats()
					if stats.FECDataTx == 0 || stats.FECParityTx == 0 || passed["fec_data"].(float64) == 0 || passed["fec_parity"].(float64) == 0 {
						t.Fatal("FEC was not exercised")
					}
				}
				if mode.drop && passed["fec_recovered"].(float64) < 1 {
					t.Fatal("injected FEC loss was not recovered")
				}
				status, err := dev.IpcGet()
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(status, "last_handshake_time_sec=") || strings.Contains(status, "last_handshake_time_sec=0\n") {
					t.Fatal("Go did not confirm WireGuard handshake")
				}
			})
		}
	}
}

func goPacket() []byte {
	// A full MTU packet exercises Go-to-Rust WGQ1 reassembly as well as the
	// deliberately small 64-byte fragments emitted by the Rust sender.
	p := make([]byte, 1280)
	p[0] = 0x45
	p[2] = 5
	p[3] = 0
	p[8] = 64
	p[9] = 253
	copy(p[12:16], []byte{10, 200, 0, 1})
	copy(p[16:20], []byte{10, 200, 0, 2})
	copy(p[20:], "go-to-rust")
	var sum uint32
	for i := 0; i < 20; i += 2 {
		sum += uint32(p[i])<<8 | uint32(p[i+1])
	}
	for sum > 65535 {
		sum = (sum & 65535) + (sum >> 16)
	}
	checksum := ^uint16(sum)
	p[10] = byte(checksum >> 8)
	p[11] = byte(checksum)
	return p
}
