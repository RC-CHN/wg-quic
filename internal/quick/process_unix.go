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
	cmd            *exec.Cmd
	done           chan struct{}
	lifetimeReader *os.File
	lifetimeWriter *os.File
	lifetimeOnce   sync.Once

	mu  sync.Mutex
	err error
}

func newCoreProcess(launch coreLaunch) (coreProcess, error) {
	executable, err := coreExecutable()
	if err != nil {
		return nil, err
	}
	return newUnixCoreProcess(executable, launch)
}

func newUnixCoreProcess(executable string, launch coreLaunch) (*execCoreProcess, error) {
	lifetimeReader, lifetimeWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create core supervisor lifetime pipe: %w", err)
	}
	launch.SupervisorFD = 3
	cmd, err := coreCommand(executable, launch)
	if err != nil {
		_ = lifetimeReader.Close()
		_ = lifetimeWriter.Close()
		return nil, err
	}
	cmd.ExtraFiles = append(cmd.ExtraFiles, lifetimeReader)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	configureCoreProcess(cmd)
	return &execCoreProcess{
		cmd: cmd, done: make(chan struct{}),
		lifetimeReader: lifetimeReader, lifetimeWriter: lifetimeWriter,
	}, nil
}

func (p *execCoreProcess) Start() error {
	if err := p.cmd.Start(); err != nil {
		p.closeLifetime()
		_ = p.lifetimeReader.Close()
		return err
	}
	if err := p.lifetimeReader.Close(); err != nil {
		p.closeLifetime()
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
		return fmt.Errorf("close parent copy of core lifetime reader: %w", err)
	}
	p.lifetimeReader = nil
	go func() {
		err := p.cmd.Wait()
		p.closeLifetime()
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
	p.closeLifetime()
	err := p.cmd.Process.Signal(syscall.SIGTERM)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("signal wg-quic core: %w", err)
	}
	return nil
}

func (p *execCoreProcess) closeLifetime() {
	p.lifetimeOnce.Do(func() {
		if p.lifetimeWriter != nil {
			_ = p.lifetimeWriter.Close()
		}
	})
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
