//go:build linux

package quick

import "os/exec"

func configureCoreProcess(*exec.Cmd) {
	// Linux services use systemd's default control-group kill mode. Do not use
	// SysProcAttr.Pdeathsig here: Linux attaches it to the creating thread, and
	// a Go runtime thread can end while the supervisor process is still alive.
	// The inherited lifetime pipe in process_unix.go covers systemd, OpenRC,
	// procd, and direct supervisors without that thread-lifetime race.
}
