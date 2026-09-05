# Clean-link performance — 2026-09-05

Baseline: `cb8d193`, which includes the earlier small-VPS allocation work.
This follow-up targets clean links while retaining WireGuard, QUIC encryption,
automatic FEC, Salamander, and the existing production queue capacities.

## Implementation

- The QUIC send queue combines equal-size, already-queued single-segment
  packets into GSO writes. It copies into the first packet's existing buffer,
  preserves each datagram and its ECN marking, and neither pads packets nor
  waits for more traffic. Each burst is bounded by available buffer capacity
  and the queue depth sampled at its start, at most nine segments. A packet
  that cannot join the batch stays first in line for the next write. Error
  cleanup releases the current and pending buffers; MTU errors report the
  segment size rather than the combined write size.
- A full DATAGRAM receive queue gives runnable consumers one scheduling
  opportunity before a single enqueue retry. If it is still full, the packet
  is dropped as before. Queue capacities do not grow, and a stalled consumer
  cannot block the connection indefinitely. Enqueue and close synchronize so
  a retry cannot append to an already closed queue.
- Model congestion control previously counted packet rounds as bandwidth
  plateaus even before its next delivery measurement. Measurements are at
  least 5 ms apart; short-RTT rounds can complete much faster. Startup now
  requires a fresh measurement for each plateau observation and consumes it
  only once. Timeout and measurement resets discard pending observations.
- Congestion-window calculations now budget for the pacer's 1 ms minimum
  delay and 1 ms timer granularity. A very small propagation RTT therefore
  cannot constrain an otherwise healthy connection to a few packets per
  timer wakeup. Minimum-rate and timeout calculations use the same budget,
  so ECN and persistent queue signals can still reduce the rate and drain
  the window. Measured propagation RTT, long-RTT BDP calculations, and the
  maximum congestion window are unchanged.
- The benchmark accepts independent `WGQ_BENCH_CPUSET_A` and
  `WGQ_BENCH_CPUSET_B` values and records them in `parameters.json`.
  Defaults impose no CPU affinity. This is a benchmark control, not a new
  production affinity policy.

## Measurement conditions

Go 1.26.3, Linux amd64, Intel Xeon CPU Max 9470C. Each endpoint has a 1 CPU
quota, 512 MiB memory without swap, `GOMAXPROCS=1`, and
`GOMEMLIMIT=192MiB`, unless a row explicitly uses 0.5 CPU. Most comparisons
pin the containers to separate physical cores (CPU 2 and CPU 3). MTU is 1280;
TCP uses one stream. Clean tests inject no delay or loss and impose no link
rate limit. Network offloads are retained on clean links.

CPU affinity materially changes this fixture's results. The earlier
unrestricted-placement baseline measured 165.74 Mbit/s, while three
fixed-placement baseline trials measured 445.02–569.45 Mbit/s, median
507.21 Mbit/s. These differences are not software optimization gains, and
the earlier 200-Mbit/s measurements are not a hardware ceiling. A CPU quota
on this host is not a measurement of a low-end VPS or hypervisor steal.

## Intermediate experiments

GSO batching alone did not establish a clean TCP peak-throughput improvement:
three interleaved trials had median 500.50 Mbit/s versus baseline 507.21.
Under 0.5 CPU, one trial measured 115.90 versus 87.24 Mbit/s. That single pair
is encouraging but cannot establish the size of a repeatable gain.

Offering UDP at 400 Mbit/s exposed a controller limitation: the baseline
delivered only 61.25 Mbit/s while allocating 5.7–9.6 KiB of congestion window
and leaving CPU capacity unused. Startup-sampling changes alone did not fix
that limit. Adding a scheduling allowance to the window, without GSO batching,
raised received goodput to 215.21 Mbit/s in one trial. Both UDP trials still
dropped packets; this is not a claim of loss-free 400-Mbit/s operation.

## Combined implementation

The last unchanged-baseline TCP trials bracketed the first combined build.
The receiver scheduling adjustment was then tested twice with otherwise
identical conditions. The final source includes that adjustment:

| Clean TCP condition | Baseline | Final source |
|---|---:|---:|
| 1 CPU, fixed placement, 20 s, two trials each | 473.29 / 464.59 Mbit/s | 940.47 / 912.18 Mbit/s |
| 1 CPU, unrestricted placement, 20 s, one trial each | 165.74 Mbit/s | 294.06 Mbit/s |
| 0.5 CPU, fixed placement, 20 s, one trial each | 87.24 Mbit/s | 118.63 Mbit/s |

The fixed-placement medians are 468.94 and 926.33 Mbit/s, approximately 1.98x.
The other two comparisons are single pairs and should not be interpreted as
precise repeatable speedup percentages. These results supersede the earlier
speculative 230–280-Mbit/s optimization target for this particular test host;
they do not promise similar absolute speeds on a cheap VPS.

Before the receiver scheduling adjustment, two combined-build trials reached
898.91 / 912.36 Mbit/s but recorded 1433 / 1269 receiver QUIC application-queue
drops. After it, the two final trials recorded 17 / 21 such drops. The baseline
recorded zero. Send-side queue drops were 131 / 140 versus baseline 62 / 65.
The throughput improvement therefore comes with a small remaining overload
loss rate at saturation; this work does not claim zero queue loss. The final
half-CPU trial had zero receiver QUIC queue drops and the unrestricted-placement
trial had two. Final clean TCP core RSS snapshots were approximately 41–71 MiB
per endpoint; this is not a long-lived or multi-peer memory bound.

The final UDP trial at 400 Mbit/s offered load delivered 291.72 Mbit/s, versus
the baseline's 61.25 Mbit/s. Loss remained 26.88%, including substantial
sender admission drops and 47 receiver QUIC queue drops: this demonstrates a
higher forwarding rate under overload, not loss-free capacity. Its core RSS
snapshots were about 88 / 98 MiB. A separate final 15-second TCP regression
with a 100-Mbit/s link, 40-ms base RTT, and 1% random loss per direction
completed at 66.68 Mbit/s. That is a functional lossy-path check, not a paired
claim of improved lossy-link performance.

## Validation and reproduction

The complete project and quic-go test suites pass. Race coverage includes
send queues, GSO fallback, connection pacing and address changes, congestion
control, and ACK handling. Regression tests cover byte-for-byte datagram
boundaries and ordering, ECN changes, disabled GSO, existing batches, buffer
capacity, close/error ownership, segment MTU reporting, fresh startup
measurements, scheduling-window bounds, and congestion response on short RTTs.
Receiver tests cover consumer progress on one Go execution thread, persistent
overload with no consumer, and rejection after close. Linux arm64, Windows
amd64 and FreeBSD amd64 builds are also checked; Linux amd64 is the platform
used for performance measurements.

To compare two builds, keep the resource settings and CPU placement identical
and alternate their order. For a clean pinned trial on a host with CPU 2 and 3:

```sh
WGQ_BENCH_CPUS=1 WGQ_BENCH_MEMORY=512m \
WGQ_BENCH_CPUSET_A=2 WGQ_BENCH_CPUSET_B=3 \
WGQ_BENCH_GOMAXPROCS=1 WGQ_BENCH_GOMEMLIMIT=192MiB \
MODE=fec-obfs WORKLOAD=tcp LINK_PROFILE=unshaped \
DURATION=20 MEASURE_OUTER=0 RUN_ID=clean-pinned \
./tests/benchmark/run.sh trial
```

Use CPU IDs valid on the test host. Raw trials, image build inputs and
experimental overlays from this session are retained locally at
`/tmp/wg-quic-batching/`. They are not production build inputs.
