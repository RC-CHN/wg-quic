package quick

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/control"
	"github.com/RC-CHN/wg-quic/internal/endpoint"
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
	PID() int
}

type coreProcessFactory func(coreLaunch) (coreProcess, error)

const (
	quickShutdownTimeout = 20 * time.Second
	coreStopTimeout      = 7 * time.Second
)

const (
	shutdownStageStopRefresh = "stopping endpoint refresh"
	shutdownStagePreDown     = "running PreDown hooks"
	shutdownStageNetwork     = "rolling back host network"
	shutdownStagePostDown    = "running PostDown hooks"
	shutdownStageEndpoint    = "releasing endpoint route leases"
	shutdownStageCore        = "stopping core process"
	shutdownStageComplete    = "shutdown complete"
)

type runLog struct {
	logger *log.Logger
	debug  bool
}

func (l runLog) debugf(format string, args ...any) {
	if l.debug {
		l.logger.Printf("debug "+format, args...)
	}
}

func runWithHost(
	ctx context.Context,
	input, requestedName string,
	host platform.Host,
	newProcess coreProcessFactory,
) error {
	return runWithHostReady(ctx, input, requestedName, host, newProcess, nil)
}

func runWithHostReady(
	ctx context.Context,
	input, requestedName string,
	host platform.Host,
	newProcess coreProcessFactory,
	ready func(),
) error {
	return runWithHostReadyLog(
		ctx, input, requestedName, host, newProcess, ready,
		nil,
		runLog{logger: log.Default()},
	)
}

func runWithHostReadyProgress(
	ctx context.Context,
	input, requestedName string,
	host platform.Host,
	newProcess coreProcessFactory,
	ready func(),
	shutdownProgress func(string),
) error {
	return runWithHostReadyLog(
		ctx, input, requestedName, host, newProcess, ready,
		shutdownProgress,
		runLog{logger: log.Default()},
	)
}

func runWithHostReadyLog(
	ctx context.Context,
	input, requestedName string,
	host platform.Host,
	newProcess coreProcessFactory,
	ready func(),
	shutdownProgress func(string),
	runLogger runLog,
) (runErr error) {
	if runLogger.logger == nil {
		runLogger.logger = log.Default()
	}
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
	// Resolve the automatic value once before creating the immutable core
	// snapshot and applying host policy.
	cfg.Interface.MTU = cfg.EffectiveMTU()
	runLogger.debugf(
		"quick configuration: interface=%q config=%q addresses=%v dns=%v mtu=%d table=%q peers=%d hooks=%d/%d/%d/%d",
		name, configPath, cfg.Interface.Addresses, cfg.Interface.DNS, cfg.Interface.MTU,
		cfg.Interface.Table, len(cfg.Peers), len(cfg.Interface.PreUp), len(cfg.Interface.PostUp),
		len(cfg.Interface.PreDown), len(cfg.Interface.PostDown),
	)
	for index, peer := range cfg.Peers {
		runLogger.debugf(
			"quick peer %d: endpoint=%q allowed_ips=%v persistent_keepalive=%d preshared_key_present=%t",
			index+1, peer.Endpoint, peer.AllowedIPs, peer.PersistentKeepalive, peer.PresharedKey != "",
		)
	}
	runLogger.debugf("preparing platform")
	if err := host.Prepare(ctx, cfg); err != nil {
		return err
	}
	for index, hook := range cfg.Interface.PreUp {
		runLogger.debugf("running PreUp hook %d", index+1)
		if err := host.RunHook(ctx, hook, name); err != nil {
			return fmt.Errorf("PreUp: %w", err)
		}
	}
	snapshot, err := config.MarshalSnapshot(cfg)
	if err != nil {
		return err
	}
	runLogger.debugf("starting wg-quic core process")
	process, err := newProcess(coreLaunch{
		ConfigPath: configPath, Name: name, Snapshot: snapshot, DeferEndpoints: true,
	})
	if err != nil {
		return err
	}
	if err := process.Start(); err != nil {
		return fmt.Errorf("start wg-quic core: %w", err)
	}
	fallbackProcess := process
	supervisorOwnsShutdown := false
	defer func() {
		if supervisorOwnsShutdown {
			return
		}
		if err := shutdownQuickRuntime(
			host, name, cfg, fallbackProcess, nil, nil, false,
			shutdownProgress, runLogger,
		); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	runLogger.debugf("waiting for core readiness on %q", host.ControlPath(name))
	if err := waitForCore(ctx, host.ControlPath(name), name, "prepared", process); err != nil {
		return err
	}
	runLogger.debugf("core is prepared; resolving peer endpoints and acquiring outer routes")
	routeLeaser, err := host.NewEndpointRouteLeaser(ctx, name, cfg)
	if err != nil {
		return err
	}
	specs := make([]endpoint.PeerSpec, 0, len(cfg.Peers))
	for _, peer := range cfg.Peers {
		specs = append(specs, endpoint.PeerSpec{
			PublicKey: peer.PublicKey, Endpoint: peer.Endpoint,
		})
	}
	supervisor, err := endpoint.NewSupervisor(
		specs,
		endpoint.SystemResolver{},
		routeLeaser,
		localCoreEndpointControl{client: control.NewClient(host.ControlPath(name))},
		endpoint.Options{Logf: runLogger.debugf},
	)
	if err != nil {
		_ = routeLeaser.Close()
		return err
	}
	var networkCleanup platform.Cleanup
	hostCleanupArmed := false
	runtimeProcess := process
	supervisorOwnsShutdown = true
	defer func() {
		if err := shutdownQuickRuntime(
			host, name, cfg, runtimeProcess, supervisor, &networkCleanup,
			hostCleanupArmed, shutdownProgress, runLogger,
		); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	if _, err := supervisor.Initialize(ctx); err != nil {
		return err
	}
	runLogger.debugf("applying platform address, route, MTU, and DNS policy")
	hostCleanupArmed = true
	networkCleanup, err = host.ConfigureNetwork(ctx, name, cfg)
	if err != nil {
		return err
	}
	runLogger.debugf("activating prepared core with numeric peer endpoints")
	if err := supervisor.Activate(ctx); err != nil {
		return err
	}
	runLogger.debugf("platform network policy applied")
	for index, hook := range cfg.Interface.PostUp {
		runLogger.debugf("running PostUp hook %d", index+1)
		if err := host.RunHook(ctx, hook, name); err != nil {
			return fmt.Errorf("PostUp: %w", err)
		}
	}
	if ready != nil {
		ready()
	}
	runLogger.logger.Printf("wg-quic-quick configured host policy for interface %s", name)
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

func shutdownQuickRuntime(
	host platform.Host,
	name string,
	cfg *config.Config,
	process coreProcess,
	supervisor *endpoint.Supervisor,
	networkCleanup *platform.Cleanup,
	hostCleanupArmed bool,
	progress func(string),
	runLogger runLog,
) error {
	if process == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		quickShutdownTimeout,
	)
	defer cancel()
	report := func(stage string) {
		runLogger.debugf("shutdown: %s", stage)
		if progress != nil {
			progress(stage)
		}
	}
	var errs []error
	refreshStopped := true
	if supervisor != nil {
		report(shutdownStageStopRefresh)
		if err := supervisor.StopContext(shutdownCtx); err != nil {
			refreshStopped = false
			runLogger.logger.Printf("stop endpoint supervisor: %v", err)
			errs = append(errs, fmt.Errorf("stop endpoint refresh: %w", err))
		}
	}
	if hostCleanupArmed {
		if err := cleanupHost(
			shutdownCtx, host, name, cfg, networkCleanup, report, runLogger,
		); err != nil {
			errs = append(errs, err)
		}
	}
	if supervisor != nil && refreshStopped {
		report(shutdownStageEndpoint)
		if err := supervisor.Close(shutdownCtx); err != nil {
			runLogger.logger.Printf("close endpoint supervisor: %v", err)
			errs = append(errs, fmt.Errorf("release endpoint routes: %w", err))
		}
	} else if supervisor != nil {
		runLogger.logger.Printf(
			"endpoint refresh did not stop before the shutdown deadline; retaining route leases for startup reconciliation",
		)
	}
	report(shutdownStageCore)
	coreCtx, cancelCore := context.WithTimeout(shutdownCtx, coreStopTimeout)
	if err := stopCoreProcess(coreCtx, process); err != nil {
		runLogger.logger.Printf("stop wg-quic core: %v", err)
		errs = append(errs, err)
	}
	cancelCore()
	report(shutdownStageComplete)
	return errors.Join(errs...)
}

func waitForCore(ctx context.Context, path, name, wantState string, process coreProcess) error {
	readyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		status, err := control.Read(path)
		if err == nil && status.Interface == name && status.State == wantState {
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

func stopCoreProcess(ctx context.Context, process coreProcess) error {
	select {
	case <-process.Done():
		return process.Err()
	default:
	}
	if err := process.Stop(); err != nil {
		return err
	}
	select {
	case <-process.Done():
		return nil
	case <-ctx.Done():
		return fmt.Errorf(
			"wait for wg-quic core process pid %d after termination during shutdown: %w",
			process.PID(), ctx.Err(),
		)
	}
}

func cleanupHost(
	ctx context.Context,
	host platform.Host,
	name string,
	cfg *config.Config,
	cleanup *platform.Cleanup,
	progress func(string),
	runLogger runLog,
) error {
	var errs []error
	progress(shutdownStagePreDown)
	for index, hook := range cfg.Interface.PreDown {
		runLogger.debugf("running PreDown hook %d", index+1)
		if err := host.RunHook(ctx, hook, name); err != nil {
			runLogger.logger.Printf("PreDown: %v", err)
			errs = append(errs, fmt.Errorf("PreDown hook %d: %w", index+1, err))
		}
	}
	progress(shutdownStageNetwork)
	if cleanup != nil && *cleanup != nil {
		runLogger.debugf("rolling back platform network policy")
		if err := (*cleanup)(ctx); err != nil {
			runLogger.logger.Printf("platform network cleanup: %v", err)
			errs = append(errs, fmt.Errorf("platform network cleanup: %w", err))
		}
	}
	progress(shutdownStagePostDown)
	for index, hook := range cfg.Interface.PostDown {
		runLogger.debugf("running PostDown hook %d", index+1)
		if err := host.RunHook(ctx, hook, name); err != nil {
			runLogger.logger.Printf("PostDown: %v", err)
			errs = append(errs, fmt.Errorf("PostDown hook %d: %w", index+1, err))
		}
	}
	return errors.Join(errs...)
}
