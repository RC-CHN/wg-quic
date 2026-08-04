package core

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/platform"
)

type RunOptions struct {
	Name   string
	FwMark *uint32
	Debug  bool
}

// Run starts only the userspace data plane. It does not assign addresses,
// install routes or DNS, execute hooks, or invoke a service manager.
func Run(ctx context.Context, configPath string, options RunOptions) error {
	cfg, err := config.ParseFile(configPath)
	if err != nil {
		return err
	}
	if options.FwMark != nil {
		cfg.Interface.FwMark = *options.FwMark
	}
	name := InterfaceName(configPath, options.Name)
	instance, err := newInstance(cfg, name, platform.Current(), options.Debug)
	if err != nil {
		return err
	}
	defer instance.Close()
	if options.Debug {
		logDebugConfiguration(configPath, name, cfg)
	}
	if err := instance.Up(ctx); err != nil {
		return err
	}
	log.Printf("wg-quic core interface %s is up on UDP port %d", name, instance.ListenPort())
	if options.Debug {
		go logDebugStats(ctx, name, instance)
	}
	return instance.Wait(ctx)
}

func logDebugConfiguration(configPath, name string, cfg *config.Config) {
	log.Printf(
		"debug core configuration: interface=%q config=%q listen_port=%d mtu=%d fwmark=%d carrier=%q congestion=%q fec=%q obfs=%q peers=%d",
		name, configPath, cfg.Interface.ListenPort, InterfaceMTU(cfg), cfg.Interface.FwMark,
		cfg.Transport.Carrier, cfg.Transport.Congestion, cfg.Transport.FEC, cfg.Transport.Obfs,
		len(cfg.Peers),
	)
	for index, peer := range cfg.Peers {
		log.Printf(
			"debug peer %d: endpoint=%q allowed_ips=%v persistent_keepalive=%d preshared_key_present=%t",
			index+1, peer.Endpoint, peer.AllowedIPs, peer.PersistentKeepalive, peer.PresharedKey != "",
		)
	}
}

func logDebugStats(ctx context.Context, name string, instance *Instance) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats := instance.Stats()
			log.Printf(
				"debug stats %s: wg_tx=%d/%dB wg_rx=%d/%dB wire_tx=%d/%dB wire_rx=%d/%dB sessions=%d queue_drops=%d fec_data=%d fec_parity=%d fec_lost=%d fec_recovered=%d fec_unrecovered=%d",
				name,
				stats.WGTxPackets, stats.WGTxBytes, stats.WGRxPackets, stats.WGRxBytes,
				stats.WireTxPackets, stats.WireTxBytes, stats.WireRxPackets, stats.WireRxBytes,
				stats.ActiveSessions, stats.QueueDrops, stats.FECDataTx, stats.FECParityTx,
				stats.FECRawLost, stats.FECRecovered, stats.FECUnrecovered,
			)
		}
	}
}

func InterfaceName(configPath, requestedName string) string {
	if requestedName != "" {
		return requestedName
	}
	return strings.TrimSuffix(filepath.Base(configPath), filepath.Ext(configPath))
}
