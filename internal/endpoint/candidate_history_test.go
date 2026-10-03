package endpoint

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

func TestSupervisorBoundsFailedCandidateHistoryDuringDNSChurn(t *testing.T) {
	active := netip.MustParseAddr("192.0.2.10")
	retained := netip.MustParseAddr("192.0.2.20")
	rotating := netip.MustParseAddr("198.51.100.1")
	resolver := &fakeResolver{responses: map[string][]Resolution{
		"peer.example": {{Addresses: []netip.Addr{active}}},
	}}
	core := &fakeCoreControl{waitError: map[netip.AddrPort]error{
		netip.AddrPortFrom(retained, 443): errors.New("unreachable candidate"),
	}}
	supervisor := testSupervisor(t, resolver, &fakeRouteLeaser{}, core, "peer.example:443")
	supervisor.options.RetryMin = time.Hour
	supervisor.options.RetryMax = time.Hour
	if _, err := supervisor.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close(context.Background())
	var originalBackoff candidateFailure
	for iteration := range 1000 {
		rotating = rotating.Next()
		resolver.responses["peer.example"] = []Resolution{{Addresses: []netip.Addr{retained, rotating}}}
		core.waitError[netip.AddrPortFrom(rotating, 443)] = errors.New("unreachable candidate")
		if err := supervisor.RefreshPeer(context.Background(), "peer"); err == nil {
			t.Fatal("unreachable candidate was accepted")
		}
		failures := supervisor.peers["peer"].failedCandidates
		if len(failures) != 2 {
			t.Fatalf("after %d DNS changes retained %d failed addresses, want current two", iteration+1, len(failures))
		}
		if iteration == 0 {
			originalBackoff = failures[retained]
		} else if failures[retained] != originalBackoff {
			t.Fatalf("still-published address lost its backoff: %#v", failures[retained])
		}
	}
	resolver.nextErrors = []error{errors.New("temporary DNS failure")}
	if err := supervisor.RefreshPeer(context.Background(), "peer"); err == nil {
		t.Fatal("DNS error was not returned")
	}
	if len(supervisor.peers["peer"].failedCandidates) != 2 {
		t.Fatal("failed DNS query discarded the last known backoff")
	}
}
