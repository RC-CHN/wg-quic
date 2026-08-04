//go:build freebsd

package platform

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"

	"github.com/RC-CHN/wg-quic/internal/endpoint"
)

type freeBSDEndpointRouteLeaser struct {
	mu      sync.Mutex
	need4   bool
	need6   bool
	closed  bool
	entries map[netip.Addr]*freeBSDEndpointRouteEntry
}

type freeBSDEndpointRouteEntry struct {
	refs    int
	managed bool
	undo    hostCommand
}

type freeBSDEndpointRouteLease struct {
	manager *freeBSDEndpointRouteLeaser
	address netip.Addr
	once    sync.Once
	err     error
}

func (m *freeBSDEndpointRouteLeaser) AcquireEndpointRoute(
	ctx context.Context,
	address netip.Addr,
) (endpoint.RouteLease, error) {
	address = address.Unmap()
	if !address.IsValid() {
		return nil, errors.New("endpoint address is required")
	}
	if (address.Is4() && !m.need4) || (address.Is6() && !m.need6) {
		return noopEndpointRouteLease{}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("FreeBSD endpoint route leaser is closed")
	}
	if entry := m.entries[address]; entry != nil {
		entry.refs++
		return &freeBSDEndpointRouteLease{manager: m, address: address}, nil
	}
	gateway, err := freeBSDDefaultGateway(ctx, address.Is6())
	if err != nil {
		return nil, err
	}
	apply, undo := freeBSDHostRouteCommands(address, gateway)
	managed := true
	if err := run(ctx, apply.name, apply.args...); err != nil {
		// An identical route not created by this process may already exist.
		// Borrow it, but never delete it.
		if !strings.Contains(strings.ToLower(err.Error()), "exist") {
			return nil, err
		}
		managed = false
	}
	m.entries[address] = &freeBSDEndpointRouteEntry{
		refs: 1, managed: managed, undo: undo,
	}
	return &freeBSDEndpointRouteLease{manager: m, address: address}, nil
}

func freeBSDHostRouteCommands(address netip.Addr, gateway freeBSDGateway) (hostCommand, hostCommand) {
	family := "-inet"
	if address.Is6() {
		family = "-inet6"
	}
	apply := []string{"-q", "-n", "add", family, address.String()}
	if gateway.address != "" {
		apply = append(apply, "-gateway", gateway.address)
	} else {
		apply = append(apply, "-interface", gateway.interfaceName)
	}
	return hostCommand{name: "route", args: apply}, hostCommand{
		name: "route",
		args: []string{"-q", "-n", "delete", family, address.String()},
	}
}

func (l *freeBSDEndpointRouteLease) Release(ctx context.Context) error {
	l.once.Do(func() {
		l.err = l.manager.release(ctx, l.address)
	})
	return l.err
}

func (m *freeBSDEndpointRouteLeaser) release(ctx context.Context, address netip.Addr) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[address]
	if entry == nil {
		return nil
	}
	entry.refs--
	if entry.refs > 0 {
		return nil
	}
	delete(m.entries, address)
	if !entry.managed {
		return nil
	}
	return run(ctx, entry.undo.name, entry.undo.args...)
}

func (m *freeBSDEndpointRouteLeaser) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	var errs []error
	for address, entry := range m.entries {
		if entry.managed {
			if err := run(context.Background(), entry.undo.name, entry.undo.args...); err != nil {
				errs = append(errs, err)
			}
		}
		delete(m.entries, address)
	}
	return errors.Join(errs...)
}

func (*freeBSDEndpointRouteLeaser) Changes() <-chan struct{} { return nil }
