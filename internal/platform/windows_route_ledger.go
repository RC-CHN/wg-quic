package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

func encodeWindowsRouteLedger(ledger windowsRouteLedger) ([]byte, error) {
	ledger.Checksum = ""
	canonical, err := json.Marshal(ledger)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	ledger.Checksum = hex.EncodeToString(sum[:])
	return json.MarshalIndent(ledger, "", "  ")
}

func decodeWindowsRouteLedger(data []byte) (windowsRouteLedger, error) {
	var ledger windowsRouteLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		return windowsRouteLedger{}, err
	}
	checksum := ledger.Checksum
	if len(checksum) != sha256.Size*2 {
		return windowsRouteLedger{}, errors.New("Windows route ledger checksum is missing")
	}
	ledger.Checksum = ""
	canonical, err := json.Marshal(ledger)
	if err != nil {
		return windowsRouteLedger{}, err
	}
	sum := sha256.Sum256(canonical)
	if !strings.EqualFold(checksum, hex.EncodeToString(sum[:])) {
		return windowsRouteLedger{}, errors.New("Windows route ledger checksum mismatch")
	}
	ledger.Checksum = checksum
	return ledger, nil
}
