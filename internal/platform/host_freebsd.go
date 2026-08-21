//go:build freebsd

package platform

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/endpoint"
	"github.com/RC-CHN/wg-quic/internal/platformenv"
)

type freeBSDHost struct {
	platformenv.Paths
}

type freeBSDNetworkState struct {
	undo []hostCommand
}

func Current() Host {
	return freeBSDHost{}
}

func (freeBSDHost) Prepare(context.Context, *config.Config) error { return nil }

func (freeBSDHost) NewEndpointRouteLeaser(
	ctx context.Context,
	name string,
	cfg *config.Config,
) (endpoint.RouteLeaser, error) {
	need4, need6 := false, false
	if usesFreeBSDAutomaticDefaultRoute(cfg) {
		need4, need6 = freeBSDDefaultFamilies(cfg)
	}
	manager := &freeBSDEndpointRouteLeaser{
		need4: need4, need6: need6,
		entries: make(map[netip.Addr]*freeBSDEndpointRouteEntry),
		name:    name, ledgerPath: "/var/run/wg-quic/" + name + ".endpoint-routes.json",
	}
	if err := manager.recover(ctx); err != nil {
		return nil, err
	}
	return manager, nil
}

func (freeBSDHost) NewPeerRouteManager(
	_ context.Context,
	name string,
	cfg *config.Config,
) (PeerRouteManager, error) {
	return newCommandPeerRouteManager(
		cfg,
		func(prefix netip.Prefix) ([]hostOperation, error) {
			projection := cfg.Clone()
			projection.Peers = []config.Peer{{AllowedIPs: []netip.Prefix{prefix}}}
			return freeBSDRouteOperations(name, projection), nil
		},
		func(ctx context.Context, command hostCommand) error {
			return run(ctx, command.name, command.args...)
		},
	)
}

func (freeBSDHost) ConfigureNetwork(ctx context.Context, name string, cfg *config.Config) (Cleanup, error) {
	state := &freeBSDNetworkState{}
	cleanup := func(cleanupCtx context.Context) error {
		var errs []error
		for i := len(state.undo) - 1; i >= 0; i-- {
			item := state.undo[i]
			if err := run(cleanupCtx, item.name, item.args...); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	for _, address := range cfg.Interface.Addresses {
		family := "inet"
		if address.Addr().Is6() {
			family = "inet6"
		}
		if err := run(ctx, "ifconfig", name, family, address.String(), "alias"); err != nil {
			return cleanup, err
		}
	}
	if err := run(ctx, "ifconfig", name, "up"); err != nil {
		return cleanup, err
	}
	for _, operation := range freeBSDRouteOperations(name, cfg) {
		if err := run(ctx, operation.apply.name, operation.apply.args...); err != nil {
			return cleanup, err
		}
		state.undo = append(state.undo, operation.undo)
	}
	if err := configureFreeBSDDNS(ctx, name, cfg, state); err != nil {
		return cleanup, err
	}
	return cleanup, nil
}

func (freeBSDHost) RunHook(ctx context.Context, hook, name string) error {
	hook = strings.ReplaceAll(hook, "%i", name)
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", hook)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

type freeBSDGateway struct {
	address       string
	interfaceName string
}

func freeBSDDefaultGateway(ctx context.Context, ipv6 bool) (freeBSDGateway, error) {
	family := "-inet"
	if ipv6 {
		family = "-inet6"
	}
	output, err := runOutput(ctx, "route", "-n", "get", family, "default")
	if err != nil {
		return freeBSDGateway{}, fmt.Errorf("query FreeBSD %s default route: %w", family, err)
	}
	var result freeBSDGateway
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "gateway":
			result.address = fields[1]
		case "interface":
			result.interfaceName = fields[1]
		}
	}
	if result.address == "" && result.interfaceName == "" {
		return freeBSDGateway{}, fmt.Errorf("FreeBSD %s default route has no gateway or interface", family)
	}
	return result, nil
}

func usesFreeBSDAutomaticDefaultRoute(cfg *config.Config) bool {
	if cfg == nil || (cfg.Interface.Table != "" && !strings.EqualFold(cfg.Interface.Table, "auto")) {
		return false
	}
	need4, need6 := freeBSDDefaultFamilies(cfg)
	return need4 || need6
}

func freeBSDDefaultFamilies(cfg *config.Config) (ipv4, ipv6 bool) {
	for _, prefix := range uniqueAllowedPrefixes(cfg) {
		if prefix.Bits() != 0 {
			continue
		}
		if prefix.Addr().Is4() {
			ipv4 = true
		} else {
			ipv6 = true
		}
	}
	return
}

func configureFreeBSDDNS(ctx context.Context, name string, cfg *config.Config, state *freeBSDNetworkState) error {
	if len(cfg.Interface.DNS) == 0 {
		return nil
	}
	path, err := exec.LookPath("resolvconf")
	if err != nil {
		return errors.New("DNS is configured, but resolvconf is not installed")
	}
	dns := config.ClassifyDNS(cfg.Interface.DNS)
	servers, domains := dns.Servers, dns.Domains
	var input strings.Builder
	for _, server := range servers {
		fmt.Fprintf(&input, "nameserver %s\n", server)
	}
	if len(domains) != 0 {
		fmt.Fprintf(&input, "search %s\n", strings.Join(domains, " "))
	}
	if err := runInput(ctx, input.String(), path, "-a", name, "-x"); err != nil {
		return err
	}
	state.undo = append(state.undo, hostCommand{name: path, args: []string{"-d", name}})
	return nil
}

func run(ctx context.Context, name string, args ...string) error {
	_, err := runOutput(ctx, name, args...)
	return err
}

func runOutput(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return text, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, text)
	}
	return text, nil
}

func runInput(ctx context.Context, input, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
