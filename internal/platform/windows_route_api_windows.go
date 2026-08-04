//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	windowsIPHLPAPI                    = windows.NewLazySystemDLL("iphlpapi.dll")
	windowsProcConvertAliasToLUID      = windowsIPHLPAPI.NewProc("ConvertInterfaceAliasToLuid")
	windowsProcCreateRoute             = windowsIPHLPAPI.NewProc("CreateIpForwardEntry2")
	windowsProcDeleteRoute             = windowsIPHLPAPI.NewProc("DeleteIpForwardEntry2")
	windowsProcGetBestRoute            = windowsIPHLPAPI.NewProc("GetBestRoute2")
	windowsProcGetCurrentCompartmentID = windowsIPHLPAPI.NewProc("GetCurrentThreadCompartmentId")
	windowsProcInitializeRoute         = windowsIPHLPAPI.NewProc("InitializeIpForwardEntry")
)

type windowsNativeRouteSystem struct{}

func (windowsNativeRouteSystem) CurrentCompartmentID() uint32 {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	return windowsCurrentCompartmentID()
}

func windowsInterfaceLUID(alias string) (uint64, error) {
	name, err := windows.UTF16PtrFromString(alias)
	if err != nil {
		return 0, fmt.Errorf("encode Windows interface alias: %w", err)
	}
	var luid uint64
	status, _, _ := syscall.SyscallN(
		windowsProcConvertAliasToLUID.Addr(),
		uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&luid)),
	)
	if status != 0 {
		return 0, fmt.Errorf("resolve Windows interface %q LUID: %w", alias, syscall.Errno(status))
	}
	if luid == 0 {
		return 0, fmt.Errorf("resolve Windows interface %q LUID: empty LUID", alias)
	}
	return luid, nil
}

func (windowsNativeRouteSystem) BestRoute(
	ctx context.Context,
	endpoint netip.Addr,
	tunnelLUID uint64,
) (windowsSelectedRoute, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ctx.Err(); err != nil {
		return windowsSelectedRoute{}, err
	}
	destination, err := windowsRawAddress(endpoint)
	if err != nil {
		return windowsSelectedRoute{}, err
	}
	family := uint16(windows.AF_INET6)
	if endpoint.Is4() {
		family = windows.AF_INET
	}
	candidate, err := windowsBestRouteExcludingInterface(
		ctx, family, destination, tunnelLUID,
	)
	if err != nil {
		return windowsSelectedRoute{}, err
	}
	nextHop, err := windowsAddressFromRaw(candidate.route.NextHop)
	if err != nil {
		return windowsSelectedRoute{}, fmt.Errorf("decode best-route next hop: %w", err)
	}
	bestSource, err := windowsAddressFromRaw(candidate.source)
	if err != nil {
		return windowsSelectedRoute{}, fmt.Errorf("decode best-route source address: %w", err)
	}
	endpoint = endpoint.Unmap()
	keyFamily := uint8(6)
	bits := 128
	if endpoint.Is4() {
		keyFamily = 4
		bits = 32
	}
	return windowsSelectedRoute{
		Key: windowsRouteKey{
			CompartmentID: windowsCurrentCompartmentID(),
			Family:        keyFamily,
			Destination:   netip.PrefixFrom(endpoint, bits).String(),
			InterfaceLUID: candidate.route.InterfaceLuid,
			NextHop:       nextHop.String(),
		},
		InterfaceIndex: candidate.route.InterfaceIndex,
		Metric:         candidate.route.Metric,
		BestSource:     bestSource,
	}, nil
}

type windowsNativeRouteCandidate struct {
	route           windows.MibIpForwardRow2
	source          windows.RawSockaddrInet
	effectiveMetric uint64
}

func windowsBestRouteExcludingInterface(
	ctx context.Context,
	family uint16,
	destination windows.RawSockaddrInet,
	excludedLUID uint64,
) (windowsNativeRouteCandidate, error) {
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(family, &table); err != nil {
		return windowsNativeRouteCandidate{}, fmt.Errorf("enumerate Windows routes: %w", err)
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))

	interfaces := make(map[uint64]struct{})
	for _, row := range table.Rows() {
		if row.InterfaceLuid != 0 && row.InterfaceLuid != excludedLUID {
			interfaces[row.InterfaceLuid] = struct{}{}
		}
	}
	var best windowsNativeRouteCandidate
	found := false
	var candidateErrors []error
	for luid := range interfaces {
		if err := ctx.Err(); err != nil {
			return windowsNativeRouteCandidate{}, err
		}
		route, source, err := windowsBestRouteOnInterface(destination, luid)
		if err != nil {
			candidateErrors = append(candidateErrors, err)
			continue
		}
		interfaceRow := windows.MibIpInterfaceRow{
			Family: family, InterfaceLuid: route.InterfaceLuid,
		}
		if err := windows.GetIpInterfaceEntry(&interfaceRow); err != nil {
			candidateErrors = append(candidateErrors, err)
			continue
		}
		if interfaceRow.Connected == 0 || route.InterfaceLuid == excludedLUID {
			continue
		}
		candidate := windowsNativeRouteCandidate{
			route: route, source: source,
			effectiveMetric: uint64(route.Metric) + uint64(interfaceRow.Metric),
		}
		if !found || betterWindowsRouteCandidate(candidate, best) {
			best = candidate
			found = true
		}
	}
	if !found {
		if cause := errors.Join(candidateErrors...); cause != nil {
			return windowsNativeRouteCandidate{}, fmt.Errorf(
				"no Windows route to endpoint outside the tunnel: %w", cause,
			)
		}
		return windowsNativeRouteCandidate{}, errors.New(
			"no Windows route to endpoint outside the tunnel",
		)
	}
	return best, nil
}

func windowsBestRouteOnInterface(
	destination windows.RawSockaddrInet,
	interfaceLUID uint64,
) (windows.MibIpForwardRow2, windows.RawSockaddrInet, error) {
	var route windows.MibIpForwardRow2
	var source windows.RawSockaddrInet
	status, _, _ := syscall.SyscallN(
		windowsProcGetBestRoute.Addr(),
		uintptr(unsafe.Pointer(&interfaceLUID)),
		0,
		0,
		uintptr(unsafe.Pointer(&destination)),
		0,
		uintptr(unsafe.Pointer(&route)),
		uintptr(unsafe.Pointer(&source)),
	)
	if status != 0 {
		return windows.MibIpForwardRow2{}, windows.RawSockaddrInet{}, syscall.Errno(status)
	}
	return route, source, nil
}

func betterWindowsRouteCandidate(
	candidate windowsNativeRouteCandidate,
	current windowsNativeRouteCandidate,
) bool {
	candidatePrefix := candidate.route.DestinationPrefix.PrefixLength
	currentPrefix := current.route.DestinationPrefix.PrefixLength
	if candidatePrefix != currentPrefix {
		return candidatePrefix > currentPrefix
	}
	if candidate.effectiveMetric != current.effectiveMetric {
		return candidate.effectiveMetric < current.effectiveMetric
	}
	if candidate.route.Metric != current.route.Metric {
		return candidate.route.Metric < current.route.Metric
	}
	return candidate.route.InterfaceIndex < current.route.InterfaceIndex
}

func (windowsNativeRouteSystem) RouteExists(
	ctx context.Context,
	key windowsRouteKey,
) (bool, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := windowsValidateCompartment(key); err != nil {
		return false, err
	}
	row, err := windowsRouteRow(key)
	if err != nil {
		return false, err
	}
	err = windows.GetIpForwardEntry2(&row)
	if errors.Is(err, windows.ERROR_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (windowsNativeRouteSystem) CreateRoute(
	ctx context.Context,
	selected windowsSelectedRoute,
) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := windowsValidateCompartment(selected.Key); err != nil {
		return err
	}
	var row windows.MibIpForwardRow2
	syscall.SyscallN(
		windowsProcInitializeRoute.Addr(),
		uintptr(unsafe.Pointer(&row)),
	)
	keyRow, err := windowsRouteRow(selected.Key)
	if err != nil {
		return err
	}
	row.InterfaceLuid = keyRow.InterfaceLuid
	row.DestinationPrefix = keyRow.DestinationPrefix
	row.NextHop = keyRow.NextHop
	row.Metric = selected.Metric
	status, _, _ := syscall.SyscallN(
		windowsProcCreateRoute.Addr(),
		uintptr(unsafe.Pointer(&row)),
	)
	if status != 0 {
		return syscall.Errno(status)
	}
	return nil
}

func (windowsNativeRouteSystem) DeleteRoute(
	ctx context.Context,
	key windowsRouteKey,
) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := windowsValidateCompartment(key); err != nil {
		return err
	}
	row, err := windowsRouteRow(key)
	if err != nil {
		return err
	}
	status, _, _ := syscall.SyscallN(
		windowsProcDeleteRoute.Addr(),
		uintptr(unsafe.Pointer(&row)),
	)
	if status == uintptr(windows.ERROR_NOT_FOUND) ||
		status == uintptr(windows.ERROR_FILE_NOT_FOUND) {
		return nil
	}
	if status != 0 {
		return syscall.Errno(status)
	}
	return nil
}

func windowsRouteRow(key windowsRouteKey) (windows.MibIpForwardRow2, error) {
	prefix, err := netip.ParsePrefix(key.Destination)
	if err != nil {
		return windows.MibIpForwardRow2{}, fmt.Errorf("parse route destination: %w", err)
	}
	nextHop, err := netip.ParseAddr(key.NextHop)
	if err != nil {
		return windows.MibIpForwardRow2{}, fmt.Errorf("parse route next hop: %w", err)
	}
	if (key.Family == 4) != prefix.Addr().Is4() ||
		prefix.Addr().Is4() != nextHop.Is4() {
		return windows.MibIpForwardRow2{}, errors.New("route address family mismatch")
	}
	rawPrefix, err := windowsRawAddress(prefix.Addr())
	if err != nil {
		return windows.MibIpForwardRow2{}, err
	}
	rawNextHop, err := windowsRawAddress(nextHop)
	if err != nil {
		return windows.MibIpForwardRow2{}, err
	}
	return windows.MibIpForwardRow2{
		InterfaceLuid: key.InterfaceLUID,
		DestinationPrefix: windows.IpAddressPrefix{
			Prefix:       rawPrefix,
			PrefixLength: uint8(prefix.Bits()),
		},
		NextHop: rawNextHop,
	}, nil
}

func windowsRawAddress(address netip.Addr) (windows.RawSockaddrInet, error) {
	var raw windows.RawSockaddrInet
	if address.Is4() {
		ipv4 := (*windows.RawSockaddrInet4)(unsafe.Pointer(&raw))
		ipv4.Family = windows.AF_INET
		ipv4.Addr = address.As4()
		return raw, nil
	}
	if address.Is6() {
		ipv6 := (*windows.RawSockaddrInet6)(unsafe.Pointer(&raw))
		ipv6.Family = windows.AF_INET6
		ipv6.Addr = address.As16()
		if zone := address.Zone(); zone != "" {
			scopeID, err := strconv.ParseUint(zone, 10, 32)
			if err != nil {
				iface, lookupErr := net.InterfaceByName(zone)
				if lookupErr != nil {
					return raw, fmt.Errorf("resolve IPv6 scope %q: %w", zone, lookupErr)
				}
				scopeID = uint64(iface.Index)
			}
			ipv6.Scope_id = uint32(scopeID)
		}
		return raw, nil
	}
	return raw, errors.New("invalid IP address")
}

func windowsAddressFromRaw(raw windows.RawSockaddrInet) (netip.Addr, error) {
	switch raw.Family {
	case windows.AF_INET:
		return netip.AddrFrom4((*windows.RawSockaddrInet4)(unsafe.Pointer(&raw)).Addr), nil
	case windows.AF_INET6:
		ipv6 := (*windows.RawSockaddrInet6)(unsafe.Pointer(&raw))
		address := netip.AddrFrom16(ipv6.Addr)
		if ipv6.Scope_id != 0 {
			address = address.WithZone(strconv.FormatUint(uint64(ipv6.Scope_id), 10))
		}
		return address, nil
	default:
		return netip.Addr{}, fmt.Errorf("unsupported Windows address family %d", raw.Family)
	}
}

func windowsCurrentCompartmentID() uint32 {
	compartment, _, _ := syscall.SyscallN(windowsProcGetCurrentCompartmentID.Addr())
	return uint32(compartment)
}

func windowsValidateCompartment(key windowsRouteKey) error {
	current := windowsCurrentCompartmentID()
	if key.CompartmentID != current {
		return fmt.Errorf(
			"route belongs to Windows compartment %d, current compartment is %d",
			key.CompartmentID, current,
		)
	}
	return nil
}
