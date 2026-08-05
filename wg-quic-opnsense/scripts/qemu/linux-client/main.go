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
	"time"

	armorbind "github.com/RC-CHN/wg-quic/internal/bind"
	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/transport/obfs"
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

func run(configPath string) error {
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
	socket, err := network.Dial("ping4", pingAddress)
	if err != nil {
		return err
	}
	defer socket.Close()

	echo := icmp.Echo{
		ID:   os.Getpid() & 0xffff,
		Seq:  rand.IntN(1 << 16),
		Data: []byte("wg-quic Linux host to OPNsense interoperability"),
	}
	request, err := (&icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Body: &echo,
	}).Marshal(nil)
	if err != nil {
		return err
	}

	buffer := make([]byte, 1500)
	var latency time.Duration
	var reply *icmp.Message
	for attempt := 0; attempt < 5; attempt++ {
		started := time.Now()
		if _, err := socket.Write(request); err != nil {
			return err
		}
		if err := socket.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}
		size, readErr := socket.Read(buffer)
		if readErr != nil {
			continue
		}
		reply, err = icmp.ParseMessage(1, buffer[:size])
		if err != nil {
			return err
		}
		latency = time.Since(started)
		break
	}
	if reply == nil {
		status, _ := wireguard.IpcGet()
		handshake, _ := fieldValue(status, "last_handshake_time_sec")
		txBytes, _ := fieldValue(status, "tx_bytes")
		rxBytes, _ := fieldValue(status, "rx_bytes")
		return fmt.Errorf(
			"no ICMP reply was received: handshake=%d tx=%d rx=%d transport=%+v",
			handshake,
			txBytes,
			rxBytes,
			bind.Stats(),
		)
	}
	replyEcho, ok := reply.Body.(*icmp.Echo)
	if !ok || reply.Type != ipv4.ICMPTypeEchoReply ||
		replyEcho.Seq != echo.Seq || !bytes.Equal(replyEcho.Data, echo.Data) {
		return fmt.Errorf("unexpected ICMP reply: %#v", reply)
	}

	status, err := wireguard.IpcGet()
	if err != nil {
		return err
	}
	handshake, handshakeErr := fieldValue(status, "last_handshake_time_sec")
	txBytes, txErr := fieldValue(status, "tx_bytes")
	rxBytes, rxErr := fieldValue(status, "rx_bytes")
	if handshakeErr != nil || txErr != nil || rxErr != nil ||
		handshake <= 0 || txBytes <= 0 || rxBytes <= 0 {
		return fmt.Errorf(
			"invalid WireGuard status: handshake=%d tx=%d rx=%d",
			handshake,
			txBytes,
			rxBytes,
		)
	}
	stats := bind.Stats()
	if stats.WireTxBytes == 0 || stats.WireRxBytes == 0 {
		return fmt.Errorf("outer transport counters did not advance: %+v", stats)
	}

	fmt.Printf(
		"HOST INTEROP PASSED: Linux netstack <-> OPNsense %s, ping=%s, tx=%d, rx=%d\n",
		pingAddress,
		latency.Round(time.Microsecond),
		txBytes,
		rxBytes,
	)
	return nil
}

func main() {
	configPath := flag.String("config", "", "path to the Linux wg-quic profile")
	flag.Parse()
	if *configPath == "" {
		log.Fatal("-config is required")
	}
	if err := run(*configPath); err != nil {
		log.Fatal(err)
	}
}
