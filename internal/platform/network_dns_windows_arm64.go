//go:build windows && arm64

package platform

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func callWindowsSetDNSSettings(proc uintptr, guid *windows.GUID, settings *windowsDNSInterfaceSettings) uintptr {
	// The Windows ARM64 ABI passes the GUID value in two 64-bit registers.
	words := *(*[2]uintptr)(unsafe.Pointer(guid))
	status, _, _ := syscall.SyscallN(proc, words[0], words[1], uintptr(unsafe.Pointer(settings)))
	return status
}
