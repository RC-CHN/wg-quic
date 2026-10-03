//go:build windows && amd64

package platform

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func callWindowsSetDNSSettings(proc uintptr, guid *windows.GUID, settings *windowsDNSInterfaceSettings) uintptr {
	// The Windows x64 ABI passes this 16-byte GUID value indirectly.
	status, _, _ := syscall.SyscallN(proc, uintptr(unsafe.Pointer(guid)), uintptr(unsafe.Pointer(settings)))
	return status
}
