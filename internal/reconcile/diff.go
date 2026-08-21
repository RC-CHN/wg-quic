package reconcile

import (
	"net/netip"
	"slices"

	"github.com/RC-CHN/wg-quic/internal/config"
)

type PeerChange struct {
	PublicKey string   `json:"public_key"`
	Fields    []string `json:"fields"`
}

type PrefixMove struct {
	Prefix string `json:"prefix"`
	From   string `json:"from"`
	To     string `json:"to"`
}

// Diff is a deterministic projection of the mutable peer changes and their
// interface-wide consequences. Public key lists follow decoded-key order.
type Diff struct {
	Added          []string       `json:"added,omitempty"`
	Updated        []PeerChange   `json:"updated,omitempty"`
	Removed        []string       `json:"removed,omitempty"`
	RouteAdditions []netip.Prefix `json:"route_additions,omitempty"`
	RouteRemovals  []netip.Prefix `json:"route_removals,omitempty"`
	OwnershipMoves []PrefixMove   `json:"ownership_moves,omitempty"`
	RestartReasons []string       `json:"restart_reasons,omitempty"`
}

func (d Diff) Changed() bool {
	return len(d.Added) != 0 || len(d.Updated) != 0 || len(d.Removed) != 0
}

func (d Diff) RestartRequired() bool {
	return len(d.RestartReasons) != 0
}

func Compare(current, desired *Desired) Diff {
	if current == nil || desired == nil {
		return Diff{RestartReasons: []string{"current and desired configuration are required"}}
	}
	result := Diff{}
	compareImmutable(&result, current.config, desired.config)

	currentPeers := peerMap(current.config.Peers)
	desiredPeers := peerMap(desired.config.Peers)
	for _, peer := range desired.config.Peers {
		before, exists := currentPeers[peer.PublicKey]
		if !exists {
			result.Added = append(result.Added, peer.PublicKey)
			continue
		}
		fields := changedPeerFields(before, peer)
		if !equalSecret(before.PresharedKey, peer.PresharedKey) {
			result.RestartReasons = append(
				result.RestartReasons,
				"active peer "+shortKey(peer.PublicKey)+" preshared key changed",
			)
		}
		if len(fields) != 0 {
			result.Updated = append(result.Updated, PeerChange{
				PublicKey: peer.PublicKey, Fields: fields,
			})
		}
	}
	for _, peer := range current.config.Peers {
		if _, exists := desiredPeers[peer.PublicKey]; !exists {
			result.Removed = append(result.Removed, peer.PublicKey)
		}
	}

	compareRouteOwnership(&result, current.config.Peers, desired.config.Peers)
	if usesAutomaticTable(current.config) && usesAutomaticTable(desired.config) {
		for _, family := range []struct {
			name string
			ipv4 bool
		}{
			{name: "IPv4", ipv4: true},
			{name: "IPv6", ipv4: false},
		} {
			if hasDefaultRoute(current.config, family.ipv4) != hasDefaultRoute(desired.config, family.ipv4) {
				result.RestartReasons = append(
					result.RestartReasons,
					"automatic "+family.name+" full-tunnel mode changed",
				)
			}
		}
	}
	return result
}

func compareImmutable(result *Diff, current, desired *config.Config) {
	add := func(changed bool, reason string) {
		if changed {
			result.RestartReasons = append(result.RestartReasons, reason)
		}
	}
	add(!equalSecret(current.Interface.PrivateKey, desired.Interface.PrivateKey), "interface private key changed")
	add(!slices.Equal(current.Interface.Addresses, desired.Interface.Addresses), "interface addresses changed")
	add(current.Interface.ListenPort != desired.Interface.ListenPort, "interface listen port changed")
	add(current.Interface.FwMark != desired.Interface.FwMark, "interface fwmark changed")
	add(!slices.Equal(current.Interface.DNS, desired.Interface.DNS), "interface DNS policy changed")
	add(current.Interface.MTU != desired.Interface.MTU, "interface MTU changed")
	add(current.Interface.Table != desired.Interface.Table, "interface route table changed")
	add(!slices.Equal(current.Interface.PreUp, desired.Interface.PreUp), "interface PreUp hooks changed")
	add(!slices.Equal(current.Interface.PostUp, desired.Interface.PostUp), "interface PostUp hooks changed")
	add(!slices.Equal(current.Interface.PreDown, desired.Interface.PreDown), "interface PreDown hooks changed")
	add(!slices.Equal(current.Interface.PostDown, desired.Interface.PostDown), "interface PostDown hooks changed")
	add(current.Interface.SaveConfig != desired.Interface.SaveConfig, "interface SaveConfig changed")
	add(current.Transport != desired.Transport, "interface transport policy changed")
}

func changedPeerFields(current, desired config.Peer) []string {
	fields := make([]string, 0, 4)
	if !slices.Equal(current.AllowedIPs, desired.AllowedIPs) {
		fields = append(fields, "allowed_ips")
	}
	if current.Endpoint != desired.Endpoint {
		fields = append(fields, "endpoint")
	}
	if current.PersistentKeepalive != desired.PersistentKeepalive {
		fields = append(fields, "persistent_keepalive")
	}
	if current.FECPolicy != desired.FECPolicy {
		fields = append(fields, "fec_policy")
	}
	return fields
}

func peerMap(peers []config.Peer) map[string]config.Peer {
	result := make(map[string]config.Peer, len(peers))
	for _, peer := range peers {
		result[peer.PublicKey] = peer
	}
	return result
}

func compareRouteOwnership(result *Diff, current, desired []config.Peer) {
	currentOwners := prefixOwners(current)
	desiredOwners := prefixOwners(desired)
	additions := make([]netip.Prefix, 0)
	removals := make([]netip.Prefix, 0)
	moved := make([]netip.Prefix, 0)
	for prefix, desiredOwner := range desiredOwners {
		currentOwner, exists := currentOwners[prefix]
		switch {
		case !exists:
			additions = append(additions, prefix)
		case currentOwner != desiredOwner:
			moved = append(moved, prefix)
		}
	}
	for prefix := range currentOwners {
		if _, exists := desiredOwners[prefix]; !exists {
			removals = append(removals, prefix)
		}
	}
	sortPrefixes(additions)
	sortPrefixes(removals)
	sortPrefixes(moved)
	result.RouteAdditions = additions
	result.RouteRemovals = removals
	for _, prefix := range moved {
		result.OwnershipMoves = append(result.OwnershipMoves, PrefixMove{
			Prefix: prefix.String(), From: currentOwners[prefix], To: desiredOwners[prefix],
		})
	}
}

func prefixOwners(peers []config.Peer) map[netip.Prefix]string {
	result := make(map[netip.Prefix]string)
	for _, peer := range peers {
		for _, prefix := range peer.AllowedIPs {
			result[prefix] = peer.PublicKey
		}
	}
	return result
}

func usesAutomaticTable(cfg *config.Config) bool {
	return cfg.Interface.Table == "auto"
}

func hasDefaultRoute(cfg *config.Config, ipv4 bool) bool {
	for _, peer := range cfg.Peers {
		for _, prefix := range peer.AllowedIPs {
			if prefix.Bits() == 0 && prefix.Addr().Is4() == ipv4 {
				return true
			}
		}
	}
	return false
}
