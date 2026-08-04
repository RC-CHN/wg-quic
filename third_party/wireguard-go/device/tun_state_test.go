/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2017-2025 WireGuard LLC. All Rights Reserved.
 */

package device

import (
	"testing"

	"github.com/RC-CHN/wg-quic/third_party/wireguard-go/tun"
)

func TestDisabledTUNStateTransitionsIgnoreUpAndDown(t *testing.T) {
	device := new(Device)
	device.state.state.Store(uint32(deviceStateDown))
	device.handleTUNStateEvent(tun.EventUp)
	if got := device.deviceState(); got != deviceStateDown {
		t.Fatalf("disabled EventUp changed device state to %s", got)
	}
	device.state.state.Store(uint32(deviceStateUp))
	device.handleTUNStateEvent(tun.EventDown)
	if got := device.deviceState(); got != deviceStateUp {
		t.Fatalf("disabled EventDown changed device state to %s", got)
	}
}
