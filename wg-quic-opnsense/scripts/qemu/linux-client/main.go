package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/netip"
	"os"
	"sync/atomic"
	"time"

	armorbind "github.com/RC-CHN/wg-quic/internal/bind"
	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
	"github.com/RC-CHN/wg-quic/internal/wgdevice"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/device"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun/netstack"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

func decodeKey(value string) ([]byte, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("decoded key length is %d, want 32", len(key))
	}
	return key, nil
}

func transportConfig(cfg *config.Config) (armorbind.Config, netip.AddrPort, error) {
	result := armorbind.DefaultConfig()
	result.CongestionMode = cfg.Transport.Congestion
	result.FECMode = cfg.Transport.FEC
	result.ObfsMode = cfg.Transport.Obfs
	if len(cfg.Peers) != 1 {
		return result, netip.AddrPort{}, errors.New("exactly one peer is required")
	}
	endpoint, err := netip.ParseAddrPort(cfg.Peers[0].Endpoint)
	if err != nil {
		return result, endpoint, err
	}
	if result.ObfsMode == "none" {
		return result, endpoint, nil
	}
	privateKey, err := decodeKey(cfg.Interface.PrivateKey)
	if err != nil {
		return result, endpoint, err
	}
	publicKey, err := decodeKey(cfg.Peers[0].PublicKey)
	if err != nil {
		return result, endpoint, err
	}
	var presharedKey []byte
	if cfg.Peers[0].PresharedKey != "" {
		presharedKey, err = decodeKey(cfg.Peers[0].PresharedKey)
		if err != nil {
			return result, endpoint, err
		}
	}
	key, err := obfs.DeriveWireGuardKey(privateKey, publicKey, presharedKey)
	if err != nil {
		return result, endpoint, err
	}
	result.ObfsKeys = []obfs.Key{key}
	return result, endpoint, nil
}

func fieldValue(status, name string) (int64, error) {
	for _, line := range bytes.Split([]byte(status), []byte{'\n'}) {
		key, value, found := bytes.Cut(line, []byte{'='})
		if found && string(key) == name {
			var parsed int64
			if _, err := fmt.Sscanf(string(value), "%d", &parsed); err != nil {
				return 0, err
			}
			return parsed, nil
		}
	}
	return 0, fmt.Errorf("%s was not present in device status", name)
}

func holdAfterSuccess(hold time.Duration, sleep func(time.Duration)) error {
	if hold < 0 {
		return errors.New("hold duration must not be negative")
	}
	if hold > 0 {
		sleep(hold)
	}
	return nil
}

type runOptions struct {
	hold                       time.Duration
	recheckAfter               time.Duration
	requireAutonomousReconnect bool
	recoveryTimeout            time.Duration
}

type peerStatus struct {
	handshake int64
	txBytes   int64
	rxBytes   int64
}

func readPeerStatus(wireguard *device.Device) (peerStatus, error) {
	status, err := wireguard.IpcGet()
	if err != nil {
		return peerStatus{}, err
	}
	result := peerStatus{}
	result.handshake, err = fieldValue(status, "last_handshake_time_sec")
	if err != nil {
		return peerStatus{}, err
	}
	result.txBytes, err = fieldValue(status, "tx_bytes")
	if err != nil {
		return peerStatus{}, err
	}
	result.rxBytes, err = fieldValue(status, "rx_bytes")
	if err != nil {
		return peerStatus{}, err
	}
	return result, nil
}

func ping(network *netstack.Net, address string) (time.Duration, error) {
	socket, err := network.Dial("ping4", address)
	if err != nil {
		return 0, err
	}
	defer socket.Close()

	echo := icmp.Echo{
		ID:   os.Getpid() & 0xffff,
		Seq:  rand.IntN(1 << 16),
		Data: []byte("wg-quic Linux netstack interoperability"),
	}
	request, err := (&icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Body: &echo,
	}).Marshal(nil)
	if err != nil {
		return 0, err
	}

	buffer := make([]byte, 1500)
	for attempt := 0; attempt < 5; attempt++ {
		started := time.Now()
		if _, err := socket.Write(request); err != nil {
			return 0, err
		}
		if err := socket.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return 0, err
		}
		size, readErr := socket.Read(buffer)
		if readErr != nil {
			continue
		}
		reply, err := icmp.ParseMessage(1, buffer[:size])
		if err != nil {
			return 0, err
		}
		replyEcho, ok := reply.Body.(*icmp.Echo)
		if !ok || reply.Type != ipv4.ICMPTypeEchoReply ||
			replyEcho.Seq != echo.Seq || !bytes.Equal(replyEcho.Data, echo.Data) {
			return 0, fmt.Errorf("unexpected ICMP reply: %#v", reply)
		}
		return time.Since(started), nil
	}
	return 0, errors.New("no ICMP reply was received")
}

func run(configPath string, options runOptions) error {
	cfg, err := config.ParseFile(configPath)
	if err != nil {
		return err
	}
	if len(cfg.Interface.Addresses) != 1 || len(cfg.Peers) != 1 {
		return errors.New("one interface address and one peer are required")
	}
	bindConfig, endpoint, err := transportConfig(cfg)
	if err != nil {
		return err
	}
	bind := armorbind.New(bindConfig)
	var activeDevice atomic.Pointer[device.Device]
	var restoredSessions atomic.Uint64
	bind.SetSessionRestored(func(netip.AddrPort) {
		wireguard := activeDevice.Load()
		if wireguard == nil {
			return
		}
		if err := wgdevice.ProbePeer(wireguard, cfg.Peers[0].PublicKey); err != nil {
			log.Printf("probe peer after automatic reconnect: %v", err)
			return
		}
		restoredSessions.Add(1)
	})
	if len(bindConfig.ObfsKeys) == 1 {
		release, err := bind.AcquireEndpointKey(endpoint, bindConfig.ObfsKeys[0])
		if err != nil {
			return err
		}
		defer release()
	}

	tunDevice, network, err := netstack.CreateNetTUN(
		[]netip.Addr{cfg.Interface.Addresses[0].Addr()},
		nil,
		cfg.EffectiveMTU(),
	)
	if err != nil {
		return err
	}
	wireguard := device.NewDevice(
		tunDevice,
		bind,
		device.NewLogger(device.LogLevelError, "wg-quic-netstack: "),
	)
	activeDevice.Store(wireguard)
	defer wireguard.Close()

	uapi, err := cfg.UAPI()
	if err != nil {
		return err
	}
	if err := wireguard.IpcSet(uapi); err != nil {
		return err
	}
	if err := wireguard.Up(); err != nil {
		return err
	}

	pingAddress := cfg.Peers[0].AllowedIPs[0].Addr().String()
	latency, err := ping(network, pingAddress)
	if err != nil {
		status, _ := wireguard.IpcGet()
		handshake, _ := fieldValue(status, "last_handshake_time_sec")
		txBytes, _ := fieldValue(status, "tx_bytes")
		rxBytes, _ := fieldValue(status, "rx_bytes")
		return fmt.Errorf(
			"initial ICMP failed: %w: handshake=%d tx=%d rx=%d transport=%+v",
			err,
			handshake,
			txBytes,
			rxBytes,
			bind.Stats(),
		)
	}
	initial, err := readPeerStatus(wireguard)
	if err != nil || initial.handshake <= 0 || initial.txBytes <= 0 || initial.rxBytes <= 0 {
		return fmt.Errorf(
			"invalid WireGuard status: handshake=%d tx=%d rx=%d: %w",
			initial.handshake,
			initial.txBytes,
			initial.rxBytes,
			err,
		)
	}
	stats := bind.Stats()
	if stats.WireTxBytes == 0 || stats.WireRxBytes == 0 {
		return fmt.Errorf("outer transport counters did not advance: %+v", stats)
	}

	fmt.Printf(
		"HOST INTEROP PASSED: Linux netstack <-> peer %s, ping=%s, tx=%d, rx=%d\n",
		pingAddress,
		latency.Round(time.Microsecond),
		initial.txBytes,
		initial.rxBytes,
	)

	if options.recheckAfter > 0 {
		time.Sleep(options.recheckAfter)
		if options.requireAutonomousReconnect {
			deadline := time.Now().Add(options.recoveryTimeout)
			for {
				current, statusErr := readPeerStatus(wireguard)
				reconnect := bind.EndpointReconnectStatus(endpoint)
				if statusErr == nil && reconnect.Attempts > 0 &&
					bind.EndpointSessionState(endpoint) == armorbind.EndpointSessionEstablished &&
					current.handshake > initial.handshake && restoredSessions.Load() > 0 {
					break
				}
				if time.Now().After(deadline) {
					return fmt.Errorf(
						"transport did not autonomously recover while idle: state=%s attempts=%d failures=%d restored=%d handshake=%d initial_handshake=%d status_error=%v",
						bind.EndpointSessionState(endpoint),
						reconnect.Attempts,
						reconnect.Failures,
						restoredSessions.Load(),
						current.handshake,
						initial.handshake,
						statusErr,
					)
				}
				time.Sleep(100 * time.Millisecond)
			}
		}

		recheckLatency, err := ping(network, pingAddress)
		if err != nil {
			return fmt.Errorf("ICMP recheck failed after outer path change: %w", err)
		}
		current, err := readPeerStatus(wireguard)
		if err != nil {
			return err
		}
		fmt.Printf(
			"HOST INTEROP RECHECK PASSED: ping=%s handshake=%d reconnect_attempts=%d restored_sessions=%d tx=%d rx=%d\n",
			recheckLatency.Round(time.Microsecond),
			current.handshake,
			bind.EndpointReconnectStatus(endpoint).Attempts,
			restoredSessions.Load(),
			current.txBytes,
			current.rxBytes,
		)
	}

	if options.hold > 0 {
		fmt.Printf("HOST INTEROP HOLDING: keeping the peer online for %s\n", options.hold)
	}
	return holdAfterSuccess(options.hold, time.Sleep)
}

func main() {
	configPath := flag.String("config", "", "path to the Linux wg-quic profile")
	hold := flag.Duration("hold", 0, "keep the validated peer online for this duration")
	recheckAfter := flag.Duration("recheck-after", 0, "stay idle, then require another tunnel ping")
	requireAutonomousReconnect := flag.Bool(
		"require-autonomous-reconnect",
		false,
		"before the recheck, require a maintained QUIC redial and a newer WireGuard handshake",
	)
	recoveryTimeout := flag.Duration(
		"recovery-timeout",
		30*time.Second,
		"maximum additional wait for autonomous recovery",
	)
	flag.Parse()
	if *configPath == "" {
		log.Fatal("-config is required")
	}
	if *hold < 0 {
		log.Fatal("-hold must not be negative")
	}
	if *recheckAfter < 0 {
		log.Fatal("-recheck-after must not be negative")
	}
	if *requireAutonomousReconnect && *recheckAfter == 0 {
		log.Fatal("-require-autonomous-reconnect requires -recheck-after")
	}
	if *recoveryTimeout <= 0 {
		log.Fatal("-recovery-timeout must be positive")
	}
	if err := run(*configPath, runOptions{
		hold:                       *hold,
		recheckAfter:               *recheckAfter,
		requireAutonomousReconnect: *requireAutonomousReconnect,
		recoveryTimeout:            *recoveryTimeout,
	}); err != nil {
		log.Fatal(err)
	}
}
