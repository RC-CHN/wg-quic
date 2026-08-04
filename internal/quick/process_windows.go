//go:build windows

package quick

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

type execCoreProcess struct {
	cmd  *exec.Cmd
	done chan struct{}

	mu  sync.Mutex
	err error
}

func newCoreProcess(launch coreLaunch) (coreProcess, error) {
	return newWindowsCoreProcess(launch, os.Stdout)
}

func newWindowsCoreProcess(
	launch coreLaunch,
	output io.Writer,
) (coreProcess, error) {
	executable, err := coreExecutable()
	if err != nil {
		return nil, err
	}
	cmd, err := coreCommand(executable, launch)
	if err != nil {
		return nil, err
	}
	cmd.Stdout = output
	cmd.Stderr = output
	return &execCoreProcess{cmd: cmd, done: make(chan struct{})}, nil
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
	err := p.cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("terminate wg-quic core: %w", err)
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
