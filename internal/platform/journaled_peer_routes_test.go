package platform

import (
	"context"
	"net/netip"
	"slices"
	"testing"
)

type recordingPeerRouteJournal struct {
	phases  []peerRouteJournalPhase
	before  []netip.Prefix
	after   []netip.Prefix
	active  []netip.Prefix
	cleaned bool
}

func (j *recordingPeerRouteJournal) Begin(
	_ context.Context,
	_ string,
	before, after []netip.Prefix,
) error {
	j.before, j.after = slices.Clone(before), slices.Clone(after)
	j.phases = append(j.phases, peerRouteJournalPrepared)
	return nil
}

func (j *recordingPeerRouteJournal) Mark(
	_ context.Context,
	phase peerRouteJournalPhase,
	_, _ bool,
) error {
	j.phases = append(j.phases, phase)
	return nil
}

func (j *recordingPeerRouteJournal) Active(_ context.Context, prefixes []netip.Prefix) error {
	j.active = slices.Clone(prefixes)
	j.phases = append(j.phases, peerRouteJournalActive)
	return nil
}

func (j *recordingPeerRouteJournal) Cleanup(context.Context) error {
	j.cleaned = true
	return nil
}

func TestJournaledPeerRoutesPersistPhasesAndProjection(t *testing.T) {
	recorder := &peerRouteRecorder{failNext: make(map[string]error)}
	inner := testPeerRouteManager(t, "auto", recorder)
	journal := &recordingPeerRouteJournal{}
	manager, err := newJournaledPeerRouteManager(
		inner, journal, []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")},
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(t.Context(), PeerRoutePlan{
		TransactionID: "epoch:request",
		Removals:      []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")},
		Additions:     []netip.Prefix{netip.MustParsePrefix("10.2.0.0/16")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.CommitRemovals(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.CommitAdditions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Finalize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(journal.phases, []peerRouteJournalPhase{
		peerRouteJournalPrepared,
		peerRouteJournalRemoving,
		peerRouteJournalRemoved,
		peerRouteJournalAdding,
		peerRouteJournalCommitted,
		peerRouteJournalActive,
	}) {
		t.Fatalf("journal phases = %#v", journal.phases)
	}
	if !slices.Equal(journal.active, []netip.Prefix{netip.MustParsePrefix("10.2.0.0/16")}) {
		t.Fatalf("active journal projection = %v", journal.active)
	}
	if err := manager.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !journal.cleaned {
		t.Fatal("journaled peer route cleanup was not forwarded")
	}
}

func TestJournaledPeerRoutesRestoreBeforeProjectionOnRollback(t *testing.T) {
	recorder := &peerRouteRecorder{failNext: make(map[string]error)}
	inner := testPeerRouteManager(t, "auto", recorder)
	journal := &recordingPeerRouteJournal{}
	manager, err := newJournaledPeerRouteManager(
		inner, journal, []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")},
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(t.Context(), PeerRoutePlan{
		TransactionID: "epoch:rollback",
		Removals:      []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")},
		Additions:     []netip.Prefix{netip.MustParsePrefix("10.2.0.0/16")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.CommitRemovals(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.CommitAdditions(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(journal.active, []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}) {
		t.Fatalf("rollback active projection = %v", journal.active)
	}
}
