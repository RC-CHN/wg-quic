#!/bin/sh
set -eu

mkdir -p /dev/net
if [ ! -c /dev/net/tun ]; then
	mknod /dev/net/tun c 10 200
fi

# Docker may disable IPv6 on newly created interfaces even though the kernel
# supports it. This sysctl is confined to the container network namespace.
sysctl -q net.ipv6.conf.all.disable_ipv6=0

exec /usr/local/bin/wg-quic-quick run wg0
