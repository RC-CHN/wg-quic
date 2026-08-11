//go:build windows

package quick

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/RC-CHN/wg-quic/internal/platform"
	"golang.org/x/sys/windows/svc"
)

const (
	defaultWindowsServiceShutdownTimeout = 25 * time.Second
	defaultWindowsServiceHeartbeat       = time.Second
	windowsServiceWaitHint               = 5 * time.Second
)

func RunWindowsService(input, requestedName string) error {
	_, name, err := ResolveConfig(input, requestedName, platform.Current())
	if err != nil {
		return err
	}
	return svc.Run(windowsServiceName(name), &windowsQuickService{
		run: func(
			ctx context.Context,
			ready func(),
			shutdownProgress func(string),
		) error {
			return runWithHostReadyProgress(
				ctx, input, name, platform.Current(), newCoreProcess,
				ready, shutdownProgress,
			)
		},
	})
}

type windowsQuickService struct {
	run               func(context.Context, func(), func(string)) error
	shutdownTimeout   time.Duration
	heartbeatInterval time.Duration
	logf              func(string, ...any)
}

func (s *windowsQuickService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	progress := make(chan string, 16)
	var readyOnce sync.Once
	result := make(chan error, 1)
	go func() {
		result <- s.run(
			ctx,
			func() { readyOnce.Do(func() { close(ready) }) },
			func(stage string) {
				select {
				case progress <- stage:
				default:
				}
			},
		)
	}()
	select {
	case err := <-result:
		return s.finish(err)
	case <-ready:
		changes <- svc.Status{State: svc.Running, Accepts: accepted}
	}
	for {
		select {
		case err := <-result:
			return s.finish(err)
		case request, ok := <-requests:
			if !ok {
				return s.shutdown(
					cancel, nil, result, progress, changes,
				)
			}
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				return s.shutdown(
					cancel, requests, result, progress, changes,
				)
			}
		}
	}
}

func (s *windowsQuickService) shutdown(
	cancel context.CancelFunc,
	requests <-chan svc.ChangeRequest,
	result <-chan error,
	progress <-chan string,
	changes chan<- svc.Status,
) (bool, uint32) {
	timeout := s.shutdownTimeout
	if timeout <= 0 {
		timeout = defaultWindowsServiceShutdownTimeout
	}
	heartbeat := s.heartbeatInterval
	if heartbeat <= 0 {
		heartbeat = defaultWindowsServiceHeartbeat
	}
	stage := "cancellation requested"
	stageIndex := uint32(1)
	checkpoint := stageIndex * 100
	status := func() svc.Status {
		return svc.Status{
			State:      svc.StopPending,
			CheckPoint: checkpoint,
			WaitHint:   uint32(windowsServiceWaitHint / time.Millisecond),
		}
	}
	changes <- status()
	s.logger()(
		"Windows service shutdown stage=%q checkpoint=%d timeout=%s",
		stage, checkpoint, timeout,
	)
	cancel()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case err := <-result:
			return s.finish(err)
		case next := <-progress:
			if index := windowsShutdownStageIndex(next); index > stageIndex {
				stage = next
				stageIndex = index
				checkpoint = stageIndex * 100
				s.logger()(
					"Windows service shutdown stage=%q checkpoint=%d",
					stage, checkpoint,
				)
				changes <- status()
			}
		case <-ticker.C:
			if checkpoint < stageIndex*100+99 {
				checkpoint++
			}
			changes <- status()
		case request, ok := <-requests:
			if !ok {
				requests = nil
				continue
			}
			if request.Cmd == svc.Interrogate {
				changes <- status()
			}
		case <-timer.C:
			s.logger()(
				"Windows service shutdown timed out after %s: stage=%q checkpoint=%d wait_hint=%dms",
				timeout, stage, checkpoint,
				uint32(windowsServiceWaitHint/time.Millisecond),
			)
			return false, 1
		}
	}
}

func (s *windowsQuickService) finish(
	err error,
) (bool, uint32) {
	if err != nil {
		s.logger()("Windows service stopped with cleanup error: %v", err)
		return false, 1
	}
	return false, 0
}

func (s *windowsQuickService) logger() func(string, ...any) {
	if s.logf != nil {
		return s.logf
	}
	return log.Printf
}

func windowsShutdownStageIndex(stage string) uint32 {
	switch stage {
	case shutdownStageStopRefresh:
		return 2
	case shutdownStagePreDown:
		return 3
	case shutdownStageNetwork:
		return 4
	case shutdownStagePostDown:
		return 5
	case shutdownStageEndpoint:
		return 6
	case shutdownStageCore:
		return 7
	case shutdownStageComplete:
		return 8
	default:
		return 1
	}
}

func windowsShutdownStageFromCheckpoint(checkpoint uint32) string {
	switch checkpoint / 100 {
	case 1:
		return "cancellation requested"
	case 2:
		return shutdownStageStopRefresh
	case 3:
		return shutdownStagePreDown
	case 4:
		return shutdownStageNetwork
	case 5:
		return shutdownStagePostDown
	case 6:
		return shutdownStageEndpoint
	case 7:
		return shutdownStageCore
	case 8:
		return shutdownStageComplete
	default:
		return "unknown"
	}
}
