//go:build windows

package platform

import (
	"errors"
	"net/netip"
	"sync"

	"golang.org/x/sys/windows"
)

const windowsRouteNotificationLimit = 256

type windowsNativeRouteNotifier struct {
	mu       sync.Mutex
	ready    chan struct{}
	pending  map[windowsRouteChange]struct{}
	overflow bool
	closed   bool
}

var (
	windowsRouteNotificationRegistrationMu sync.Mutex
	windowsRouteNotificationMu             sync.Mutex
	windowsRouteNotificationSubscribers    = make(map[*windowsNativeRouteNotifier]struct{})
	windowsRouteNotificationHandle         windows.Handle
	windowsRouteNotificationCallback       uintptr
	windowsInterfaceNotificationHandle     windows.Handle
	windowsInterfaceNotificationCallback   uintptr
	windowsAddressNotificationHandle       windows.Handle
	windowsAddressNotificationCallback     uintptr
)

func newWindowsRouteNotifier() (*windowsNativeRouteNotifier, error) {
	notifier := &windowsNativeRouteNotifier{
		ready:   make(chan struct{}, 1),
		pending: make(map[windowsRouteChange]struct{}),
	}
	windowsRouteNotificationRegistrationMu.Lock()
	defer windowsRouteNotificationRegistrationMu.Unlock()
	windowsRouteNotificationMu.Lock()
	windowsRouteNotificationSubscribers[notifier] = struct{}{}
	windowsRouteNotificationMu.Unlock()
	if err := ensureWindowsNetworkNotifications(); err != nil {
		windowsRouteNotificationMu.Lock()
		delete(windowsRouteNotificationSubscribers, notifier)
		noSubscribers := len(windowsRouteNotificationSubscribers) == 0
		windowsRouteNotificationMu.Unlock()
		if noSubscribers {
			err = errors.Join(err, cancelWindowsNetworkNotifications())
		}
		return nil, err
	}
	return notifier, nil
}

func (n *windowsNativeRouteNotifier) Ready() <-chan struct{} {
	return n.ready
}

func (n *windowsNativeRouteNotifier) Drain() []windowsRouteChange {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.overflow {
		n.overflow = false
		clear(n.pending)
		return []windowsRouteChange{{Valid: false}}
	}
	changes := make([]windowsRouteChange, 0, len(n.pending))
	for change := range n.pending {
		changes = append(changes, change)
	}
	clear(n.pending)
	return changes
}

func (n *windowsNativeRouteNotifier) enqueue(change windowsRouteChange) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return
	}
	if !change.Valid || len(n.pending) >= windowsRouteNotificationLimit {
		n.overflow = true
		clear(n.pending)
	} else if !n.overflow {
		n.pending[change] = struct{}{}
	}
	select {
	case n.ready <- struct{}{}:
	default:
	}
}

func (n *windowsNativeRouteNotifier) Close() error {
	windowsRouteNotificationRegistrationMu.Lock()
	defer windowsRouteNotificationRegistrationMu.Unlock()

	windowsRouteNotificationMu.Lock()
	if _, ok := windowsRouteNotificationSubscribers[n]; !ok {
		windowsRouteNotificationMu.Unlock()
		return nil
	}
	delete(windowsRouteNotificationSubscribers, n)
	removeNativeSubscription := len(windowsRouteNotificationSubscribers) == 0 &&
		(windowsRouteNotificationHandle != 0 ||
			windowsInterfaceNotificationHandle != 0 ||
			windowsAddressNotificationHandle != 0)
	windowsRouteNotificationMu.Unlock()

	if removeNativeSubscription {
		errs := cancelWindowsNetworkNotifications()
		n.mu.Lock()
		n.closed = true
		clear(n.pending)
		n.mu.Unlock()
		return errs
	}
	n.mu.Lock()
	n.closed = true
	clear(n.pending)
	n.mu.Unlock()
	return nil
}

func ensureWindowsNetworkNotifications() error {
	// Creating syscall callbacks during package initialization makes even
	// non-network commands such as `wg-quic-quick version` depend on the full
	// Windows networking API. Defer them until the route supervisor starts.
	if windowsRouteNotificationCallback == 0 {
		windowsRouteNotificationCallback = windows.NewCallback(windowsNativeRouteChanged)
		windowsInterfaceNotificationCallback = windows.NewCallback(windowsNativeInterfaceChanged)
		windowsAddressNotificationCallback = windows.NewCallback(windowsNativeAddressChanged)
	}
	if windowsRouteNotificationHandle == 0 {
		if err := windows.NotifyRouteChange2(
			windows.AF_UNSPEC,
			windowsRouteNotificationCallback,
			nil,
			false,
			&windowsRouteNotificationHandle,
		); err != nil {
			return err
		}
	}
	if windowsInterfaceNotificationHandle == 0 {
		if err := windows.NotifyIpInterfaceChange(
			windows.AF_UNSPEC,
			windowsInterfaceNotificationCallback,
			nil,
			false,
			&windowsInterfaceNotificationHandle,
		); err != nil {
			return err
		}
	}
	if windowsAddressNotificationHandle == 0 {
		if err := windows.NotifyUnicastIpAddressChange(
			windows.AF_UNSPEC,
			windowsAddressNotificationCallback,
			nil,
			false,
			&windowsAddressNotificationHandle,
		); err != nil {
			return err
		}
	}
	return nil
}

func cancelWindowsNetworkNotifications() error {
	var errs []error
	if windowsRouteNotificationHandle != 0 {
		if err := windows.CancelMibChangeNotify2(windowsRouteNotificationHandle); err != nil {
			errs = append(errs, err)
		} else {
			windowsRouteNotificationHandle = 0
		}
	}
	if windowsInterfaceNotificationHandle != 0 {
		if err := windows.CancelMibChangeNotify2(windowsInterfaceNotificationHandle); err != nil {
			errs = append(errs, err)
		} else {
			windowsInterfaceNotificationHandle = 0
		}
	}
	if windowsAddressNotificationHandle != 0 {
		if err := windows.CancelMibChangeNotify2(windowsAddressNotificationHandle); err != nil {
			errs = append(errs, err)
		} else {
			windowsAddressNotificationHandle = 0
		}
	}
	return errors.Join(errs...)
}

func windowsNativeRouteChanged(
	_ uintptr,
	row *windows.MibIpForwardRow2,
	_ uint32,
) uintptr {
	change := windowsRouteChange{Valid: false}
	if row != nil {
		if decoded, ok := windowsRouteChangeFromRow(row); ok {
			change = decoded
		}
	}
	windowsRouteNotificationMu.Lock()
	for subscriber := range windowsRouteNotificationSubscribers {
		subscriber.enqueue(change)
	}
	windowsRouteNotificationMu.Unlock()
	return 0
}

func windowsNativeInterfaceChanged(
	_ uintptr,
	_ *windows.MibIpInterfaceRow,
	_ uint32,
) uintptr {
	windowsPublishUnknownNetworkChange()
	return 0
}

func windowsNativeAddressChanged(
	_ uintptr,
	_ *windows.MibUnicastIpAddressRow,
	_ uint32,
) uintptr {
	windowsPublishUnknownNetworkChange()
	return 0
}

func windowsPublishUnknownNetworkChange() {
	windowsRouteNotificationMu.Lock()
	for subscriber := range windowsRouteNotificationSubscribers {
		subscriber.enqueue(windowsRouteChange{Valid: false})
	}
	windowsRouteNotificationMu.Unlock()
}

func windowsRouteChangeFromRow(row *windows.MibIpForwardRow2) (windowsRouteChange, bool) {
	destination, err := windowsAddressFromRaw(row.DestinationPrefix.Prefix)
	if err != nil {
		return windowsRouteChange{}, false
	}
	destination = destination.Unmap()
	bits := int(row.DestinationPrefix.PrefixLength)
	if bits < 0 || bits > destination.BitLen() {
		return windowsRouteChange{}, false
	}
	nextHop, err := windowsAddressFromRaw(row.NextHop)
	if err != nil {
		return windowsRouteChange{}, false
	}
	nextHop = nextHop.Unmap()
	if destination.Is4() != nextHop.Is4() {
		return windowsRouteChange{}, false
	}
	family := uint8(6)
	if destination.Is4() {
		family = 4
	}
	return windowsRouteChange{
		Family:        family,
		Destination:   netip.PrefixFrom(destination, bits).Masked().String(),
		InterfaceLUID: row.InterfaceLuid,
		NextHop:       nextHop.String(),
		Valid:         true,
	}, true
}
