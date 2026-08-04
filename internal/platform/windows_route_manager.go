package platform

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"
)

const windowsRouteLedgerSchemaVersion = 1

type windowsRouteOwnership string

const (
	windowsRouteManaged   windowsRouteOwnership = "managed"
	windowsRouteExternal  windowsRouteOwnership = "external"
	windowsRouteAmbiguous windowsRouteOwnership = "ambiguous"
)

type windowsRouteState string

const (
	windowsRoutePendingAdd    windowsRouteState = "pending-add"
	windowsRouteActive        windowsRouteState = "active"
	windowsRoutePendingDelete windowsRouteState = "pending-delete"
)

// RouteLease represents one process owner's reference to an endpoint host
// route. Release is idempotent.
type RouteLease interface {
	Release(context.Context) error
}

// windowsRouteKey is the complete identity used by the Windows route APIs.
// Metric is deliberately not part of the key: Windows uses it as route cost,
// not route identity.
type windowsRouteKey struct {
	CompartmentID uint32 `json:"compartmentId"`
	Family        uint8  `json:"family"`
	Destination   string `json:"destination"`
	InterfaceLUID uint64 `json:"interfaceLuid"`
	NextHop       string `json:"nextHop"`
}

type windowsRouteOwner struct {
	Tunnel     string `json:"tunnel"`
	InstanceID string `json:"instanceId"`
	LeaseFile  string `json:"leaseFile"`
}

type windowsRouteRecord struct {
	Key            windowsRouteKey       `json:"key"`
	InterfaceIndex uint32                `json:"interfaceIndex,omitempty"`
	Metric         uint32                `json:"routeMetric"`
	BestSource     string                `json:"bestSource,omitempty"`
	Ownership      windowsRouteOwnership `json:"ownership"`
	State          windowsRouteState     `json:"state"`
	Owners         []windowsRouteOwner   `json:"owners,omitempty"`
}

type windowsRouteLedger struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Generation    uint64               `json:"generation"`
	Checksum      string               `json:"checksum,omitempty"`
	Routes        []windowsRouteRecord `json:"routes"`
}

type windowsSelectedRoute struct {
	Key            windowsRouteKey
	InterfaceIndex uint32
	Metric         uint32
	BestSource     netip.Addr
}

type windowsRouteSystem interface {
	CurrentCompartmentID() uint32
	BestRoute(context.Context, netip.Addr, uint64) (windowsSelectedRoute, error)
	RouteExists(context.Context, windowsRouteKey) (bool, error)
	CreateRoute(context.Context, windowsSelectedRoute) error
	DeleteRoute(context.Context, windowsRouteKey) error
}

type windowsRouteLedgerStore interface {
	Lock(context.Context) (func() error, error)
	Load() (windowsRouteLedger, error)
	Save(*windowsRouteLedger) error
	OwnerAlive(windowsRouteOwner) (bool, error)
}

type windowsRouteManager struct {
	system     windowsRouteSystem
	store      windowsRouteLedgerStore
	owner      windowsRouteOwner
	tunnelLUID uint64
	closeOwner func() error

	mu     sync.Mutex
	closed bool
}

type windowsRouteLease struct {
	manager *windowsRouteManager
	key     windowsRouteKey

	mu       sync.Mutex
	released bool
}

func (m *windowsRouteManager) AcquireEndpointRoute(
	ctx context.Context,
	endpoint netip.Addr,
) (RouteLease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("Windows route manager is closed")
	}
	if !endpoint.IsValid() {
		return nil, errors.New("endpoint address is required")
	}
	endpoint = endpoint.Unmap()

	unlock, err := m.store.Lock(ctx)
	if err != nil {
		return nil, fmt.Errorf("lock Windows route ledger: %w", err)
	}
	defer func() {
		_ = unlock()
	}()

	ledger, err := m.loadLedger()
	if err != nil {
		return nil, err
	}
	reconciled, err := m.reconcile(ctx, &ledger)
	if err != nil {
		return nil, fmt.Errorf("reconcile Windows endpoint routes: %w", err)
	}
	if reconciled {
		if err := m.saveLedger(&ledger); err != nil {
			return nil, err
		}
	}

	selected, err := m.system.BestRoute(ctx, endpoint, m.tunnelLUID)
	if err != nil {
		return nil, fmt.Errorf("select Windows route for endpoint %s: %w", endpoint, err)
	}
	if err := validateSelectedRoute(endpoint, selected, m.tunnelLUID); err != nil {
		return nil, err
	}

	index := routeRecordIndex(ledger.Routes, selected.Key)
	if index >= 0 {
		record := &ledger.Routes[index]
		exists, err := m.system.RouteExists(ctx, record.Key)
		if err != nil {
			return nil, fmt.Errorf("check existing Windows endpoint route: %w", err)
		}
		if !ownerPresent(record.Owners, m.owner.InstanceID) {
			record.Owners = append(record.Owners, m.owner)
		}
		record.InterfaceIndex = selected.InterfaceIndex
		record.Metric = selected.Metric
		record.BestSource = addrString(selected.BestSource)

		switch {
		case exists && record.State == windowsRoutePendingAdd:
			// The ledger was not durably advanced after route creation. There
			// is no kernel object ID that can prove who created the identical
			// route, so fail safe and borrow it.
			record.Ownership = windowsRouteAmbiguous
			record.State = windowsRouteActive
		case exists:
			record.State = windowsRouteActive
		case record.Ownership == windowsRouteExternal ||
			record.Ownership == windowsRouteAmbiguous:
			record.Ownership = windowsRouteManaged
			record.State = windowsRoutePendingAdd
			if err := m.saveLedger(&ledger); err != nil {
				return nil, err
			}
			if err := m.system.CreateRoute(ctx, selected); err != nil {
				ledger.Routes = slices.Delete(ledger.Routes, index, index+1)
				_ = m.saveLedger(&ledger)
				return nil, fmt.Errorf("create Windows endpoint route: %w", err)
			}
			record = &ledger.Routes[index]
			record.State = windowsRouteActive
		default:
			record.State = windowsRoutePendingAdd
			if err := m.saveLedger(&ledger); err != nil {
				return nil, err
			}
			if err := m.system.CreateRoute(ctx, selected); err != nil {
				ledger.Routes = slices.Delete(ledger.Routes, index, index+1)
				_ = m.saveLedger(&ledger)
				return nil, fmt.Errorf("recreate Windows endpoint route: %w", err)
			}
			record = &ledger.Routes[index]
			record.State = windowsRouteActive
		}
		if err := m.saveLedger(&ledger); err != nil {
			return nil, err
		}
		return &windowsRouteLease{manager: m, key: selected.Key}, nil
	}

	exists, err := m.system.RouteExists(ctx, selected.Key)
	if err != nil {
		return nil, fmt.Errorf("check selected Windows endpoint route: %w", err)
	}
	record := windowsRouteRecord{
		Key:            selected.Key,
		InterfaceIndex: selected.InterfaceIndex,
		Metric:         selected.Metric,
		BestSource:     addrString(selected.BestSource),
		State:          windowsRouteActive,
		Owners:         []windowsRouteOwner{m.owner},
	}
	if exists {
		record.Ownership = windowsRouteExternal
		ledger.Routes = append(ledger.Routes, record)
		if err := m.saveLedger(&ledger); err != nil {
			return nil, err
		}
		return &windowsRouteLease{manager: m, key: selected.Key}, nil
	}

	record.Ownership = windowsRouteManaged
	record.State = windowsRoutePendingAdd
	ledger.Routes = append(ledger.Routes, record)
	recordIndex := len(ledger.Routes) - 1
	if err := m.saveLedger(&ledger); err != nil {
		return nil, err
	}
	if err := m.system.CreateRoute(ctx, selected); err != nil {
		ledger.Routes = slices.Delete(ledger.Routes, recordIndex, recordIndex+1)
		_ = m.saveLedger(&ledger)
		return nil, fmt.Errorf("create Windows endpoint route: %w", err)
	}
	ledger.Routes[recordIndex].State = windowsRouteActive
	if err := m.saveLedger(&ledger); err != nil {
		// We know this call created the route, so an immediate exact rollback
		// is safe. If rollback fails, pending-add recovery will retain rather
		// than guess at ownership.
		_ = m.system.DeleteRoute(ctx, selected.Key)
		return nil, err
	}
	return &windowsRouteLease{manager: m, key: selected.Key}, nil
}

func (l *windowsRouteLease) Release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	if err := l.manager.release(ctx, l.key); err != nil {
		return err
	}
	l.released = true
	return nil
}

func (m *windowsRouteManager) release(ctx context.Context, key windowsRouteKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	unlock, err := m.store.Lock(ctx)
	if err != nil {
		return fmt.Errorf("lock Windows route ledger: %w", err)
	}
	defer func() {
		_ = unlock()
	}()

	ledger, err := m.loadLedger()
	if err != nil {
		return err
	}
	reconciled, err := m.reconcile(ctx, &ledger)
	if err != nil {
		return fmt.Errorf("reconcile Windows endpoint routes: %w", err)
	}
	if reconciled {
		if err := m.saveLedger(&ledger); err != nil {
			return err
		}
	}
	index := routeRecordIndex(ledger.Routes, key)
	if index < 0 {
		return nil
	}
	record := &ledger.Routes[index]
	record.Owners = removeOwner(record.Owners, m.owner.InstanceID)
	if len(record.Owners) != 0 {
		return m.saveLedger(&ledger)
	}
	if record.Ownership != windowsRouteManaged {
		ledger.Routes = slices.Delete(ledger.Routes, index, index+1)
		return m.saveLedger(&ledger)
	}

	record.State = windowsRoutePendingDelete
	if err := m.saveLedger(&ledger); err != nil {
		return err
	}
	if err := m.system.DeleteRoute(ctx, record.Key); err != nil {
		return fmt.Errorf("delete Windows endpoint route: %w", err)
	}
	ledger.Routes = slices.Delete(ledger.Routes, index, index+1)
	return m.saveLedger(&ledger)
}

func (m *windowsRouteManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	if m.closeOwner != nil {
		return m.closeOwner()
	}
	return nil
}

func (m *windowsRouteManager) loadLedger() (windowsRouteLedger, error) {
	ledger, err := m.store.Load()
	if err != nil {
		return windowsRouteLedger{}, fmt.Errorf("load Windows route ledger: %w", err)
	}
	if ledger.SchemaVersion == 0 {
		ledger.SchemaVersion = windowsRouteLedgerSchemaVersion
	}
	if ledger.SchemaVersion != windowsRouteLedgerSchemaVersion {
		return windowsRouteLedger{}, fmt.Errorf(
			"unsupported Windows route ledger schema %d", ledger.SchemaVersion,
		)
	}
	if err := validateWindowsRouteLedger(ledger); err != nil {
		return windowsRouteLedger{}, fmt.Errorf("validate Windows route ledger: %w", err)
	}
	return ledger, nil
}

func (m *windowsRouteManager) saveLedger(ledger *windowsRouteLedger) error {
	ledger.SchemaVersion = windowsRouteLedgerSchemaVersion
	ledger.Generation++
	if err := m.store.Save(ledger); err != nil {
		return fmt.Errorf("save Windows route ledger: %w", err)
	}
	return nil
}

func (m *windowsRouteManager) reconcile(
	ctx context.Context,
	ledger *windowsRouteLedger,
) (bool, error) {
	changed := false
	for i := 0; i < len(ledger.Routes); {
		record := &ledger.Routes[i]
		if record.Key.CompartmentID != m.system.CurrentCompartmentID() {
			i++
			continue
		}
		ownerCount := len(record.Owners)
		liveOwners := record.Owners[:0]
		for _, owner := range record.Owners {
			alive := owner.InstanceID == m.owner.InstanceID
			var err error
			if !alive {
				alive, err = m.store.OwnerAlive(owner)
			}
			if err != nil {
				return false, fmt.Errorf("check route owner %s: %w", owner.InstanceID, err)
			}
			if alive {
				liveOwners = append(liveOwners, owner)
			}
		}
		record.Owners = liveOwners
		changed = changed || ownerCount != len(liveOwners)

		exists, err := m.system.RouteExists(ctx, record.Key)
		if err != nil {
			return false, fmt.Errorf("check route %s: %w", record.Key.Destination, err)
		}
		if len(record.Owners) == 0 {
			if record.Ownership == windowsRouteManaged &&
				record.State != windowsRoutePendingAdd && exists {
				if record.State != windowsRoutePendingDelete {
					record.State = windowsRoutePendingDelete
					if err := m.saveLedger(ledger); err != nil {
						return false, err
					}
				}
				if err := m.system.DeleteRoute(ctx, record.Key); err != nil {
					return false, fmt.Errorf("delete abandoned route %s: %w", record.Key.Destination, err)
				}
			}
			// A pending-add route that exists is intentionally leaked: the
			// ledger cannot prove whether our create completed or an external
			// actor installed the identical route.
			ledger.Routes = slices.Delete(ledger.Routes, i, i+1)
			changed = true
			continue
		}

		switch {
		case record.State == windowsRoutePendingAdd && exists:
			record.Ownership = windowsRouteAmbiguous
			record.State = windowsRouteActive
			changed = true
		case record.State == windowsRoutePendingAdd && !exists:
			return false, fmt.Errorf("live owner has incomplete pending-add route %s", record.Key.Destination)
		case record.State == windowsRoutePendingDelete:
			return false, fmt.Errorf("route %s is pending deletion but still has live owners", record.Key.Destination)
		case record.State == windowsRouteActive &&
			record.Ownership == windowsRouteManaged && !exists:
			return false, fmt.Errorf("managed route %s disappeared while its owner is alive", record.Key.Destination)
		}
		i++
	}
	return changed, nil
}

func validateWindowsRouteLedger(ledger windowsRouteLedger) error {
	seenRoutes := make(map[windowsRouteKey]struct{}, len(ledger.Routes))
	for index, record := range ledger.Routes {
		if _, duplicate := seenRoutes[record.Key]; duplicate {
			return fmt.Errorf("route %d duplicates key for %s", index, record.Key.Destination)
		}
		seenRoutes[record.Key] = struct{}{}
		if record.Key.CompartmentID == 0 {
			return fmt.Errorf("route %d has an empty compartment ID", index)
		}
		if record.Key.InterfaceLUID == 0 {
			return fmt.Errorf("route %d has an empty interface LUID", index)
		}
		prefix, err := netip.ParsePrefix(record.Key.Destination)
		if err != nil {
			return fmt.Errorf("route %d destination: %w", index, err)
		}
		wantFamily, wantBits := uint8(6), 128
		if prefix.Addr().Is4() {
			wantFamily, wantBits = 4, 32
		}
		if record.Key.Family != wantFamily || prefix.Bits() != wantBits {
			return fmt.Errorf("route %d is not an endpoint host route", index)
		}
		nextHop, err := netip.ParseAddr(record.Key.NextHop)
		if err != nil || nextHop.Is4() != prefix.Addr().Is4() {
			return fmt.Errorf("route %d has invalid next hop %q", index, record.Key.NextHop)
		}
		if record.BestSource != "" {
			source, err := netip.ParseAddr(record.BestSource)
			if err != nil || source.Is4() != prefix.Addr().Is4() {
				return fmt.Errorf("route %d has invalid best source %q", index, record.BestSource)
			}
		}
		switch record.Ownership {
		case windowsRouteManaged, windowsRouteExternal, windowsRouteAmbiguous:
		default:
			return fmt.Errorf("route %d has invalid ownership %q", index, record.Ownership)
		}
		switch record.State {
		case windowsRoutePendingAdd, windowsRouteActive, windowsRoutePendingDelete:
		default:
			return fmt.Errorf("route %d has invalid state %q", index, record.State)
		}
		seenOwners := make(map[string]struct{}, len(record.Owners))
		for ownerIndex, owner := range record.Owners {
			if owner.Tunnel == "" || !validWindowsRouteOwnerID(owner.InstanceID) ||
				owner.LeaseFile != owner.InstanceID+".lease" {
				return fmt.Errorf("route %d owner %d is invalid", index, ownerIndex)
			}
			if _, duplicate := seenOwners[owner.InstanceID]; duplicate {
				return fmt.Errorf("route %d repeats owner %q", index, owner.InstanceID)
			}
			seenOwners[owner.InstanceID] = struct{}{}
		}
	}
	return nil
}

func validWindowsRouteOwnerID(instanceID string) bool {
	if len(instanceID) != 36 {
		return false
	}
	for index, character := range instanceID {
		switch index {
		case 8, 13, 18, 23:
			if character != '-' {
				return false
			}
		default:
			if !((character >= '0' && character <= '9') ||
				(character >= 'a' && character <= 'f')) {
				return false
			}
		}
	}
	return true
}

func validateSelectedRoute(endpoint netip.Addr, selected windowsSelectedRoute, tunnelLUID uint64) error {
	bits := 128
	family := uint8(6)
	if endpoint.Is4() {
		bits = 32
		family = 4
	}
	wantDestination := netip.PrefixFrom(endpoint, bits).String()
	if selected.Key.Destination != wantDestination || selected.Key.Family != family {
		return fmt.Errorf("Windows route selection returned invalid destination %q", selected.Key.Destination)
	}
	if selected.Key.InterfaceLUID == 0 {
		return errors.New("Windows route selection returned an empty interface LUID")
	}
	if selected.Key.InterfaceLUID == tunnelLUID {
		return errors.New("best route for wg-quic endpoint resolves through the tunnel")
	}
	nextHop, err := netip.ParseAddr(selected.Key.NextHop)
	if err != nil || nextHop.Is4() != endpoint.Is4() {
		return fmt.Errorf("Windows route selection returned invalid next hop %q", selected.Key.NextHop)
	}
	if selected.BestSource.IsValid() && selected.BestSource.Is4() != endpoint.Is4() {
		return fmt.Errorf("Windows route selection returned invalid source %q", selected.BestSource)
	}
	return nil
}

func routeRecordIndex(routes []windowsRouteRecord, key windowsRouteKey) int {
	return slices.IndexFunc(routes, func(record windowsRouteRecord) bool {
		return record.Key == key
	})
}

func ownerPresent(owners []windowsRouteOwner, instanceID string) bool {
	return slices.ContainsFunc(owners, func(owner windowsRouteOwner) bool {
		return owner.InstanceID == instanceID
	})
}

func removeOwner(owners []windowsRouteOwner, instanceID string) []windowsRouteOwner {
	return slices.DeleteFunc(owners, func(owner windowsRouteOwner) bool {
		return owner.InstanceID == instanceID
	})
}

func addrString(address netip.Addr) string {
	if !address.IsValid() {
		return ""
	}
	return address.String()
}
