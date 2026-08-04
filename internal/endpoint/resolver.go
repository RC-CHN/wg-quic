package endpoint

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

const systemResolverRefresh = time.Minute

type SystemResolver struct {
	Resolver *net.Resolver
}

func (r SystemResolver) Resolve(ctx context.Context, host string) (Resolution, error) {
	resolver := r.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	resolved, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return Resolution{}, err
	}
	seen := make(map[netip.Addr]struct{}, len(resolved))
	addresses := make([]netip.Addr, 0, len(resolved))
	for _, address := range resolved {
		address = address.Unmap()
		if !address.IsValid() || address.IsUnspecified() {
			continue
		}
		if _, ok := seen[address]; ok {
			continue
		}
		seen[address] = struct{}{}
		addresses = append(addresses, address)
	}
	if len(addresses) == 0 {
		return Resolution{}, errors.New("DNS response did not contain a usable IP address")
	}
	// net.Resolver doesn't expose record TTLs. The interface deliberately does,
	// so native resolvers can replace this conservative fallback without
	// changing supervisor policy.
	return Resolution{Addresses: addresses, RefreshAfter: systemResolverRefresh}, nil
}
