//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
)

type windowsNetworkSystem interface {
	RunBatch(context.Context, string, uint64, []string, bool) ([]int, error)
	DeleteRoute(context.Context, windowsRouteKey) error
	DeleteAddress(context.Context, uint32, uint64, netip.Addr) error
	ResetDNS(context.Context, uint32, uint64) error
	ConfigureInterface(context.Context, uint32, uint64, uint32) error
	CreateAddress(context.Context, uint32, uint64, netip.Prefix) error
	CreateRoute(context.Context, windowsSelectedRoute) error
}

type windowsNativeNetworkSystem struct{ windowsNativeRouteSystem }

func (windowsNativeNetworkSystem) RunBatch(ctx context.Context, name string, luid uint64, scripts []string, keepGoing bool) ([]int, error) {
	return runWindowsNetworkBatchOnInterface(ctx, name, luid, scripts, keepGoing)
}

type windowsNetworkState struct {
	name          string
	interfaceLUID uint64
	compartmentID uint32
	undo          []windowsOperation
}

func (s *windowsNetworkState) apply(ctx context.Context, operations []windowsOperation, system windowsNetworkSystem) error {
	for i, operation := range operations {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		applied := false
		switch {
		case operation.mtu != 0:
			err = system.ConfigureInterface(ctx, s.compartmentID, s.interfaceLUID, operation.mtu)
			applied = err == nil
		case operation.address.IsValid():
			err = system.CreateAddress(ctx, s.compartmentID, s.interfaceLUID, operation.address)
			applied = err == nil
		case operation.route.IsValid():
			key, keyErr := windowsPeerRouteKey(s.compartmentID, s.interfaceLUID, operation.route)
			err = keyErr
			if err == nil {
				err = system.CreateRoute(ctx, windowsSelectedRoute{Key: key})
			}
			applied = err == nil
		default:
			var completed []int
			completed, err = system.RunBatch(ctx, s.name, s.interfaceLUID, []string{operation.apply}, false)
			applied = len(completed) == 1 && completed[0] == 0
			if err == nil && !applied {
				err = errors.New("incomplete network operation result")
			}
		}
		if applied && (operation.undo != "" || operation.address.IsValid() || operation.route.IsValid()) {
			s.undo = append(s.undo, operation)
		}
		if err != nil {
			return fmt.Errorf("apply Windows network step %d: %w", i+1, err)
		}
	}
	return nil
}

func (s *windowsNetworkState) rollback(ctx context.Context, system windowsNetworkSystem) error {
	var errs []error
	for i := len(s.undo) - 1; i >= 0; i-- {
		operation := s.undo[i]
		var err error
		switch {
		case operation.route.IsValid():
			key, keyErr := windowsPeerRouteKey(s.compartmentID, s.interfaceLUID, operation.route)
			err = keyErr
			if err == nil {
				err = system.DeleteRoute(ctx, key)
			}
			if err != nil {
				err = fmt.Errorf("remove Windows tunnel route %s: %w", operation.route, err)
			}
		case operation.address.IsValid():
			err = system.DeleteAddress(ctx, s.compartmentID, s.interfaceLUID, operation.address.Addr())
			if err != nil {
				err = fmt.Errorf("remove Windows tunnel address %s: %w", operation.address, err)
			}
		case operation.dns:
			err = system.ResetDNS(ctx, s.compartmentID, s.interfaceLUID)
			if errors.Is(err, errWindowsDNSAPIUnavailable) {
				_, err = system.RunBatch(ctx, s.name, s.interfaceLUID, []string{operation.undo}, true)
			}
			if err != nil {
				err = fmt.Errorf("reset Windows tunnel DNS: %w", err)
			}
		default:
			// Resolve the captured LUID, never the alias: a replacement with
			// the same name is not ours.
			_, err = system.RunBatch(ctx, s.name, s.interfaceLUID, []string{operation.undo}, true)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
