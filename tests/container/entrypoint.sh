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

exec /usr/local/bin/wg-quic-quick run wg0
