package platform

import (
	"bytes"
	"testing"
)

func TestPeerRouteJournalRoundTripAndChecksum(t *testing.T) {
	journal := peerRouteJournal{
		SchemaVersion: peerRouteJournalSchemaVersion,
		Generation:    7, Tunnel: "office", TransactionID: "epoch:request",
		InterfaceLUID: 91, CompartmentID: 1, Phase: peerRouteJournalCommitted,
		Before:          []string{"10.0.0.0/8", "2001:db8::/32"},
		After:           []string{"10.1.0.0/16", "2001:db8::/32"},
		RemovalsApplied: true, AdditionsApplied: true,
	}
	encoded, err := encodePeerRouteJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodePeerRouteJournal(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Generation != journal.Generation ||
		decoded.Phase != journal.Phase || len(decoded.After) != 2 {
		t.Fatalf("decoded journal = %#v", decoded)
	}
	modified := bytes.Replace(encoded, []byte("10.1.0.0/16"), []byte("10.2.0.0/16"), 1)
	if _, err := decodePeerRouteJournal(modified); err == nil {
		t.Fatal("peer route journal checksum accepted modified content")
	}
}

func TestPeerRouteJournalRejectsNonCanonicalOrSecretLikeState(t *testing.T) {
	base := peerRouteJournal{
		SchemaVersion: peerRouteJournalSchemaVersion,
		Tunnel:        "office", TransactionID: "request", InterfaceLUID: 1,
		CompartmentID: 1, Phase: peerRouteJournalPrepared,
	}
	tests := []peerRouteJournal{
		func() peerRouteJournal { value := base; value.Before = []string{"10.0.0.1/8"}; return value }(),
		func() peerRouteJournal {
			value := base
			value.Before = []string{"2001:db8::/32", "10.0.0.0/8"}
			return value
		}(),
		func() peerRouteJournal { value := base; value.AdditionsApplied = true; return value }(),
		func() peerRouteJournal { value := base; value.Phase = peerRouteJournalActive; return value }(),
	}
	for index, journal := range tests {
		if _, err := encodePeerRouteJournal(journal); err == nil {
			t.Fatalf("invalid journal %d was accepted", index+1)
		}
	}
}
