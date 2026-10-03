//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	windowsProcSetIPInterface    = windowsIPHLPAPI.NewProc("SetIpInterfaceEntry")
	windowsProcInitializeAddress = windowsIPHLPAPI.NewProc("InitializeUnicastIpAddressEntry")
	windowsProcCreateAddress     = windowsIPHLPAPI.NewProc("CreateUnicastIpAddressEntry")
)

func (windowsNativeNetworkSystem) ConfigureInterface(ctx context.Context, compartmentID uint32, luid uint64, mtu uint32) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := windowsValidateCompartment(windowsRouteKey{CompartmentID: compartmentID}); err != nil {
		return err
	}
	if luid == 0 || mtu == 0 {
		return errors.New("configure Windows interface: LUID and MTU are required")
	}
	for _, family := range []uint16{windows.AF_INET, windows.AF_INET6} {
		if err := ctx.Err(); err != nil {
			return err
		}
		row := windows.MibIpInterfaceRow{Family: family, InterfaceLuid: luid}
		if err := windows.GetIpInterfaceEntry(&row); err != nil {
			return fmt.Errorf("read Windows IP interface family %d: %w", family, err)
		}
		row.DadTransmits = 0
		row.NlMtu = mtu
		// GetIpInterfaceEntry may return a value rejected by Set for IPv4.
		// Microsoft requires zero SitePrefixLength for the IPv4 family.
		if family == windows.AF_INET {
			row.SitePrefixLength = 0
		}
		status, _, _ := syscall.SyscallN(windowsProcSetIPInterface.Addr(), uintptr(unsafe.Pointer(&row)))
		if status != 0 {
			return fmt.Errorf("configure Windows IP interface family %d: %w", family, syscall.Errno(status))
		}
	}
	return nil
}

func (windowsNativeNetworkSystem) CreateAddress(ctx context.Context, compartmentID uint32, luid uint64, prefix netip.Prefix) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := windowsValidateCompartment(windowsRouteKey{CompartmentID: compartmentID}); err != nil {
		return err
	}
	if luid == 0 || !prefix.IsValid() {
		return errors.New("create Windows address: interface LUID and address prefix are required")
	}
	raw, err := windowsRawAddress(prefix.Addr())
	if err != nil {
		return err
	}
	var row windows.MibUnicastIpAddressRow
	syscall.SyscallN(windowsProcInitializeAddress.Addr(), uintptr(unsafe.Pointer(&row)))
	row.Address = *(*windows.RawSockaddrInet6)(unsafe.Pointer(&raw))
	row.InterfaceLuid = luid
	row.OnLinkPrefixLength = uint8(prefix.Bits())
	row.PrefixOrigin = 1 // IpPrefixOriginManual
	row.SuffixOrigin = 1 // IpSuffixOriginManual
	row.DadState = 4     // IpDadStatePreferred; DAD was disabled before creation.
	status, _, _ := syscall.SyscallN(windowsProcCreateAddress.Addr(), uintptr(unsafe.Pointer(&row)))
	if status != 0 {
		return syscall.Errno(status)
	}
	return nil
}
