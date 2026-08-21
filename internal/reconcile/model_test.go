package reconcile

import (
	"encoding/base64"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/RC-CHN/wg-quic/internal/config"
)

func modelTestKey(value byte) string {
	key := make([]byte, 32)
	for index := range key {
		key[index] = value
	}
	return base64.StdEncoding.EncodeToString(key)
}

func modelTestConfig(peers ...config.Peer) *config.Config {
	return &config.Config{
		Interface: config.Interface{
			PrivateKey: modelTestKey(100),
			Addresses:  []netip.Prefix{netip.MustParsePrefix("10.0.0.1/24")},
		},
		Peers: peers,
	}
}

func mustDesired(t *testing.T, cfg *config.Config, active *Desired) *Desired {
	t.Helper()
	desired, err := FromConfig(cfg, active)
	if err != nil {
		t.Fatal(err)
	}
	return desired
}

func TestDesiredCanonicalizationMakesReorderingANoOp(t *testing.T) {
	first := config.Peer{
		PublicKey: modelTestKey(2), Endpoint: "PEER.EXAMPLE.:443",
		AllowedIPs: []netip.Prefix{
			netip.MustParsePrefix("10.2.0.7/16"),
			netip.MustParsePrefix("2001:db8:2::7/64"),
		},
	}
	second := config.Peer{
		PublicKey: modelTestKey(1), Endpoint: "[::ffff:192.0.2.9]:8443",
		AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.1.0.7/16")},
	}
	left := mustDesired(t, modelTestConfig(first, second), nil)

	first.Endpoint = "peer.example:443"
	first.AllowedIPs[0], first.AllowedIPs[1] = first.AllowedIPs[1], first.AllowedIPs[0]
	right := mustDesired(t, modelTestConfig(second, first), nil)
	if diff := Compare(left, right); diff.Changed() || diff.RestartRequired() {
		t.Fatalf("canonical reorder diff = %#v", diff)
	}
	if left.Digest() != right.Digest() {
		t.Fatalf("canonical digests differ: %q != %q", left.Digest(), right.Digest())
	}

	canonical := left.Config()
	if got, want := canonical.Interface.Table, "auto"; got != want {
		t.Fatalf("table = %q, want %q", got, want)
	}
	if got, want := canonical.Interface.MTU, config.DefaultMTU; got != want {
		t.Fatalf("MTU = %d, want %d", got, want)
	}
	if got, want := canonical.Peers[0].PublicKey, modelTestKey(1); got != want {
		t.Fatalf("first peer = %q, want decoded-key order %q", got, want)
	}
	if got, want := canonical.Peers[0].Endpoint, "192.0.2.9:8443"; got != want {
		t.Fatalf("numeric endpoint = %q, want %q", got, want)
	}
	if got, want := canonical.Peers[1].Endpoint, "peer.example:443"; got != want {
		t.Fatalf("hostname endpoint = %q, want %q", got, want)
	}
	if got, want := canonical.Peers[1].FECPolicy, DefaultFECPolicy; got != want {
		t.Fatalf("FEC policy = %q, want %q", got, want)
	}
	if got, want := canonical.Peers[1].AllowedIPs[0].String(), "10.2.0.0/16"; got != want {
		t.Fatalf("first canonical AllowedIP = %q, want %q", got, want)
	}
}

func TestDesiredRejectsDuplicateExactPrefixButAllowsNestedPrefixes(t *testing.T) {
	cfg := modelTestConfig(
		config.Peer{PublicKey: modelTestKey(1), AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}},
		config.Peer{PublicKey: modelTestKey(2), AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16")}},
	)
	if _, err := FromConfig(cfg, nil); err != nil {
		t.Fatalf("nested prefixes must be valid: %v", err)
	}
	cfg.Peers[1].AllowedIPs[0] = netip.MustParsePrefix("10.0.0.1/8")
	if _, err := FromConfig(cfg, nil); err == nil || !strings.Contains(err.Error(), "already owned") {
		t.Fatalf("exact duplicate ownership error = %v", err)
	}
}

func TestDesiredRejectsDuplicatePrefixWithinPeerAndUnspecifiedEndpoint(t *testing.T) {
	cfg := modelTestConfig(config.Peer{
		PublicKey: modelTestKey(1),
		AllowedIPs: []netip.Prefix{
			netip.MustParsePrefix("10.0.0.0/24"),
			netip.MustParsePrefix("10.0.0.9/24"),
		},
	})
	if _, err := FromConfig(cfg, nil); err == nil || !strings.Contains(err.Error(), "is duplicated") {
		t.Fatalf("duplicate peer prefix error = %v", err)
	}
	cfg.Peers[0].AllowedIPs = nil
	cfg.Peers[0].Endpoint = "0.0.0.0:443"
	if _, err := FromConfig(cfg, nil); err == nil || !strings.Contains(err.Error(), "unspecified") {
		t.Fatalf("unspecified endpoint error = %v", err)
	}
}

func TestDesiredRetainsOmittedPresharedKeyAndRejectsRotationAtDiff(t *testing.T) {
	active := mustDesired(t, modelTestConfig(config.Peer{
		PublicKey: modelTestKey(1), PresharedKey: modelTestKey(10),
	}), nil)
	omitted := mustDesired(t, modelTestConfig(config.Peer{
		PublicKey: modelTestKey(1),
	}), active)
	if diff := Compare(active, omitted); diff.Changed() || diff.RestartRequired() {
		t.Fatalf("omitted active PSK diff = %#v", diff)
	}
	if got := omitted.Config().Peers[0].PresharedKey; got != modelTestKey(10) {
		t.Fatal("omitted active PSK was not retained")
	}

	rotated := mustDesired(t, modelTestConfig(config.Peer{
		PublicKey: modelTestKey(1), PresharedKey: modelTestKey(11),
	}), active)
	diff := Compare(active, rotated)
	if !diff.RestartRequired() || len(diff.RestartReasons) != 1 ||
		!strings.Contains(diff.RestartReasons[0], "preshared key") {
		t.Fatalf("PSK rotation diff = %#v", diff)
	}
}

func TestCompareReportsPeerAndRouteOwnershipChanges(t *testing.T) {
	current := mustDesired(t, modelTestConfig(
		config.Peer{
			PublicKey: modelTestKey(1), Endpoint: "old.example:443",
			AllowedIPs: []netip.Prefix{
				netip.MustParsePrefix("10.1.0.0/16"),
				netip.MustParsePrefix("10.9.0.0/16"),
			},
		},
		config.Peer{PublicKey: modelTestKey(2), AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.2.0.0/16")}},
	), nil)
	desired := mustDesired(t, modelTestConfig(
		config.Peer{
			PublicKey: modelTestKey(1), Endpoint: "new.example:443", PersistentKeepalive: 25,
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.3.0.0/16")},
		},
		config.Peer{PublicKey: modelTestKey(3), AllowedIPs: []netip.Prefix{
			netip.MustParsePrefix("10.2.0.0/16"),
			netip.MustParsePrefix("10.4.0.0/16"),
		}},
	), nil)
	diff := Compare(current, desired)
	if !slices.Equal(diff.Added, []string{modelTestKey(3)}) ||
		!slices.Equal(diff.Removed, []string{modelTestKey(2)}) {
		t.Fatalf("peer add/remove diff = %#v", diff)
	}
	if len(diff.Updated) != 1 || !slices.Equal(
		diff.Updated[0].Fields,
		[]string{"allowed_ips", "endpoint", "persistent_keepalive"},
	) {
		t.Fatalf("peer update diff = %#v", diff.Updated)
	}
	if got := prefixStrings(diff.RouteAdditions); !slices.Equal(got, []string{"10.3.0.0/16", "10.4.0.0/16"}) {
		t.Fatalf("route additions = %v", got)
	}
	if got := prefixStrings(diff.RouteRemovals); !slices.Equal(got, []string{"10.1.0.0/16", "10.9.0.0/16"}) {
		t.Fatalf("route removals = %v", got)
	}
	if len(diff.OwnershipMoves) != 1 || diff.OwnershipMoves[0].Prefix != "10.2.0.0/16" {
		t.Fatalf("ownership moves = %#v", diff.OwnershipMoves)
	}
}

func TestCompareRequiresRestartForHooksAndAutomaticDefaultTransition(t *testing.T) {
	current := mustDesired(t, modelTestConfig(config.Peer{PublicKey: modelTestKey(1)}), nil)
	candidate := modelTestConfig(config.Peer{
		PublicKey:  modelTestKey(1),
		AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
	})
	candidate.Interface.PostUp = []string{"echo changed"}
	desired := mustDesired(t, candidate, current)
	diff := Compare(current, desired)
	if !diff.RestartRequired() || len(diff.RestartReasons) != 2 {
		t.Fatalf("restart reasons = %#v", diff.RestartReasons)
	}
	if !strings.Contains(strings.Join(diff.RestartReasons, "\n"), "PostUp") ||
		!strings.Contains(strings.Join(diff.RestartReasons, "\n"), "full-tunnel") {
		t.Fatalf("restart reasons = %#v", diff.RestartReasons)
	}
}

func TestNonSecretDigestDoesNotRevealSecretChanges(t *testing.T) {
	leftConfig := modelTestConfig(config.Peer{
		PublicKey: modelTestKey(1), PresharedKey: modelTestKey(2),
	})
	rightConfig := leftConfig.Clone()
	rightConfig.Interface.PrivateKey = modelTestKey(101)
	rightConfig.Peers[0].PresharedKey = modelTestKey(3)
	left := mustDesired(t, leftConfig, nil)
	right := mustDesired(t, rightConfig, nil)
	if left.Digest() != right.Digest() {
		t.Fatalf("non-secret digest changed with only secrets: %q != %q", left.Digest(), right.Digest())
	}
	if strings.Contains(left.Digest(), modelTestKey(2)) {
		t.Fatal("digest exposed preshared key")
	}
}

func prefixStrings(prefixes []netip.Prefix) []string {
	result := make([]string, len(prefixes))
	for index, prefix := range prefixes {
		result[index] = prefix.String()
	}
	return result
}
