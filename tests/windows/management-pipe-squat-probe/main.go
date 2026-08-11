//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

const managementPipePath = `\\.\pipe\wg-quic-management-v1`

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "created" && os.Args[1] != "denied") {
		fmt.Fprintln(os.Stderr, "usage: management-pipe-squat-probe created|denied")
		os.Exit(2)
	}
	name, err := windows.UTF16PtrFromString(managementPipePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	handle, err := windows.CreateNamedPipe(
		name,
		windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|
			windows.PIPE_READMODE_BYTE|
			windows.PIPE_WAIT|
			windows.PIPE_REJECT_REMOTE_CLIENTS,
		windows.PIPE_UNLIMITED_INSTANCES,
		64*1024,
		64*1024,
		50,
		nil,
	)
	if err == nil {
		_ = windows.CloseHandle(handle)
		if os.Args[1] != "created" {
			fmt.Fprintln(
				os.Stderr,
				"ordinary user unexpectedly created a management pipe instance",
			)
			os.Exit(1)
		}
		fmt.Println("management pipe instance creation succeeded")
		return
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || os.Args[1] != "denied" {
		fmt.Fprintf(
			os.Stderr,
			"management pipe instance creation returned %v; expected %s\n",
			err, os.Args[1],
		)
		os.Exit(1)
	}
	fmt.Println("management pipe instance creation denied")
}
