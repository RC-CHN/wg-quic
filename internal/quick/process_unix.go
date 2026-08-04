//go:build linux || freebsd

package quick

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
)

type execCoreProcess struct {
	cmd  *exec.Cmd
	done chan struct{}

	mu  sync.Mutex
	err error
}

func newCoreProcess(configPath, name string, fwmark uint32) (coreProcess, error) {
	executable, err := coreExecutable()
	if err != nil {
		return nil, err
	}
	args := []string{"run", configPath, "--name", name}
	if fwmark != 0 {
		args = append(args, "--fwmark", strconv.FormatUint(uint64(fwmark), 10))
	}
	cmd := exec.Command(executable, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return &execCoreProcess{cmd: cmd, done: make(chan struct{})}, nil
}

func coreExecutable() (string, error) {
	current, err := os.Executable()
	if err == nil {
		sibling := filepath.Join(filepath.Dir(current), "wg-quic")
		if info, statErr := os.Stat(sibling); statErr == nil && !info.IsDir() {
			return sibling, nil
		}
	}
	path, err := exec.LookPath("wg-quic")
	if err != nil {
		return "", errors.New("cannot find the wg-quic core executable next to wg-quic-quick or in PATH")
	}
	return path, nil
}

func (p *execCoreProcess) Start() error {
	if err := p.cmd.Start(); err != nil {
		return err
	}
	go func() {
		err := p.cmd.Wait()
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		close(p.done)
	}()
	return nil
}

func (p *execCoreProcess) Stop() error {
	if p.cmd.Process == nil {
		return errors.New("wg-quic core process was not started")
	}
	err := p.cmd.Process.Signal(syscall.SIGTERM)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("signal wg-quic core: %w", err)
	}
	return nil
}

func (p *execCoreProcess) Done() <-chan struct{} {
	return p.done
}

func (p *execCoreProcess) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}
