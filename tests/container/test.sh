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

wait_background_result() {
	service=$1
	result_path=$2
	description=$3
	attempt=0
	while :; do
		result=$($compose exec -T "$service" sh -c \
			"test -f '$result_path' && cat '$result_path'" 2>/dev/null || true)
		case "$result" in
		passed) return 0 ;;
		failed) fail_with_logs "$description" ;;
		esac
		attempt=$((attempt + 1))
		if [ "$attempt" -ge 90 ]; then
			fail_with_logs "$description did not finish"
		fi
		sleep 0.5
	done
}

assert_unrelated_peer_stable() {
	status=$($compose exec -T a wg-quic-quick show wg0 --json)
	if [ "$(printf '%s\n' "$status" | jq -r '.supervisor_epoch')" != "$continuity_epoch" ]; then
		fail_with_logs "peer reconciliation replaced the supervisor"
	fi
	peer=$(printf '%s\n' "$status" | jq -c --arg key "$continuity_peer_key" \
		'.peers[] | select(.public_key == $key)')
	if [ -z "$peer" ] || [ "$(printf '%s\n' "$peer" | jq -r '.session')" != established ]; then
		fail_with_logs "unrelated peer lost its established session during reconciliation"
	fi
	if [ "$(printf '%s\n' "$peer" | jq -r '.authenticated_endpoint_generation')" != \
		"$continuity_endpoint_generation" ]; then
		fail_with_logs "unrelated peer endpoint generation changed during reconciliation"
	fi
	if [ "$(printf '%s\n' "$peer" | jq -r '.reconnect_attempts')" != \
		"$continuity_reconnect_attempts" ]; then
		fail_with_logs "unrelated peer reconnected during reconciliation"
	fi
}

mkdir -p "$script_dir/build"
(cd "$repo_dir" && CGO_ENABLED=0 go build -trimpath -o "$script_dir/build/wg-quic" ./cmd/wg-quic)
(cd "$repo_dir" && CGO_ENABLED=0 go build -trimpath -o "$script_dir/build/wg-quic-quick" ./cmd/wg-quic-quick)
docker build -t wg-quic-e2e:local -f "$script_dir/Dockerfile" "$repo_dir"

$compose up -d

netns_a=$($compose exec -T a readlink /proc/1/ns/net)
netns_b=$($compose exec -T b readlink /proc/1/ns/net)
netns_c=$($compose exec -T c readlink /proc/1/ns/net)
netns_lifetime=$($compose exec -T lifetime readlink /proc/1/ns/net)
if [ "$netns_a" = "$netns_b" ] || [ "$netns_a" = "$netns_c" ] ||
	[ "$netns_b" = "$netns_c" ] || [ "$netns_a" = "$netns_lifetime" ]; then
	fail_with_logs "interoperability peers unexpectedly share a network namespace"
fi

# Kill only wg-quic-quick while leaving the container/init process alive. The
# core must observe EOF on its inherited supervisor descriptor, exit, and
# release wg0 without relying on a service-manager cgroup.
$compose exec -T lifetime sh -ec \
	'test -s /tmp/wg-quic-quick.pid; xargs kill -KILL </tmp/wg-quic-quick.pid'
wait_background_result lifetime /tmp/parent-death.result \
	"core or TUN survived abrupt quick supervisor termination"

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

# Reconcile a second peer while unrelated TCP and UDP flows remain active.
# This is the privileged acceptance path for the guarantee that an add,
# update, or removal preserves the existing peer object and QUIC session.
continuity_peer_key=IMmmkZOkcoM8nTU8QQPTEreFZj0CIjIGvkQRxrk6sjA=
continuity_status=$($compose exec -T a wg-quic-quick show wg0 --json)
continuity_epoch=$(printf '%s\n' "$continuity_status" | jq -r '.supervisor_epoch')
continuity_endpoint_generation=$(printf '%s\n' "$continuity_status" |
	jq -r --arg key "$continuity_peer_key" \
		'.peers[] | select(.public_key == $key) | .authenticated_endpoint_generation')
continuity_reconnect_attempts=$(printf '%s\n' "$continuity_status" |
	jq -r --arg key "$continuity_peer_key" \
		'.peers[] | select(.public_key == $key) | .reconnect_attempts')
test -n "$continuity_epoch"
test "$continuity_endpoint_generation" -gt 0

start_iperf_server b 10.77.0.2 5210
start_iperf_server b 10.77.0.2 5211
$compose exec -T a sh -c \
	'rm -f /tmp/reconcile-tcp.result /tmp/reconcile-udp.result;
	(iperf3 -c 10.77.0.2 -p 5210 -t 20 --get-server-output &&
	 printf passed > /tmp/reconcile-tcp.result ||
	 printf failed > /tmp/reconcile-tcp.result) >/tmp/reconcile-tcp.log 2>&1 &
	(iperf3 -c 10.77.0.2 -p 5211 -u -b 5M -t 20 --get-server-output &&
	 printf passed > /tmp/reconcile-udp.result ||
	 printf failed > /tmp/reconcile-udp.result) >/tmp/reconcile-udp.log 2>&1 &'

$compose exec -T a sh -ec \
	'cp /run/wg-quic-test/a-with-c.conf /etc/wg-quic/reconcile.conf;
	chown 0:0 /etc/wg-quic/reconcile.conf;
	chmod 0600 /etc/wg-quic/reconcile.conf'
add_result=$($compose exec -T a wg-quic-quick reconcile wg0 \
	/etc/wg-quic/reconcile.conf --expected-epoch "$continuity_epoch" \
	--expected-generation 1 --request-id container-add-c --json)
printf '%s\n' "$add_result" | jq -e \
	'.result.state == "committed" and .result.generation == 2' >/dev/null
wait_ping a 10.77.1.2 "" "newly reconciled peer did not pass traffic"
assert_unrelated_peer_stable

# The same request ID and content returns the cached terminal result even
# though its original CAS generation is now old. Different content with the
# same ID and an ordinary stale request must both fail without mutation.
cached_add=$($compose exec -T a wg-quic-quick reconcile wg0 \
	/etc/wg-quic/reconcile.conf --expected-epoch "$continuity_epoch" \
	--expected-generation 1 --request-id container-add-c --json)
printf '%s\n' "$cached_add" | jq -e \
	'.result.state == "committed" and .result.generation == 2' >/dev/null
$compose exec -T a sh -ec \
	'cp /run/wg-quic-test/a-update-c.conf /etc/wg-quic/reconcile.conf;
	chown 0:0 /etc/wg-quic/reconcile.conf;
	chmod 0600 /etc/wg-quic/reconcile.conf'
if $compose exec -T a wg-quic-quick reconcile wg0 \
	/etc/wg-quic/reconcile.conf --expected-epoch "$continuity_epoch" \
	--expected-generation 2 --request-id container-add-c --json >/dev/null 2>&1; then
	fail_with_logs "request ID collision accepted different desired content"
fi
if $compose exec -T a wg-quic-quick reconcile wg0 \
	/etc/wg-quic/reconcile.conf --expected-epoch "$continuity_epoch" \
	--expected-generation 1 --request-id container-stale-c --json >/dev/null 2>&1; then
	fail_with_logs "stale desired generation mutated the running interface"
fi
test "$($compose exec -T a wg-quic-quick show wg0 --json |
	jq -r '.desired_generation')" = 2
assert_unrelated_peer_stable

update_result=$($compose exec -T a wg-quic-quick reconcile wg0 \
	/etc/wg-quic/reconcile.conf --expected-epoch "$continuity_epoch" \
	--expected-generation 2 --request-id container-update-c --json)
printf '%s\n' "$update_result" | jq -e \
	'.result.state == "committed" and .result.generation == 3' >/dev/null
$compose exec -T a wg-quic-quick show wg0 --json | jq -e \
	'.peers[] | select(.public_key == "4k+Xrgxvp1aRW7v4fZaoh2Bmgea6jQEpMIYSlnWU0Es=") |
	 .fec_policy == "throughput" and (.configured_endpoint == "172.29.0.6:51820")' \
	>/dev/null
wait_ping a 10.77.1.2 "" "updated peer stopped passing traffic"
assert_unrelated_peer_stable

$compose exec -T a sh -ec \
	'cp /run/wg-quic-test/wg0.conf /etc/wg-quic/reconcile.conf;
	chown 0:0 /etc/wg-quic/reconcile.conf;
	chmod 0600 /etc/wg-quic/reconcile.conf'
remove_result=$($compose exec -T a wg-quic-quick reconcile wg0 \
	/etc/wg-quic/reconcile.conf --expected-epoch "$continuity_epoch" \
	--expected-generation 3 --request-id container-remove-c --json)
printf '%s\n' "$remove_result" | jq -e \
	'.result.state == "committed" and .result.generation == 4' >/dev/null
test "$($compose exec -T a wg-quic-quick show wg0 --json |
	jq '[.peers[]] | length')" = 1
test "$($compose exec -T a wg-quic-quick show wg0 --json |
	jq -r '.persistent_drift')" = false
assert_unrelated_peer_stable

wait_background_result a /tmp/reconcile-tcp.result \
	"unrelated TCP flow failed during peer reconciliation"
wait_background_result a /tmp/reconcile-udp.result \
	"unrelated UDP flow failed during peer reconciliation"
$compose exec -T a grep -Eq '0/[1-9][0-9]* \(0%\)' /tmp/reconcile-udp.log ||
	fail_with_logs "unrelated UDP flow lost packets during peer reconciliation"

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
wg_tx=$(printf '%s\n' "$status" | jq -er '.stats.wg_tx_packets')
wg_rx=$(printf '%s\n' "$status" | jq -er '.stats.wg_rx_packets')
if [ -z "$wg_tx" ] || [ "$wg_tx" -eq 0 ] || [ -z "$wg_rx" ] || [ "$wg_rx" -eq 0 ]; then
	echo "WireGuard status counters did not account for bidirectional traffic" >&2
	exit 1
fi
fec_data_tx=$(printf '%s\n' "$status" | jq -er '.stats.fec_data_tx')
fec_parity_tx=$(printf '%s\n' "$status" | jq -er '.stats.fec_parity_tx')
if [ -z "$fec_data_tx" ] || [ "$fec_data_tx" -eq 0 ] ||
	[ -z "$fec_parity_tx" ] || [ "$fec_parity_tx" -eq 0 ]; then
	echo "FEC status did not report protected data and parity traffic" >&2
	exit 1
fi

# Exercise the installed management client against a live privileged tunnel.
# This catches transport, peer selection, filesystem permission, and artifact
# finalization regressions that a fake collector client cannot expose.
collection_peer=$(printf '%s\n' "$quick_status" | jq -er '.peers[0].public_key')
$compose exec -T a wg-quic-quick collect wg0 \
	--peer "$collection_peer" --duration 500ms --interval 100ms \
	--max-bytes 1M --output /tmp/wg-quic-observe-smoke
# The substitutions below intentionally run in the guest shell, not this one.
# shellcheck disable=SC2016
$compose exec -T a sh -ec '
	test "$(stat -c %a /tmp/wg-quic-observe-smoke)" = 700
	test -f /tmp/wg-quic-observe-smoke/COMPLETE
	test ! -e /tmp/wg-quic-observe-smoke/INCOMPLETE
	for artifact in manifest.json status.ndjson peer-telemetry.csv controller-events.ndjson summary.json; do
		test -f "/tmp/wg-quic-observe-smoke/$artifact"
		test "$(stat -c %a "/tmp/wg-quic-observe-smoke/$artifact")" = 600
	done
	jq -e . /tmp/wg-quic-observe-smoke/manifest.json >/dev/null
	jq -e . /tmp/wg-quic-observe-smoke/summary.json >/dev/null
	jq -e -s "length > 0" /tmp/wg-quic-observe-smoke/status.ndjson >/dev/null
	test "$(wc -l </tmp/wg-quic-observe-smoke/peer-telemetry.csv)" -gt 1
'

$compose exec -T a ip -details link show wg0
$compose exec -T b ip -details link show wg0

echo "wg-quic container interoperability and pinned WireGuard behavior test passed"
