//go:build windows

package quick

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type execCoreProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	job  windows.Handle

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
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create wg-quic core job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("configure wg-quic core job object: %w", err)
	}
	if err := p.cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return err
	}
	processHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(p.cmd.Process.Pid),
	)
	if err != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
		windows.CloseHandle(job)
		return fmt.Errorf("open wg-quic core process for job assignment: %w", err)
	}
	if err := windows.AssignProcessToJobObject(
		job,
		processHandle,
	); err != nil {
		windows.CloseHandle(processHandle)
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
		windows.CloseHandle(job)
		return fmt.Errorf("assign wg-quic core process to job object: %w", err)
	}
	windows.CloseHandle(processHandle)
	p.job = job
	go func() {
		err := p.cmd.Wait()
		if p.job != 0 {
			_ = windows.CloseHandle(p.job)
		}
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

func (p *execCoreProcess) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}
