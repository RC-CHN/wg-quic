package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/platform"
)

func Show(name string, jsonOutput bool) error {
	host := platform.Current()
	var paths []string
	if name != "" {
		if err := host.ValidateInterfaceName(name); err != nil {
			return err
		}
		paths = []string{host.ControlPath(name)}
	} else {
		pattern := filepath.Join(filepath.Dir(host.ControlPath("placeholder")), "*.sock")
		var err error
		paths, err = filepath.Glob(pattern)
		if err != nil {
			return err
		}
		sort.Strings(paths)
		if len(paths) == 0 {
			return errors.New("no running wg-quic interfaces")
		}
	}
	statuses := make([]control.Status, 0, len(paths))
	for _, path := range paths {
		status, err := control.Read(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		statuses = append(statuses, status)
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
	}
	return nil
}
