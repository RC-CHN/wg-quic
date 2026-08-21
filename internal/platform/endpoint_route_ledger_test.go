package platform

import (
	"bytes"
	"testing"
)

func TestEndpointRouteLedgerChecksumAndValidation(t *testing.T) {
	ledger := endpointRouteLedger{
		SchemaVersion: endpointRouteLedgerSchemaVersion,
		Generation:    3,
		Records: []endpointRouteLedgerRecord{{
			Interface: "wg0", Address: "192.0.2.10", Gateway: "192.0.2.1",
			State: endpointRouteActive,
		}},
	}
	encoded, err := encodeEndpointRouteLedger(ledger)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeEndpointRouteLedger(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Generation != 3 || len(decoded.Records) != 1 || decoded.Checksum == "" {
		t.Fatalf("decoded endpoint route ledger = %#v", decoded)
	}
	tampered := bytes.Replace(encoded, []byte("192.0.2.10"), []byte("192.0.2.11"), 1)
	if _, err := decodeEndpointRouteLedger(tampered); err == nil {
		t.Fatal("tampered endpoint route ledger passed checksum validation")
	}
	ledger.Records[0].Gateway = ""
	if _, err := encodeEndpointRouteLedger(ledger); err == nil {
		t.Fatal("ledger record without exact gateway identity was accepted")
	}
}
