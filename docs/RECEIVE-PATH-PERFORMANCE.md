# Small-VPS receive and GSO follow-up — 2026-09-05

Baseline: `6c4edb5`. Implementation commits: `5915381` and `bfad9ca`.
This follow-up retains smaller receive-endpoint snapshots,
allocation-free socket address conversion, and Linux GSO coordinated with
automatic FEC. Single packets use ordinary writes; FEC state controls batching
for subsequent packets. WireGuard, QUIC encryption, automatic FEC, Salamander,
and production queue capacities remain enabled and unchanged.

## Retained implementation results

The final policy adds zero estimated loss and completion of model startup to
the FEC guard. The `stable-verify-clean-*` series contains two fresh-session
pairs, reversing build order for the second pair. The clean table entry is
their median. The half-CPU entry is a separate fresh-session pair.

| Condition | Baseline | Retained implementation | Change |
|---|---:|---:|---:|
| 1 CPU, fixed placement, 20 s; two trials per build | 921.94 Mbit/s | 1064.84 Mbit/s | +15.5% |
| 0.5 CPU, fixed placement, 20 s; one pair | 120.05 Mbit/s | 186.22 Mbit/s | +55.1% |
| 1 CPU, fixed placement, 60 s; one pair | 939.79 Mbit/s | 1303.10 Mbit/s | +38.7% |
| 100 Mbit/s, 40 ms RTT, 1% loss per direction, 60 s; one pair | 68.07 Mbit/s | 72.66 Mbit/s | +6.7% |

The two final clean trials were 1111.46 and 1018.22 Mbit/s, versus 930.33 and
913.55 for the baseline. An earlier exploratory run of the same final policy
measured 1069.81 Mbit/s clean and 180.45 Mbit/s at half CPU. Those exploratory
values are excluded from the table. These results support improvements in both
the short clean transfer and CPU-constrained cases; the small sample counts do
not establish exact gains on other machines.

The final 60-second clean run (`long-stable`) is compared with the earlier
`long-base` under identical limits. Its last 30 seconds averaged 1442.35
Mbit/s versus 952.73 for the baseline, using sender interval data. This is
evidence of higher sustained throughput on this host, not a guaranteed ceiling.
Receiver QUIC application-queue drops were 140 versus 21 over the full minute;
the stack can still drop at saturation despite no injected link loss.
Sender/receiver RSS snapshots were 73.4/50.8 MiB versus 89.3/47.5 MiB. These
are process snapshots, not total container memory, peak use, or a long-running
memory bound.

The final lossy pair (`long-lossy-base` / `long-lossy-stable`) extends the
workload to 60 seconds to reduce the influence of startup. The receiver recovered
4410/4415 missing FEC shards, versus 4204/4215 for the baseline. This pair shows
no lossy-path regression; one random-loss pair is insufficient to establish a
general loss-performance improvement. The superseded guard's shorter negative
results remain recorded below.

The separate `profile-stable` run confirms that `sendPacketsWithGSO` executes
in the retained implementation. `sendmsg` accounts for 26.61% of sender CPU
samples versus 50.39% in the baseline profile. These fractions have different
throughput and CPU totals and are not a direct whole-program speedup. Receive
endpoint construction accounts for 26.43% of receiver sampled allocation bytes,
down from 49.80%; socket address parsing and remote-address cloning now account
for 56.49% together. The allocation microbenchmarks below isolate the concrete
per-operation savings.

The feature is automatic on supported Linux sockets. Setting
`QUIC_GO_DISABLE_GSO=true` in the core process environment before startup
selects ordinary writes while retaining both allocation improvements.

## Findings and retained implementation

A new profile of the baseline's actual core processes showed `sendmsg` at
50.39% of sender CPU samples. The receive-endpoint constructor accounted for
49.80% of receiver sampled allocation bytes; UDP address cloning and socket
address parsing accounted for another 16.01% and 16.33%, respectively. These
allocation percentages are sampled cumulative allocations, not RSS.

The profile also exposed an attribution error in the earlier report:
`sys_conn_helper_linux.go` had returned false unconditionally from
`isGSOEnabled` since `31b28ab`. Consequently, the coalescer added in `6c4edb5`
was not active in the earlier Linux trials. Their measured improvements remain
valid, but cannot be credited to GSO. Both earlier performance reports now
state this explicitly.

The retained changes are:

- Received WireGuard endpoints use a compact immutable snapshot, containing
  the packet's address, ingress sequence, session and reply route. Mutable
  configuration and reconnect state stay on the configured endpoint. Replies
  use the live session or its configured fallback, preserving migration and
  closed-session behavior.
- Salamander sends through `WriteMsgUDPAddrPort` and `WriteToUDPAddrPort`.
  Address validation and key selection still occur before sending. This
  removes the standard library's per-write heap allocation for a socket
  address without changing wire contents.
- Linux uses the upstream kernel/socket GSO capability probe again. Kernels
  older than version 5, failed probes, and `QUIC_GO_DISABLE_GSO=true` retain
  ordinary writes. The send queue requests UDP segmentation only when a write
  actually contains multiple packets; a single packet uses an ordinary write.
  Existing bounded coalescing, pacing admission, ECN and MTU rules remain.
- With automatic FEC, the session starts with GSO batching disabled. At the
  existing 32-frame path-sampling cadence, it enables batching only when the
  FEC controller requests zero parity, its loss estimate is zero PPM, and the
  model congestion controller has left startup. Other congestion controllers
  use the same FEC conditions without the model-state check. A path needing
  protection or restarting model startup returns to ordinary writes. This
  preserves initial bandwidth sampling and avoids grouping related shards into a
  single loss event in queues/shapers that act before segmentation. Already
  queued packets retain their packing decision. Explicit FEC-off mode leaves
  GSO available, subject to socket support and the environment opt-out.
- A GSO device failure publishes the per-connection fallback state atomically.
  An ordinary write error cannot enter a zero-segment-size retry loop.

## Allocation measurements

Go 1.26.3, Linux amd64, Intel Xeon CPU Max 9470C, `GOMAXPROCS=1`, pinned CPU 4.
Each implementation has three 300-ms microbenchmark samples; numbers below are
medians. The socket benchmark writes one real IPv4 UDP packet and reads it
back with the value-address API; it does not measure the whole tunnel.

| Operation | Baseline | Retained implementation | Allocation change |
|---|---:|---:|---:|
| Immutable receive endpoint | 63.50 ns | 40.53 ns | 176 → 64 B; still one allocation |
| Salamander UDP write + raw receive | 4661 ns | 4614 ns | 32 → 0 B; 1 → 0 allocations |

The socket timings do not establish a speedup. They establish elimination of
one allocation per IPv4 socket write. The endpoint remains separately allocated
because WireGuard can retain its immutable ingress identity beyond delivery.

## Receive experiments not retained

A candidate drained up to 16 QUIC DATAGRAMs per lock acquisition, transferred
batches through four reusable slots (64 datagrams total), and retained a QUIC
buffer through WireGuard's final copy to avoid the intermediate reassembly
copy for whole frames. Ownership, cancellation, close, pool reuse, and fragment
behavior were tested before end-to-end comparison.

This combination did not demonstrate a throughput benefit. Without GSO, one
clean pair was 976.81 Mbit/s baseline versus 911.86 with the receive changes;
one half-CPU pair was 117.65 versus 118.40. After the other optimizations and
GSO changes, the full receive candidate measured 687.16 Mbit/s versus 953.19
without it, while half-CPU results remained close (183.62 versus 186.54).
These are exploratory single trials, not precise effect sizes. The receive
batching and buffer-transfer changes were removed together. Their code and
results are retained under `/tmp/wg-quic-receive/full-receive-experiment/`.

## End-to-end method

Two isolated Docker containers use real TUN devices. Each endpoint has a
1-CPU quota, 512 MiB without swap, `GOMAXPROCS=1`, and `GOMEMLIMIT=192MiB`;
half-CPU trials change only the quota to 0.5. Fixed placement uses separate
physical cores 2 and 3; unrestricted placement permits CPUs 0–103. Clean TCP
uses one stream, MTU 1280, no injected delay/loss or bandwidth limit, and
retains network offloads. Baseline and retained builds alternate order across
repeats. The host is the same modern Xeon used for the earlier reports.
Core binaries use `CGO_ENABLED=0`; diagnostic profiles add signal-triggered
profiling through a temporary build overlay only.

CPU quota suspension is not hypervisor steal. The absolute throughput is not
a promise for a cheap VPS, and short startup-inclusive trials must not be
confused with sustained capacity. No larger production queues or weaker
cryptography were used to obtain these results.

## Parity-only guard experiment, superseded

The `guard-verify-*` trials compare the baseline with the first FEC guard,
which checked only parity and did not wait for zero estimated loss or the end
of model startup. This is not the final implementation.
Each 20-second clean trial includes startup; the loss trials last 15 seconds.
Both builds have two trials per condition, reversing their order in the second
pair. Values are medians in Mbit/s. The 60-second comparison is a separate
single pair, also including startup.

| Condition | Baseline | FEC guard | Change |
|---|---:|---:|---:|
| 1 CPU, fixed placement, 20 s | 904.40 | 771.42 | -14.7% |
| 0.5 CPU, fixed placement, 20 s | 120.19 | 184.24 | +53.3% |
| 1 CPU, unrestricted placement, 20 s | 296.58 | 361.32 | +21.8% |
| 1 CPU, fixed placement, 60 s; one pair | 939.79 | 1167.38 | +24.2% |
| 100 Mbit/s, 40 ms RTT, 1% loss per direction, 15 s | 65.22 | 53.55 | -17.9% |

The two short clean guard trials were 684.12 and 858.72 Mbit/s, versus 892.44
and 916.35 for the baseline. Their slow ramp is a real short-transfer cost in
these measurements. In the long trial, the last 30 seconds averaged 1279.48
Mbit/s for the guard and 952.73 for the baseline, using iperf's sender intervals.
This supports a sustained-capacity improvement on this host, but one long pair
does not establish a precise effect size or a guaranteed ceiling.

The loss guard trials were 51.03 and 56.07 Mbit/s, versus 60.51 and 69.92.
The receiver recovered all 766 and 925 missing FEC shards in the guard trials;
the baseline recovered 934/934 and 1074/1078. Thus the catastrophic unguarded
FEC failure below is fixed, but the short lossy throughput comparison still
shows a regression. Random-loss and startup variability are not grounds for
discarding that result.

During the long pair, sender/receiver RSS snapshots were 76.7/51.1 MiB for
the guard and 89.3/47.5 MiB for the baseline. These are process snapshots,
not total container memory, peak usage, or a long-running memory bound.

A separate guard CPU profile confirms that `sendPacketsWithGSO` is actually
executing. `sendmsg` accounts for 26.08% of sender CPU samples, versus 50.39%
in the earlier baseline profile; different throughput and CPU totals mean
these percentages are not a direct whole-program speedup. The compact endpoint
constructor accounts for 29.12% of receiver sampled allocation bytes, down
from 49.80%. Socket address parsing and remote-address cloning together still
account for about 52%, leaving a concrete target for future receive work.

## Unconditional GSO experiment, not retained

Before the FEC guard, these 20-second trials included startup. Each build
was restarted for each transfer, reversing order on every second pair.
Values are medians in Mbit/s; this is not the final implementation.

| Condition | Trials per build | Baseline | Unguarded GSO | Change |
|---|---:|---:|---:|---:|
| 1 CPU, fixed placement | 2 | 901.37 | 758.09 | -15.9% |
| 0.5 CPU, fixed placement | 3 | 119.57 | 181.78 | +52.0% |
| 1 CPU, unrestricted placement | 2 | 286.11 | 367.64 | +28.5% |

The half-CPU candidate trials were 181.78 / 182.59 / 181.22 Mbit/s, versus
121.09 / 119.13 / 119.57 for the baseline. The unrestricted-placement trials
were 373.80 / 361.49 versus 288.04 / 284.18. These results support the target
of intermittent CPU availability on this host, but do not simulate hypervisor
steal or establish a precise improvement for other CPUs.

The fixed-placement result is a regression for short transfers:
759.20 / 756.99 versus 892.56 / 910.19. It must not be hidden by the earlier
single 953.19-Mbit/s exploratory result. The time series showed a slower ramp. More seriously, a 15-second
100-Mbit/s, 40-ms-RTT trial with 1% random loss per direction fell from 60.79
to 9.83 Mbit/s. FEC recovered only 41 of 121 missing shards, versus 931 of
960 in the baseline. This is consistent with correlated shard losses in
batches, and ruled out shipping unconditional GSO. The retained FEC guard
was added after this failure.

All repeated clean transfers completed. Receiver QUIC application-queue drops
were 8–39 per candidate trial versus 0–18 in the baseline. Saturation is therefore
not loss-free, even though the link injects no loss. RSS snapshots and sampled
allocation totals must not be interpreted as a long-lived memory bound.

## Validation and reproduction

Project and full local quic-go suites pass. Race coverage includes bind
sessions, immutable ingress identity, migration, WireGuard, Salamander, GSO
queues, connection batching and send fallback. Linux tests exercise the real
kernel's segmentation of Salamander traffic, including a short final segment,
and verify decoded packet contents and boundaries. IPv4 and IPv6 writes,
explicit GSO disable, application suppression and re-enabling, unsupported
probes and ordinary-write errors are covered.
Linux arm64, Windows amd64 and FreeBSD amd64 builds pass; only Linux amd64 was
executed.

Use the [controlled benchmark](../tests/benchmark/README.md) with identical
CPU placement and resource limits for both builds, fresh run IDs and alternating
order. Set `QUIC_GO_DISABLE_GSO=true` in the core process environment before
startup to compare ordinary writes on the same build. Raw trials, diagnostic
build overlays, comparison data and test logs are retained locally under
`/tmp/wg-quic-receive/`; none are production build inputs.
