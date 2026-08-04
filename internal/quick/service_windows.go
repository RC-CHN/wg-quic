//go:build windows

package quick

import (
	"context"

	"github.com/RC-CHN/wg-quic/internal/platform"
	"golang.org/x/sys/windows/svc"
)

func RunWindowsService(input, requestedName string) error {
	_, name, err := ResolveConfig(input, requestedName, platform.Current())
	if err != nil {
		return err
	}
	return svc.Run(windowsServiceName(name), &windowsQuickService{
		run: func(ctx context.Context, ready func()) error {
			return runWithHostReady(ctx, input, name, platform.Current(), newCoreProcess, ready)
		},
	})
}

type windowsQuickService struct {
	run func(context.Context, func()) error
}

func (s *windowsQuickService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- s.run(ctx, func() { close(ready) })
	}()
	select {
	case err := <-result:
		changes <- svc.Status{State: svc.Stopped}
		if err != nil {
			return false, 1
		}
		return false, 0
	case <-ready:
		changes <- svc.Status{State: svc.Running, Accepts: accepted}
	}
	for {
		select {
		case err := <-result:
			changes <- svc.Status{State: svc.Stopped}
			if err != nil {
				return false, 1
			}
			return false, 0
		case request, ok := <-requests:
			if !ok {
				cancel()
				err := <-result
				changes <- svc.Status{State: svc.Stopped}
				if err != nil {
					return false, 1
				}
				return false, 0
			}
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				err := <-result
				changes <- svc.Status{State: svc.Stopped}
				if err != nil {
					return false, 1
				}
				return false, 0
			}
		}
	}
}
