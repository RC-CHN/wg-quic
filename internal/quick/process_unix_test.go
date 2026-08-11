//go:build freebsd

package quick

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

const coreParentDeathTestRole = "WG_QUIC_TEST_CORE_PARENT_DEATH_ROLE"

func TestConfigureCoreProcessTerminatesWithSupervisor(t *testing.T) {
	cmd := exec.Command("wg-quic")
	configureCoreProcess(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("core process attributes were not configured")
	}
	if got := cmd.SysProcAttr.Pdeathsig; got != syscall.SIGTERM {
		t.Fatalf("core parent-death signal = %v, want %v", got, syscall.SIGTERM)
	}
}

func TestCoreProcessTerminatesWithSupervisor(t *testing.T) {
	switch os.Getenv(coreParentDeathTestRole) {
	case "core":
		time.Sleep(time.Minute)
		return
	case "supervisor":
		lifetime := os.NewFile(3, "core-lifetime")
		if lifetime == nil {
			t.Fatal("core lifetime descriptor is unavailable")
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestCoreProcessTerminatesWithSupervisor$")
		cmd.Env = append(os.Environ(), coreParentDeathTestRole+"=core")
		cmd.ExtraFiles = []*os.File{lifetime}
		configureCoreProcess(cmd)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if err := lifetime.Close(); err != nil {
			t.Fatal(err)
		}
		fmt.Println(cmd.Process.Pid)
		time.Sleep(time.Minute)
		return
	}

	lifetimeReader, lifetimeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer lifetimeReader.Close()
	helper := exec.Command(os.Args[0], "-test.run=^TestCoreProcessTerminatesWithSupervisor$")
	helper.Env = append(os.Environ(), coreParentDeathTestRole+"=supervisor")
	helper.ExtraFiles = []*os.File{lifetimeWriter}
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	helper.Stderr = os.Stderr
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	if err := lifetimeWriter.Close(); err != nil {
		t.Fatal(err)
	}

	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- scanner.Text()
			return
		}
		ready <- ""
	}()
	var corePID int
	select {
	case line := <-ready:
		corePID, err = strconv.Atoi(line)
		if err != nil {
			_ = helper.Process.Kill()
			_ = helper.Wait()
			t.Fatalf("invalid core PID %q: %v", line, err)
		}
	case <-time.After(5 * time.Second):
		_ = helper.Process.Kill()
		_ = helper.Wait()
		t.Fatal("timed out waiting for supervised core process")
	}
	coreExited := false
	defer func() {
		if !coreExited {
			_ = syscall.Kill(corePID, syscall.SIGKILL)
		}
	}()

	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()

	closed := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, lifetimeReader)
		closed <- err
	}()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("read core lifetime descriptor: %v", err)
		}
		coreExited = true
	case <-time.After(5 * time.Second):
		t.Fatalf("core process %d survived its supervisor", corePID)
	}
}
