package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
)

const (
	endpointRouteLedgerSchemaVersion = 1
	maxEndpointRouteLedgerSize       = 1 << 20
	maxEndpointRouteLedgerRecords    = 4096
)

type endpointRouteLedgerState string

const (
	endpointRoutePendingAdd    endpointRouteLedgerState = "pending-add"
	endpointRouteActive        endpointRouteLedgerState = "active"
	endpointRoutePendingDelete endpointRouteLedgerState = "pending-delete"
)

type endpointRouteLedger struct {
	SchemaVersion int                         `json:"schema_version"`
	Generation    uint64                      `json:"generation"`
	Records       []endpointRouteLedgerRecord `json:"records,omitempty"`
	Checksum      string                      `json:"checksum"`
}

type endpointRouteLedgerRecord struct {
	Interface        string                   `json:"interface"`
	Address          string                   `json:"address"`
	Gateway          string                   `json:"gateway,omitempty"`
	GatewayInterface string                   `json:"gateway_interface,omitempty"`
	State            endpointRouteLedgerState `json:"state"`
}

func encodeEndpointRouteLedger(ledger endpointRouteLedger) ([]byte, error) {
	ledger.SchemaVersion = endpointRouteLedgerSchemaVersion
	ledger.Checksum = ""
	if err := validateEndpointRouteLedger(ledger); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(ledger)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	ledger.Checksum = hex.EncodeToString(sum[:])
	return json.MarshalIndent(ledger, "", "  ")
}

func decodeEndpointRouteLedger(data []byte) (endpointRouteLedger, error) {
	if len(data) > maxEndpointRouteLedgerSize {
		return endpointRouteLedger{}, errors.New("endpoint route ledger is too large")
	}
	var ledger endpointRouteLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return endpointRouteLedger{}, err
	}
	checksum := ledger.Checksum
	if checksum == "" {
		return endpointRouteLedger{}, errors.New("endpoint route ledger checksum is missing")
	}
	ledger.Checksum = ""
	canonical, err := json.Marshal(ledger)
	if err != nil {
		return endpointRouteLedger{}, err
	}
	want := sha256.Sum256(canonical)
	got, err := hex.DecodeString(checksum)
	if err != nil || len(got) != len(want) {
		return endpointRouteLedger{}, errors.New("endpoint route ledger checksum is invalid")
	}
	if !equalBytes(got, want[:]) {
		return endpointRouteLedger{}, errors.New("endpoint route ledger checksum mismatch")
	}
	ledger.Checksum = checksum
	if err := validateEndpointRouteLedger(ledger); err != nil {
		return endpointRouteLedger{}, err
	}
	return ledger, nil
}

func validateEndpointRouteLedger(ledger endpointRouteLedger) error {
	if ledger.SchemaVersion != endpointRouteLedgerSchemaVersion {
		return fmt.Errorf("unsupported endpoint route ledger schema %d", ledger.SchemaVersion)
	}
	if len(ledger.Records) > maxEndpointRouteLedgerRecords {
		return errors.New("endpoint route ledger has too many records")
	}
	seen := make(map[string]struct{}, len(ledger.Records))
	for index, record := range ledger.Records {
		if record.Interface == "" || len(record.Interface) > 128 {
			return fmt.Errorf("endpoint route record %d has an invalid interface", index+1)
		}
		address, err := netip.ParseAddr(record.Address)
		if err != nil || address.IsUnspecified() || address != address.Unmap() {
			return fmt.Errorf("endpoint route record %d has an invalid address", index+1)
		}
		if (record.Gateway == "") == (record.GatewayInterface == "") {
			return fmt.Errorf("endpoint route record %d must have exactly one gateway identity", index+1)
		}
		if record.Gateway != "" {
			gateway, err := netip.ParseAddr(record.Gateway)
			if err != nil || gateway.IsUnspecified() {
				return fmt.Errorf("endpoint route record %d has an invalid gateway", index+1)
			}
		}
		switch record.State {
		case endpointRoutePendingAdd, endpointRouteActive, endpointRoutePendingDelete:
		default:
			return fmt.Errorf("endpoint route record %d has an invalid state", index+1)
		}
		key := record.Interface + "\x00" + record.Address
		if _, exists := seen[key]; exists {
			return fmt.Errorf("endpoint route record %d is duplicated", index+1)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}
