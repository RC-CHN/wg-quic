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

The fixture compares direct userspace WireGuard with the full two-by-two
wg-quic transport matrix:

| Mode | Carrier | FEC | Salamander |
| --- | --- | --- | --- |
| `direct-wireguard-go` | standard WireGuard UDP bind | n/a | n/a |
| `nofec-plain` | QUIC DATAGRAM | off | off |
| `nofec-obfs` | QUIC DATAGRAM | off | on |
| `fec-plain` | QUIC DATAGRAM | auto | off |
| `fec-obfs` | QUIC DATAGRAM | auto | on |

`outer-tcp` and `outer-udp` bypass the tunnel and provide the Docker/veth
baseline. `direct-wireguard-go` is the stock userspace WireGuard baseline built
from this repository's pinned fork.

The benchmark's small `wg-uapi` helper configures that process and reads its
transfer counters through wireguard-go's standard Unix UAPI. It is used only
before and after the timed workload; it is not part of the direct WireGuard data
path.

By default, every tunnel trial first runs a short outer measurement under the
exact same qdisc and records `outer_baseline_bps`. TCP trials use outer TCP;
UDP trials use the same offered UDP rate and payload size. This distinguishes
the configured netem rate from the rate the container path actually delivers.
Set `MEASURE_OUTER=0` only when the extra measurement would disturb a specific
experiment.

Loss, duplication, reordering, and protocol-policy trials automatically disable
TSO/GSO/GRO plus UDP segmentation/GRO forwarding on the container outer
interfaces. Without this, one large virtual segment can count as one netem
packet and a configured 5% loss rate can become only a fraction of a percent on
the wire. Clean trials leave offloads enabled. Override the decision only for
a deliberate control with `DISABLE_OFFLOADS=0|1`.

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

Each transition uses `tc qdisc replace`, so there is no transient unshaped
window between profiles, and is recorded in `link-schedule.log`. A replacement
resets that qdisc's own counters, so scheduled trials should be interpreted
using iperf and wg-quic counters rather than summing `tc` counters. Every trial
writes `intervals.csv` and samples controller state into `controller.csv`
(0.5-second cadence by default), making rate collapse, protection changes, and
recovery visible instead of only as one run-wide average.

Every iperf client has a hard deadline of `DURATION + CONTROL_GRACE_SECONDS`
(20 seconds of grace by default). A broken or extremely lossy path is recorded
as a failed CSV row instead of blocking the rest of a matrix. Increase the
grace for satellite or outage schedules when completing the control exchange
is itself part of the experiment.

## Trial isolation

Every trial:

1. renders a fresh configuration;
2. recreates both containers, resetting WireGuard, QUIC congestion, and FEC
   state;
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
./tests/benchmark/run.sh matrix transports
./tests/benchmark/run.sh matrix quick
./tests/benchmark/run.sh matrix ceiling
./tests/benchmark/run.sh matrix loss
./tests/benchmark/run.sh matrix profiles
./tests/benchmark/run.sh matrix bandwidth
./tests/benchmark/run.sh matrix protocol
```

- `transports` is the shortest apples-to-apples zero-loss TCP comparison: raw
  outer plus all five tunnel modes, one repetition by default.
- `quick` is a short functional sample, not publication-quality evidence.
- `ceiling` runs raw outer TCP plus all five tunnel modes with 1 and 4 streams.
- `loss` runs all five modes using TCP and a conservative 0.5 Mbit/s UDP load
  at 100 Mbit/s outer rate, approximately 40 ms RTT, and 0–15% symmetric
  random loss. Override `OFFERED_MBIT` after a bandwidth sweep.
- `profiles` runs all five modes with TCP capacity traffic and a low-load
  1 Mbit/s UDP probe across every synthetic link profile.
- `bandwidth` sweeps offered UDP load through outer and all five tunnel modes.
  Override its space-separated load points with
  `OFFERED_RATES='5 10 20 40 80'`; select its link with `LINK_PROFILE`.
- `protocol` applies WireGuard signature drop/police and QUIC long-header
  handshake-drop policies to direct, plain QUIC, and Salamander modes.

All full matrices accept a space-separated `MODES` subset. For example, compare
only direct WireGuard and the production FEC/obfuscation combination:

```sh
MODES='direct-wireguard-go fec-obfs' \
./tests/benchmark/run.sh matrix profiles
```

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

## Protocol discrimination and QoS controls

`PROTOCOL_POLICY` installs IPv4 egress classifiers on both fixture nodes:

- `wireguard-block` drops outer UDP payloads beginning with WireGuard message
  types 1–4;
- `wireguard-throttle` polices those signatures to
  `PROTOCOL_RATE_MBIT` (1 Mbit/s by default);
- `quic-handshake-block` drops QUIC v1/v2 long-header handshake packets;
- `none` installs no policy.

The classifier counters are saved as `tc-filter-a.txt` and
`tc-filter-b.txt`. These are synthetic, fixture-specific DPI controls. They
demonstrate protocol discrimination and do not emulate a blanket UDP ban:
wg-quic is currently UDP-only, so generic UDP blocking needs another carrier.

Stop any retained fixture:

```sh
./tests/benchmark/run.sh down
```

## Result interpretation

`summary.csv` contains delivered iperf goodput together with wg-quic deltas:

- measured outer baseline and tunnel/outer utilization;
- configured forward/reverse link parameters;
- WireGuard and wire bytes;
- measured sender transport-payload bit rate and goodput/payload ratio;
- comparable sender `eth0` bytes, bit rate, and goodput/outer ratio;
- FEC data/parity counts;
- raw missing, recovered, and unrecovered FEC shards;
- current adaptive parity and FEC loss estimate;
- QUIC acknowledged/lost bytes and packets;
- connection minimum, current path baseline, latest, and smoothed RTT;
- congestion window, in-flight bytes, delivery-capacity estimate, total pacing
  budget, queue delay, FEC-classified loss, and model state;
- local queue drops;
- core process CPU seconds and final RSS;
- explicit `status` and `error` fields for timeouts and failed paths.

`controller.csv` records the dynamic fields throughout the workload. The
connection-lifetime `quic_min_rtt_us` and controller-local
`quic_path_rtt_us` intentionally differ after an access-path change.

`outer_baseline_bps` is a short protocol-matched sanity measurement, not an
oracle for bottleneck capacity. In high-RTT TCP profiles such as `satellite`,
increase `OUTER_MEASURE_DURATION` so TCP has enough RTTs to leave startup. The
UDP `bandwidth` sweep is the stronger capacity check: the sustainable point is
the highest offered load that keeps receiver loss and local queue drops within
the chosen acceptance threshold.

`wire_tx_bytes_a` is a transport-internal counter: wg-quic counts submitted QUIC
DATAGRAM payload while direct WireGuard counts encrypted WireGuard messages.
Use `outer_tx_bytes_a`, `outer_tx_bps_a`, and `goodput_to_outer_ratio` for
cross-mode efficiency comparisons because those fields use the same sender
`eth0` counter in every mode.

`queue_drops_a` and `queue_drops_b` are ArmorBind queue counters and are
therefore zero for `direct-wireguard-go`, which has no equivalent ArmorBind
queue. Inspect `tc-a.txt` and `tc-b.txt` when comparing qdisc loss or overflow
across direct and wg-quic modes.

Always retain the raw JSON and qdisc output beside the CSV. A useful comparison
requires at least three repetitions on an otherwise idle host. Report medians
and dispersion, not only the best run.

Aggregate repeated rows without external Python packages:

```sh
./tests/benchmark/report.py tests/benchmark/results/<run-id>/summary.csv \
  > tests/benchmark/results/<run-id>/report.csv
```

The report groups identical transport/link/workload conditions and emits the
outer and tunnel medians, goodput P10/P90, sender outer bit rate, UDP loss,
queue drops, combined core CPU time, and both internal-payload and comparable
outer-wire efficiency. Dynamic and static link trials remain separate groups
through the `link_schedule` field.

For the loss matrix, keep outer rate, RTT, MTU, offered UDP rate, direction,
duration, and queue limit unchanged while varying only FEC mode and loss. For
the ceiling matrix, verify that the outer baseline is comfortably above the
tunnel result; otherwise the host or Docker network, not wg-quic, is the
bottleneck.

Before treating a UDP loss comparison as an FEC comparison, verify that
`queue_drops_a` remains zero. If it is nonzero, the application offered more
than the current QUIC path can drain, and local overload dominates the
configured network loss. Run the `bandwidth` sweep first, then choose an offered
load below the first local-drop point for the FEC loss matrix.

`congestion=auto` currently selects the experimental model controller. It
estimates acknowledged outer delivery, maintains a dynamic path RTT and queue
signal, and paces source and repair traffic under one QUIC wire budget. Use
`CONGESTION=reno|cubic|model` for controlled comparisons. FEC reconstruction
does not erase QUIC packet-loss accounting; it supplies an additional
recoverable/residual classification to the model. Application-limited rounds
do not advance the model's Startup plateau detector, but a faster bounded
delivery sample may still raise the capacity estimate. Adaptive FEC also uses
the measured path RTT: long-recovery paths require a lower measured-loss
threshold and a longer clean evidence window before parity is disabled.

Clean ceiling numbers need their link condition. `LINK_PROFILE=lan` is a
1 Gbit/s quality-link acceptance test. `unshaped` is a near-zero-RTT container
GSO/CPU ceiling and can be several times faster for direct WireGuard; those two
baselines must not be compared as if they represented the same target.
