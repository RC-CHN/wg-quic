//go:build linux

package platform

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/RC-CHN/wg-quic/internal/config"
	"github.com/RC-CHN/wg-quic/internal/endpoint"
	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"
)

const firstAutoRouteTable = 51820

var linuxInterfaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_=+.-]{1,15}$`)

type linuxHost struct{}

type linuxNetworkState struct {
	undo []hostCommand
}

func Current() Host {
	return linuxHost{}
}

func (linuxHost) ValidateInterfaceName(name string) error {
	if !linuxInterfaceNamePattern.MatchString(name) {
		return fmt.Errorf("invalid Linux interface name %q", name)
	}
	return nil
}

func (linuxHost) ControlPath(name string) string {
	return "/run/wg-quic/" + name + ".sock"
}

func (linuxHost) ConfigPath(name string) string {
	return "/etc/wg-quic/" + name + ".conf"
}

func (linuxHost) Prepare(ctx context.Context, cfg *config.Config) error {
	if !usesAutomaticDefaultRoute(cfg) || cfg.Interface.FwMark != 0 {
		return nil
	}
	mark, err := findFreeRouteTable(ctx, firstAutoRouteTable)
	if err != nil {
		return fmt.Errorf("select policy-routing table: %w", err)
	}
	cfg.Interface.FwMark = mark
	return nil
}

func (linuxHost) CreateTUN(name string, mtu int) (tun.Device, error) {
	return tun.CreateTUN(name, mtu)
}

func (linuxHost) NewEndpointRouteLeaser(
	context.Context,
	string,
	*config.Config,
) (endpoint.RouteLeaser, error) {
	// Linux full-tunnel routing excludes the marked outer socket, so it does
	// not need per-endpoint host routes.
	return noopEndpointRouteLeaser{}, nil
}

func (linuxHost) ConfigureNetwork(ctx context.Context, name string, cfg *config.Config) (Cleanup, error) {
	state := &linuxNetworkState{}
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
		if err := run(ctx, "ip", "address", "add", address.String(), "dev", name); err != nil {
			return cleanup, err
		}
	}
	mtu := cfg.Interface.MTU
	if mtu == 0 {
		mtu = 1380
	}
	if err := run(ctx, "ip", "link", "set", "dev", name, "mtu", fmt.Sprint(mtu), "up"); err != nil {
		return cleanup, err
	}
	operations, err := linuxRouteOperations(name, cfg)
	if err != nil {
		return cleanup, err
	}
	for _, operation := range operations {
		if err := run(ctx, operation.apply.name, operation.apply.args...); err != nil {
			return cleanup, err
		}
		state.undo = append(state.undo, operation.undo)
	}
	if hasIPv4AutomaticDefaultRoute(cfg) {
		oldValue, err := runOutput(ctx, "sysctl", "-n", "net.ipv4.conf.all.src_valid_mark")
		if err != nil {
			return cleanup, err
		}
		if strings.TrimSpace(oldValue) != "1" {
			if err := run(ctx, "sysctl", "-q", "net.ipv4.conf.all.src_valid_mark=1"); err != nil {
				return cleanup, err
			}
			state.undo = append(state.undo, hostCommand{
				name: "sysctl", args: []string{"-q", "net.ipv4.conf.all.src_valid_mark=" + strings.TrimSpace(oldValue)},
			})
		}
	}
	if err := configureLinuxDNS(ctx, name, cfg, state); err != nil {
		return cleanup, err
	}
	return cleanup, nil
}

func (linuxHost) RunHook(ctx context.Context, hook, name string) error {
	hook = strings.ReplaceAll(hook, "%i", name)
	cmd := exec.CommandContext(ctx, "sh", "-c", hook)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func usesAutomaticDefaultRoute(cfg *config.Config) bool {
	if cfg == nil || (!strings.EqualFold(cfg.Interface.Table, "auto") && cfg.Interface.Table != "") {
		return false
	}
	for _, peer := range cfg.Peers {
		for _, prefix := range peer.AllowedIPs {
			if prefix.Bits() == 0 {
				return true
			}
		}
	}
	return false
}

func hasIPv4AutomaticDefaultRoute(cfg *config.Config) bool {
	if !usesAutomaticDefaultRoute(cfg) {
		return false
	}
	for _, prefix := range uniqueAllowedPrefixes(cfg) {
		if prefix.Addr().Is4() && prefix.Bits() == 0 {
			return true
		}
	}
	return false
}

func findFreeRouteTable(ctx context.Context, first uint32) (uint32, error) {
	for candidate := first; candidate < first+1024; candidate++ {
		used, err := routeTableInUse(ctx, candidate)
		if err != nil {
			return 0, err
		}
		if !used {
			return candidate, nil
		}
	}
	return 0, errors.New("no free policy-routing table in the automatic range")
}

func routeTableInUse(ctx context.Context, table uint32) (bool, error) {
	value := strconv.FormatUint(uint64(table), 10)
	for _, family := range []string{"-4", "-6"} {
		output, err := runOutput(ctx, "ip", family, "route", "show", "table", value)
		if err != nil {
			if strings.Contains(output, "does not exist") || strings.Contains(output, "FIB table") {
				continue
			}
			return false, err
		}
		if strings.TrimSpace(output) != "" {
			return true, nil
		}
		rules, err := runOutput(ctx, "ip", family, "rule", "show")
		if err != nil {
			return false, err
		}
		if strings.Contains(rules, "lookup "+value) {
			return true, nil
		}
	}
	return false, nil
}

func linuxRouteOperations(name string, cfg *config.Config) ([]hostOperation, error) {
	if strings.EqualFold(cfg.Interface.Table, "off") {
		return nil, nil
	}
	table := cfg.Interface.Table
	auto := table == "" || strings.EqualFold(table, "auto")
	if auto && usesAutomaticDefaultRoute(cfg) {
		if cfg.Interface.FwMark == 0 {
			return nil, errors.New("automatic default route requires an fwmark")
		}
		table = strconv.FormatUint(uint64(cfg.Interface.FwMark), 10)
	}
	prefixes := uniqueAllowedPrefixes(cfg)
	operations := make([]hostOperation, 0, len(prefixes)+4)
	for _, prefix := range prefixes {
		family := "-4"
		if prefix.Addr().Is6() {
			family = "-6"
		}
		apply := []string{family, "route", "add", prefix.String(), "dev", name}
		undo := []string{family, "route", "delete", prefix.String(), "dev", name}
		if !auto || prefix.Bits() == 0 {
			apply = append(apply, "table", table)
			undo = append(undo, "table", table)
		}
		routeOperation := hostOperation{
			apply: hostCommand{name: "ip", args: apply},
			undo:  hostCommand{name: "ip", args: undo},
		}
		if auto && prefix.Bits() == 0 {
			mark := strconv.FormatUint(uint64(cfg.Interface.FwMark), 10)
			operations = append(operations,
				hostOperation{
					apply: hostCommand{name: "ip", args: []string{family, "rule", "add", "not", "fwmark", mark, "table", mark}},
					undo:  hostCommand{name: "ip", args: []string{family, "rule", "delete", "not", "fwmark", mark, "table", mark}},
				},
				hostOperation{
					apply: hostCommand{name: "ip", args: []string{family, "rule", "add", "table", "main", "suppress_prefixlength", "0"}},
					undo:  hostCommand{name: "ip", args: []string{family, "rule", "delete", "table", "main", "suppress_prefixlength", "0"}},
				},
			)
		}
		operations = append(operations, routeOperation)
	}
	return operations, nil
}

func configureLinuxDNS(ctx context.Context, name string, cfg *config.Config, state *linuxNetworkState) error {
	if len(cfg.Interface.DNS) == 0 {
		return nil
	}
	var servers, domains []string
	for _, value := range cfg.Interface.DNS {
		if _, err := netip.ParseAddr(value); err == nil {
			servers = append(servers, value)
		} else {
			domains = append(domains, value)
		}
	}
	if path, err := exec.LookPath("resolvectl"); err == nil {
		if len(servers) != 0 {
			if err := run(ctx, path, append([]string{"dns", name}, servers...)...); err != nil {
				return err
			}
		}
		if usesAutomaticDefaultRoute(cfg) {
			domains = append(domains, "~.")
		}
		if len(domains) != 0 {
			if err := run(ctx, path, append([]string{"domain", name}, domains...)...); err != nil {
				_ = run(ctx, path, "revert", name)
				return err
			}
		}
		state.undo = append(state.undo, hostCommand{name: path, args: []string{"revert", name}})
		return nil
	}
	if path, err := exec.LookPath("resolvconf"); err == nil {
		var input strings.Builder
		for _, server := range servers {
			fmt.Fprintf(&input, "nameserver %s\n", server)
		}
		if len(domains) != 0 {
			fmt.Fprintf(&input, "search %s\n", strings.Join(domains, " "))
		}
		if err := runInput(ctx, input.String(), path, "-a", name, "-m", "0", "-x"); err != nil {
			return err
		}
		state.undo = append(state.undo, hostCommand{name: path, args: []string{"-d", name, "-f"}})
		return nil
	}
	return errors.New("DNS is configured, but neither resolvectl nor resolvconf is installed")
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
