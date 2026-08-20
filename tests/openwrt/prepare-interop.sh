#!/bin/sh
# shellcheck disable=SC2016

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH='' cd -- "$script_dir/../.." && pwd)
state_dir="$script_dir/.qemu"
tools_dir="$state_dir/tools"

: "${WG_QUIC_OPENWRT_ENDPOINT:=127.0.0.1:25180}"
: "${GOCACHE:=/tmp/wg-quic-openwrt-go-cache}"

mkdir -p "$state_dir" "$tools_dir" "$GOCACHE"

CGO_ENABLED=0 GOCACHE="$GOCACHE" \
	go build -buildvcs=false -trimpath \
	-o "$tools_dir/wg-quic" "$repo_dir/cmd/wg-quic"
CGO_ENABLED=0 GOCACHE="$GOCACHE" \
	go build -buildvcs=false -trimpath \
	-o "$tools_dir/wg-quic-netstack-client" \
	"$repo_dir/wg-quic-opnsense/scripts/qemu/linux-client"

guest_private=$("$tools_dir/wg-quic" genkey)
guest_public=$(printf '%s\n' "$guest_private" | "$tools_dir/wg-quic" pubkey)
client_private=$("$tools_dir/wg-quic" genkey)
client_public=$(printf '%s\n' "$client_private" | "$tools_dir/wg-quic" pubkey)

umask 077
{
	printf '%s\n' '[Interface]'
	printf 'PrivateKey = %s\n' "$guest_private"
	printf '%s\n' 'Address = 10.79.0.1/32'
	printf '%s\n' 'ListenPort = 51820'
	printf '%s\n' 'PostUp = nft add table inet wg_quic_qemu; nft insert rule inet fw4 input iifname "%i" accept comment "wg-quic-qemu-%i"'
	printf '%s\n' 'PostDown = for handle in $(nft -a list chain inet fw4 input | awk '\''/wg-quic-qemu-%i/ { print $NF }'\''); do nft delete rule inet fw4 input handle "$handle"; done; nft delete table inet wg_quic_qemu'
	printf '%s\n' '# wg-quic: fec = auto'
	printf '%s\n' '# wg-quic: obfs = salamander'
	printf '\n%s\n' '[Peer]'
	printf 'PublicKey = %s\n' "$client_public"
	printf '%s\n' 'AllowedIPs = 10.79.0.2/32'
} > "$state_dir/openwrt.conf"

{
	printf '%s\n' '[Interface]'
	printf 'PrivateKey = %s\n' "$client_private"
	printf '%s\n' 'Address = 10.79.0.2/32'
	printf '%s\n' 'ListenPort = 51821'
	printf '%s\n' 'MTU = 1280'
	printf '%s\n' 'Table = off'
	printf '%s\n' '# wg-quic: fec = auto'
	printf '%s\n' '# wg-quic: obfs = salamander'
	printf '\n%s\n' '[Peer]'
	printf 'PublicKey = %s\n' "$guest_public"
	printf 'Endpoint = %s\n' "$WG_QUIC_OPENWRT_ENDPOINT"
	printf '%s\n' 'AllowedIPs = 10.79.0.1/32'
} > "$state_dir/host.conf"

chmod 0600 "$state_dir/openwrt.conf" "$state_dir/host.conf"
"$tools_dir/wg-quic" check "$state_dir/openwrt.conf"
"$tools_dir/wg-quic" check "$state_dir/host.conf"
echo "prepared OpenWrt interoperability fixture in $state_dir"
