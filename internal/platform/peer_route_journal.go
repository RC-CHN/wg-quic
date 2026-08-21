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
	peerRouteJournalSchemaVersion = 1
	maxPeerRouteJournalSize       = 4 << 20
	maxPeerRouteJournalPrefixes   = 1 << 16
)

type peerRouteJournalPhase string

const (
	peerRouteJournalPrepared          peerRouteJournalPhase = "prepared"
	peerRouteJournalRemoving          peerRouteJournalPhase = "removing"
	peerRouteJournalRemoved           peerRouteJournalPhase = "removed"
	peerRouteJournalAdding            peerRouteJournalPhase = "adding"
	peerRouteJournalCommitted         peerRouteJournalPhase = "committed"
	peerRouteJournalRollbackAdditions peerRouteJournalPhase = "rollback-additions"
	peerRouteJournalRollbackRemovals  peerRouteJournalPhase = "rollback-removals"
	peerRouteJournalActive            peerRouteJournalPhase = "active"
)

// peerRouteJournal records only non-secret route ownership. Before is the
// projection already proven to be owned by the previous completed
// transaction. After is the candidate projection. A phase is persisted before
// each host mutation; the Applied flags are persisted only after the complete
// corresponding phase succeeds.
type peerRouteJournal struct {
	SchemaVersion    int                   `json:"schema_version"`
	Generation       uint64                `json:"generation"`
	Tunnel           string                `json:"tunnel"`
	TransactionID    string                `json:"transaction_id,omitempty"`
	InterfaceLUID    uint64                `json:"interface_luid"`
	CompartmentID    uint32                `json:"compartment_id"`
	Phase            peerRouteJournalPhase `json:"phase"`
	Before           []string              `json:"before"`
	After            []string              `json:"after"`
	RemovalsApplied  bool                  `json:"removals_applied,omitempty"`
	AdditionsApplied bool                  `json:"additions_applied,omitempty"`
	Checksum         string                `json:"checksum"`
}

func encodePeerRouteJournal(journal peerRouteJournal) ([]byte, error) {
	journal.SchemaVersion = peerRouteJournalSchemaVersion
	journal.Checksum = ""
	if err := validatePeerRouteJournal(journal); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(journal)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	journal.Checksum = hex.EncodeToString(sum[:])
	return json.MarshalIndent(journal, "", "  ")
}

func decodePeerRouteJournal(data []byte) (peerRouteJournal, error) {
	if len(data) > maxPeerRouteJournalSize {
		return peerRouteJournal{}, errors.New("peer route journal is too large")
	}
	var journal peerRouteJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return peerRouteJournal{}, err
	}
	checksum := journal.Checksum
	if checksum == "" {
		return peerRouteJournal{}, errors.New("peer route journal checksum is missing")
	}
	journal.Checksum = ""
	canonical, err := json.Marshal(journal)
	if err != nil {
		return peerRouteJournal{}, err
	}
	want := sha256.Sum256(canonical)
	got, err := hex.DecodeString(checksum)
	if err != nil || len(got) != len(want) || !equalBytes(got, want[:]) {
		return peerRouteJournal{}, errors.New("peer route journal checksum mismatch")
	}
	journal.Checksum = checksum
	if err := validatePeerRouteJournal(journal); err != nil {
		return peerRouteJournal{}, err
	}
	return journal, nil
}

func validatePeerRouteJournal(journal peerRouteJournal) error {
	if journal.SchemaVersion != peerRouteJournalSchemaVersion {
		return fmt.Errorf("unsupported peer route journal schema %d", journal.SchemaVersion)
	}
	if journal.Tunnel == "" || len(journal.Tunnel) > 128 {
		return errors.New("peer route journal has an invalid tunnel")
	}
	if journal.InterfaceLUID == 0 || journal.CompartmentID == 0 {
		return errors.New("peer route journal has an empty Windows route identity")
	}
	switch journal.Phase {
	case peerRouteJournalPrepared,
		peerRouteJournalRemoving,
		peerRouteJournalRemoved,
		peerRouteJournalAdding,
		peerRouteJournalCommitted,
		peerRouteJournalRollbackAdditions,
		peerRouteJournalRollbackRemovals,
		peerRouteJournalActive:
	default:
		return fmt.Errorf("peer route journal has invalid phase %q", journal.Phase)
	}
	if journal.Phase == peerRouteJournalActive && journal.TransactionID != "" {
		return errors.New("active peer route journal retains a transaction ID")
	}
	if journal.Phase != peerRouteJournalActive && journal.TransactionID == "" {
		return errors.New("transactional peer route journal has no transaction ID")
	}
	if len(journal.Before)+len(journal.After) > maxPeerRouteJournalPrefixes {
		return errors.New("peer route journal has too many prefixes")
	}
	if err := validatePeerRouteJournalPrefixes("before", journal.Before); err != nil {
		return err
	}
	if err := validatePeerRouteJournalPrefixes("after", journal.After); err != nil {
		return err
	}
	if journal.AdditionsApplied && !journal.RemovalsApplied {
		return errors.New("peer route additions were applied before removals")
	}
	return nil
}

func validatePeerRouteJournalPrefixes(field string, values []string) error {
	seen := make(map[netip.Prefix]struct{}, len(values))
	var previous netip.Prefix
	for index, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix != prefix.Masked() {
			return fmt.Errorf("peer route journal %s prefix %d is invalid", field, index+1)
		}
		if _, exists := seen[prefix]; exists {
			return fmt.Errorf("peer route journal %s prefix %s is duplicated", field, prefix)
		}
		if index > 0 && comparePrefixes(previous, prefix) >= 0 {
			return fmt.Errorf("peer route journal %s prefixes are not canonical", field)
		}
		seen[prefix] = struct{}{}
		previous = prefix
	}
	return nil
}

func comparePrefixes(left, right netip.Prefix) int {
	if left.Addr().Is4() != right.Addr().Is4() {
		if left.Addr().Is4() {
			return -1
		}
		return 1
	}
	if left.Bits() != right.Bits() {
		return left.Bits() - right.Bits()
	}
	return left.Addr().Compare(right.Addr())
}

func peerRoutePrefixStrings(prefixes []netip.Prefix) []string {
	result := make([]string, len(prefixes))
	for index, prefix := range prefixes {
		result[index] = prefix.Masked().String()
	}
	return result
}

func peerRouteJournalPrefixes(values []string) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		if prefix, err := netip.ParsePrefix(value); err == nil {
			result = append(result, prefix)
		}
	}
	return result
}
