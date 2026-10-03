//go:build windows && 386

package platform

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func callWindowsSetDNSSettings(proc uintptr, guid *windows.GUID, settings *windowsDNSInterfaceSettings) uintptr {
	// The Windows x86 ABI passes the GUID value in four stack words.
	words := *(*[4]uintptr)(unsafe.Pointer(guid))
	status, _, _ := syscall.SyscallN(proc, words[0], words[1], words[2], words[3], uintptr(unsafe.Pointer(settings)))
	return status
}
