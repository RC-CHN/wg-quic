//go:build linux

package quick

import (
	"os/exec"
	"testing"
)

func TestConfigureCoreProcessLeavesLinuxLifetimeToServiceManager(t *testing.T) {
	cmd := exec.Command("wg-quic")
	configureCoreProcess(cmd)
	if cmd.SysProcAttr != nil {
		t.Fatalf("Linux core process attributes = %#v, want nil", cmd.SysProcAttr)
	}
}
