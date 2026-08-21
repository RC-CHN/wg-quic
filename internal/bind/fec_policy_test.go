package armorbind

import (
	"net/netip"
	"testing"
	"time"
)

func TestFECPolicyProfiles(t *testing.T) {
	cfg := DefaultConfig()
	cfg.FECDataShards = 12
	cfg.FECInterleave = 1
	cfg.FECFlushDeadline = 2 * time.Millisecond
	tests := []struct {
		policy     string
		data       int
		interleave int
		deadline   time.Duration
	}{
		{policy: "latency", data: 4, interleave: 1, deadline: time.Millisecond},
		{policy: "balanced", data: 12, interleave: 1, deadline: 2 * time.Millisecond},
		{policy: "throughput", data: 12, interleave: 2, deadline: 4 * time.Millisecond},
	}
	for _, test := range tests {
		t.Run(test.policy, func(t *testing.T) {
			profile := fecPolicyProfileFor(cfg, test.policy)
			if profile.dataShards != test.data || profile.interleave != test.interleave ||
				profile.flushDeadline != test.deadline {
				t.Fatalf("profile = %#v", profile)
			}
		})
	}
}

func TestEndpointFECPolicyLeaseAndHotReplacement(t *testing.T) {
	b := New(DefaultConfig())
	address := netip.MustParseAddrPort("192.0.2.10:443")
	release, err := b.AcquireEndpointFECPolicy(address, "latency")
	if err != nil {
		t.Fatal(err)
	}
	endpointValue, err := b.ParseEndpoint(address.String())
	if err != nil {
		t.Fatal(err)
	}
	endpoint := endpointValue.(*Endpoint)
	if endpoint.fecPolicy != "latency" {
		t.Fatalf("parsed endpoint policy = %q", endpoint.fecPolicy)
	}
	b.state = &runState{endpoints: map[netip.AddrPort]*Endpoint{address: endpoint}}

	replacement, err := b.ReplaceEndpointFECPolicy(address, "latency", "throughput")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.fecPolicy != "throughput" {
		t.Fatalf("replaced endpoint policy = %q", endpoint.fecPolicy)
	}
	// The superseded release token cannot consume the replacement lease.
	release()
	if got := b.fecPolicies[address]; got.refs != 1 || got.policy != "throughput" {
		t.Fatalf("replacement lease = %#v", got)
	}
	replacement()
	if _, exists := b.fecPolicies[address]; exists {
		t.Fatal("replacement lease was not released")
	}
}

func TestSessionFECPolicyUpdateKeepsNewestRequest(t *testing.T) {
	session := &session{fecPolicyUpdates: make(chan string, 1)}
	session.setFECPolicy("latency")
	session.setFECPolicy("throughput")
	if got := <-session.fecPolicyUpdates; got != "throughput" {
		t.Fatalf("pending FEC policy = %q", got)
	}
}
