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
}

type coreProcessFactory func(coreLaunch) (coreProcess, error)

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
		runLog{logger: log.Default()},
	)
}

func runWithHostReadyLog(
	ctx context.Context,
	input, requestedName string,
	host platform.Host,
	newProcess coreProcessFactory,
	ready func(),
	runLogger runLog,
) error {
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
	defer func() {
		if err := stopCoreProcess(process); err != nil {
			runLogger.logger.Printf("stop wg-quic core: %v", err)
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
	defer func() {
		if err := supervisor.Close(context.Background()); err != nil {
			runLogger.logger.Printf("close endpoint supervisor: %v", err)
		}
	}()
	selected, err := supervisor.Initialize(ctx)
	if err != nil {
		return err
	}
	runtimeConfig := cfg.Clone()
	for index := range runtimeConfig.Peers {
		if selectedEndpoint, ok := selected[runtimeConfig.Peers[index].PublicKey]; ok {
			runtimeConfig.Peers[index].Endpoint = selectedEndpoint.String()
		}
	}
	runLogger.debugf("applying platform address, route, MTU, and DNS policy")
	networkCleanup, err := host.ConfigureNetwork(ctx, name, runtimeConfig)
	defer func() {
		supervisor.Stop()
		cleanupHost(context.Background(), host, name, runtimeConfig, &networkCleanup, runLogger)
	}()
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

func cleanupHost(
	ctx context.Context,
	host platform.Host,
	name string,
	cfg *config.Config,
	cleanup *platform.Cleanup,
	runLogger runLog,
) {
	for index, hook := range cfg.Interface.PreDown {
		runLogger.debugf("running PreDown hook %d", index+1)
		if err := host.RunHook(ctx, hook, name); err != nil {
			runLogger.logger.Printf("PreDown: %v", err)
		}
	}
	if cleanup != nil && *cleanup != nil {
		runLogger.debugf("rolling back platform network policy")
		if err := (*cleanup)(ctx); err != nil {
			runLogger.logger.Printf("platform network cleanup: %v", err)
		}
	}
	for index, hook := range cfg.Interface.PostDown {
		runLogger.debugf("running PostDown hook %d", index+1)
		if err := host.RunHook(ctx, hook, name); err != nil {
			runLogger.logger.Printf("PostDown: %v", err)
		}
	}
}
