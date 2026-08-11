//go:build linux || freebsd

package quick

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

type execCoreProcess struct {
	cmd  *exec.Cmd
	done chan struct{}

	mu  sync.Mutex
	err error
}

func newCoreProcess(launch coreLaunch) (coreProcess, error) {
	executable, err := coreExecutable()
	if err != nil {
		return nil, err
	}
	cmd, err := coreCommand(executable, launch)
	if err != nil {
		return nil, err
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	configureCoreProcess(cmd)
	return &execCoreProcess{cmd: cmd, done: make(chan struct{})}, nil
}

// configureCoreProcess makes the data-plane process follow the lifetime of
// wg-quic-quick. This matters in particular on FreeBSD, where a supervisor
// killed with SIGKILL cannot run its normal cleanup and an orphaned core keeps
// the tun device open indefinitely.
func configureCoreProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
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

func (p *execCoreProcess) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}
