//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"strings"
	"syscall"

	"github.com/RC-CHN/wg-quic/internal/config"
	"golang.org/x/sys/windows"
)

// ApplyDNS returns whether any native operation succeeded, even when a later
// family or suffix fails. The caller must retain cleanup ownership in that case.
func (windowsNativeNetworkSystem) ApplyDNS(ctx context.Context, compartmentID uint32, luid uint64, values []string) (bool, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := windowsValidateCompartment(windowsRouteKey{CompartmentID: compartmentID}); err != nil {
		return false, err
	}
	if err := windowsProcSetDNSSettings.Find(); err != nil {
		if errors.Is(err, windows.ERROR_PROC_NOT_FOUND) {
			return false, fmt.Errorf("%w: %v", errWindowsDNSAPIUnavailable, err)
		}
		return false, err
	}
	guid, err := windowsInterfaceGUID(luid)
	if err != nil {
		return false, err
	}
	return applyWindowsDNSSettings(ctx, values, func(settings *windowsDNSInterfaceSettings) error {
		status := callWindowsSetDNSSettings(windowsProcSetDNSSettings.Addr(), &guid, settings)
		if status != 0 {
			return syscall.Errno(status)
		}
		return nil
	})
}

func applyWindowsDNSSettings(ctx context.Context, values []string, set func(*windowsDNSInterfaceSettings) error) (bool, error) {
	dns := config.ClassifyDNS(values)
	if len(dns.Domains) > 1 {
		return false, fmt.Errorf("Windows supports at most one connection-specific DNS suffix, got %d", len(dns.Domains))
	}
	var settings []windowsDNSInterfaceSettings
	if len(dns.Servers) != 0 {
		var servers [2][]string
		for _, server := range dns.Servers {
			address, err := netip.ParseAddr(server)
			if err != nil {
				return false, err
			}
			family := 0
			if address.Is6() {
				family = 1
			}
			servers[family] = append(servers[family], server)
		}
		for family := range servers {
			if len(servers[family]) == 0 {
				continue
			}
			list, err := windows.UTF16PtrFromString(strings.Join(servers[family], ","))
			if err != nil {
				return false, err
			}
			flags := uint64(windowsDNSSettingsNameServer)
			if family == 1 {
				flags |= windowsDNSSettingsIPv6
			}
			// Set-DnsClientServerAddress updates only address families present
			// in its input. Preserve the other family's current settings.
			settings = append(settings, windowsDNSInterfaceSettings{Version: 1, Flags: flags, NameServer: list})
		}
	}
	if len(dns.Domains) != 0 {
		domain, err := windows.UTF16PtrFromString(dns.Domains[0])
		if err != nil {
			return false, err
		}
		// The cmdlet applies the suffix after both server families, and only
		// when a domain was provided. Never overwrite SearchList here.
		settings = append(settings, windowsDNSInterfaceSettings{Version: 1, Flags: windowsDNSSettingsDomain, Domain: domain})
	}
	applied := false
	for i := range settings {
		if err := ctx.Err(); err != nil {
			return applied, err
		}
		if err := set(&settings[i]); err != nil {
			return applied, fmt.Errorf("DNS settings step %d: %w", i+1, err)
		}
		applied = true
	}
	return applied, nil
}
