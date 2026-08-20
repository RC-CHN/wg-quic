#!/bin/sh
# shellcheck shell=sh disable=SC2012,SC3043

set -eu

mode=${1:?usage: guest-validate.sh install-smoke PACKAGE | interop-start CONFIG | interop-verify | interop-stop | boot-verify | uninstall}

wait_for_state()
{
	local name="$1"
	local want="$2"
	local attempt=0 status state

	while [ "$attempt" -lt 30 ]; do
		status=$(wg-quic-quick show "$name" --json 2>/dev/null || true)
		state=$(printf '%s\n' "$status" | jsonfilter -e '@.state' 2>/dev/null || true)
		[ "$state" = "$want" ] && return 0
		sleep 1
		attempt=$((attempt + 1))
	done
	echo "interface $name did not reach state $want" >&2
	return 1
}

wait_for_absence()
{
	local name="$1"
	local attempt=0

	while [ "$attempt" -lt 30 ]; do
		if [ ! -e "/sys/class/net/$name" ] &&
			[ ! -e "/run/wg-quic/$name.sock" ]; then
			return 0
		fi
		sleep 1
		attempt=$((attempt + 1))
	done
	echo "interface $name did not stop cleanly" >&2
	return 1
}

install_package()
{
	local package="$1"
	case "$package" in
		*.apk) apk add --allow-untrusted "$package" ;;
		*.ipk) opkg install "$package" ;;
		*) echo "unsupported OpenWrt package: $package" >&2; return 64 ;;
	esac
}

remove_package()
{
	if command -v apk >/dev/null 2>&1; then
		apk del wg-quic
	else
		opkg remove wg-quic
	fi
}

case "$mode" in
install-smoke)
	package=${2:?install-smoke requires a package path}
	install_package "$package"
	test -c /dev/net/tun
	ip -Version 2>&1 | grep -q 'iproute2'
	test -x /usr/bin/wg-quic
	test -x /usr/bin/wg-quic-quick
	test -x /etc/init.d/wg-quic
	test "$(ls -ld /etc/wg-quic | cut -c1-10)" = drwx------

	mkdir -p /etc/wg-quic
	umask 077
	printf '%s\n' \
		'[Interface]' \
		'PrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=' \
		'Address = 10.78.0.1/32' \
		'ListenPort = 51780' \
		'PostUp = echo up-%i >> /tmp/wg-quic-smoke-hooks' \
		'PostDown = echo down-%i >> /tmp/wg-quic-smoke-hooks' \
		> /etc/wg-quic/smoke.conf

	if wg-quic run /etc/wg-quic/smoke.conf >/tmp/wg-quic-direct.log 2>&1; then
		echo 'direct wg-quic unexpectedly accepted host hooks' >&2
		exit 1
	fi
	grep -q 'run wg-quic-quick instead' /tmp/wg-quic-direct.log

	rm -f /tmp/wg-quic-smoke-hooks
	wg-quic-quick up smoke
	wait_for_state smoke up
	/etc/init.d/wg-quic running smoke
	wg-quic-quick down smoke
	wait_for_absence smoke
	test "$(sed -n '1p' /tmp/wg-quic-smoke-hooks)" = up-smoke
	test "$(sed -n '2p' /tmp/wg-quic-smoke-hooks)" = down-smoke
	test ! -e /sys/class/net/smoke
	test ! -e /run/wg-quic/smoke.sock

	uci -q delete wg-quic.smoke || true
	uci set wg-quic.smoke=instance
	uci set wg-quic.smoke.enabled=1
	uci set wg-quic.smoke.config=/etc/wg-quic/smoke.conf
	uci commit wg-quic
	/etc/init.d/wg-quic start smoke
	wait_for_state smoke up
	/etc/init.d/wg-quic stop smoke
	wait_for_absence smoke
	test ! -e /sys/class/net/smoke
	echo 'OPENWRT PACKAGE SMOKE PASSED'
	;;
interop-start)
	config=${2:?interop-start requires a config path}
	cp "$config" /etc/wg-quic/openwrt0.conf
	chmod 0600 /etc/wg-quic/openwrt0.conf
	uci -q delete wg-quic.openwrt0 || true
	uci set wg-quic.openwrt0=instance
	uci set wg-quic.openwrt0.enabled=1
	uci set wg-quic.openwrt0.config=/etc/wg-quic/openwrt0.conf
	uci commit wg-quic
	wg-quic-quick check openwrt0
	wg-quic-quick up openwrt0
	wait_for_state openwrt0 up
	nft list table inet wg_quic_qemu >/dev/null
	echo 'OPENWRT INTEROP READY'
	;;
interop-verify)
	status=$(wg-quic-quick show openwrt0 --json)
	printf '%s\n' "$status" > /tmp/wg-quic-openwrt-status.json
	test "$(printf '%s\n' "$status" | jsonfilter -e '@.peers[0].session')" = established
	for expression in \
		'@.stats.wg_tx_bytes' \
		'@.stats.wg_rx_bytes' \
		'@.stats.wire_tx_bytes' \
		'@.stats.wire_rx_bytes'; do
		value=$(printf '%s\n' "$status" | jsonfilter -e "$expression")
		[ "$value" -gt 0 ]
	done
	echo 'OPENWRT INTEROP TRAFFIC PASSED'
	;;
interop-stop)
	wg-quic-quick down openwrt0
	wait_for_absence openwrt0
	test ! -e /sys/class/net/openwrt0
	test ! -e /run/wg-quic/openwrt0.sock
	if nft -a list chain inet fw4 input |
		grep -q 'wg-quic-qemu-openwrt0'; then
		echo 'PostDown left the test firewall rule behind' >&2
		exit 1
	fi
	if nft list table inet wg_quic_qemu >/dev/null 2>&1; then
		echo 'PostDown left the test firewall table behind' >&2
		exit 1
	fi
	echo 'OPENWRT INTEROP CLEANUP PASSED'
	;;
boot-verify)
	/etc/init.d/wg-quic enabled
	wait_for_state smoke up
	wait_for_state openwrt0 up
	/etc/init.d/wg-quic running smoke
	/etc/init.d/wg-quic running openwrt0
	nft list table inet wg_quic_qemu >/dev/null
	nft -a list chain inet fw4 input |
		grep -q 'wg-quic-qemu-openwrt0'
	echo 'OPENWRT PROCD REBOOT PASSED'
	;;
uninstall)
	remove_package
	wait_for_absence smoke
	wait_for_absence openwrt0
	test ! -e /usr/bin/wg-quic
	test ! -e /usr/bin/wg-quic-quick
	test ! -e /etc/init.d/wg-quic
	test -f /etc/wg-quic/smoke.conf
	test -f /etc/wg-quic/openwrt0.conf
	if nft list table inet wg_quic_qemu >/dev/null 2>&1; then
		echo 'package uninstall left the test firewall table behind' >&2
		exit 1
	fi
	if nft -a list chain inet fw4 input |
		grep -q 'wg-quic-qemu-openwrt0'; then
		echo 'package uninstall left the test firewall rule behind' >&2
		exit 1
	fi
	echo 'OPENWRT PACKAGE UNINSTALL PASSED'
	;;
*)
	echo "unknown mode: $mode" >&2
	exit 64
	;;
esac
