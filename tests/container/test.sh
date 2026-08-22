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
(cd "$repo_dir" && CGO_ENABLED=0 go build -trimpath -o "$script_dir/build/wg-quic-quick" ./cmd/wg-quic-quick)
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
# single-shard recovery; this verifies that the FEC path emits protected data
# and remains usable through QUIC, Linux TUN devices, and random netem loss.
# Don't require a recovery counter here: a finite random sample can drop only
# parity shards or multiple shards from a block.
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
# Netfilter chooses NAT only for the first packet in a conntrack flow. Drop the
# established mapping so the next QUIC packet actually exercises the new path.
# A packet sent by B's QUIC keepalive can otherwise win the race and create the
# reverse flow first, bypassing A's POSTROUTING SNAT for the lifetime of that
# entry. Block only B's old-path packets in raw PREROUTING until A establishes
# the translated flow; raw runs before conntrack, while replies to the new .22
# address remain allowed.
$compose exec -T a iptables -t raw -I PREROUTING 1 \
	-p udp -s 172.29.0.3 --sport 51820 -d 172.29.0.2 --dport 51820 \
	-j DROP
$compose exec -T a conntrack -F
wait_ping a 10.77.0.2 "" "tunnel did not survive outer NAT address rebinding"
$compose exec -T a iptables -t raw -D PREROUTING \
	-p udp -s 172.29.0.3 --sport 51820 -d 172.29.0.2 --dport 51820 \
	-j DROP
$compose exec -T a sh -ec \
	"iptables-save -c -t nat | grep -Eq '^\\[[1-9][0-9]*:[0-9]+\\] -A POSTROUTING .*--to-source 172\\.29\\.0\\.22$'" ||
	fail_with_logs "outer rebinding fixture did not traverse the SNAT rule"
attempt=0
while :; do
	rebound_endpoint=$($compose exec -T b wg-quic show wg0 --json |
		sed -n 's/.*"endpoint": "\([^"]*\)".*/\1/p')
	if [ "${rebound_endpoint}" = "172.29.0.22:51820" ]; then
		break
	fi
	# QUIC can deliver the first packet from a rebinding before promoting the
	# validated remote path reported to later datagrams. Endpoint roaming is
	# authenticated by WireGuard traffic, so keep supplying bounded probes
	# instead of polling status after a single possibly pre-promotion packet.
	$compose exec -T a ping -c 1 -W 1 10.77.0.2 >/dev/null 2>&1 || true
	attempt=$((attempt + 1))
	if [ "${attempt}" -ge 100 ]; then
		fail_with_logs "peer status retained the pre-migration outer endpoint"
	fi
	sleep 0.1
done
# Leave the translated path idle from the application's perspective before
# proving QUIC's own keepalive preserves it without WireGuard PersistentKeepalive.
sleep 3
$compose exec -T b ping -c 3 -W 2 10.77.0.1
$compose exec -T a iptables -t nat -D POSTROUTING \
	-p udp -s 172.29.0.2 --sport 51820 -d 172.29.0.3 --dport 51820 \
	-j SNAT --to-source 172.29.0.22
$compose exec -T a conntrack -F
$compose exec -T a ip address delete 172.29.0.22/24 dev eth0
wait_ping a 10.77.0.2 "" "tunnel did not migrate back to its original outer address"
attempt=0
while :; do
	restored_endpoint=$($compose exec -T b wg-quic show wg0 --json |
		sed -n 's/.*"endpoint": "\([^"]*\)".*/\1/p')
	if [ "${restored_endpoint}" = "172.29.0.2:51820" ]; then
		break
	fi
	$compose exec -T a ping -c 1 -W 1 10.77.0.2 >/dev/null 2>&1 || true
	attempt=$((attempt + 1))
	if [ "${attempt}" -ge 100 ]; then
		fail_with_logs "peer status did not follow the restored outer endpoint"
	fi
	sleep 0.1
done

# A peer process disappearing destroys QUIC and WireGuard state. Neither side
# has PersistentKeepalive in this fixture. Require the surviving side to
# restore QUIC and proactively complete a newer WireGuard handshake while no
# inner traffic is being generated.
before_restart_status=$($compose exec -T a wg-quic show wg0 --json)
before_restart_attempts=$(echo "$before_restart_status" |
	sed -n 's/.*"reconnect_attempts": \([0-9][0-9]*\).*/\1/p')
before_restart_handshake=$(echo "$before_restart_status" |
	sed -n 's/.*"latest_handshake": \([0-9][0-9]*\).*/\1/p')
: "${before_restart_attempts:=0}"
: "${before_restart_handshake:=0}"
$compose restart b
attempt=0
while :; do
	after_restart_status=$($compose exec -T a wg-quic show wg0 --json)
	after_restart_session=$(echo "$after_restart_status" |
		sed -n 's/.*"session": "\([^"]*\)".*/\1/p')
	after_restart_attempts=$(echo "$after_restart_status" |
		sed -n 's/.*"reconnect_attempts": \([0-9][0-9]*\).*/\1/p')
	after_restart_handshake=$(echo "$after_restart_status" |
		sed -n 's/.*"latest_handshake": \([0-9][0-9]*\).*/\1/p')
	: "${after_restart_attempts:=0}"
	: "${after_restart_handshake:=0}"
	if [ "$after_restart_session" = established ] &&
		[ "$after_restart_attempts" -gt "$before_restart_attempts" ] &&
		[ "$after_restart_handshake" -gt "$before_restart_handshake" ]; then
		break
	fi
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 45 ]; then
		fail_with_logs "tunnel did not autonomously recover while idle after peer restart"
	fi
	sleep 1
done
wait_ping a 10.77.0.2 "" "tunnel did not recover after peer process restart"
wait_ping a fd00:77::2 6 "IPv6 tunnel did not recover after peer process restart"
$compose exec -T b ping -c 3 -W 2 10.77.0.1

status=$($compose exec -T a wg-quic show wg0 --json)
echo "$status"
quick_status=$($compose exec -T a wg-quic-quick show wg0 --json)
echo "$quick_status"
if ! echo "$quick_status" | grep -Eq '"endpoint": "[^"]+:[0-9]+"'; then
	echo "wg-quic-quick status did not expose the current peer endpoint" >&2
	exit 1
fi
for field in last_rx last_tx last_activity; do
	value=$(echo "$quick_status" |
		sed -n 's/.*"'"$field"'": \([0-9][0-9]*\).*/\1/p')
	if [ -z "$value" ] || [ "$value" -eq 0 ]; then
		echo "wg-quic-quick status did not expose nonzero $field" >&2
		exit 1
	fi
done
if ! echo "$quick_status" |
	grep -Eq '"last_activity_direction": "(received|sent)"'; then
	echo "wg-quic-quick status did not expose the activity direction" >&2
	exit 1
fi
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
fec_data_tx=$(echo "$status" | sed -n 's/.*"fec_data_tx": \([0-9][0-9]*\).*/\1/p')
fec_parity_tx=$(echo "$status" | sed -n 's/.*"fec_parity_tx": \([0-9][0-9]*\).*/\1/p')
if [ -z "$fec_data_tx" ] || [ "$fec_data_tx" -eq 0 ] ||
	[ -z "$fec_parity_tx" ] || [ "$fec_parity_tx" -eq 0 ]; then
	echo "FEC status did not report protected data and parity traffic" >&2
	exit 1
fi

$compose exec -T a ip -details link show wg0
$compose exec -T b ip -details link show wg0

echo "wg-quic container interoperability and pinned WireGuard behavior test passed"
