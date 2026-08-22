#!/bin/sh
set -eu

# A bind-mounted source keeps the host runner's uid. Production quick rightly
# rejects that as an insecure canonical path, so materialize the disposable
# fixture as the same root-owned 0600 file an installed system would use.
mkdir -p /etc/wg-quic
cp /run/wg-quic-test/wg0.conf /etc/wg-quic/wg0.conf
chown 0:0 /etc/wg-quic /etc/wg-quic/wg0.conf
chmod 0700 /etc/wg-quic
chmod 0600 /etc/wg-quic/wg0.conf

mkdir -p /dev/net
if [ ! -c /dev/net/tun ]; then
	mknod /dev/net/tun c 10 200
fi

# Docker may disable IPv6 on newly created interfaces even though the kernel
# supports it. This sysctl is confined to the container network namespace.
sysctl -q net.ipv6.conf.all.disable_ipv6=0

if [ "${WG_QUIC_TEST_PARENT_DEATH:-0}" = 1 ]; then
	/usr/local/bin/wg-quic-quick run wg0 &
	quick_pid=$!
	printf '%s\n' "$quick_pid" >/tmp/wg-quic-quick.pid
	if wait "$quick_pid"; then
		quick_status=0
	else
		quick_status=$?
	fi
	# This fixture deliberately kills quick with SIGKILL. The inherited
	# supervisor pipe must still make the core close its TUN without help from
	# a systemd cgroup, procd, or the container runtime.
	attempt=0
	while :; do
		core_alive=false
		for comm in /proc/[0-9]*/comm; do
			if [ "$(cat "$comm" 2>/dev/null || true)" = wg-quic ]; then
				core_alive=true
				break
			fi
		done
		if [ "$core_alive" = false ] && ! ip link show dev wg0 >/dev/null 2>&1; then
			if [ "$quick_status" -eq 137 ]; then
				printf passed >/tmp/parent-death.result
			else
				printf failed >/tmp/parent-death.result
			fi
			break
		fi
		attempt=$((attempt + 1))
		if [ "$attempt" -ge 100 ]; then
			printf failed >/tmp/parent-death.result
			break
		fi
		sleep 0.05
	done
	while :; do sleep 3600; done
fi

exec /usr/local/bin/wg-quic-quick run wg0
