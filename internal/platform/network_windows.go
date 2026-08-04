package platform

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/config"
)

type windowsOperation struct {
	apply string
	undo  string
}

func windowsEndpointAddresses(ctx context.Context, cfg *config.Config) ([]netip.Addr, error) {
	seen := make(map[netip.Addr]struct{})
	var addresses []netip.Addr
	for i, peer := range cfg.Peers {
		if peer.Endpoint == "" {
			continue
		}
		host, _, err := net.SplitHostPort(peer.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("Peer %d Endpoint: %w", i+1, err)
		}
		host = strings.Trim(host, "[]")
		if address, err := netip.ParseAddr(host); err == nil {
			if _, ok := seen[address]; !ok {
				seen[address] = struct{}{}
				addresses = append(addresses, address)
			}
			continue
		}
		resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve Peer %d Endpoint: %w", i+1, err)
		}
		for _, address := range resolved {
			address = address.Unmap()
			if _, ok := seen[address]; ok {
				continue
			}
			seen[address] = struct{}{}
			addresses = append(addresses, address)
		}
	}
	slices.SortFunc(addresses, func(a, b netip.Addr) int {
		return a.Compare(b)
	})
	return addresses, nil
}

func windowsNetworkOperations(name string, cfg *config.Config) ([]windowsOperation, error) {
	if cfg == nil {
		return nil, fmt.Errorf("configuration is required")
	}
	table := strings.ToLower(strings.TrimSpace(cfg.Interface.Table))
	if table != "" && table != "auto" && table != "off" {
		return nil, fmt.Errorf("Windows does not support explicit route table %q", cfg.Interface.Table)
	}
	base := windowsPowerShellBase(name)
	var operations []windowsOperation
	for _, prefix := range cfg.Interface.Addresses {
		address := prefix.Addr().String()
		operations = append(operations, windowsOperation{
			apply: base +
				"New-NetIPAddress -InterfaceIndex $ifIndex -IPAddress " + powerShellQuote(address) +
				" -PrefixLength " + fmt.Sprint(prefix.Bits()) +
				" -PolicyStore ActiveStore -ErrorAction Stop | Out-Null",
			undo: base +
				"Get-NetIPAddress -InterfaceIndex $ifIndex -IPAddress " + powerShellQuote(address) +
				" -ErrorAction SilentlyContinue | Remove-NetIPAddress -Confirm:$false -ErrorAction SilentlyContinue",
		})
	}
	mtu := cfg.Interface.MTU
	if mtu == 0 {
		mtu = 1380
	}
	operations = append(operations, windowsOperation{
		apply: base +
			"Set-NetIPInterface -InterfaceIndex $ifIndex -AddressFamily IPv4 -NlMtuBytes " + fmt.Sprint(mtu) + " -ErrorAction Stop;" +
			"Set-NetIPInterface -InterfaceIndex $ifIndex -AddressFamily IPv6 -NlMtuBytes " + fmt.Sprint(mtu) + " -ErrorAction Stop",
	})
	if table != "off" {
		for _, prefix := range uniqueAllowedPrefixes(cfg) {
			nextHop := "0.0.0.0"
			if prefix.Addr().Is6() {
				nextHop = "::"
			}
			destination := prefix.String()
			operations = append(operations, windowsOperation{
				apply: base +
					"New-NetRoute -InterfaceIndex $ifIndex -DestinationPrefix " + powerShellQuote(destination) +
					" -NextHop " + powerShellQuote(nextHop) +
					" -RouteMetric 0 -PolicyStore ActiveStore -ErrorAction Stop | Out-Null",
				undo: base +
					"Get-NetRoute -InterfaceIndex $ifIndex -DestinationPrefix " + powerShellQuote(destination) +
					" -ErrorAction SilentlyContinue | Remove-NetRoute -Confirm:$false -ErrorAction SilentlyContinue",
			})
		}
	}
	dnsOperation, err := windowsDNSOperation(base, cfg.Interface.DNS)
	if err != nil {
		return nil, err
	}
	if dnsOperation.apply != "" {
		operations = append(operations, dnsOperation)
	}
	return operations, nil
}

func windowsDNSOperation(base string, values []string) (windowsOperation, error) {
	var servers, domains []string
	for _, value := range values {
		if _, err := netip.ParseAddr(value); err == nil {
			servers = append(servers, value)
		} else {
			domains = append(domains, value)
		}
	}
	if len(domains) > 1 {
		return windowsOperation{}, fmt.Errorf("Windows supports at most one connection-specific DNS suffix, got %d", len(domains))
	}
	if len(servers) == 0 && len(domains) == 0 {
		return windowsOperation{}, nil
	}
	var apply strings.Builder
	apply.WriteString(base)
	if len(servers) != 0 {
		apply.WriteString("Set-DnsClientServerAddress -InterfaceIndex $ifIndex -ServerAddresses ")
		apply.WriteString(powerShellArray(servers))
		apply.WriteString(" -ErrorAction Stop;")
	}
	if len(domains) != 0 {
		apply.WriteString("Set-DnsClient -InterfaceIndex $ifIndex -ConnectionSpecificSuffix ")
		apply.WriteString(powerShellQuote(domains[0]))
		apply.WriteString(" -ErrorAction Stop;")
	}
	undo := base +
		"Set-DnsClientServerAddress -InterfaceIndex $ifIndex -ResetServerAddresses -ErrorAction SilentlyContinue;" +
		"Set-DnsClient -InterfaceIndex $ifIndex -ResetConnectionSpecificSuffix -ErrorAction SilentlyContinue"
	return windowsOperation{apply: apply.String(), undo: undo}, nil
}

func windowsPowerShellBase(name string) string {
	return "$ErrorActionPreference='Stop';" +
		"$adapter=Get-NetAdapter -Name " + powerShellQuote(name) + " -ErrorAction Stop;" +
		"$ifIndex=$adapter.ifIndex;"
}

func powerShellArray(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = powerShellQuote(value)
	}
	return "@(" + strings.Join(quoted, ",") + ")"
}

func powerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
