//go:build freebsd

package quick

import (
	"os/exec"
	"syscall"
)

// configureCoreProcess makes the data-plane process follow the lifetime of
// wg-quic-quick. On FreeBSD, a supervisor killed with SIGKILL cannot run its
// normal cleanup and an orphaned core keeps the tun device open indefinitely.
func configureCoreProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
