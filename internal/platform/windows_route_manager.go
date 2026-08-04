package platform

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"

	endpointmgr "github.com/RC-CHN/wg-quic/internal/endpoint"
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
	windowsRoutePendingAdd           windowsRouteState = "pending-add"
	windowsRouteActive               windowsRouteState = "active"
	windowsRoutePendingDelete        windowsRouteState = "pending-delete"
	windowsRoutePendingMigrateRemove windowsRouteState = "pending-migrate-remove"
	windowsRoutePendingMigrateAdd    windowsRouteState = "pending-migrate-add"
)

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
	References uint32 `json:"references"`
}

type windowsRouteSnapshot struct {
	Key            windowsRouteKey       `json:"key"`
	InterfaceIndex uint32                `json:"interfaceIndex,omitempty"`
	Metric         uint32                `json:"routeMetric"`
	BestSource     string                `json:"bestSource,omitempty"`
	Ownership      windowsRouteOwnership `json:"ownership"`
	Revision       uint64                `json:"revision"`
}

type windowsRouteRecord struct {
	Key            windowsRouteKey       `json:"key"`
	InterfaceIndex uint32                `json:"interfaceIndex,omitempty"`
	Metric         uint32                `json:"routeMetric"`
	BestSource     string                `json:"bestSource,omitempty"`
	Ownership      windowsRouteOwnership `json:"ownership"`
	State          windowsRouteState     `json:"state"`
	Revision       uint64                `json:"revision"`
	Previous       *windowsRouteSnapshot `json:"previous,omitempty"`
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

type windowsRouteChange struct {
	Family        uint8
	Destination   string
	InterfaceLUID uint64
	NextHop       string
	Valid         bool
}

type windowsRouteNotifier interface {
	Ready() <-chan struct{}
	Drain() []windowsRouteChange
	Close() error
}

type windowsRouteManager struct {
	system     windowsRouteSystem
	store      windowsRouteLedgerStore
	owner      windowsRouteOwner
	tunnelLUID uint64
	closeOwner func() error
	notifier   windowsRouteNotifier
	changes    chan struct{}
	watchStop  context.CancelFunc
	watchDone  chan struct{}

	mu     sync.Mutex
	closed bool
}

type windowsRouteLease struct {
	manager  *windowsRouteManager
	endpoint netip.Addr
	key      windowsRouteKey
	revision uint64

	mu       sync.Mutex
	released bool
}

func (m *windowsRouteManager) AcquireEndpointRoute(
	ctx context.Context,
	endpoint netip.Addr,
) (endpointmgr.RouteLease, error) {
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
		original := cloneWindowsRouteRecord(*record)
		exists, err := m.system.RouteExists(ctx, record.Key)
		if err != nil {
			return nil, fmt.Errorf("check existing Windows endpoint route: %w", err)
		}
		record.Owners = addOwnerReference(record.Owners, m.owner)
		if record.Revision == 0 {
			record.Revision = 1
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
			record.State = windowsRoutePendingMigrateAdd
			previous := snapshotWindowsRoute(original)
			record.Previous = &previous
			if err := m.saveLedger(&ledger); err != nil {
				return nil, err
			}
			if err := m.system.CreateRoute(ctx, selected); err != nil {
				ledger.Routes[index] = original
				_ = m.saveLedger(&ledger)
				return nil, fmt.Errorf("create Windows endpoint route: %w", err)
			}
			record = &ledger.Routes[index]
			record.State = windowsRouteActive
			record.Previous = nil
		default:
			record.State = windowsRoutePendingAdd
			if err := m.saveLedger(&ledger); err != nil {
				return nil, err
			}
			if err := m.system.CreateRoute(ctx, selected); err != nil {
				ledger.Routes[index] = original
				_ = m.saveLedger(&ledger)
				return nil, fmt.Errorf("recreate Windows endpoint route: %w", err)
			}
			record = &ledger.Routes[index]
			record.State = windowsRouteActive
		}
		if err := m.saveLedger(&ledger); err != nil {
			// If this call created the route, the durable pending state lets
			// recovery mark ownership ambiguous without guessing whether the
			// kernel mutation completed.
			return nil, err
		}
		return &windowsRouteLease{
			manager: m, endpoint: endpoint, key: selected.Key, revision: record.Revision,
		}, nil
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
		Revision:       1,
		Owners:         []windowsRouteOwner{m.owner},
	}
	if exists {
		record.Ownership = windowsRouteExternal
		ledger.Routes = append(ledger.Routes, record)
		if err := m.saveLedger(&ledger); err != nil {
			return nil, err
		}
		return &windowsRouteLease{
			manager: m, endpoint: endpoint, key: selected.Key, revision: record.Revision,
		}, nil
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
		// Keep the durable pending-add record. The next reconciliation can
		// observe the route and conservatively mark ownership ambiguous.
		return nil, err
	}
	return &windowsRouteLease{
		manager: m, endpoint: endpoint, key: selected.Key,
		revision: ledger.Routes[recordIndex].Revision,
	}, nil
}

func (l *windowsRouteLease) Release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	if err := l.manager.release(ctx, l.endpoint); err != nil {
		return err
	}
	l.released = true
	return nil
}

func (l *windowsRouteLease) Refresh(ctx context.Context) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return false, nil
	}
	key, revision, changed, err := l.manager.refresh(
		ctx, l.endpoint, l.key, l.revision,
	)
	if err != nil {
		return false, err
	}
	l.key = key
	l.revision = revision
	return changed, nil
}

func (m *windowsRouteManager) refresh(
	ctx context.Context,
	endpoint netip.Addr,
	leaseKey windowsRouteKey,
	leaseRevision uint64,
) (windowsRouteKey, uint64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return windowsRouteKey{}, 0, false, errors.New("Windows route manager is closed")
	}
	unlock, err := m.store.Lock(ctx)
	if err != nil {
		return windowsRouteKey{}, 0, false, fmt.Errorf("lock Windows route ledger: %w", err)
	}
	defer func() {
		_ = unlock()
	}()

	ledger, err := m.loadLedger()
	if err != nil {
		return windowsRouteKey{}, 0, false, err
	}
	reconciled, err := m.reconcile(ctx, &ledger)
	if err != nil {
		return windowsRouteKey{}, 0, false, fmt.Errorf("reconcile Windows endpoint routes: %w", err)
	}
	if reconciled {
		if err := m.saveLedger(&ledger); err != nil {
			return windowsRouteKey{}, 0, false, err
		}
	}
	index := routeRecordOwnerEndpointIndex(
		ledger.Routes, endpoint, m.owner.InstanceID, m.system.CurrentCompartmentID(),
	)
	if index < 0 {
		return windowsRouteKey{}, 0, false, errors.New("Windows endpoint route lease is absent from the ledger")
	}
	record := &ledger.Routes[index]
	if record.Key != leaseKey || record.Revision != leaseRevision {
		return record.Key, record.Revision, true, nil
	}
	exists, err := m.system.RouteExists(ctx, record.Key)
	if err != nil {
		return windowsRouteKey{}, 0, false, fmt.Errorf("check Windows endpoint route: %w", err)
	}
	if record.Ownership != windowsRouteManaged && exists {
		// An external exact host route is itself the selected path. We never
		// remove it merely to inspect what would have won without it.
		return record.Key, record.Revision, false, nil
	}
	key, revision, changed, err := m.migrateRecord(ctx, &ledger, index, endpoint, exists)
	if err != nil {
		return windowsRouteKey{}, 0, false, err
	}
	return key, revision, changed, nil
}

func (m *windowsRouteManager) migrateRecord(
	ctx context.Context,
	ledger *windowsRouteLedger,
	index int,
	endpoint netip.Addr,
	oldExists bool,
) (windowsRouteKey, uint64, bool, error) {
	record := &ledger.Routes[index]
	previous := snapshotWindowsRoute(*record)
	if previous.Ownership == windowsRouteManaged && oldExists {
		record.State = windowsRoutePendingMigrateRemove
		record.Previous = &previous
		if err := m.saveLedger(ledger); err != nil {
			return windowsRouteKey{}, 0, false, err
		}
		if err := m.system.DeleteRoute(ctx, previous.Key); err != nil {
			record.State = windowsRouteActive
			_ = m.saveLedger(ledger)
			return windowsRouteKey{}, 0, false, fmt.Errorf("remove Windows endpoint pin for route selection: %w", err)
		}
	}

	selected, err := m.system.BestRoute(ctx, endpoint, m.tunnelLUID)
	if err == nil {
		err = validateSelectedRoute(endpoint, selected, m.tunnelLUID)
	}
	if err != nil {
		return windowsRouteKey{}, 0, false, m.restoreMigration(
			ctx, ledger, index, previous, oldExists,
			fmt.Errorf("re-evaluate Windows route for endpoint %s: %w", endpoint, err),
		)
	}
	if selected.Key == previous.Key {
		changed := !oldExists ||
			selected.InterfaceIndex != previous.InterfaceIndex ||
			selected.Metric != previous.Metric ||
			addrString(selected.BestSource) != previous.BestSource
		if previous.Ownership == windowsRouteManaged {
			if err := m.system.CreateRoute(ctx, selected); err != nil {
				return windowsRouteKey{}, 0, false, m.restoreMigration(
					ctx, ledger, index, previous, oldExists,
					fmt.Errorf("restore unchanged Windows endpoint route: %w", err),
				)
			}
		}
		restoreWindowsRouteRecord(record, previous)
		record.InterfaceIndex = selected.InterfaceIndex
		record.Metric = selected.Metric
		record.BestSource = addrString(selected.BestSource)
		if changed {
			record.Revision++
		}
		record.State = windowsRouteActive
		if err := m.saveLedger(ledger); err != nil {
			return windowsRouteKey{}, 0, false, err
		}
		return record.Key, record.Revision, changed, nil
	}
	if other := routeRecordIndex(ledger.Routes, selected.Key); other >= 0 && other != index {
		return windowsRouteKey{}, 0, false, m.restoreMigration(
			ctx, ledger, index, previous, oldExists,
			fmt.Errorf("selected Windows endpoint route already has ledger record %d", other),
		)
	}

	record.Key = selected.Key
	record.InterfaceIndex = selected.InterfaceIndex
	record.Metric = selected.Metric
	record.BestSource = addrString(selected.BestSource)
	record.Ownership = windowsRouteManaged
	record.State = windowsRoutePendingMigrateAdd
	record.Previous = &previous
	if err := m.saveLedger(ledger); err != nil {
		return windowsRouteKey{}, 0, false, m.restoreMigration(
			ctx, ledger, index, previous, oldExists, err,
		)
	}

	newExists, err := m.system.RouteExists(ctx, selected.Key)
	if err != nil {
		return windowsRouteKey{}, 0, false, m.restoreMigration(
			ctx, ledger, index, previous, oldExists,
			fmt.Errorf("check migrated Windows endpoint route: %w", err),
		)
	}
	if newExists {
		record.Ownership = windowsRouteExternal
	} else {
		if err := m.system.CreateRoute(ctx, selected); err != nil {
			return windowsRouteKey{}, 0, false, m.restoreMigration(
				ctx, ledger, index, previous, oldExists,
				fmt.Errorf("create migrated Windows endpoint route: %w", err),
			)
		}
	}
	record.State = windowsRouteActive
	record.Previous = nil
	record.Revision = max(previous.Revision+1, uint64(1))
	if err := m.saveLedger(ledger); err != nil {
		// Keep the durable pending-migrate-add record. Recovery can observe
		// whether the new route exists, mark its ownership ambiguous, and
		// avoid deleting a route it cannot prove was created by this call.
		return windowsRouteKey{}, 0, false, err
	}
	return record.Key, record.Revision, true, nil
}

func (m *windowsRouteManager) restoreMigration(
	ctx context.Context,
	ledger *windowsRouteLedger,
	index int,
	previous windowsRouteSnapshot,
	restoreKernelRoute bool,
	cause error,
) error {
	var errs []error
	if restoreKernelRoute && previous.Ownership == windowsRouteManaged {
		exists, err := m.system.RouteExists(ctx, previous.Key)
		if err != nil {
			errs = append(errs, err)
		} else if !exists {
			if err := m.system.CreateRoute(ctx, selectedWindowsRoute(previous)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	restoreWindowsRouteRecord(&ledger.Routes[index], previous)
	ledger.Routes[index].State = windowsRouteActive
	if err := m.saveLedger(ledger); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(append([]error{cause}, errs...)...)
}

func (m *windowsRouteManager) release(ctx context.Context, endpoint netip.Addr) error {
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
	index := routeRecordOwnerEndpointIndex(
		ledger.Routes, endpoint, m.owner.InstanceID, m.system.CurrentCompartmentID(),
	)
	if index < 0 {
		return nil
	}
	record := &ledger.Routes[index]
	record.Owners = removeOwnerReference(record.Owners, m.owner.InstanceID)
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
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	stop := m.watchStop
	notifier := m.notifier
	done := m.watchDone
	closeOwner := m.closeOwner
	m.mu.Unlock()

	if stop != nil {
		stop()
	}
	var errs []error
	if notifier != nil {
		errs = append(errs, notifier.Close())
	}
	if done != nil {
		<-done
	}
	if closeOwner != nil {
		errs = append(errs, closeOwner())
	}
	return errors.Join(errs...)
}

func (m *windowsRouteManager) Changes() <-chan struct{} {
	return m.changes
}

func (m *windowsRouteManager) startRouteWatcher(notifier windowsRouteNotifier) {
	ctx, cancel := context.WithCancel(context.Background())
	m.notifier = notifier
	m.changes = make(chan struct{}, 1)
	m.watchStop = cancel
	m.watchDone = make(chan struct{})
	go m.watchRouteChanges(ctx)
}

func (m *windowsRouteManager) watchRouteChanges(ctx context.Context) {
	defer close(m.watchDone)
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-m.notifier.Ready():
			if !ok {
				return
			}
		}
		relevant := false
		for _, change := range m.notifier.Drain() {
			ignored, err := m.isManagedRouteNotification(ctx, change)
			if err != nil || !ignored {
				relevant = true
				break
			}
		}
		if !relevant {
			continue
		}
		select {
		case m.changes <- struct{}{}:
		default:
		}
	}
}

func (m *windowsRouteManager) isManagedRouteNotification(
	ctx context.Context,
	change windowsRouteChange,
) (bool, error) {
	if !change.Valid {
		return false, nil
	}
	unlock, err := m.store.Lock(ctx)
	if err != nil {
		return false, err
	}
	defer func() {
		_ = unlock()
	}()
	ledger, err := m.loadLedger()
	if err != nil {
		return false, err
	}
	compartmentID := m.system.CurrentCompartmentID()
	for _, record := range ledger.Routes {
		if record.Key.CompartmentID != compartmentID ||
			record.Ownership != windowsRouteManaged {
			continue
		}
		if windowsRouteChangeMatches(change, record.Key) ||
			(record.Previous != nil &&
				record.Previous.Ownership == windowsRouteManaged &&
				windowsRouteChangeMatches(change, record.Previous.Key)) {
			return true, nil
		}
	}
	return false, nil
}

func windowsRouteChangeMatches(change windowsRouteChange, key windowsRouteKey) bool {
	return change.Family == key.Family &&
		change.Destination == key.Destination &&
		change.InterfaceLUID == key.InterfaceLUID &&
		change.NextHop == key.NextHop
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
	for routeIndex := range ledger.Routes {
		record := &ledger.Routes[routeIndex]
		if record.Revision == 0 {
			record.Revision = 1
		}
		for ownerIndex := range record.Owners {
			if record.Owners[ownerIndex].References == 0 {
				record.Owners[ownerIndex].References = 1
			}
		}
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
				record.State != windowsRoutePendingAdd &&
				record.State != windowsRoutePendingMigrateAdd &&
				exists {
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
			// No route was ever made durable. Dropping this reservation is
			// safe even if the process that attempted Acquire is still alive:
			// it cannot hold a usable lease for a nonexistent kernel route.
			ledger.Routes = slices.Delete(ledger.Routes, i, i+1)
			changed = true
			continue
		case record.State == windowsRoutePendingMigrateRemove:
			if record.Previous == nil {
				return false, fmt.Errorf("route %s has no migration snapshot", record.Key.Destination)
			}
			if !exists && record.Ownership == windowsRouteManaged {
				if err := m.system.CreateRoute(ctx, selectedWindowsRoute(*record.Previous)); err != nil {
					return false, fmt.Errorf("restore interrupted route migration: %w", err)
				}
			}
			restoreWindowsRouteRecord(record, *record.Previous)
			record.State = windowsRouteActive
			changed = true
		case record.State == windowsRoutePendingMigrateAdd && exists:
			if record.Ownership == windowsRouteManaged {
				// Creation may have completed without the final ledger save.
				// The identical kernel route has no object ID that proves it.
				record.Ownership = windowsRouteAmbiguous
			}
			record.State = windowsRouteActive
			record.Previous = nil
			record.Revision++
			changed = true
		case record.State == windowsRoutePendingMigrateAdd && !exists:
			if record.Previous == nil {
				return false, fmt.Errorf("route %s has no migration snapshot", record.Key.Destination)
			}
			previous := *record.Previous
			if previous.Ownership == windowsRouteManaged {
				if err := m.system.CreateRoute(ctx, selectedWindowsRoute(previous)); err != nil {
					return false, fmt.Errorf("restore interrupted route migration: %w", err)
				}
			}
			restoreWindowsRouteRecord(record, previous)
			record.State = windowsRouteActive
			changed = true
		case record.State == windowsRoutePendingDelete:
			return false, fmt.Errorf("route %s is pending deletion but still has live owners", record.Key.Destination)
		case record.State == windowsRouteActive &&
			record.Ownership == windowsRouteManaged && !exists:
			prefix, err := netip.ParsePrefix(record.Key.Destination)
			if err != nil {
				return false, fmt.Errorf("parse disappeared managed route %s: %w", record.Key.Destination, err)
			}
			_, _, routeChanged, err := m.migrateRecord(
				ctx, ledger, i, prefix.Addr().Unmap(), false,
			)
			if err != nil {
				return false, fmt.Errorf(
					"re-evaluate disappeared managed route %s: %w",
					record.Key.Destination, err,
				)
			}
			changed = changed || routeChanged
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
		case windowsRoutePendingAdd, windowsRouteActive, windowsRoutePendingDelete,
			windowsRoutePendingMigrateRemove, windowsRoutePendingMigrateAdd:
		default:
			return fmt.Errorf("route %d has invalid state %q", index, record.State)
		}
		if record.State == windowsRoutePendingMigrateRemove ||
			record.State == windowsRoutePendingMigrateAdd {
			if record.Previous == nil {
				return fmt.Errorf("route %d has no migration snapshot", index)
			}
			if err := validateWindowsRouteSnapshot(index, *record.Previous); err != nil {
				return err
			}
		} else if record.Previous != nil {
			return fmt.Errorf("route %d has an unexpected migration snapshot", index)
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

func validateWindowsRouteSnapshot(index int, snapshot windowsRouteSnapshot) error {
	if snapshot.Revision == 0 {
		return fmt.Errorf("route %d migration snapshot has an empty revision", index)
	}
	ledger := windowsRouteLedger{
		SchemaVersion: windowsRouteLedgerSchemaVersion,
		Routes: []windowsRouteRecord{{
			Key:            snapshot.Key,
			InterfaceIndex: snapshot.InterfaceIndex,
			Metric:         snapshot.Metric,
			BestSource:     snapshot.BestSource,
			Ownership:      snapshot.Ownership,
			State:          windowsRouteActive,
			Revision:       snapshot.Revision,
			Owners: []windowsRouteOwner{{
				Tunnel: "snapshot", InstanceID: "00000000-0000-0000-0000-000000000000",
				LeaseFile: "00000000-0000-0000-0000-000000000000.lease", References: 1,
			}},
		}},
	}
	if err := validateWindowsRouteLedger(ledger); err != nil {
		return fmt.Errorf("route %d migration snapshot: %w", index, err)
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

func routeRecordOwnerEndpointIndex(
	routes []windowsRouteRecord,
	endpoint netip.Addr,
	instanceID string,
	compartmentID uint32,
) int {
	bits := 128
	if endpoint.Is4() {
		bits = 32
	}
	destination := netip.PrefixFrom(endpoint.Unmap(), bits).String()
	return slices.IndexFunc(routes, func(record windowsRouteRecord) bool {
		return record.Key.CompartmentID == compartmentID &&
			record.Key.Destination == destination &&
			slices.ContainsFunc(record.Owners, func(owner windowsRouteOwner) bool {
				return owner.InstanceID == instanceID
			})
	})
}

func addOwnerReference(owners []windowsRouteOwner, owner windowsRouteOwner) []windowsRouteOwner {
	for index := range owners {
		if owners[index].InstanceID == owner.InstanceID {
			owners[index].References++
			return owners
		}
	}
	owner.References = max(owner.References, uint32(1))
	return append(owners, owner)
}

func removeOwnerReference(owners []windowsRouteOwner, instanceID string) []windowsRouteOwner {
	for index := range owners {
		if owners[index].InstanceID != instanceID {
			continue
		}
		if owners[index].References > 1 {
			owners[index].References--
			return owners
		}
		return slices.Delete(owners, index, index+1)
	}
	return owners
}

func snapshotWindowsRoute(record windowsRouteRecord) windowsRouteSnapshot {
	return windowsRouteSnapshot{
		Key:            record.Key,
		InterfaceIndex: record.InterfaceIndex,
		Metric:         record.Metric,
		BestSource:     record.BestSource,
		Ownership:      record.Ownership,
		Revision:       record.Revision,
	}
}

func cloneWindowsRouteRecord(record windowsRouteRecord) windowsRouteRecord {
	record.Owners = slices.Clone(record.Owners)
	if record.Previous != nil {
		previous := *record.Previous
		record.Previous = &previous
	}
	return record
}

func restoreWindowsRouteRecord(record *windowsRouteRecord, snapshot windowsRouteSnapshot) {
	record.Key = snapshot.Key
	record.InterfaceIndex = snapshot.InterfaceIndex
	record.Metric = snapshot.Metric
	record.BestSource = snapshot.BestSource
	record.Ownership = snapshot.Ownership
	record.Revision = snapshot.Revision
	record.Previous = nil
}

func selectedWindowsRoute(snapshot windowsRouteSnapshot) windowsSelectedRoute {
	selected := windowsSelectedRoute{
		Key:            snapshot.Key,
		InterfaceIndex: snapshot.InterfaceIndex,
		Metric:         snapshot.Metric,
	}
	if snapshot.BestSource != "" {
		selected.BestSource, _ = netip.ParseAddr(snapshot.BestSource)
	}
	return selected
}

func addrString(address netip.Addr) string {
	if !address.IsValid() {
		return ""
	}
	return address.String()
}
