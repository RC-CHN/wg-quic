#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH='' cd -- "$script_dir/../.." && pwd)
compose="docker compose -f $script_dir/compose.yaml"

cleanup() {
	$compose down --volumes --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

fail_with_logs() {
	$compose ps
	$compose logs
	echo "$1" >&2
	exit 1
}

wait_ping() {
	service=$1
	destination=$2
	family=$3
	description=$4
	attempt=0
	until $compose exec -T "$service" "ping$family" -c 1 -W 1 "$destination" >/dev/null 2>&1; do
		attempt=$((attempt + 1))
		if [ "$attempt" -ge 45 ]; then
			fail_with_logs "$description"
		fi
		sleep 1
	done
}

start_iperf_server() {
	service=$1
	address=$2
	port=$3
	$compose exec -T "$service" sh -ec \
		"iperf3 -s -1 -B '$address' -p '$port' >/tmp/iperf-$port.log 2>&1 &"
	attempt=0
	until $compose exec -T "$service" sh -ec \
		"ss -H -ltn 'sport = :$port' | grep -q LISTEN"; do
		attempt=$((attempt + 1))
		if [ "$attempt" -ge 50 ]; then
			$compose exec -T "$service" sh -c "cat /tmp/iperf-$port.log" || true
			fail_with_logs "iperf3 server on $service:$port did not become ready"
		fi
		sleep 0.1
	done
}

mkdir -p "$script_dir/build"
(cd "$repo_dir" && CGO_ENABLED=0 go build -trimpath -o "$script_dir/build/wg-quic" ./cmd/wg-quic)
docker build -t wg-quic-e2e:local -f "$script_dir/Dockerfile" "$repo_dir"

$compose up -d

netns_a=$($compose exec -T a readlink /proc/1/ns/net)
netns_b=$($compose exec -T b readlink /proc/1/ns/net)
if [ "$netns_a" = "$netns_b" ]; then
	fail_with_logs "A and B unexpectedly share a network namespace"
fi

wait_ping a 10.77.0.2 "" "A to B IPv4 tunnel ping did not become ready"
wait_ping a fd00:77::2 6 "A to B IPv6 tunnel ping did not become ready"
wait_ping a6 10.78.0.2 "" "tunnel over an IPv6 QUIC endpoint did not become ready"

$compose exec -T a sh -ec \
	"ip -4 route show table 51820 | grep -q 'default dev wg0'"
$compose exec -T a sh -ec \
	"ip -4 rule show | grep -q 'lookup 51820'"

$compose exec -T b ping -c 3 -W 2 10.77.0.1
$compose exec -T b ping6 -c 3 -W 2 fd00:77::1
$compose exec -T b6 ping -c 3 -W 2 10.78.0.1
$compose exec -T b6 ping6 -c 3 -W 2 fd00:78::1
$compose exec -T a ping -c 3 -W 2 -s 1300 10.77.0.2

# Prove that WireGuard cryptokey routing rejects an authenticated packet whose
# inner source is outside the sending peer's AllowedIPs on the receiver.
$compose exec -T a ip address add 10.77.99.1/32 dev wg0
if $compose exec -T a ping -I 10.77.99.1 -c 1 -W 1 10.77.0.2 >/dev/null 2>&1; then
	fail_with_logs "receiver accepted a tunnel source outside the peer's AllowedIPs"
fi
$compose exec -T a ip address delete 10.77.99.1/32 dev wg0

# Mirror the imported WireGuard fork's netns data-plane matrix with bounded CI
# transfer sizes: TCP and UDP in both address families and both directions.
start_iperf_server b 10.77.0.2 5201
$compose exec -T a iperf3 -c 10.77.0.2 -p 5201 -n 16M
start_iperf_server a fd00:77::1 5202
$compose exec -T b iperf3 -6 -c fd00:77::1 -p 5202 -n 16M
start_iperf_server a 10.77.0.1 5203
$compose exec -T b iperf3 -c 10.77.0.1 -p 5203 -u -b 10M -t 2
start_iperf_server b fd00:77::2 5204
$compose exec -T a iperf3 -6 -c fd00:77::2 -p 5204 -u -b 10M -t 2

# The imported suite raises the interface MTU far beyond Ethernet MTU. Verify
# that a single large inner packet survives WireGuard encryption plus wg-quic
# fragmentation/reassembly, over both IPv4 and IPv6.
$compose exec -T a ip link set dev wg0 mtu 16000
$compose exec -T b ip link set dev wg0 mtu 16000
$compose exec -T a ping -c 3 -W 3 -M "do" -s 12000 10.77.0.2
$compose exec -T b ping6 -c 3 -W 3 -s 12000 fd00:77::1

# Lower the actual carrier MTU while retaining the large TUN MTU. Each
# wg-quic fragment must remain below the path MTU.
$compose exec -T a ip link set dev eth0 mtu 1280
$compose exec -T b ip link set dev eth0 mtu 1280
$compose exec -T a ping -c 3 -W 3 -M "do" -s 12000 10.77.0.2
$compose exec -T a ip link set dev eth0 mtu 1500
$compose exec -T b ip link set dev eth0 mtu 1500
$compose exec -T a ip link set dev wg0 mtu 1380
$compose exec -T b ip link set dev wg0 mtu 1380

# Exercise the real carrier under loss. Unit tests deterministically prove
# single-shard recovery; this verifies that the FEC path remains usable through
# QUIC, Linux TUN devices, and netem together.
$compose exec -T b tc qdisc replace dev eth0 root netem loss 10%
$compose exec -T a ping -c 50 -i 0.02 -W 2 10.77.0.2
$compose exec -T b tc qdisc del dev eth0 root

# Reordering, duplication, latency, and asymmetric impairment must not corrupt
# packet boundaries or wedge the session.
$compose exec -T b tc qdisc replace dev eth0 root netem \
	delay 25ms 8ms loss 3% duplicate 2% reorder 20% 50%
$compose exec -T a ping -c 20 -i 0.05 -W 3 10.77.0.2
$compose exec -T b tc qdisc del dev eth0 root

# Exercise QUIC path migration through an outer source-address change. The
# alias, NAT rule, and all traffic stay inside container A's network namespace.
$compose exec -T a ip address add 172.29.0.22/24 dev eth0
$compose exec -T a iptables -t nat -I POSTROUTING 1 \
	-p udp -s 172.29.0.2 --sport 51820 -d 172.29.0.3 --dport 51820 \
	-j SNAT --to-source 172.29.0.22
wait_ping a 10.77.0.2 "" "tunnel did not survive outer NAT address rebinding"
# The WireGuard keepalive interval is one second. Leave the translated path
# idle from the application's perspective before proving it remains usable.
sleep 3
$compose exec -T b ping -c 3 -W 2 10.77.0.1
$compose exec -T a iptables -t nat -D POSTROUTING \
	-p udp -s 172.29.0.2 --sport 51820 -d 172.29.0.3 --dport 51820 \
	-j SNAT --to-source 172.29.0.22
$compose exec -T a ip address delete 172.29.0.22/24 dev eth0
wait_ping a 10.77.0.2 "" "tunnel did not migrate back to its original outer address"

# A peer process disappearing destroys all QUIC state. WireGuard's retry
# traffic must cause ArmorBind to establish a fresh session without restarting
# the surviving peer.
$compose restart b
wait_ping a 10.77.0.2 "" "tunnel did not recover after peer process restart"
wait_ping a fd00:77::2 6 "IPv6 tunnel did not recover after peer process restart"
$compose exec -T b ping -c 3 -W 2 10.77.0.1

status=$($compose exec -T a wg-quic show wg0 --json)
echo "$status"
obfs=$(echo "$status" | sed -n 's/.*"obfs_mode": "\([^"]*\)".*/\1/p')
if [ "$obfs" != "salamander" ]; then
	echo "standard WireGuard config did not enable key-derived Salamander obfuscation" >&2
	exit 1
fi
wg_tx=$(echo "$status" | sed -n 's/.*"wg_tx_packets": \([0-9][0-9]*\).*/\1/p')
wg_rx=$(echo "$status" | sed -n 's/.*"wg_rx_packets": \([0-9][0-9]*\).*/\1/p')
if [ -z "$wg_tx" ] || [ "$wg_tx" -eq 0 ] || [ -z "$wg_rx" ] || [ "$wg_rx" -eq 0 ]; then
	echo "WireGuard status counters did not account for bidirectional traffic" >&2
	exit 1
fi
recovered=$(echo "$status" | sed -n 's/.*"fec_recovered": \([0-9][0-9]*\).*/\1/p')
if [ -z "$recovered" ] || [ "$recovered" -eq 0 ]; then
	echo "FEC status did not report a recovered shard" >&2
	exit 1
fi

$compose exec -T a ip -details link show wg0
$compose exec -T b ip -details link show wg0

echo "wg-quic container interoperability and pinned WireGuard behavior test passed"
