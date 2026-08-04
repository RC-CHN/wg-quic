package platform

import (
	"net/netip"
	"sort"

	"github.com/RC-CHN/wg-quic/internal/config"
)

func uniqueAllowedPrefixes(cfg *config.Config) []netip.Prefix {
	seen := make(map[netip.Prefix]struct{})
	var prefixes []netip.Prefix
	for _, peer := range cfg.Peers {
		for _, prefix := range peer.AllowedIPs {
			prefix = prefix.Masked()
			if _, ok := seen[prefix]; ok {
				continue
			}
			seen[prefix] = struct{}{}
			prefixes = append(prefixes, prefix)
		}
	}
	sort.Slice(prefixes, func(i, j int) bool {
		if prefixes[i].Bits() != prefixes[j].Bits() {
			return prefixes[i].Bits() > prefixes[j].Bits()
		}
		return prefixes[i].String() < prefixes[j].String()
	})
	return prefixes
}
