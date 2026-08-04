# wg-quic controlled network benchmark

This fixture measures the current data plane without changing host routes or
DNS. Two privileged containers have independent network namespaces and real TUN
devices. All `tc netem` state is installed inside those namespaces.

It is deliberately separate from `tests/container/test.sh`: that test is a
correctness gate with full-tunnel routing, while this fixture uses one split
tunnel prefix and restarts both peers before every trial.

## Questions answered

The first matrices target two questions:

1. What is the zero-loss bandwidth ceiling and CPU cost of the current stack?
2. At a fixed outer rate and RTT, how do delivered goodput and residual loss
   change as independent random loss increases?

The fixture compares a two-by-two transport matrix:

| Mode | FEC | Salamander |
| --- | --- | --- |
| `nofec-plain` | off | off |
| `nofec-obfs` | off | on |
| `fec-plain` | auto | off |
| `fec-obfs` | auto | on |

`outer-tcp` and `outer-udp` bypass the tunnel and provide the Docker/veth
baseline. They are not a stock WireGuard baseline.

By default, every tunnel trial first runs a short outer measurement under the
exact same qdisc and records `outer_baseline_bps`. TCP trials use outer TCP;
UDP trials use the same offered UDP rate and payload size. This distinguishes
the configured netem rate from the rate the container path actually delivers.
Set `MEASURE_OUTER=0` only when the extra measurement would disturb a specific
experiment.

## Synthetic link profiles

`LINK_PROFILE` selects a starting link. These profiles are controlled synthetic
conditions, not claims that every real network of that name behaves this way.

| Profile | Forward/reverse rate | Approximate base RTT | Extra effects |
| --- | ---: | ---: | --- |
| `unshaped` | host limit | host limit | none |
| `lan` | 1000/1000 Mbit/s | 0.4 ms | small jitter |
| `fiber` | 500/500 Mbit/s | 10 ms | 0.01% loss |
| `cable` | 300/30 Mbit/s | 20 ms | 0.1% loss |
| `dsl` | 50/10 Mbit/s | 30 ms | 0.1% loss |
| `wifi` | 100/100 Mbit/s | 10 ms | jitter, loss, duplicate, reorder |
| `cellular` | 50/15 Mbit/s | 70 ms | high jitter, loss, reorder |
| `satellite` | 25/5 Mbit/s | 600 ms | jitter and loss |
| `lossy-wifi` | 100/100 Mbit/s | 40 ms | correlated loss and reorder |

For fully controlled asymmetric experiments use `LINK_PROFILE=custom` and the
directional variables:

```sh
LINK_PROFILE=custom \
IMPAIRMENT=symmetric \
FWD_RATE_MBIT=200 REV_RATE_MBIT=20 \
FWD_DELAY_MS=10 REV_DELAY_MS=30 \
FWD_JITTER_MS=2 REV_JITTER_MS=10 \
FWD_LOSS_PCT=0.1 REV_LOSS_PCT=2 \
FWD_DUPLICATE_PCT=0.05 REV_DUPLICATE_PCT=0 \
FWD_REORDER_PCT=0.5 REV_REORDER_PCT=2 \
LOSS_CORRELATION_PCT=25 \
REORDER_CORRELATION_PCT=25 \
./tests/benchmark/run.sh trial
```

To model a changing path, `LINK_SCHEDULE` applies complete profiles at absolute
seconds after the main iperf workload starts:

```sh
LINK_PROFILE=fiber \
LINK_SCHEDULE='5:cellular,12:dsl,20:fiber' \
DURATION=25 \
./tests/benchmark/run.sh trial
```

Each transition is recorded in `link-schedule.log`. A qdisc replacement resets
that qdisc's own counters, so scheduled trials should be interpreted using
iperf and wg-quic counters rather than summing `tc` counters. Every trial also
writes `intervals.csv`, making rate collapse and recovery visible per iperf
interval instead of only as one run-wide average.

Every iperf client has a hard deadline of `DURATION + CONTROL_GRACE_SECONDS`
(20 seconds of grace by default). A broken or extremely lossy path is recorded
as a failed CSV row instead of blocking the rest of a matrix. Increase the
grace for satellite or outage schedules when completing the control exchange
is itself part of the experiment.

## Trial isolation

Every trial:

1. renders a fresh configuration;
2. recreates both containers, resetting QUIC Reno and FEC controller state;
3. applies the requested one-way or symmetric `netem` before the first probe;
4. waits for the tunnel through that shaped path;
5. captures baseline status and core CPU ticks;
6. runs one A-to-B iperf3 workload;
7. captures raw iperf JSON, sender/receiver status, qdisc statistics, core CPU,
   and RSS.

The outer direction is explicit:

- `forward`: impairment on A egress, primarily data A to B;
- `reverse`: impairment on B egress, primarily QUIC and inner-protocol ACKs;
- `symmetric`: the same independent model on both egress paths.

`ONE_WAY_DELAY_MS=20` with `symmetric` therefore produces approximately 40 ms
base RTT. `LOSS_PCT` is the independent per-direction netem probability, not an
end-to-end aggregate.

## Commands

Run one short sanity check:

```sh
./tests/benchmark/run.sh smoke
```

Run a custom trial:

```sh
MODE=fec-obfs \
WORKLOAD=udp \
IMPAIRMENT=symmetric \
RATE_MBIT=100 \
ONE_WAY_DELAY_MS=20 \
LOSS_PCT=2 \
OFFERED_MBIT=50 \
DURATION=15 \
./tests/benchmark/run.sh trial
```

Build once and reuse the image when iterating:

```sh
./tests/benchmark/run.sh prepare
SKIP_BUILD=1 MODE=nofec-obfs WORKLOAD=tcp ./tests/benchmark/run.sh trial
```

Available matrices:

```sh
./tests/benchmark/run.sh matrix quick
./tests/benchmark/run.sh matrix ceiling
./tests/benchmark/run.sh matrix loss
./tests/benchmark/run.sh matrix profiles
./tests/benchmark/run.sh matrix bandwidth
```

- `quick` is a short functional sample, not publication-quality evidence.
- `ceiling` runs raw outer TCP plus all four tunnel modes with 1 and 4 streams.
- `loss` compares FEC on/off using TCP and a conservative 0.5 Mbit/s UDP load
  at 100 Mbit/s outer rate, approximately 40 ms RTT, and 0–15% symmetric
  random loss. Override `OFFERED_MBIT` after a bandwidth sweep.
- `profiles` compares FEC on/off with TCP capacity traffic and a low-load
  1 Mbit/s UDP probe across every synthetic link profile.
- `bandwidth` sweeps offered UDP load through outer, plain, obfuscated, and FEC
  paths. Override its space-separated load points with
  `OFFERED_RATES='5 10 20 40 80'`; select its link with `LINK_PROFILE`.

For example, measure where a cellular-shaped path starts dropping offered load:

```sh
LINK_PROFILE=cellular \
OFFERED_RATES='5 10 15 20 30 50 80' \
REPEATS=3 \
./tests/benchmark/run.sh matrix bandwidth
```

Use `REPEATS`, `DURATION`, `RATE_MBIT`, `ONE_WAY_DELAY_MS`, and
`OFFERED_MBIT` to override the full matrices. Results are written under
`tests/benchmark/results/<UTC run id>/`.

Stop any retained fixture:

```sh
./tests/benchmark/run.sh down
```

## Result interpretation

`summary.csv` contains delivered iperf goodput together with wg-quic deltas:

- measured outer baseline and tunnel/outer utilization;
- configured forward/reverse link parameters;
- WireGuard and wire bytes;
- measured sender wire bit rate and goodput/wire ratio;
- FEC data/parity counts;
- raw missing, recovered, and unrecovered FEC shards;
- local queue drops;
- core process CPU seconds and final RSS.
- explicit `status` and `error` fields for timeouts and failed paths.

`outer_baseline_bps` is a short protocol-matched sanity measurement, not an
oracle for bottleneck capacity. In high-RTT TCP profiles such as `satellite`,
increase `OUTER_MEASURE_DURATION` so TCP has enough RTTs to leave startup. The
UDP `bandwidth` sweep is the stronger capacity check: the sustainable point is
the highest offered load that keeps receiver loss and local queue drops within
the chosen acceptance threshold.

Always retain the raw JSON and qdisc output beside the CSV. A useful comparison
requires at least three repetitions on an otherwise idle host. Report medians
and dispersion, not only the best run.

Aggregate repeated rows without external Python packages:

```sh
./tests/benchmark/report.py tests/benchmark/results/<run-id>/summary.csv \
  > tests/benchmark/results/<run-id>/report.csv
```

The report groups identical transport/link/workload conditions and emits the
outer and tunnel medians, goodput P10/P90, UDP loss, queue drops, combined core
CPU time, and goodput-to-wire efficiency. Dynamic and static link trials remain
separate groups through the `link_schedule` field.

For the loss matrix, keep outer rate, RTT, MTU, offered UDP rate, direction,
duration, and queue limit unchanged while varying only FEC mode and loss. For
the ceiling matrix, verify that the outer baseline is comfortably above the
tunnel result; otherwise the host or Docker network, not wg-quic, is the
bottleneck.

Before treating a UDP loss comparison as an FEC comparison, verify that
`queue_drops_a` remains zero. If it is nonzero, the application offered more
than the current QUIC/Reno path can drain, and local overload dominates the
configured network loss. Run the `bandwidth` sweep first, then choose an offered
load below the first local-drop point for the FEC loss matrix.

The current QUIC implementation uses Reno and sees raw QUIC packet loss even
when FEC reconstructs an application shard. Consequently, FEC may reduce
residual application loss without preserving TCP goodput at high random-loss
rates. This fixture is intended to quantify that distinction.
