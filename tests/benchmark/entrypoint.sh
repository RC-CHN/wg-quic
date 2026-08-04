#!/bin/sh
set -eu

mkdir -p /dev/net
if [ ! -c /dev/net/tun ]; then
	mknod /dev/net/tun c 10 200
fi

sysctl -q net.ipv6.conf.all.disable_ipv6=0
if [ "${WGQ_BENCH_DISABLE_OFFLOADS:-0}" = 1 ]; then
	ethtool -K eth0 tso off gso off gro off \
		tx-udp-segmentation off rx-udp-gro-forwarding off >/dev/null
fi

case "${WGQ_BENCH_TRANSPORT:-wg-quic}" in
wg-quic)
	exec /usr/local/bin/wg-quic-quick run wg0
	;;
direct-wireguard-go)
	: "${WGQ_BENCH_ADDRESS:?direct WireGuard address is required}"
	: "${WGQ_BENCH_PEER_ADDRESS:?direct WireGuard peer address is required}"
	: "${WGQ_BENCH_MTU:?direct WireGuard MTU is required}"

	mkdir -p /var/run/wireguard
	WG_PROCESS_FOREGROUND=1 LOG_LEVEL=error /usr/local/bin/wireguard-go -f wg0 &
	wireguard_pid=$!
	trap 'kill "$wireguard_pid" 2>/dev/null || true' INT TERM EXIT

	attempt=0
	while ! ip link show dev wg0 >/dev/null 2>&1; do
		if ! kill -0 "$wireguard_pid" 2>/dev/null; then
			wait "$wireguard_pid"
		fi
		attempt=$((attempt + 1))
		if [ "$attempt" -ge 100 ]; then
			echo "wireguard-go did not create wg0" >&2
			exit 1
		fi
		sleep 0.05
	done

	/usr/local/bin/wg-uapi set \
		/var/run/wireguard/wg0.sock /etc/wg-quic/wg0.uapi
	ip address add "$WGQ_BENCH_ADDRESS" dev wg0
	ip link set dev wg0 mtu "$WGQ_BENCH_MTU" up
	ip route replace "$WGQ_BENCH_PEER_ADDRESS" dev wg0

	wait "$wireguard_pid"
	;;
*)
	echo "unsupported benchmark transport: $WGQ_BENCH_TRANSPORT" >&2
	exit 1
	;;
esac
