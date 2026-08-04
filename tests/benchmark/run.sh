#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH='' cd -- "$script_dir/../.." && pwd)
generated_dir="$script_dir/.generated"
results_root=${RESULTS_DIR:-"$script_dir/results"}
project_name=${COMPOSE_PROJECT_NAME:-wg-quic-bench}
compose=(docker compose -p "$project_name" -f "$script_dir/compose.yaml")
benchmark_transport=wg-quic
benchmark_mtu=1380
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
		"${compose[@]}" "$@"
}

compose_timeout() {
	local seconds=$1
	shift
	timeout "$seconds" env \
		WGQ_BENCH_CONFIG_DIR="$generated_dir" \
		WGQ_BENCH_TRANSPORT="$benchmark_transport" \
		WGQ_BENCH_MTU="$benchmark_mtu" \
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
	local settings fec obfs
	settings=$(mode_settings "$mode")
	read -r fec obfs <<<"$settings"
	mkdir -p "$generated_dir"
	sed \
		-e "s/@FEC@/$fec/g" \
		-e "s/@OBFS@/$obfs/g" \
		-e "s/@MTU@/$mtu/g" \
		"$script_dir/a.conf.in" >"$generated_dir/a.conf"
	sed \
		-e "s/@FEC@/$fec/g" \
		-e "s/@OBFS@/$obfs/g" \
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
	for _ in $(seq 1 60); do
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

clear_netem() {
	local service
	for service in a b; do
		compose_run exec -T "$service" tc qdisc del dev eth0 root >/dev/null 2>&1 || true
	done
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
		args+=(loss random "${loss_pct}%")
		if [[ $link_loss_correlation != 0 ]]; then
			args+=("${link_loss_correlation}%")
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
	clear_netem
	if [[ $link_impairment == none ]]; then
		return
	fi
	case "$link_impairment" in
	none)
		;;
	forward)
		apply_netem_to a \
			"$link_fwd_rate" "$link_fwd_delay" "$link_fwd_jitter" "$link_fwd_loss" \
			"$link_fwd_duplicate" "$link_fwd_reorder" "$link_queue_packets"
		;;
	reverse)
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

init_run_directory() {
	if [[ -z ${run_dir:-} ]]; then
		local run_id
		run_id=${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}
		run_dir="$results_root/$run_id"
		mkdir -p "$run_dir"
		summary_csv="$run_dir/summary.csv"
		if [[ ! -f $summary_csv ]]; then
			printf '%s\n' \
				'trial_id,mode,link_profile,link_schedule,workload,repeat,impairment,fwd_rate_mbit,rev_rate_mbit,fwd_delay_ms,rev_delay_ms,fwd_jitter_ms,rev_jitter_ms,fwd_loss_pct,rev_loss_pct,fwd_duplicate_pct,rev_duplicate_pct,fwd_reorder_pct,rev_reorder_pct,queue_packets,mtu,duration_s,parallel,offered_mbit,outer_baseline_bps,goodput_bps,outer_utilization,retransmits,udp_lost_pct,wire_tx_bytes_a,wire_tx_bps_a,goodput_to_wire_ratio,outer_tx_bytes_a,outer_tx_bps_a,goodput_to_outer_ratio,wg_tx_bytes_a,fec_data_tx_a,fec_parity_tx_a,fec_raw_lost_b,fec_recovered_b,fec_unrecovered_b,queue_drops_a,queue_drops_b,core_cpu_a_s,core_cpu_b_s,core_rss_a_kib,core_rss_b_kib,status,error,result_json' \
				>"$summary_csv"
			{
				date -u
				uname -a
				go version
				docker version --format '{{.Client.Version}}'
				docker compose version
				git -C "$repo_dir" rev-parse HEAD
			} >"$run_dir/environment.txt"
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

	init_run_directory
	render_configs "$mode" "$mtu"
	compose_run up -d --force-recreate a b
	apply_netem
	wait_for_tunnel
	sleep "${WARMUP_SECONDS:-1}"

	local safe_fwd_loss=${link_fwd_loss//./p}
	local safe_rev_loss=${link_rev_loss//./p}
	local safe_fwd_rate=${link_fwd_rate//./p}
	local safe_rev_rate=${link_rev_rate//./p}
	local safe_fwd_delay=${link_fwd_delay//./p}
	local safe_rev_delay=${link_rev_delay//./p}
	local safe_schedule=${LINK_SCHEDULE:-static}
	safe_schedule=${safe_schedule//:/-}
	safe_schedule=${safe_schedule//,/_}
	local trial_id="${mode}-${link_profile}-${safe_schedule}-${workload}-r${repeat}-rate${safe_fwd_rate}-${safe_rev_rate}-delay${safe_fwd_delay}-${safe_rev_delay}-loss${safe_fwd_loss}-${safe_rev_loss}-p${parallel}"
	local trial_dir="$run_dir/$trial_id"
	mkdir -p "$trial_dir"
	jq -n \
		--arg trial_id "$trial_id" \
		--arg mode "$mode" \
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
		--argjson parallel "$parallel" \
		--argjson offered_mbit "$offered_mbit" \
		'{
			trial_id: $trial_id,
			mode: $mode,
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
			parallel: $parallel,
			offered_mbit: $offered_mbit
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
	local iperf_args=(-c "$destination" -p "$port" -t "$duration" -P "$parallel" --json --get-server-output)
	if [[ $workload == udp || $workload == outer-udp ]]; then
		iperf_args+=(-u -b "${offered_mbit}M" -l 1200)
	fi
	start_link_schedule "$trial_dir"
	local iperf_exit
	set +e
	compose_timeout "$((duration + ${CONTROL_GRACE_SECONDS:-20}))" \
		exec -T a iperf3 "${iperf_args[@]}" >"$trial_dir/iperf-client.json"
	iperf_exit=$?
	set -e
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
	printf '%s\n' 'start_s,end_s,seconds,bits_per_second,retransmits,lost_percent,omitted' \
		>"$trial_dir/intervals.csv"
	jq -r '
		.intervals[]? |
		[
			(.sum.start // 0),
			(.sum.end // 0),
			(.sum.seconds // 0),
			(.sum.bits_per_second // 0),
			(.sum.retransmits // 0),
			(.sum.lost_percent // 0),
			(.sum.omitted // false)
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

	local goodput retransmits udp_lost
	goodput=$(jq -r '
		.end.sum_received.bits_per_second //
		.end.sum.bits_per_second //
		.end.streams[0].udp.bits_per_second //
		0
	' "$trial_dir/iperf-client.json")
	retransmits=$(jq -r '.end.sum_sent.retransmits // 0' "$trial_dir/iperf-client.json")
	udp_lost=$(jq -r '
		.end.sum.lost_percent //
		.end.streams[0].udp.lost_percent //
		0
	' "$trial_dir/iperf-client.json")
	if [[ $workload == outer-* ]]; then
		outer_baseline=$goodput
	fi

	local wire_tx wg_tx fec_data fec_parity raw_lost recovered unrecovered drops_a drops_b
	wire_tx=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" wire_tx_bytes)
	wg_tx=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" wg_tx_bytes)
	fec_data=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" fec_data_tx)
	fec_parity=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" fec_parity_tx)
	raw_lost=$(stat_delta "$trial_dir/status-b-before.json" "$trial_dir/status-b-after.json" fec_raw_lost)
	recovered=$(stat_delta "$trial_dir/status-b-before.json" "$trial_dir/status-b-after.json" fec_recovered)
	unrecovered=$(stat_delta "$trial_dir/status-b-before.json" "$trial_dir/status-b-after.json" fec_unrecovered)
	drops_a=$(stat_delta "$trial_dir/status-a-before.json" "$trial_dir/status-a-after.json" queue_drops)
	drops_b=$(stat_delta "$trial_dir/status-b-before.json" "$trial_dir/status-b-after.json" queue_drops)

	local clock_ticks cpu_a_s cpu_b_s rss_a rss_b
	clock_ticks=$(getconf CLK_TCK)
	cpu_a_s=$(jq -n --argjson delta "$((cpu_a_after - cpu_a_before))" --argjson hz "$clock_ticks" '$delta / $hz')
	cpu_b_s=$(jq -n --argjson delta "$((cpu_b_after - cpu_b_before))" --argjson hz "$clock_ticks" '$delta / $hz')
	rss_a=$(core_rss_kib a)
	rss_b=$(core_rss_kib b)
	local wire_tx_bps outer_tx outer_tx_bps outer_utilization goodput_to_wire goodput_to_outer
	wire_tx_bps=$(jq -n --argjson bytes "$wire_tx" --argjson duration "$duration" \
		'$bytes * 8 / $duration')
	outer_tx=$((outer_tx_a_after - outer_tx_a_before))
	outer_tx_bps=$(jq -n --argjson bytes "$outer_tx" --argjson duration "$duration" \
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
		"$trial_id,$mode,$link_profile,$csv_schedule,$workload,$repeat,$link_impairment,$link_fwd_rate,$link_rev_rate,$link_fwd_delay,$link_rev_delay,$link_fwd_jitter,$link_rev_jitter,$link_fwd_loss,$link_rev_loss,$link_fwd_duplicate,$link_rev_duplicate,$link_fwd_reorder,$link_rev_reorder,$link_queue_packets,$mtu,$duration,$parallel,$offered_mbit,$outer_baseline,$goodput,$outer_utilization,$retransmits,$udp_lost,$wire_tx,$wire_tx_bps,$goodput_to_wire,$outer_tx,$outer_tx_bps,$goodput_to_outer,$wg_tx,$fec_data,$fec_parity,$raw_lost,$recovered,$unrecovered,$drops_a,$drops_b,$cpu_a_s,$cpu_b_s,$rss_a,$rss_b,$workload_status,$workload_error,$trial_dir/iperf-client.json" \
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
		"${MTU:-1380}" \
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
			run_trial nofec-plain outer-tcp "$repeat" none 0 0 0 0 1000 1380 "${DURATION:-10}" 1 0
			for mode in "${selected_modes[@]}"; do
				run_trial "$mode" tcp "$repeat" none 0 0 0 0 1000 1380 "${DURATION:-10}" 1 0
			done
		done
		;;
	quick)
		CURRENT_LINK_PROFILE=custom
		for mode in nofec-obfs fec-obfs; do
			for loss in 0 2 10; do
				run_trial "$mode" udp 1 symmetric 100 20 0 "$loss" 1000 1380 3 1 1
			done
		done
		run_trial nofec-plain outer-tcp 1 none 0 0 0 0 1000 1380 3 1 0
		for mode in "${selected_modes[@]}"; do
			run_trial "$mode" tcp 1 none 0 0 0 0 1000 1380 3 1 0
		done
		;;
	ceiling)
		CURRENT_LINK_PROFILE=unshaped
		for repeat in $(seq 1 "${REPEATS:-3}"); do
			for parallel in 1 4; do
				run_trial nofec-plain outer-tcp "$repeat" none 0 0 0 0 1000 1380 "${DURATION:-15}" "$parallel" 0
				for mode in "${selected_modes[@]}"; do
					run_trial "$mode" tcp "$repeat" none 0 0 0 0 1000 1380 "${DURATION:-15}" "$parallel" 0
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
							"${RATE_MBIT:-100}" "${ONE_WAY_DELAY_MS:-20}" 0 "$loss" 1000 1380 \
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
					run_trial "$mode" udp "$repeat" symmetric 0 0 0 0 1000 1380 \
						"${DURATION:-15}" 1 "${OFFERED_MBIT:-1}"
					run_trial "$mode" tcp "$repeat" symmetric 0 0 0 0 1000 1380 \
						"${DURATION:-15}" 1 0
				done
			done
		done
		;;
	bandwidth)
		CURRENT_LINK_PROFILE=${LINK_PROFILE:-unshaped}
		for repeat in $(seq 1 "${REPEATS:-3}"); do
			for offered_mbit in ${OFFERED_RATES:-10 25 50 100 200 500}; do
				run_trial nofec-plain outer-udp "$repeat" symmetric 0 0 0 0 1000 1380 \
					"${DURATION:-10}" 1 "$offered_mbit"
				for mode in "${selected_modes[@]}"; do
					run_trial "$mode" udp "$repeat" symmetric 0 0 0 0 1000 1380 \
						"${DURATION:-10}" 1 "$offered_mbit"
				done
			done
		done
		;;
	*)
		echo "matrix must be transports, quick, ceiling, loss, profiles, or bandwidth" >&2
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
  tests/benchmark/run.sh matrix transports|quick|ceiling|loss|profiles|bandwidth
  tests/benchmark/run.sh down

The trial command is configured with environment variables. Common values:
  MODE=direct-wireguard-go|nofec-plain|nofec-obfs|fec-plain|fec-obfs
  WORKLOAD=tcp|udp|outer-tcp|outer-udp
  LINK_PROFILE=custom|unshaped|lan|fiber|cable|dsl|wifi|cellular|satellite|lossy-wifi
  IMPAIRMENT=none|forward|reverse|symmetric
  RATE_MBIT=0 LOSS_PCT=0 ONE_WAY_DELAY_MS=0 JITTER_MS=0
  FWD_RATE_MBIT=0 REV_RATE_MBIT=0 FWD_LOSS_PCT=0 REV_LOSS_PCT=0
  LINK_SCHEDULE=5:cellular,10:fiber
  MODES='direct-wireguard-go nofec-plain nofec-obfs fec-plain fec-obfs'
  OFFERED_RATES='10 25 50 100 200 500'
  DURATION=10 PARALLEL=1 OFFERED_MBIT=50 MTU=1380
EOF
}

main() {
	local command=${1:-}
	case "$command" in
	prepare)
		render_configs fec-obfs 1380
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
		render_configs fec-obfs 1380
		compose_run down --volumes --remove-orphans
		;;
	*)
		usage
		exit 1
		;;
	esac
}

main "$@"
