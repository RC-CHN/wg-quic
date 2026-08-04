//go:build freebsd

package platform

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/config"
	"golang.zx2c4.com/wireguard/tun"
)

var freeBSDInterfaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_=+.-]{1,15}$`)

type freeBSDHost struct{}

type freeBSDNetworkState struct {
	undo []hostCommand
}

func Current() Host {
	return freeBSDHost{}
}

func (freeBSDHost) ValidateInterfaceName(name string) error {
	if !freeBSDInterfaceNamePattern.MatchString(name) {
		return fmt.Errorf("invalid FreeBSD interface name %q", name)
	}
	return nil
}

func (freeBSDHost) ControlPath(name string) string {
	return "/var/run/wg-quic/" + name + ".sock"
}

func (freeBSDHost) Prepare(_ context.Context, cfg *config.Config) error {
	for i := range cfg.Peers {
		if cfg.Peers[i].Endpoint == "" {
			continue
		}
		endpoint, err := net.ResolveUDPAddr("udp", cfg.Peers[i].Endpoint)
		if err != nil {
			return fmt.Errorf("resolve Peer %d Endpoint: %w", i+1, err)
		}
		if endpoint.IP == nil {
			return fmt.Errorf("resolve Peer %d Endpoint: no IP address", i+1)
		}
		cfg.Peers[i].Endpoint = endpoint.String()
	}
	return nil
}

func (freeBSDHost) CreateTUN(name string, mtu int) (tun.Device, error) {
	return tun.CreateTUN(name, mtu)
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
	mtu := cfg.Interface.MTU
	if mtu == 0 {
		mtu = 1380
	}
	if err := run(ctx, "ifconfig", name, "mtu", fmt.Sprint(mtu), "up"); err != nil {
		return cleanup, err
	}
	endpointRoutes, err := freeBSDEndpointRouteOperations(ctx, cfg)
	if err != nil {
		return cleanup, err
	}
	for _, operation := range append(endpointRoutes, freeBSDRouteOperations(name, cfg)...) {
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

func freeBSDEndpointRouteOperations(ctx context.Context, cfg *config.Config) ([]hostOperation, error) {
	if !usesFreeBSDAutomaticDefaultRoute(cfg) {
		return nil, nil
	}
	need4, need6 := freeBSDDefaultFamilies(cfg)
	gateways := make(map[bool]freeBSDGateway)
	var operations []hostOperation
	seen := make(map[netip.Addr]struct{})
	for i, peer := range cfg.Peers {
		if peer.Endpoint == "" {
			continue
		}
		host, _, err := net.SplitHostPort(peer.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("Peer %d Endpoint: %w", i+1, err)
		}
		address, err := netip.ParseAddr(strings.Trim(host, "[]"))
		if err != nil {
			return nil, fmt.Errorf("Peer %d resolved Endpoint: %w", i+1, err)
		}
		if _, ok := seen[address]; ok {
			continue
		}
		seen[address] = struct{}{}
		is6 := address.Is6()
		if (is6 && !need6) || (!is6 && !need4) {
			continue
		}
		gateway, ok := gateways[is6]
		if !ok {
			gateway, err = freeBSDDefaultGateway(ctx, is6)
			if err != nil {
				return nil, err
			}
			gateways[is6] = gateway
		}
		family := "-inet"
		if is6 {
			family = "-inet6"
		}
		apply := []string{"-q", "-n", "add", family, address.String()}
		if gateway.address != "" {
			apply = append(apply, "-gateway", gateway.address)
		} else {
			apply = append(apply, "-interface", gateway.interfaceName)
		}
		operations = append(operations, hostOperation{
			apply: hostCommand{name: "route", args: apply},
			undo: hostCommand{name: "route", args: []string{
				"-q", "-n", "delete", family, address.String(),
			}},
		})
	}
	return operations, nil
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
	var servers, domains []string
	for _, value := range cfg.Interface.DNS {
		if _, err := netip.ParseAddr(value); err == nil {
			servers = append(servers, value)
		} else {
			domains = append(domains, value)
		}
	}
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
