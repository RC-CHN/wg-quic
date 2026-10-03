//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	windowsProcConvertLUIDToGUID = windowsIPHLPAPI.NewProc("ConvertInterfaceLuidToGuid")
	windowsProcSetDNSSettings    = windowsIPHLPAPI.NewProc("SetInterfaceDnsSettings")
	errWindowsDNSAPIUnavailable  = errors.New("native Windows DNS settings API unavailable")
)

const (
	windowsDNSSettingsIPv6       = 0x0001
	windowsDNSSettingsNameServer = 0x0002
	windowsDNSSettingsDomain     = 0x0020
)

// DNS_INTERFACE_SETTINGS version 1 from netioapi.h. Flags is aligned to eight
// bytes even on 32-bit Windows. Unselected fields must remain zero.
type windowsDNSInterfaceSettings struct {
	Version             uint32
	_                   [4]byte
	Flags               uint64
	Domain              *uint16
	NameServer          *uint16
	SearchList          *uint16
	RegistrationEnabled uint32
	RegisterAdapterName uint32
	EnableLLMNR         uint32
	QueryAdapterName    uint32
	ProfileNameServer   *uint16
}

func windowsInterfaceGUID(luid uint64) (windows.GUID, error) {
	var guid windows.GUID
	if luid == 0 {
		return guid, errors.New("resolve Windows interface GUID: empty LUID")
	}
	status, _, _ := syscall.SyscallN(windowsProcConvertLUIDToGUID.Addr(), uintptr(unsafe.Pointer(&luid)), uintptr(unsafe.Pointer(&guid)))
	if status != 0 {
		return guid, fmt.Errorf("resolve Windows interface LUID %d GUID: %w", luid, syscall.Errno(status))
	}
	return guid, nil
}

func (windowsNativeNetworkSystem) ResetDNS(ctx context.Context, compartmentID uint32, luid uint64) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := windowsValidateCompartment(windowsRouteKey{CompartmentID: compartmentID}); err != nil {
		return err
	}
	if err := windowsProcSetDNSSettings.Find(); err != nil {
		if errors.Is(err, windows.ERROR_PROC_NOT_FOUND) {
			return fmt.Errorf("%w: %v", errWindowsDNSAPIUnavailable, err)
		}
		return err
	}
	guid, err := windowsInterfaceGUID(luid)
	if err != nil {
		return err
	}
	return resetWindowsDNSSettings(ctx, func(settings *windowsDNSInterfaceSettings) error {
		status := callWindowsSetDNSSettings(windowsProcSetDNSSettings.Addr(), &guid, settings)
		if status != 0 {
			return syscall.Errno(status)
		}
		return nil
	})
}

func resetWindowsDNSSettings(ctx context.Context, set func(*windowsDNSInterfaceSettings) error) error {
	var empty [1]uint16
	var errs []error
	for _, family := range []string{"IPv4", "IPv6"} {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		settings := windowsDNSInterfaceSettings{
			Version: 1, Flags: windowsDNSSettingsNameServer,
			NameServer: &empty[0],
		}
		if family == "IPv4" {
			// Match ResetConnectionSpecificSuffix, which resets Domain. The
			// separate SearchList setting was never changed by our apply.
			settings.Flags |= windowsDNSSettingsDomain
			settings.Domain = &empty[0]
		} else {
			settings.Flags |= windowsDNSSettingsIPv6
		}
		if err := set(&settings); err != nil {
			errs = append(errs, fmt.Errorf("%s DNS settings: %w", family, err))
		}
	}
	return errors.Join(errs...)
}
