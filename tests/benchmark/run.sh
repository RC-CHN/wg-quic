#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH='' cd -- "$script_dir/../.." && pwd)
generated_dir="$script_dir/.generated"
results_root=${RESULTS_DIR:-"$script_dir/results"}
project_name=${COMPOSE_PROJECT_NAME:-wg-quic-bench}
compose=(docker compose -p "$project_name" -f "$script_dir/compose.yaml")
benchmark_transport=wg-quic
benchmark_mtu=1280
benchmark_disable_offloads=0
benchmark_protocol_policy=none
all_modes=(direct-wireguard-go nofec-plain nofec-obfs fec-plain fec-obfs)

require_command() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "required command is not installed: $1" >&2
		exit 1
	fi
}

compose_run() {
	WGQ_BENCH_CONFIG_DIR="$generated_dir" \
	WGQ_BENCH_TRANSPORT="$benchmark_transport" \
	WGQ_BENCH_MTU="$benchmark_mtu" \
	WGQ_BENCH_DISABLE_OFFLOADS="$benchmark_disable_offloads" \
		"${compose[@]}" "$@"
}

compose_timeout() {
	local seconds=$1
	shift
	timeout "$seconds" env \
		WGQ_BENCH_CONFIG_DIR="$generated_dir" \
		WGQ_BENCH_TRANSPORT="$benchmark_transport" \
		WGQ_BENCH_MTU="$benchmark_mtu" \
		WGQ_BENCH_DISABLE_OFFLOADS="$benchmark_disable_offloads" \
		"${compose[@]}" "$@"
}

mode_settings() {
	case "$1" in
	fec-obfs)
		printf '%s %s\n' auto salamander
		;;
	fec-plain)
		printf '%s %s\n' auto none
		;;
	nofec-obfs)
		printf '%s %s\n' off salamander
		;;
	nofec-plain)
		printf '%s %s\n' off none
		;;
	direct-wireguard-go)
		printf '%s %s\n' off none
		;;
	*)
		echo "unsupported MODE $1" >&2
		return 1
		;;
	esac
}

transport_for_mode() {
	case "$1" in
	direct-wireguard-go)
		echo direct-wireguard-go
		;;
	fec-obfs | fec-plain | nofec-obfs | nofec-plain)
		echo wg-quic
		;;
	*)
		echo "unsupported MODE $1" >&2
		return 1
		;;
	esac
}

validate_number() {
	local name=$1
	local value=$2
	if [[ ! $value =~ ^[0-9]+([.][0-9]+)?$ ]]; then
		echo "$name must be a non-negative number, got $value" >&2
		return 1
	fi
}

validate_integer() {
	local name=$1
	local value=$2
	if [[ ! $value =~ ^[0-9]+$ ]]; then
		echo "$name must be a non-negative integer, got $value" >&2
		return 1
	fi
}

resolve_link_profile() {
	local profile=$1
	local base_impairment=$2
	local base_rate=$3
	local base_delay=$4
	local base_jitter=$5
	local base_loss=$6
	local base_queue=$7

	link_profile=$profile
	link_queue_packets=$base_queue
	link_loss_correlation=0
	link_reorder_correlation=0
	link_loss_model=random
	link_burst_len=0
	case "$profile" in
	custom)
		link_impairment=$base_impairment
		link_fwd_rate=${FWD_RATE_MBIT:-$base_rate}
		link_rev_rate=${REV_RATE_MBIT:-$base_rate}
		link_fwd_delay=${FWD_DELAY_MS:-$base_delay}
		link_rev_delay=${REV_DELAY_MS:-$base_delay}
		link_fwd_jitter=${FWD_JITTER_MS:-$base_jitter}
		link_rev_jitter=${REV_JITTER_MS:-$base_jitter}
		link_fwd_loss=${FWD_LOSS_PCT:-$base_loss}
		link_rev_loss=${REV_LOSS_PCT:-$base_loss}
		link_fwd_duplicate=${FWD_DUPLICATE_PCT:-0}
		link_rev_duplicate=${REV_DUPLICATE_PCT:-0}
		link_fwd_reorder=${FWD_REORDER_PCT:-0}
		link_rev_reorder=${REV_REORDER_PCT:-0}
		link_loss_correlation=${LOSS_CORRELATION_PCT:-0}
		link_reorder_correlation=${REORDER_CORRELATION_PCT:-0}
		link_loss_model=${LOSS_MODEL:-random}
		link_burst_len=${BURST_LEN:-0}
		;;
	unshaped)
		link_impairment=none
		link_fwd_rate=0
		link_rev_rate=0
		link_fwd_delay=0
		link_rev_delay=0
		link_fwd_jitter=0
		link_rev_jitter=0
		link_fwd_loss=0
		link_rev_loss=0
		link_fwd_duplicate=0
		link_rev_duplicate=0
		link_fwd_reorder=0
		link_rev_reorder=0
		;;
	lan)
		link_impairment=symmetric
		link_fwd_rate=1000
		link_rev_rate=1000
		link_fwd_delay=0.2
		link_rev_delay=0.2
		link_fwd_jitter=0.05
		link_rev_jitter=0.05
		link_fwd_loss=0
		link_rev_loss=0
		link_fwd_duplicate=0
		link_rev_duplicate=0
		link_fwd_reorder=0
		link_rev_reorder=0
		;;
	fiber)
		link_impairment=symmetric
		link_fwd_rate=500
		link_rev_rate=500
		link_fwd_delay=5
		link_rev_delay=5
		link_fwd_jitter=0.5
		link_rev_jitter=0.5
		link_fwd_loss=0.01
		link_rev_loss=0.01
		link_fwd_duplicate=0
		link_rev_duplicate=0
		link_fwd_reorder=0
		link_rev_reorder=0
		;;
	cable)
		link_impairment=symmetric
		link_fwd_rate=300
		link_rev_rate=30
		link_fwd_delay=10
		link_rev_delay=10
		link_fwd_jitter=2
		link_rev_jitter=2
		link_fwd_loss=0.1
		link_rev_loss=0.1
		link_fwd_duplicate=0
		link_rev_duplicate=0
		link_fwd_reorder=0
		link_rev_reorder=0
		;;
	dsl)
		link_impairment=symmetric
		link_fwd_rate=50
		link_rev_rate=10
		link_fwd_delay=15
		link_rev_delay=15
		link_fwd_jitter=3
		link_rev_jitter=3
		link_fwd_loss=0.1
		link_rev_loss=0.1
		link_fwd_duplicate=0
		link_rev_duplicate=0
		link_fwd_reorder=0
		link_rev_reorder=0
		;;
	wifi)
		link_impairment=symmetric
		link_fwd_rate=100
		link_rev_rate=100
		link_fwd_delay=5
		link_rev_delay=5
		link_fwd_jitter=5
		link_rev_jitter=5
		link_fwd_loss=1
		link_rev_loss=1
		link_fwd_duplicate=0.05
		link_rev_duplicate=0.05
		link_fwd_reorder=0.5
		link_rev_reorder=0.5
		link_reorder_correlation=25
		;;
	cellular)
		link_impairment=symmetric
		link_fwd_rate=50
		link_rev_rate=15
		link_fwd_delay=35
		link_rev_delay=35
		link_fwd_jitter=15
		link_rev_jitter=15
		link_fwd_loss=1
		link_rev_loss=1
		link_fwd_duplicate=0
		link_rev_duplicate=0
		link_fwd_reorder=0.2
		link_rev_reorder=0.2
		link_reorder_correlation=25
		;;
	satellite)
		link_impairment=symmetric
		link_fwd_rate=25
		link_rev_rate=5
		link_fwd_delay=300
		link_rev_delay=300
		link_fwd_jitter=20
		link_rev_jitter=20
		link_fwd_loss=0.2
		link_rev_loss=0.2
		link_fwd_duplicate=0
		link_rev_duplicate=0
		link_fwd_reorder=0
		link_rev_reorder=0
		;;
	lossy-wifi)
		link_impairment=symmetric
		link_fwd_rate=100
		link_rev_rate=100
		link_fwd_delay=20
		link_rev_delay=20
		link_fwd_jitter=8
		link_rev_jitter=8
		link_fwd_loss=5
		link_rev_loss=5
		link_fwd_duplicate=0.1
		link_rev_duplicate=0.1
		link_fwd_reorder=1
		link_rev_reorder=1
		link_loss_correlation=25
		link_reorder_correlation=25
		;;
	*)
		echo "unsupported LINK_PROFILE $profile" >&2
		return 1
		;;
	esac
}

render_configs() {
	local mode=$1
	local mtu=$2
	local settings fec obfs congestion data_shards
	settings=$(mode_settings "$mode")
	read -r fec obfs <<<"$settings"
	congestion=${CONGESTION:-auto}
	data_shards=${FEC_DATA_SHARDS:-32}
	mkdir -p "$generated_dir"
	sed \
		-e "s/@FEC@/$fec/g" \
		-e "s/@OBFS@/$obfs/g" \
		-e "s/@CONGESTION@/$congestion/g" \
		-e "s/@FEC_DATA_SHARDS@/$data_shards/g" \
		-e "s/@MTU@/$mtu/g" \
		"$script_dir/a.conf.in" >"$generated_dir/a.conf"
	sed \
		-e "s/@FEC@/$fec/g" \
		-e "s/@OBFS@/$obfs/g" \
		-e "s/@CONGESTION@/$congestion/g" \
		-e "s/@FEC_DATA_SHARDS@/$data_shards/g" \
		-e "s/@MTU@/$mtu/g" \
		"$script_dir/b.conf.in" >"$generated_dir/b.conf"
	cp "$script_dir/a.uapi.in" "$generated_dir/a.uapi"
	cp "$script_dir/b.uapi.in" "$generated_dir/b.uapi"
}

prepare_image() {
	require_command docker
	require_command go
	require_command jq
	require_command timeout
	mkdir -p "$script_dir/build"
	CGO_ENABLED=0 go build -trimpath -o "$script_dir/build/wg-quic" ./cmd/wg-quic
	CGO_ENABLED=0 go build -trimpath -o "$script_dir/build/wg-quic-quick" ./cmd/wg-quic-quick
	CGO_ENABLED=0 go -C "$repo_dir/third_party/wireguard-go" build \
		-trimpath -o "$script_dir/build/wireguard-go" .
	CGO_ENABLED=0 go build -trimpath -o "$script_dir/build/wg-uapi" ./tests/benchmark
	docker build -t wg-quic-bench:local -f "$script_dir/Dockerfile" "$repo_dir"
}

wait_for_tunnel() {
	for _ in $(seq 1 "${TUNNEL_WAIT_ATTEMPTS:-60}"); do
		if compose_run exec -T a ping -c 1 -W 1 10.88.0.2 >/dev/null 2>&1; then
			return 0
		fi
		sleep 0.5
	done
	compose_run ps >&2 || true
	compose_run logs >&2 || true
	echo "benchmark tunnel did not become ready" >&2
	return 1
}

apply_protocol_policy_to() {
	local service=$1
	local policy=$2
	local action=(action drop)
	if [[ $policy == wireguard-throttle ]]; then
		# tc-police orders this as EXCEED/CONFORM, despite the option name.
		action=(action police rate "${PROTOCOL_RATE_MBIT:-1}mbit" burst 64k conform-exceed drop/pass)
	fi
	compose_run exec -T "$service" tc qdisc replace dev eth0 clsact
	case "$policy" in
	wireguard-block | wireguard-throttle)
		local priority=10
		local signature
		for signature in 0x01000000 0x02000000 0x03000000 0x04000000; do
			compose_run exec -T "$service" tc filter add dev eth0 egress \
				protocol ip prio "$priority" u32 \
				match ip protocol 17 0xff \
				match u32 "$signature" 0xffffffff at 28 \
				"${action[@]}"
			priority=$((priority + 1))
		done
		;;
	quic-handshake-block)
		# Match the QUIC long-header bits and either standardized version.
		compose_run exec -T "$service" tc filter add dev eth0 egress \
			protocol ip prio 20 u32 \
			match ip protocol 17 0xff \
			match u8 0xc0 0xc0 at 28 \
			match u8 0x00 0xff at 29 \
			match u8 0x00 0xff at 30 \
			match u8 0x00 0xff at 31 \
			match u8 0x01 0xff at 32 \
			action drop
		compose_run exec -T "$service" tc filter add dev eth0 egress \
			protocol ip prio 21 u32 \
			match ip protocol 17 0xff \
			match u8 0xc0 0xc0 at 28 \
			match u8 0x6b 0xff at 29 \
			match u8 0x33 0xff at 30 \
			match u8 0x43 0xff at 31 \
			match u8 0xcf 0xff at 32 \
			action drop
		;;
	esac
}

apply_protocol_policy() {
	if [[ $benchmark_protocol_policy == none ]]; then
		return
	fi
	apply_protocol_policy_to a "$benchmark_protocol_policy"
	apply_protocol_policy_to b "$benchmark_protocol_policy"
}

apply_netem_to() {
	local service=$1
	local rate_mbit=$2
	local delay_ms=$3
	local jitter_ms=$4
	local loss_pct=$5
	local duplicate_pct=$6
	local reorder_pct=$7
	local queue_packets=$8
	local args=(qdisc replace dev eth0 root netem limit "$queue_packets")
	if [[ $delay_ms != 0 ]]; then
		args+=(delay "${delay_ms}ms")
		if [[ $jitter_ms != 0 ]]; then
			args+=("${jitter_ms}ms" distribution normal)
		fi
	fi
	if [[ $loss_pct != 0 ]]; then
		if [[ $link_loss_model == burst && $link_burst_len -gt 0 ]]; then
			# loss state P13 P31 models a two-state Markov chain. P31 is the
			# per-packet probability of leaving the burst state (mean burst
			# length = 1/P31), and P13 = P31 * p / (1 - p) yields the requested
			# steady-state loss probability p.
			local p31 p13
			p31=$(awk "BEGIN { printf \"%.4f\", 100 / $link_burst_len }")
			p13=$(awk "BEGIN { printf \"%.4f\", $p31 * $loss_pct / (100 - $loss_pct) }")
			args+=(loss state "${p13}%" "${p31}%")
		else
			args+=(loss random "${loss_pct}%")
			if [[ $link_loss_correlation != 0 ]]; then
				args+=("${link_loss_correlation}%")
			fi
		fi
	fi
	if [[ $duplicate_pct != 0 ]]; then
		args+=(duplicate "${duplicate_pct}%")
	fi
	if [[ $reorder_pct != 0 ]]; then
		args+=(reorder "${reorder_pct}%")
		if [[ $link_reorder_correlation != 0 ]]; then
			args+=("${link_reorder_correlation}%")
		fi
	fi
	if [[ $rate_mbit != 0 ]]; then
		args+=(rate "${rate_mbit}mbit")
	fi
	compose_run exec -T "$service" tc "${args[@]}"
}

apply_netem() {
	case "$link_impairment" in
	none)
		compose_run exec -T a tc qdisc del dev eth0 root >/dev/null 2>&1 || true
		compose_run exec -T b tc qdisc del dev eth0 root >/dev/null 2>&1 || true
		;;
	forward)
		apply_netem_to a \
			"$link_fwd_rate" "$link_fwd_delay" "$link_fwd_jitter" "$link_fwd_loss" \
			"$link_fwd_duplicate" "$link_fwd_reorder" "$link_queue_packets"
		compose_run exec -T b tc qdisc del dev eth0 root >/dev/null 2>&1 || true
		;;
	reverse)
		compose_run exec -T a tc qdisc del dev eth0 root >/dev/null 2>&1 || true
		apply_netem_to b \
			"$link_rev_rate" "$link_rev_delay" "$link_rev_jitter" "$link_rev_loss" \
			"$link_rev_duplicate" "$link_rev_reorder" "$link_queue_packets"
		;;
	symmetric)
		apply_netem_to a \
			"$link_fwd_rate" "$link_fwd_delay" "$link_fwd_jitter" "$link_fwd_loss" \
			"$link_fwd_duplicate" "$link_fwd_reorder" "$link_queue_packets"
		apply_netem_to b \
			"$link_rev_rate" "$link_rev_delay" "$link_rev_jitter" "$link_rev_loss" \
			"$link_rev_duplicate" "$link_rev_reorder" "$link_queue_packets"
		;;
	*)
		echo "IMPAIRMENT must be none, forward, reverse, or symmetric" >&2
		return 1
		;;
	esac
	apply_protocol_policy
}

start_link_schedule() {
	local trial_dir=$1
	local schedule=${LINK_SCHEDULE:-}
	schedule_pid=
	if [[ -z $schedule ]]; then
		return
	fi
	(
		local previous=0
		local step at profile pause
		IFS=',' read -r -a schedule_steps <<<"$schedule"
		for step in "${schedule_steps[@]}"; do
			at=${step%%:*}
			profile=${step#*:}
			validate_number LINK_SCHEDULE_TIME "$at"
			pause=$(awk -v at="$at" -v previous="$previous" 'BEGIN {
				delta = at - previous
				if (delta < 0) {
					exit 1
				}
				printf "%.3f", delta
			}')
			sleep "$pause"
			resolve_link_profile "$profile" symmetric 0 0 0 0 1000
			apply_netem
			printf '%s seconds: applied %s (%s/%s Mbit, %s/%s ms, %s/%s%% loss)\n' \
				"$at" "$profile" "$link_fwd_rate" "$link_rev_rate" \
				"$link_fwd_delay" "$link_rev_delay" "$link_fwd_loss" "$link_rev_loss"
			previous=$at
		done
	) >"$trial_dir/link-schedule.log" 2>&1 &
	schedule_pid=$!
}

stop_link_schedule() {
	if [[ -n ${schedule_pid:-} ]]; then
		kill "$schedule_pid" >/dev/null 2>&1 || true
		wait "$schedule_pid" >/dev/null 2>&1 || true
		schedule_pid=
	fi
}

init_trial_events() {
	local trial_dir=$1
	trial_epoch_ns=$(date +%s%N)
	printf '%s\n' 'elapsed_s,unix_ns,event,detail' >"$trial_dir/events.csv"
	record_trial_event "$trial_dir" telemetry_start ""
}

record_trial_event() {
	local trial_dir=$1
	local event=$2
	local detail=${3:-}
	local now_ns elapsed
	now_ns=$(date +%s%N)
	elapsed=$(awk -v now="$now_ns" -v started="$trial_epoch_ns" \
		'BEGIN {printf "%.6f", (now - started) / 1000000000}')
	jq -rn \
		--arg elapsed "$elapsed" \
		--arg unix_ns "$now_ns" \
		--arg event "$event" \
		--arg detail "$detail" \
		'[$elapsed, $unix_ns, $event, $detail] | @csv' \
		>>"$trial_dir/events.csv"
}

start_tcp_telemetry() {
	local trial_dir=$1
	local port=$2
	local interval=${TCP_TELEMETRY_INTERVAL_SECONDS:-0}
	validate_number TCP_TELEMETRY_INTERVAL_SECONDS "$interval"
	tcp_telemetry_pid=
	: >"$trial_dir/tcp-info.log"
	if [[ $interval == 0 ]]; then
		return
	fi
	(
		local now_ns elapsed service
		while :; do
			now_ns=$(date +%s%N)
			elapsed=$(awk -v now="$now_ns" -v started="$trial_epoch_ns" \
				'BEGIN {printf "%.6f", (now - started) / 1000000000}')
			for service in a b; do
				printf '### elapsed_s=%s side=%s port=%s\n' "$elapsed" "$service" "$port"
				compose_run exec -T "$service" sh -c \
					"ss -tinH state established | grep ':$port' || true"
			done
			sleep "$interval"
		done
	) >"$trial_dir/tcp-info.log" 2>"$trial_dir/tcp-sampler.log" &
	tcp_telemetry_pid=$!
}

stop_tcp_telemetry() {
	if [[ -n ${tcp_telemetry_pid:-} ]]; then
		kill "$tcp_telemetry_pid" >/dev/null 2>&1 || true
		wait "$tcp_telemetry_pid" >/dev/null 2>&1 || true
		tcp_telemetry_pid=
	fi
}

start_controller_telemetry() {
	local trial_dir=$1
	local interval=${TELEMETRY_INTERVAL_SECONDS:-0.5}
	validate_number TELEMETRY_INTERVAL_SECONDS "$interval"
	telemetry_pid=
	printf '%s\n' \
		'elapsed_s,fec_parity_shards,fec_loss_estimate_ppm,quic_bytes_acked,quic_packets_lost,quic_min_rtt_us,quic_path_rtt_us,quic_smoothed_rtt_us,quic_latest_rtt_us,quic_cwnd_bytes,quic_bytes_in_flight,quic_bandwidth_estimate_bps,quic_pacing_rate_bps,quic_queue_delay_us,quic_fec_recoverable_loss_ppm,quic_fec_residual_loss_ppm,quic_model_state,send_queue_depth,priority_queue_depth,control_queue_depth,quic_datagram_queue_depth,runtime_alloc_bytes,runtime_alloc_objects,runtime_heap_objects,runtime_gc_cycles,runtime_gc_pause_cpu_ns' \
		>"$trial_dir/controller.csv"
	(
		local now_ns elapsed sample_file
		sample_file="$trial_dir/.controller-status.json"
		while :; do
			if snapshot_status a "$sample_file" 2>/dev/null; then
				now_ns=$(date +%s%N)
				elapsed=$(awk -v now="$now_ns" -v started="$trial_epoch_ns" \
					'BEGIN {printf "%.3f", (now - started) / 1000000000}')
				jq -r --arg elapsed "$elapsed" '
					[
						$elapsed,
						(.stats.fec_current_parity_shards // 0),
						(.stats.fec_loss_estimate_ppm // 0),
						(.stats.quic_bytes_acked // 0),
						(.stats.quic_packets_lost // 0),
						(.stats.quic_min_rtt_us // 0),
						(.stats.quic_path_rtt_us // 0),
						(.stats.quic_smoothed_rtt_us // 0),
						(.stats.quic_latest_rtt_us // 0),
						(.stats.quic_congestion_window_bytes // 0),
						(.stats.quic_bytes_in_flight // 0),
						(.stats.quic_bandwidth_estimate_bps // 0),
						(.stats.quic_pacing_rate_bps // 0),
						(.stats.quic_queue_delay_us // 0),
						(.stats.quic_fec_recoverable_loss_ppm // 0),
						(.stats.quic_fec_residual_loss_ppm // 0),
						(.stats.quic_congestion_model_state // 0),
						(.stats.send_queue_depth // 0),
						(.stats.priority_queue_depth // 0),
						(.stats.control_queue_depth // 0),
						(.stats.quic_datagram_send_queue_len // 0),
						(.stats.runtime_alloc_bytes // 0),
						(.stats.runtime_alloc_objects // 0),
						(.stats.runtime_heap_objects // 0),
						(.stats.runtime_gc_cycles // 0),
						(.stats.runtime_gc_pause_cpu_ns // 0)
					] | @csv
				' "$sample_file" >>"$trial_dir/controller.csv"
			fi
			sleep "$interval"
		done
	) >"$trial_dir/controller-sampler.log" 2>&1 &
	telemetry_pid=$!
}

stop_controller_telemetry() {
	local trial_dir=$1
	if [[ -n ${telemetry_pid:-} ]]; then
		kill "$telemetry_pid" >/dev/null 2>&1 || true
		wait "$telemetry_pid" >/dev/null 2>&1 || true
		telemetry_pid=
	fi
	rm -f "$trial_dir/.controller-status.json"
}

start_iperf_server() {
	local address=$1
	local port=$2
	compose_run exec -T b sh -c \
		"iperf3 -s -1 -B '$address' -p '$port' --json >/tmp/wgq-bench-server-$port.json 2>/tmp/wgq-bench-server-$port.err &"
	for _ in $(seq 1 50); do
		if compose_run exec -T b sh -c \
			"ss -H -ltn 'sport = :$port' | grep -q LISTEN"; then
			return 0
		fi
		sleep 0.1
	done
	compose_run exec -T b sh -c "sed -n '1,160p' /tmp/wgq-bench-server-$port.err" >&2 || true
	echo "iperf3 server failed to listen on $address:$port" >&2
	return 1
}

snapshot_status() {
	local service=$1
	local output=$2
	if [[ $benchmark_transport == direct-wireguard-go ]]; then
		local status peer rx tx
		status=$(compose_run exec -T "$service" \
			/usr/local/bin/wg-uapi get /var/run/wireguard/wg0.sock)
		peer=$(awk -F= '$1 == "public_key" {print $2; exit}' <<<"$status")
		rx=$(awk -F= '$1 == "rx_bytes" {print $2; exit}' <<<"$status")
		tx=$(awk -F= '$1 == "tx_bytes" {print $2; exit}' <<<"$status")
		rx=${rx:-0}
		tx=${tx:-0}
		jq -n \
			--arg peer "$peer" \
			--argjson rx "${rx:-0}" \
			--argjson tx "${tx:-0}" \
			'{
				transport: "direct-wireguard-go",
				peer: $peer,
				stats: {
					wg_tx_bytes: $tx,
					wg_rx_bytes: $rx,
					wire_tx_bytes: $tx,
					wire_rx_bytes: $rx,
					queue_drops: 0,
					fec_data_tx: 0,
					fec_parity_tx: 0,
					fec_raw_lost: 0,
					fec_recovered: 0,
					fec_unrecovered: 0
				}
			}' >"$output"
		return
	fi
	compose_run exec -T "$service" wg-quic show wg0 --json >"$output"
}

core_pid() {
	local service=$1
	local process=wg-quic
	if [[ $benchmark_transport == direct-wireguard-go ]]; then
		process=wireguard-go
	fi
	compose_run exec -T "$service" pidof "$process" 2>/dev/null | tr -d '\r' | awk '{print $1}'
}

core_cpu_ticks() {
	local service=$1
	local pid
	pid=$(core_pid "$service")
	if [[ -z $pid ]]; then
		echo 0
		return
	fi
	# shellcheck disable=SC2016
	compose_run exec -T "$service" awk '{print $14 + $15}' "/proc/$pid/stat" | tr -d '\r'
}

core_rss_kib() {
	local service=$1
	local pid
	pid=$(core_pid "$service")
	if [[ -z $pid ]]; then
		echo 0
		return
	fi
	# shellcheck disable=SC2016
	compose_run exec -T "$service" awk '/^VmRSS:/ {print $2}' "/proc/$pid/status" | tr -d '\r'
}

interface_bytes() {
	local service=$1
	local interface=$2
	local direction=$3
	compose_run exec -T "$service" \
		cat "/sys/class/net/$interface/statistics/${direction}_bytes" | tr -d '\r'
}

stat_delta() {
	local before=$1
	local after=$2
	local field=$3
	jq -n \
		--arg field "$field" \
		--slurpfile before "$before" \
		--slurpfile after "$after" \
		'(($after[0].stats[$field] // 0) - ($before[0].stats[$field] // 0))'
}

csv_column_max() {
	local input=$1
	local column=$2
	awk -F, -v wanted="$column" '
		NR == 1 {
			for (i = 1; i <= NF; i++) {
				gsub(/^"|"$/, "", $i)
				if ($i == wanted) {
					column_index = i
				}
			}
			next
		}
		column_index > 0 {
			value = $column_index
			gsub(/^"|"$/, "", value)
			if (value + 0 > maximum) {
				maximum = value + 0
			}
		}
		END {printf "%.0f", maximum + 0}
	' "$input"
}

init_run_directory() {
	if [[ -z ${run_dir:-} ]]; then
		local run_id
		local expected_summary_header
		run_id=${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}
		run_dir="$results_root/$run_id"
		mkdir -p "$run_dir"
		summary_csv="$run_dir/summary.csv"
		expected_summary_header='trial_id,mode,congestion,protocol_policy,link_profile,link_schedule,workload,repeat,impairment,fwd_rate_mbit,rev_rate_mbit,fwd_delay_ms,rev_delay_ms,fwd_jitter_ms,rev_jitter_ms,fwd_loss_pct,rev_loss_pct,fwd_duplicate_pct,rev_duplicate_pct,fwd_reorder_pct,rev_reorder_pct,queue_packets,mtu,duration_s,measured_duration_s,parallel,offered_mbit,disable_offloads,outer_baseline_bps,goodput_bps,outer_utilization,retransmits,udp_lost_pct,first_delivery_s,longest_stall_s,total_stall_s,stall_count,wire_tx_bytes_a,wire_tx_bps_a,goodput_to_wire_ratio,outer_tx_bytes_a,outer_tx_bps_a,goodput_to_outer_ratio,wg_tx_bytes_a,fec_data_tx_a,fec_parity_tx_a,fec_raw_lost_b,fec_recovered_b,fec_unrecovered_b,fec_current_parity_a,fec_loss_estimate_ppm_a,queue_drops_a,queue_drops_b,send_queue_depth_max_a,priority_queue_depth_max_a,quic_datagram_queue_depth_max_a,quic_bytes_acked_a,quic_acked_bps_a,quic_bytes_lost_a,quic_packets_lost_a,quic_min_rtt_us_a,quic_path_rtt_us_a,quic_smoothed_rtt_us_a,quic_latest_rtt_us_a,quic_cwnd_bytes_a,quic_bytes_in_flight_a,quic_bandwidth_estimate_bps_a,quic_pacing_rate_bps_a,quic_queue_delay_us_a,quic_fec_recoverable_loss_ppm_a,quic_fec_residual_loss_ppm_a,quic_model_state_a,runtime_alloc_bytes_a,runtime_alloc_bytes_b,runtime_alloc_objects_a,runtime_alloc_objects_b,runtime_heap_objects_max_a,runtime_gc_cycles_a,runtime_gc_cycles_b,runtime_gc_pause_cpu_ns_a,runtime_gc_pause_cpu_ns_b,core_cpu_a_s,core_cpu_b_s,core_rss_a_kib,core_rss_b_kib,status,error,result_json'
		if [[ ! -f $summary_csv ]]; then
			printf '%s\n' "$expected_summary_header" >"$summary_csv"
			{
				date -u
				uname -a
				go version
				docker version --format '{{.Client.Version}}'
				docker compose version
				git -C "$repo_dir" rev-parse HEAD
			} >"$run_dir/environment.txt"
		elif [[ $(head -n 1 "$summary_csv") != "$expected_summary_header" ]]; then
			echo "RUN_ID $run_id uses an incompatible summary.csv schema; choose a new RUN_ID" >&2
			return 1
		fi
		echo "results: $run_dir"
	fi
}

run_trial() {
	local mode=$1
	local workload=$2
	local repeat=$3
	local impairment=$4
	local rate_mbit=$5
	local delay_ms=$6
	local jitter_ms=$7
	local loss_pct=$8
	local queue_packets=$9
	shift 9
	local mtu=$1
	local duration=$2
	local parallel=$3
	local offered_mbit=$4

	validate_number RATE_MBIT "$rate_mbit"
	validate_number ONE_WAY_DELAY_MS "$delay_ms"
	validate_number JITTER_MS "$jitter_ms"
	validate_number LOSS_PCT "$loss_pct"
	validate_integer QUEUE_PACKETS "$queue_packets"
	validate_integer MTU "$mtu"
	validate_integer DURATION "$duration"
	validate_integer PARALLEL "$parallel"
	validate_number OFFERED_MBIT "$offered_mbit"
	local iperf_interval=${IPERF_INTERVAL_SECONDS:-0.25}
	local stall_bps_threshold=${STALL_BPS_THRESHOLD:-0}
	validate_number IPERF_INTERVAL_SECONDS "$iperf_interval"
	validate_number STALL_BPS_THRESHOLD "$stall_bps_threshold"
	validate_integer CONTROL_GRACE_SECONDS "${CONTROL_GRACE_SECONDS:-20}"
	benchmark_transport=$(transport_for_mode "$mode")
	benchmark_mtu=$mtu
	case "$workload" in
	tcp | udp | outer-tcp | outer-udp)
		;;
	*)
		echo "unsupported WORKLOAD $workload" >&2
		return 1
		;;
	esac
	resolve_link_profile \
		"${CURRENT_LINK_PROFILE:-custom}" \
		"$impairment" "$rate_mbit" "$delay_ms" "$jitter_ms" "$loss_pct" "$queue_packets"
	validate_number FWD_RATE_MBIT "$link_fwd_rate"
	validate_number REV_RATE_MBIT "$link_rev_rate"
	validate_number FWD_DELAY_MS "$link_fwd_delay"
	validate_number REV_DELAY_MS "$link_rev_delay"
	validate_number FWD_JITTER_MS "$link_fwd_jitter"
	validate_number REV_JITTER_MS "$link_rev_jitter"
	validate_number FWD_LOSS_PCT "$link_fwd_loss"
	validate_number REV_LOSS_PCT "$link_rev_loss"
	validate_number FWD_DUPLICATE_PCT "$link_fwd_duplicate"
	validate_number REV_DUPLICATE_PCT "$link_rev_duplicate"
	validate_number FWD_REORDER_PCT "$link_fwd_reorder"
	validate_number REV_REORDER_PCT "$link_rev_reorder"
	benchmark_protocol_policy=${PROTOCOL_POLICY:-none}
	case "$benchmark_protocol_policy" in
	none | wireguard-block | wireguard-throttle | quic-handshake-block)
		;;
	*)
		echo "PROTOCOL_POLICY must be none, wireguard-block, wireguard-throttle, or quic-handshake-block" >&2
		return 1
		;;
	esac
	case "${DISABLE_OFFLOADS:-auto}" in
	auto)
		benchmark_disable_offloads=0
		if [[ $link_fwd_loss != 0 || $link_rev_loss != 0 ||
			$link_fwd_duplicate != 0 || $link_rev_duplicate != 0 ||
			$link_fwd_reorder != 0 || $link_rev_reorder != 0 ||
			$benchmark_protocol_policy != none ]]; then
			benchmark_disable_offloads=1
		fi
		;;
	0 | 1)
		benchmark_disable_offloads=$DISABLE_OFFLOADS
		;;
	*)
		echo "DISABLE_OFFLOADS must be auto, 0, or 1" >&2
		return 1
		;;
	esac

	init_run_directory
	render_configs "$mode" "$mtu"
	compose_run up -d --force-recreate a b
	apply_netem
	if ! wait_for_tunnel; then
		if [[ $benchmark_protocol_policy == none ]]; then
			return 1
		fi
		echo "tunnel unavailable under expected protocol policy: $benchmark_protocol_policy" >&2
	fi
	sleep "${WARMUP_SECONDS:-1}"

	local safe_fwd_loss=${link_fwd_loss//./p}
	local safe_rev_loss=${link_rev_loss//./p}
	local safe_fwd_rate=${link_fwd_rate//./p}
	local safe_rev_rate=${link_rev_rate//./p}
	local safe_fwd_delay=${link_fwd_delay//./p}
	local safe_rev_delay=${link_rev_delay//./p}
	local safe_schedule=${LINK_SCHEDULE:-static}
	local congestion=${CONGESTION:-auto}
	safe_schedule=${safe_schedule//:/-}
	safe_schedule=${safe_schedule//,/_}
	local trial_id="${mode}-${congestion}-${benchmark_protocol_policy}-${link_profile}-${safe_schedule}-${workload}-r${repeat}-rate${safe_fwd_rate}-${safe_rev_rate}-delay${safe_fwd_delay}-${safe_rev_delay}-loss${safe_fwd_loss}-${safe_rev_loss}-p${parallel}"
	local trial_dir="$run_dir/$trial_id"
	mkdir -p "$trial_dir"
	jq -n \
		--arg trial_id "$trial_id" \
		--arg mode "$mode" \
		--arg congestion "$congestion" \
		--arg protocol_policy "$benchmark_protocol_policy" \
		--arg link_profile "$link_profile" \
		--arg link_schedule "${LINK_SCHEDULE:-}" \
		--arg workload "$workload" \
		--arg impairment "$link_impairment" \
		--argjson repeat "$repeat" \
		--argjson fwd_rate_mbit "$link_fwd_rate" \
		--argjson rev_rate_mbit "$link_rev_rate" \
		--argjson fwd_delay_ms "$link_fwd_delay" \
		--argjson rev_delay_ms "$link_rev_delay" \
		--argjson fwd_jitter_ms "$link_fwd_jitter" \
		--argjson rev_jitter_ms "$link_rev_jitter" \
		--argjson fwd_loss_pct "$link_fwd_loss" \
		--argjson rev_loss_pct "$link_rev_loss" \
		--argjson fwd_duplicate_pct "$link_fwd_duplicate" \
		--argjson rev_duplicate_pct "$link_rev_duplicate" \
		--argjson fwd_reorder_pct "$link_fwd_reorder" \
		--argjson rev_reorder_pct "$link_rev_reorder" \
		--argjson queue_packets "$link_queue_packets" \
		--argjson mtu "$mtu" \
		--argjson duration_s "$duration" \
		--argjson iperf_interval_s "$iperf_interval" \
		--argjson stall_bps_threshold "$stall_bps_threshold" \
		--argjson parallel "$parallel" \
		--argjson offered_mbit "$offered_mbit" \
		--argjson disable_offloads "$benchmark_disable_offloads" \
		'{
			trial_id: $trial_id,
			mode: $mode,
			congestion: $congestion,
			protocol_policy: $protocol_policy,
			link_profile: $link_profile,
			link_schedule: $link_schedule,
			workload: $workload,
			repeat: $repeat,
			link: {
				impairment: $impairment,
				fwd_rate_mbit: $fwd_rate_mbit,
				rev_rate_mbit: $rev_rate_mbit,
				fwd_delay_ms: $fwd_delay_ms,
				rev_delay_ms: $rev_delay_ms,
				fwd_jitter_ms: $fwd_jitter_ms,
				rev_jitter_ms: $rev_jitter_ms,
				fwd_loss_pct: $fwd_loss_pct,
				rev_loss_pct: $rev_loss_pct,
				fwd_duplicate_pct: $fwd_duplicate_pct,
				rev_duplicate_pct: $rev_duplicate_pct,
				fwd_reorder_pct: $fwd_reorder_pct,
				rev_reorder_pct: $rev_reorder_pct
			},
			queue_packets: $queue_packets,
			mtu: $mtu,
			duration_s: $duration_s,
			iperf_interval_s: $iperf_interval_s,
			stall_bps_threshold: $stall_bps_threshold,
			parallel: $parallel,
			offered_mbit: $offered_mbit,
			disable_offloads: $disable_offloads
		}' >"$trial_dir/parameters.json"

	local outer_baseline=0
	if [[ ${MEASURE_OUTER:-1} == 1 && $workload != outer-* ]]; then
		local outer_duration=${OUTER_MEASURE_DURATION:-3}
		validate_integer OUTER_MEASURE_DURATION "$outer_duration"
		start_iperf_server 172.30.0.3 5200
		local outer_iperf_args=(-c 172.30.0.3 -p 5200 -t "$outer_duration" -P 1 --json --get-server-output)
		if [[ $workload == udp ]]; then
			outer_iperf_args+=(-u -b "${offered_mbit}M" -l 1200)
		fi
		if compose_timeout "$((outer_duration + ${CONTROL_GRACE_SECONDS:-20}))" \
			exec -T a iperf3 "${outer_iperf_args[@]}" \
			>"$trial_dir/outer-baseline-client.json"; then
			outer_baseline=$(jq -r \
				'.end.sum_received.bits_per_second //
				 .end.sum.bits_per_second //
				 .end.streams[0].udp.bits_per_second //
				 0' \
				"$trial_dir/outer-baseline-client.json")
		else
			compose_run logs >"$trial_dir/outer-baseline-containers.log" 2>&1 || true
		fi
		compose_run exec -T b sh -c \
			"sed -n '1,400p' /tmp/wgq-bench-server-5200.json" \
			>"$trial_dir/outer-baseline-server.json" || true
		sleep "${OUTER_MEASURE_COOLDOWN:-1}"
	fi

	snapshot_status a "$trial_dir/status-a-before.json"
	snapshot_status b "$trial_dir/status-b-before.json"
	local cpu_a_before cpu_b_before
	cpu_a_before=$(core_cpu_ticks a)
	cpu_b_before=$(core_cpu_ticks b)
	local outer_tx_a_before
	outer_tx_a_before=$(interface_bytes a eth0 tx)

	local destination=10.88.0.2
	if [[ $workload == outer-* ]]; then
		destination=172.30.0.3
	fi
	local port=5201
	start_iperf_server "$destination" "$port"
	local iperf_args=(-c "$destination" -p "$port" -t "$duration" -P "$parallel" \
		-i "$iperf_interval" --json --get-server-output)
	if [[ $workload == udp || $workload == outer-udp ]]; then
		iperf_args+=(-u -b "${offered_mbit}M" -l 1200)
	fi
	init_trial_events "$trial_dir"
	record_trial_event "$trial_dir" workload_server_ready "$destination:$port"
	start_link_schedule "$trial_dir"
	start_controller_telemetry "$trial_dir"
	start_tcp_telemetry "$trial_dir" "$port"
	record_trial_event "$trial_dir" iperf_exec_start "$workload"
	local iperf_exit
	set +e
	compose_timeout "$((duration + ${CONTROL_GRACE_SECONDS:-20}))" \
		exec -T a iperf3 "${iperf_args[@]}" >"$trial_dir/iperf-client.json"
	iperf_exit=$?
	set -e
	record_trial_event "$trial_dir" iperf_exec_end "exit=$iperf_exit"
	stop_tcp_telemetry
	stop_controller_telemetry "$trial_dir"
	stop_link_schedule
	local workload_status=ok
	local workload_error=
	if [[ $iperf_exit != 0 ]]; then
		workload_status=failed
		workload_error="iperf_exit_$iperf_exit"
		jq -n --arg error "$workload_error" '{error: $error}' >"$trial_dir/iperf-client.json"
		compose_run logs >"$trial_dir/containers.log" 2>&1 || true
	else
		workload_error=$(jq -r '.error // empty' "$trial_dir/iperf-client.json")
		if [[ -n $workload_error ]]; then
			workload_status=failed
			workload_error=${workload_error//,/;}
		fi
	fi
	compose_run exec -T b sh -c "sed -n '1,400p' /tmp/wgq-bench-server-5201.json" >"$trial_dir/iperf-server.json" || true
	printf '%s\n' \
		'start_s,end_s,seconds,bytes,bits_per_second,retransmits,lost_percent,omitted,snd_cwnd_bytes,snd_rtt_us,snd_rttvar_us,snd_pmtu_bytes' \
		>"$trial_dir/intervals.csv"
	jq -r '
		.intervals[]? |
		[
			(.sum.start // 0),
			(.sum.end // 0),
			(.sum.seconds // 0),
			([.streams[]?.bytes // 0] | add // 0),
			(.sum.bits_per_second // 0),
			(.sum.retransmits // 0),
			(.sum.lost_percent // 0),
			(.sum.omitted // false),
			([.streams[]?.snd_cwnd // 0] | add // 0),
			([.streams[]?.rtt // 0] | max // 0),
			([.streams[]?.rttvar // 0] | max // 0),
			([.streams[]?.pmtu // 0] | min // 0)
		] |
		@csv
	' "$trial_dir/iperf-client.json" >>"$trial_dir/intervals.csv"
	sleep "${DRAIN_SECONDS:-1}"

	local cpu_a_after cpu_b_after
	cpu_a_after=$(core_cpu_ticks a)
	cpu_b_after=$(core_cpu_ticks b)
	local outer_tx_a_after
	outer_tx_a_after=$(interface_bytes a eth0 tx)
	snapshot_status a "$trial_dir/status-a-after.json"
	snapshot_status b "$trial_dir/status-b-after.json"
	compose_run exec -T a tc -s qdisc show dev eth0 >"$trial_dir/tc-a.txt"
	compose_run exec -T b tc -s qdisc show dev eth0 >"$trial_dir/tc-b.txt"
	compose_run exec -T a tc -s filter show dev eth0 egress >"$trial_dir/tc-filter-a.txt" || true
	compose_run exec -T b tc -s filter show dev eth0 egress >"$trial_dir/tc-filter-b.txt" || true

	local goodput retransmits udp_lost measured_duration
	local first_delivery longest_stall total_stall stall_count
	goodput=$(jq -r '
		.end.sum_received.bits_per_second //
		.end.sum.bits_per_second //
		.end.streams[0].udp.bits_per_second //
		0
	' "$trial_dir/iperf-client.json")
	measured_duration=$(jq -r --argjson configured "$duration" '
		(
			.end.sum_received.seconds //
			.end.sum.seconds //
			.end.streams[0].udp.seconds //
			$configured
		) as $measured |
		if $measured > 0 then $measured else $configured end
	' "$trial_dir/iperf-client.json")
	read -r first_delivery longest_stall total_stall stall_count < <(
		jq -r --argjson threshold "$stall_bps_threshold" '
			[.intervals[]? | {
				end: (.sum.end // 0),
				seconds: (.sum.seconds // 0),
				bps: (.sum.bits_per_second // 0)
			}] as $intervals |
			($intervals | map(select(.bps > $threshold)) |
				if length > 0 then .[0].end else 0 end) as $first |
			(reduce $intervals[] as $interval (
				{current: 0, longest: 0, total: 0, count: 0, active: false};
				if $interval.bps <= $threshold then
					.current += $interval.seconds |
					.total += $interval.seconds |
					(if .active then . else .active = true | .count += 1 end) |
					.longest = ([.longest, .current] | max)
				else
					.current = 0 | .active = false
				end
			)) as $stalls |
			[$first, $stalls.longest, $stalls.total, $stalls.count] | @tsv
		' "$trial_dir/iperf-client.json"
	)
	retransmits=$(jq -r '.end.sum_sent.retransmits // 0' "$trial_dir/iperf-client.json")
	udp_lost=$(jq -r '
		.end.sum.lost_percent //
		.end.streams[0].udp.lost_percent //
		0
	' "$trial_dir/iperf-client.json")
	if [[ $workload == outer-* ]]; then
		outer_baseline=$goodput
	fi
	record_trial_event "$trial_dir" workload_metrics \
		"measured_s=$measured_duration first_delivery_s=$first_delivery longest_stall_s=$longest_stall"

	local wire_tx wg_tx fec_data fec_parity raw_lost recovered unrecovered
	local fec_current_parity fec_loss_estimate drops_a drops_b
	local send_queue_max priority_queue_max quic_datagram_queue_max
	wire_tx=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" wire_tx_bytes)
	wg_tx=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" wg_tx_bytes)
	fec_data=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" fec_data_tx)
	fec_parity=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" fec_parity_tx)
	raw_lost=$(stat_delta "$trial_dir/status-b-before.json" "$trial_dir/status-b-after.json" fec_raw_lost)
	recovered=$(stat_delta "$trial_dir/status-b-before.json" "$trial_dir/status-b-after.json" fec_recovered)
	unrecovered=$(stat_delta "$trial_dir/status-b-before.json" "$trial_dir/status-b-after.json" fec_unrecovered)
	fec_current_parity=$(jq -r '.stats.fec_current_parity_shards // 0' "$trial_dir/status-a-after.json")
	fec_loss_estimate=$(jq -r '.stats.fec_loss_estimate_ppm // 0' "$trial_dir/status-a-after.json")
	drops_a=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" queue_drops)
	drops_b=$(stat_delta "$trial_dir/status-b-before.json" "$trial_dir/status-b-after.json" queue_drops)
	send_queue_max=$(csv_column_max "$trial_dir/controller.csv" send_queue_depth)
	priority_queue_max=$(csv_column_max "$trial_dir/controller.csv" priority_queue_depth)
	quic_datagram_queue_max=$(csv_column_max "$trial_dir/controller.csv" quic_datagram_queue_depth)
	local quic_acked quic_acked_bps quic_lost quic_packets_lost
	local quic_min_rtt quic_path_rtt quic_smoothed_rtt quic_latest_rtt quic_cwnd quic_inflight
	local quic_bandwidth quic_pacing quic_queue_delay
	local quic_fec_recoverable quic_fec_residual quic_model_state
	quic_acked=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" quic_bytes_acked)
	quic_acked_bps=$(jq -n --argjson bytes "$quic_acked" --argjson duration "$measured_duration" \
		'$bytes * 8 / $duration')
	quic_lost=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" quic_bytes_lost)
	quic_packets_lost=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" quic_packets_lost)
	quic_min_rtt=$(jq -r '.stats.quic_min_rtt_us // 0' "$trial_dir/status-a-after.json")
	quic_path_rtt=$(jq -r '.stats.quic_path_rtt_us // 0' "$trial_dir/status-a-after.json")
	quic_smoothed_rtt=$(jq -r '.stats.quic_smoothed_rtt_us // 0' "$trial_dir/status-a-after.json")
	quic_latest_rtt=$(jq -r '.stats.quic_latest_rtt_us // 0' "$trial_dir/status-a-after.json")
	quic_cwnd=$(jq -r '.stats.quic_congestion_window_bytes // 0' "$trial_dir/status-a-after.json")
	quic_inflight=$(jq -r '.stats.quic_bytes_in_flight // 0' "$trial_dir/status-a-after.json")
	quic_bandwidth=$(jq -r '.stats.quic_bandwidth_estimate_bps // 0' "$trial_dir/status-a-after.json")
	quic_pacing=$(jq -r '.stats.quic_pacing_rate_bps // 0' "$trial_dir/status-a-after.json")
	quic_queue_delay=$(jq -r '.stats.quic_queue_delay_us // 0' "$trial_dir/status-a-after.json")
	quic_fec_recoverable=$(jq -r '.stats.quic_fec_recoverable_loss_ppm // 0' "$trial_dir/status-a-after.json")
	quic_fec_residual=$(jq -r '.stats.quic_fec_residual_loss_ppm // 0' "$trial_dir/status-a-after.json")
	quic_model_state=$(jq -r '.stats.quic_congestion_model_state // 0' "$trial_dir/status-a-after.json")

	local runtime_alloc_bytes_a runtime_alloc_bytes_b
	local runtime_alloc_objects_a runtime_alloc_objects_b
	local runtime_heap_objects_max_a
	local runtime_gc_cycles_a runtime_gc_cycles_b
	local runtime_gc_pause_a runtime_gc_pause_b
	runtime_alloc_bytes_a=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" runtime_alloc_bytes)
	runtime_alloc_bytes_b=$(stat_delta "$trial_dir/status-b-before.json" "$trial_dir/status-b-after.json" runtime_alloc_bytes)
	runtime_alloc_objects_a=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" runtime_alloc_objects)
	runtime_alloc_objects_b=$(stat_delta "$trial_dir/status-b-before.json" "$trial_dir/status-b-after.json" runtime_alloc_objects)
	runtime_heap_objects_max_a=$(csv_column_max "$trial_dir/controller.csv" runtime_heap_objects)
	runtime_gc_cycles_a=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" runtime_gc_cycles)
	runtime_gc_cycles_b=$(stat_delta "$trial_dir/status-b-before.json" "$trial_dir/status-b-after.json" runtime_gc_cycles)
	runtime_gc_pause_a=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" runtime_gc_pause_cpu_ns)
	runtime_gc_pause_b=$(stat_delta "$trial_dir/status-b-before.json" "$trial_dir/status-b-after.json" runtime_gc_pause_cpu_ns)

	local clock_ticks cpu_a_s cpu_b_s rss_a rss_b
	clock_ticks=$(getconf CLK_TCK)
	cpu_a_s=$(jq -n --argjson delta "$((cpu_a_after - cpu_a_before))" --argjson hz "$clock_ticks" '$delta / $hz')
	cpu_b_s=$(jq -n --argjson delta "$((cpu_b_after - cpu_b_before))" --argjson hz "$clock_ticks" '$delta / $hz')
	rss_a=$(core_rss_kib a)
	rss_b=$(core_rss_kib b)
	local wire_tx_bps outer_tx outer_tx_bps outer_utilization goodput_to_wire goodput_to_outer
	wire_tx_bps=$(jq -n --argjson bytes "$wire_tx" --argjson duration "$measured_duration" \
		'$bytes * 8 / $duration')
	outer_tx=$((outer_tx_a_after - outer_tx_a_before))
	outer_tx_bps=$(jq -n --argjson bytes "$outer_tx" --argjson duration "$measured_duration" \
		'$bytes * 8 / $duration')
	outer_utilization=$(jq -n --argjson goodput "$goodput" --argjson outer "$outer_baseline" \
		'if $outer > 0 then $goodput / $outer else 0 end')
	goodput_to_wire=$(jq -n --argjson goodput "$goodput" --argjson wire "$wire_tx_bps" \
		'if $wire > 0 then $goodput / $wire else 0 end')
	goodput_to_outer=$(jq -n --argjson goodput "$goodput" --argjson wire "$outer_tx_bps" \
		'if $wire > 0 then $goodput / $wire else 0 end')
	local csv_schedule=${LINK_SCHEDULE:-}
	csv_schedule=${csv_schedule//,/;}

	printf '%s\n' \
		"$trial_id,$mode,$congestion,$benchmark_protocol_policy,$link_profile,$csv_schedule,$workload,$repeat,$link_impairment,$link_fwd_rate,$link_rev_rate,$link_fwd_delay,$link_rev_delay,$link_fwd_jitter,$link_rev_jitter,$link_fwd_loss,$link_rev_loss,$link_fwd_duplicate,$link_rev_duplicate,$link_fwd_reorder,$link_rev_reorder,$link_queue_packets,$mtu,$duration,$measured_duration,$parallel,$offered_mbit,$benchmark_disable_offloads,$outer_baseline,$goodput,$outer_utilization,$retransmits,$udp_lost,$first_delivery,$longest_stall,$total_stall,$stall_count,$wire_tx,$wire_tx_bps,$goodput_to_wire,$outer_tx,$outer_tx_bps,$goodput_to_outer,$wg_tx,$fec_data,$fec_parity,$raw_lost,$recovered,$unrecovered,$fec_current_parity,$fec_loss_estimate,$drops_a,$drops_b,$send_queue_max,$priority_queue_max,$quic_datagram_queue_max,$quic_acked,$quic_acked_bps,$quic_lost,$quic_packets_lost,$quic_min_rtt,$quic_path_rtt,$quic_smoothed_rtt,$quic_latest_rtt,$quic_cwnd,$quic_inflight,$quic_bandwidth,$quic_pacing,$quic_queue_delay,$quic_fec_recoverable,$quic_fec_residual,$quic_model_state,$runtime_alloc_bytes_a,$runtime_alloc_bytes_b,$runtime_alloc_objects_a,$runtime_alloc_objects_b,$runtime_heap_objects_max_a,$runtime_gc_cycles_a,$runtime_gc_cycles_b,$runtime_gc_pause_a,$runtime_gc_pause_b,$cpu_a_s,$cpu_b_s,$rss_a,$rss_b,$workload_status,$workload_error,$trial_dir/iperf-client.json" \
		>>"$summary_csv"
	echo "$trial_id: $workload_status, $(awk -v bps="$goodput" -v outer="$outer_baseline" \
		'BEGIN {printf "%.2f Mbit/s (outer %.2f Mbit/s)", bps / 1000000, outer / 1000000}')"
}

run_from_environment() {
	CURRENT_LINK_PROFILE=${LINK_PROFILE:-custom}
	run_trial \
		"${MODE:-fec-obfs}" \
		"${WORKLOAD:-tcp}" \
		"${REPEAT:-1}" \
		"${IMPAIRMENT:-none}" \
		"${RATE_MBIT:-0}" \
		"${ONE_WAY_DELAY_MS:-0}" \
		"${JITTER_MS:-0}" \
		"${LOSS_PCT:-0}" \
		"${QUEUE_PACKETS:-1000}" \
		"${MTU:-1280}" \
		"${DURATION:-10}" \
		"${PARALLEL:-1}" \
		"${OFFERED_MBIT:-50}"
}

run_matrix() {
	local matrix=$1
	local repeat mode loss parallel workload
	local -a selected_modes
	read -r -a selected_modes <<<"${MODES:-${all_modes[*]}}"
	case "$matrix" in
	transports)
		CURRENT_LINK_PROFILE=unshaped
		for repeat in $(seq 1 "${REPEATS:-1}"); do
			run_trial nofec-plain outer-tcp "$repeat" none 0 0 0 0 1000 1280 "${DURATION:-10}" 1 0
			for mode in "${selected_modes[@]}"; do
				run_trial "$mode" tcp "$repeat" none 0 0 0 0 1000 1280 "${DURATION:-10}" 1 0
			done
		done
		;;
	quick)
		CURRENT_LINK_PROFILE=custom
		for mode in nofec-obfs fec-obfs; do
			for loss in 0 2 10; do
				run_trial "$mode" udp 1 symmetric 100 20 0 "$loss" 1000 1280 3 1 1
			done
		done
		run_trial nofec-plain outer-tcp 1 none 0 0 0 0 1000 1280 3 1 0
		for mode in "${selected_modes[@]}"; do
			run_trial "$mode" tcp 1 none 0 0 0 0 1000 1280 3 1 0
		done
		;;
	ceiling)
		CURRENT_LINK_PROFILE=unshaped
		for repeat in $(seq 1 "${REPEATS:-3}"); do
			for parallel in 1 4; do
				run_trial nofec-plain outer-tcp "$repeat" none 0 0 0 0 1000 1280 "${DURATION:-15}" "$parallel" 0
				for mode in "${selected_modes[@]}"; do
					run_trial "$mode" tcp "$repeat" none 0 0 0 0 1000 1280 "${DURATION:-15}" "$parallel" 0
				done
			done
		done
		;;
	loss)
		CURRENT_LINK_PROFILE=custom
		for repeat in $(seq 1 "${REPEATS:-3}"); do
			for workload in udp tcp; do
				for mode in "${selected_modes[@]}"; do
					for loss in 0 0.5 1 2 5 10 15; do
						run_trial "$mode" "$workload" "$repeat" symmetric \
							"${RATE_MBIT:-100}" "${ONE_WAY_DELAY_MS:-20}" 0 "$loss" 1000 1280 \
							"${DURATION:-15}" 1 "${OFFERED_MBIT:-0.5}"
					done
				done
			done
		done
		;;
	profiles)
		for repeat in $(seq 1 "${REPEATS:-3}"); do
			for CURRENT_LINK_PROFILE in lan fiber cable dsl wifi cellular satellite lossy-wifi; do
				for mode in "${selected_modes[@]}"; do
					run_trial "$mode" udp "$repeat" symmetric 0 0 0 0 1000 1280 \
						"${DURATION:-15}" 1 "${OFFERED_MBIT:-1}"
					run_trial "$mode" tcp "$repeat" symmetric 0 0 0 0 1000 1280 \
						"${DURATION:-15}" 1 0
				done
			done
		done
		;;
	bandwidth)
		CURRENT_LINK_PROFILE=${LINK_PROFILE:-unshaped}
		for repeat in $(seq 1 "${REPEATS:-3}"); do
			for offered_mbit in ${OFFERED_RATES:-10 25 50 100 200 500}; do
				run_trial nofec-plain outer-udp "$repeat" symmetric 0 0 0 0 1000 1280 \
					"${DURATION:-10}" 1 "$offered_mbit"
				for mode in "${selected_modes[@]}"; do
					run_trial "$mode" udp "$repeat" symmetric 0 0 0 0 1000 1280 \
						"${DURATION:-10}" 1 "$offered_mbit"
				done
			done
		done
		;;
	protocol)
		CURRENT_LINK_PROFILE=lan
		local policy
		for repeat in $(seq 1 "${REPEATS:-1}"); do
			for policy in wireguard-block wireguard-throttle quic-handshake-block; do
				for mode in direct-wireguard-go nofec-plain nofec-obfs; do
					PROTOCOL_POLICY="$policy" \
					TUNNEL_WAIT_ATTEMPTS="${TUNNEL_WAIT_ATTEMPTS:-5}" \
					CONTROL_GRACE_SECONDS="${CONTROL_GRACE_SECONDS:-5}" \
						run_trial "$mode" tcp "$repeat" symmetric 0 0 0 0 1000 1280 \
						"${DURATION:-5}" 1 0
				done
			done
		done
		;;
	*)
		echo "matrix must be transports, quick, ceiling, loss, profiles, bandwidth, or protocol" >&2
		return 1
		;;
	esac
}

usage() {
	cat <<'EOF'
Usage:
  tests/benchmark/run.sh prepare
  tests/benchmark/run.sh trial
  tests/benchmark/run.sh smoke
  tests/benchmark/run.sh matrix transports|quick|ceiling|loss|profiles|bandwidth|protocol
  tests/benchmark/run.sh down

The trial command is configured with environment variables. Common values:
  MODE=direct-wireguard-go|nofec-plain|nofec-obfs|fec-plain|fec-obfs
  WORKLOAD=tcp|udp|outer-tcp|outer-udp
  LINK_PROFILE=custom|unshaped|lan|fiber|cable|dsl|wifi|cellular|satellite|lossy-wifi
  IMPAIRMENT=none|forward|reverse|symmetric
  RATE_MBIT=0 LOSS_PCT=0 ONE_WAY_DELAY_MS=0 JITTER_MS=0
  FWD_RATE_MBIT=0 REV_RATE_MBIT=0 FWD_LOSS_PCT=0 REV_LOSS_PCT=0
  LOSS_MODEL=random|burst BURST_LEN=8 LOSS_CORRELATION_PCT=0
  LINK_SCHEDULE=5:cellular,10:fiber
  TELEMETRY_INTERVAL_SECONDS=0.5
  IPERF_INTERVAL_SECONDS=0.25 STALL_BPS_THRESHOLD=0
  TCP_TELEMETRY_INTERVAL_SECONDS=0
  PROTOCOL_POLICY=none|wireguard-block|wireguard-throttle|quic-handshake-block
  MODES='direct-wireguard-go nofec-plain nofec-obfs fec-plain fec-obfs'
  OFFERED_RATES='10 25 50 100 200 500'
  DURATION=10 PARALLEL=1 OFFERED_MBIT=50 MTU=1280
EOF
}

main() {
	local command=${1:-}
	case "$command" in
	prepare)
		render_configs fec-obfs 1280
		prepare_image
		;;
	trial)
		if [[ ${SKIP_BUILD:-0} != 1 ]]; then
			prepare_image
		fi
		run_from_environment
		;;
	smoke)
		prepare_image
		MODE=fec-obfs WORKLOAD=tcp LINK_PROFILE=unshaped DURATION=2 OUTER_MEASURE_DURATION=1 run_from_environment
		;;
	matrix)
		if [[ $# != 2 ]]; then
			usage
			exit 1
		fi
		if [[ ${SKIP_BUILD:-0} != 1 ]]; then
			prepare_image
		fi
		run_matrix "$2"
		;;
	down)
		render_configs fec-obfs 1280
		compose_run down --volumes --remove-orphans
		;;
	*)
		usage
		exit 1
		;;
	esac
}

main "$@"
