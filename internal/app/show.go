package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/platformenv"
)

func Show(name string, jsonOutput bool) error {
	host := platformenv.Paths{}
	var paths []string
	if name != "" {
		if err := host.ValidateInterfaceName(name); err != nil {
			return err
		}
		paths = []string{host.ControlPath(name)}
	} else {
		var err error
		paths, err = host.ControlPaths()
		if err != nil {
			return err
		}
		sort.Strings(paths)
		if len(paths) == 0 {
			return errors.New("no running wg-quic interfaces")
		}
	}
	statuses := make([]control.Status, 0, len(paths))
	readErrors := make([]error, 0)
	for _, path := range paths {
		status, err := control.Read(path)
		if err != nil {
			if name != "" {
				return namedStatusReadError(name, path, err)
			}
			readErrors = append(readErrors, fmt.Errorf("%s: %w", path, err))
			continue
		}
		statuses = append(statuses, status)
	}
	if len(statuses) == 0 {
		if len(readErrors) != 0 {
			return fmt.Errorf(
				"no running wg-quic interfaces: %w",
				errors.Join(readErrors...),
			)
		}
		return errors.New("no running wg-quic interfaces")
	}
	if jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if name != "" {
			return encoder.Encode(statuses[0])
		}
		return encoder.Encode(statuses)
	}
	for i, status := range statuses {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("interface: %s\n", status.Interface)
		fmt.Printf("  state: %s\n", status.State)
		fmt.Printf("  listener: udp/%d\n", status.ListenPort)
		fmt.Printf("  carrier: %s\n", status.Carrier)
		fmt.Printf("  fec: %s\n", status.FECMode)
		fmt.Printf("  obfs: %s\n", status.ObfsMode)
		fmt.Printf("  sessions: %d\n", status.Stats.ActiveSessions)
		fmt.Printf("  WireGuard: tx %d packets/%d bytes, rx %d packets/%d bytes\n",
			status.Stats.WGTxPackets, status.Stats.WGTxBytes, status.Stats.WGRxPackets, status.Stats.WGRxBytes)
		fmt.Printf("  FEC: parity %d, raw lost %d, recovered %d, unrecovered %d\n",
			status.Stats.FECParityTx, status.Stats.FECRawLost, status.Stats.FECRecovered, status.Stats.FECUnrecovered)
		for _, peer := range status.Peers {
			fmt.Printf("  peer: %s\n", peer.PublicKey)
			if peer.Endpoint != "" {
				fmt.Printf("    endpoint: %s\n", peer.Endpoint)
			}
			fmt.Printf("    session: %s\n", peer.Session)
			if peer.LatestHandshake != 0 {
				fmt.Printf(
					"    latest handshake: %s\n",
					time.Unix(peer.LatestHandshake, 0).Format(time.RFC3339),
				)
			}
			fmt.Printf(
				"    transfer: tx %d bytes, rx %d bytes\n",
				peer.TransferTx,
				peer.TransferRx,
			)
		}
	}
	return nil
}

func namedStatusReadError(name, path string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf(
			"interface is inactive: %q: %s: %w", name, path, err,
		)
	}
	return fmt.Errorf("%s: %w", path, err)
}
