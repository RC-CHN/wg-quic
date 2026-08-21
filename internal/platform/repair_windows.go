//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"

	"github.com/RC-CHN/wg-quic/internal/config"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
)

// RepairWindows removes only state that can be tied to a stopped tunnel:
// config-derived interface policy on a named Wintun adapter and stale route
// leases whose owner lock is no longer alive.
func RepairWindows(
	ctx context.Context,
	name string,
	cfg *config.Config,
) error {
	if err := repairWindowsRouteLedger(ctx, name); err != nil {
		return err
	}
	return repairWindowsAdapter(ctx, name, cfg)
}

func repairWindowsAdapter(
	ctx context.Context,
	name string,
	cfg *config.Config,
) error {
	adapter, err := wintun.OpenAdapter(name)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return fmt.Errorf(
			"open residual Wintun adapter %q: %w",
			name, err,
		)
	}
	var errs []error
	if journal, journalErr := newWindowsPeerRouteJournal(
		name,
		adapter.LUID(),
		windowsNativeRouteSystem{},
	); journalErr != nil {
		errs = append(errs, fmt.Errorf("open Windows peer route journal: %w", journalErr))
	} else if journalErr := journal.forceCleanup(ctx); journalErr != nil {
		errs = append(errs, fmt.Errorf("repair Windows peer route journal: %w", journalErr))
	}
	if cfg != nil {
		operations, planErr := windowsNetworkOperations(name, cfg)
		if planErr != nil {
			errs = append(errs, planErr)
		} else {
			for index := len(operations) - 1; index >= 0; index-- {
				if operations[index].undo == "" {
					continue
				}
				if err := runWindowsPowerShell(
					ctx, operations[index].undo,
				); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	if err := adapter.Close(); err != nil {
		errs = append(errs, fmt.Errorf(
			"close residual Wintun adapter %q: %w",
			name, err,
		))
	}
	return errors.Join(errs...)
}

func repairWindowsRouteLedger(ctx context.Context, tunnel string) error {
	store := newWindowsDiskRouteStore()
	if err := store.prepare(); err != nil {
		return err
	}
	manager := &windowsRouteManager{
		system: windowsNativeRouteSystem{},
		store:  store,
	}
	unlock, err := store.Lock(ctx)
	if err != nil {
		return fmt.Errorf("lock Windows route ledger for repair: %w", err)
	}
	defer func() {
		_ = unlock()
	}()
	ledger, err := manager.loadLedger()
	if err != nil {
		return err
	}
	changed, err := reconcileAbandonedWindowsRoutes(
		ctx, manager, &ledger,
	)
	if err != nil {
		return fmt.Errorf("reconcile Windows route ledger for repair: %w", err)
	}
	if changed {
		if err := manager.saveLedger(&ledger); err != nil {
			return err
		}
	}
	for _, record := range ledger.Routes {
		for _, owner := range record.Owners {
			if owner.Tunnel == tunnel {
				return fmt.Errorf(
					"route ledger still has a live owner for tunnel %q; refusing to remove its route",
					tunnel,
				)
			}
		}
	}
	return nil
}
