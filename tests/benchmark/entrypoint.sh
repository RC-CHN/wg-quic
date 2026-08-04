#!/bin/sh
set -eu

mkdir -p /dev/net
if [ ! -c /dev/net/tun ]; then
	mknod /dev/net/tun c 10 200
fi

sysctl -q net.ipv6.conf.all.disable_ipv6=0

exec /usr/local/bin/wg-quic-quick run wg0
