//go:build freebsd

package platform

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"

	"github.com/RC-CHN/wg-quic/internal/endpoint"
)

type freeBSDEndpointRouteLeaser struct {
	mu         sync.Mutex
	need4      bool
	need6      bool
	closed     bool
	entries    map[netip.Addr]*freeBSDEndpointRouteEntry
	name       string
	ledgerPath string
	ledger     endpointRouteLedger
	recovery   RecoveryStatus

	runCommand     func(context.Context, string, ...string) error
	defaultGateway func(context.Context, bool) (freeBSDGateway, error)
	queryRoute     func(context.Context, netip.Addr) (freeBSDGateway, bool, error)
}

type freeBSDEndpointRouteEntry struct {
	refs    int
	managed bool
	undo    hostCommand
	deleted bool
}

type freeBSDEndpointRouteLease struct {
	manager *freeBSDEndpointRouteLeaser
	address netip.Addr

	mu       sync.Mutex
	released bool
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
	gateway, err := m.getDefaultGateway(ctx, address.Is6())
	if err != nil {
		return nil, err
	}
	apply, undo := freeBSDHostRouteCommands(address, gateway)
	record := endpointRouteLedgerRecord{
		Interface: m.name, Address: address.String(), Gateway: gateway.address,
		GatewayInterface: gateway.interfaceName, State: endpointRoutePendingAdd,
	}
	if m.ledgerPath != "" {
		m.ledger.Records = append(m.ledger.Records, record)
		if err := m.saveLedgerLocked(); err != nil {
			m.ledger.Records = m.ledger.Records[:len(m.ledger.Records)-1]
			return nil, err
		}
	}
	managed := true
	if err := m.execute(ctx, apply.name, apply.args...); err != nil {
		// An identical route not created by this process may already exist.
		// Borrow it, but never delete it.
		if !strings.Contains(strings.ToLower(err.Error()), "exist") {
			_ = m.removeLedgerRecordLocked(address)
			return nil, err
		}
		managed = false
	}
	if !managed {
		if err := m.removeLedgerRecordLocked(address); err != nil {
			return nil, err
		}
	} else if m.ledgerPath != "" {
		index := m.ledgerRecordIndexLocked(address)
		m.ledger.Records[index].State = endpointRouteActive
		if err := m.saveLedgerLocked(); err != nil {
			if undoErr := m.execute(context.Background(), undo.name, undo.args...); undoErr != nil {
				return nil, errors.Join(err, fmt.Errorf("undo unjournaled endpoint route: %w", undoErr))
			}
			_ = m.removeLedgerRecordLocked(address)
			return nil, err
		}
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
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	if err := l.manager.release(ctx, l.address); err != nil {
		return err
	}
	l.released = true
	return nil
}

func (m *freeBSDEndpointRouteLeaser) release(ctx context.Context, address netip.Addr) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[address]
	if entry == nil {
		return nil
	}
	if entry.refs > 1 {
		entry.refs--
		return nil
	}
	if !entry.managed {
		delete(m.entries, address)
		return nil
	}
	if entry.deleted {
		if err := m.removeLedgerRecordLocked(address); err != nil {
			return err
		}
		delete(m.entries, address)
		return nil
	}
	if m.ledgerPath != "" {
		index := m.ledgerRecordIndexLocked(address)
		if index < 0 {
			return errors.New("managed FreeBSD endpoint route is missing from its ledger")
		}
		m.ledger.Records[index].State = endpointRoutePendingDelete
		if err := m.saveLedgerLocked(); err != nil {
			return err
		}
	}
	if err := m.execute(ctx, entry.undo.name, entry.undo.args...); err != nil {
		return err
	}
	entry.deleted = true
	if err := m.removeLedgerRecordLocked(address); err != nil {
		return err
	}
	delete(m.entries, address)
	return nil
}

func (m *freeBSDEndpointRouteLeaser) Close() error {
	m.mu.Lock()
	m.closed = true
	addresses := make([]netip.Addr, 0, len(m.entries))
	for address := range m.entries {
		addresses = append(addresses, address)
	}
	m.mu.Unlock()
	var errs []error
	for _, address := range addresses {
		if err := m.releaseAllReferences(context.Background(), address); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *freeBSDEndpointRouteLeaser) releaseAllReferences(
	ctx context.Context,
	address netip.Addr,
) error {
	m.mu.Lock()
	entry := m.entries[address]
	if entry != nil {
		entry.refs = 1
	}
	m.mu.Unlock()
	return m.release(ctx, address)
}

func (*freeBSDEndpointRouteLeaser) Changes() <-chan struct{} { return nil }

func (m *freeBSDEndpointRouteLeaser) RecoveryStatus() RecoveryStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.recovery
	if status.State == "" {
		status.State = "clean"
	}
	return status
}

func (m *freeBSDEndpointRouteLeaser) execute(
	ctx context.Context,
	name string,
	args ...string,
) error {
	if m.runCommand != nil {
		return m.runCommand(ctx, name, args...)
	}
	return run(ctx, name, args...)
}

func (m *freeBSDEndpointRouteLeaser) getDefaultGateway(
	ctx context.Context,
	ipv6 bool,
) (freeBSDGateway, error) {
	if m.defaultGateway != nil {
		return m.defaultGateway(ctx, ipv6)
	}
	return freeBSDDefaultGateway(ctx, ipv6)
}

func (m *freeBSDEndpointRouteLeaser) recover(ctx context.Context) error {
	if m.ledgerPath == "" {
		return nil
	}
	ledger, err := loadFreeBSDEndpointRouteLedger(m.ledgerPath)
	if err != nil {
		return err
	}
	m.ledger = ledger
	for index := 0; index < len(m.ledger.Records); {
		record := m.ledger.Records[index]
		if record.Interface != m.name {
			index++
			continue
		}
		address, _ := netip.ParseAddr(record.Address)
		if record.State == endpointRoutePendingAdd {
			log.Printf("wg-quic: retaining ambiguous FreeBSD endpoint route %s from pending add", address)
			m.recovery.State = "degraded"
			m.recovery.RetainedAmbiguousObjects++
			m.recovery.Message = "retained ambiguous FreeBSD endpoint routes left by interrupted adds"
			m.ledger.Records = slices.Delete(m.ledger.Records, index, index+1)
			if err := m.saveLedgerLocked(); err != nil {
				return err
			}
			continue
		}
		gateway, exists, err := m.inspectRoute(ctx, address)
		if err != nil {
			return fmt.Errorf("inspect recorded FreeBSD endpoint route %s: %w", address, err)
		}
		matches := exists && gateway.address == record.Gateway &&
			gateway.interfaceName == record.GatewayInterface
		if !matches {
			m.ledger.Records = slices.Delete(m.ledger.Records, index, index+1)
			if err := m.saveLedgerLocked(); err != nil {
				return err
			}
			continue
		}
		m.ledger.Records[index].State = endpointRoutePendingDelete
		if err := m.saveLedgerLocked(); err != nil {
			return err
		}
		_, undo := freeBSDHostRouteCommands(address, gateway)
		if err := m.execute(ctx, undo.name, undo.args...); err != nil {
			return fmt.Errorf("remove abandoned FreeBSD endpoint route %s: %w", address, err)
		}
		m.ledger.Records = slices.Delete(m.ledger.Records, index, index+1)
		if err := m.saveLedgerLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (m *freeBSDEndpointRouteLeaser) inspectRoute(
	ctx context.Context,
	address netip.Addr,
) (freeBSDGateway, bool, error) {
	if m.queryRoute != nil {
		return m.queryRoute(ctx, address)
	}
	family := "-inet"
	if address.Is6() {
		family = "-inet6"
	}
	output, err := runOutput(ctx, "route", "-n", "get", family, address.String())
	if err != nil {
		lower := strings.ToLower(output + " " + err.Error())
		if strings.Contains(lower, "not in table") || strings.Contains(lower, "not found") {
			return freeBSDGateway{}, false, nil
		}
		return freeBSDGateway{}, false, err
	}
	return parseFreeBSDGateway(output), true, nil
}

func parseFreeBSDGateway(output string) freeBSDGateway {
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
	return result
}

func (m *freeBSDEndpointRouteLeaser) ledgerRecordIndexLocked(address netip.Addr) int {
	return slices.IndexFunc(m.ledger.Records, func(record endpointRouteLedgerRecord) bool {
		return record.Interface == m.name && record.Address == address.String()
	})
}

func (m *freeBSDEndpointRouteLeaser) removeLedgerRecordLocked(address netip.Addr) error {
	if m.ledgerPath == "" {
		return nil
	}
	if index := m.ledgerRecordIndexLocked(address); index >= 0 {
		m.ledger.Records = slices.Delete(m.ledger.Records, index, index+1)
	}
	return m.saveLedgerLocked()
}

func (m *freeBSDEndpointRouteLeaser) saveLedgerLocked() error {
	if m.ledgerPath == "" {
		return nil
	}
	m.ledger.SchemaVersion = endpointRouteLedgerSchemaVersion
	m.ledger.Generation++
	encoded, err := encodeEndpointRouteLedger(m.ledger)
	if err != nil {
		return err
	}
	return writeFreeBSDEndpointRouteLedger(m.ledgerPath, encoded, len(m.ledger.Records) == 0)
}

func loadFreeBSDEndpointRouteLedger(path string) (endpointRouteLedger, error) {
	directory := filepath.Dir(path)
	if info, directoryErr := os.Lstat(directory); directoryErr == nil {
		if err := validateFreeBSDLedgerDirectoryInfo(info); err != nil {
			return endpointRouteLedger{}, err
		}
	} else if !errors.Is(directoryErr, os.ErrNotExist) {
		return endpointRouteLedger{}, directoryErr
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return endpointRouteLedger{SchemaVersion: endpointRouteLedgerSchemaVersion}, nil
	}
	if err != nil {
		return endpointRouteLedger{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return endpointRouteLedger{}, errors.New("FreeBSD endpoint route ledger is not a protected root-owned regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return endpointRouteLedger{}, err
	}
	return decodeEndpointRouteLedger(data)
}

func writeFreeBSDEndpointRouteLedger(path string, data []byte, empty bool) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if err := validateFreeBSDLedgerDirectoryInfo(info); err != nil {
		return err
	}
	if empty {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncFreeBSDLedgerDirectory(directory)
	}
	temporary, err := os.CreateTemp(directory, ".endpoint-routes-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncFreeBSDLedgerDirectory(directory)
}

func validateFreeBSDLedgerDirectoryInfo(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o022 != 0 {
		return errors.New("FreeBSD endpoint route ledger directory is not a protected root-owned directory")
	}
	return nil
}

func syncFreeBSDLedgerDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
