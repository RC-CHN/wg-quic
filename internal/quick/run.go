package quick

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/platform"
)

func Run(ctx context.Context, input, requestedName string) error {
	return runWithHost(ctx, input, requestedName, platform.Current(), newCoreProcess)
}

type coreProcess interface {
	Start() error
	Stop() error
	Done() <-chan struct{}
	Err() error
}

type coreProcessFactory func(configPath, name string, fwmark uint32) (coreProcess, error)

func runWithHost(
	ctx context.Context,
	input, requestedName string,
	host platform.Host,
	newProcess coreProcessFactory,
) error {
	configPath, name, err := ResolveConfig(input, requestedName, host)
	if err != nil {
		return err
	}
	cfg, err := config.ParseFile(configPath)
	if err != nil {
		return err
	}
	if err := validateConfig(cfg); err != nil {
		return err
	}
	if err := host.Prepare(ctx, cfg); err != nil {
		return err
	}
	for _, hook := range cfg.Interface.PreUp {
		if err := host.RunHook(ctx, hook, name); err != nil {
			return fmt.Errorf("PreUp: %w", err)
		}
	}
	process, err := newProcess(configPath, name, cfg.Interface.FwMark)
	if err != nil {
		return err
	}
	if err := process.Start(); err != nil {
		return fmt.Errorf("start wg-quic core: %w", err)
	}
	defer func() {
		if err := stopCoreProcess(process); err != nil {
			log.Printf("stop wg-quic core: %v", err)
		}
	}()
	if err := waitForCore(ctx, host.ControlPath(name), name, process); err != nil {
		return err
	}
	networkCleanup, err := host.ConfigureNetwork(ctx, name, cfg)
	defer cleanupHost(context.Background(), host, name, cfg, &networkCleanup)
	if err != nil {
		return err
	}
	for _, hook := range cfg.Interface.PostUp {
		if err := host.RunHook(ctx, hook, name); err != nil {
			return fmt.Errorf("PostUp: %w", err)
		}
	}
	log.Printf("wg-quic-quick configured host policy for interface %s", name)
	select {
	case <-ctx.Done():
		return nil
	case <-process.Done():
		if err := process.Err(); err != nil {
			return fmt.Errorf("wg-quic core exited: %w", err)
		}
		return errors.New("wg-quic core exited unexpectedly")
	}
}

func waitForCore(ctx context.Context, path, name string, process coreProcess) error {
	readyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		status, err := control.Read(path)
		if err == nil && status.Interface == name && status.State == "up" {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("core status belongs to interface %q in state %q", status.Interface, status.State)
		}
		select {
		case <-readyCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("wait for wg-quic core readiness: %w", lastErr)
		case <-process.Done():
			if err := process.Err(); err != nil {
				return fmt.Errorf("wg-quic core exited before readiness: %w", err)
			}
			return errors.New("wg-quic core exited before readiness")
		case <-ticker.C:
		}
	}
}

func stopCoreProcess(process coreProcess) error {
	select {
	case <-process.Done():
		return process.Err()
	default:
	}
	if err := process.Stop(); err != nil {
		return err
	}
	<-process.Done()
	return nil
}

func cleanupHost(ctx context.Context, host platform.Host, name string, cfg *config.Config, cleanup *platform.Cleanup) {
	for _, hook := range cfg.Interface.PreDown {
		if err := host.RunHook(ctx, hook, name); err != nil {
			log.Printf("PreDown: %v", err)
		}
	}
	if cleanup != nil && *cleanup != nil {
		if err := (*cleanup)(ctx); err != nil {
			log.Printf("platform network cleanup: %v", err)
		}
	}
	for _, hook := range cfg.Interface.PostDown {
		if err := host.RunHook(ctx, hook, name); err != nil {
			log.Printf("PostDown: %v", err)
		}
	}
}
