# Public Internet Field Performance and Diagnostics

## Status

- First field observation: 2026-08-23
- Tested release: `wg-quic v0.3.2`
- Tested deployment: four Ubuntu 22.04 backbone nodes on independent public
  networks
- Document purpose: preserve the first complex public-path observations,
  distinguish path faults from implementation limits, and define the telemetry
  required for reproducible controller research
- Follow-up: `session_telemetry_v1` now supplies bounded, portable active-session
  attribution plus cumulative PTO and spurious-loss counters. Controller events,
  bounded trace capture, and kernel receive-overflow attribution remain future
  work.
- Data sensitivity: node labels are region aliases. Public addresses,
  credentials, WireGuard keys, and application payloads are intentionally not
  recorded here.

This is an engineering field report, not a throughput claim. The initial runs
were deliberately short and traffic-bounded because they used production
public servers. They are sufficient to identify hypotheses and instrumentation
gaps, but not to rank congestion controllers statistically.

## 1. Background

`wg-quic` was introduced as the managed underlay for a small full-mesh
backbone. The application control plane assigns one stable tunnel address to
each node and uses the resulting interface for application-level adjacency and
routing checks.

The tested nodes were:

| Alias | Region | Tunnel alias |
| --- | --- | --- |
| `tokyo-1` | Tokyo | `10.254.0.4` |
| `hong-kong-1` | Hong Kong | `10.254.0.5` |
| `los-angeles-1` | Los Angeles | `10.254.0.1` |
| `tokyo-2` | Tokyo | `10.254.0.3` |

One additional backbone node was offline because its traffic quota had been
exhausted and was excluded from the experiment.

The common underlay configuration was:

```text
interface=mzwq0
listen_port=12580/udp
mtu=1280
carrier=quic
congestion=auto
fec=auto
obfs=salamander
persistent_keepalive=20s
```

In `v0.3.2`, `congestion=auto` maps directly to the experimental `model`
controller. All peers on an interface share the interface policy, even though
each public direction can have substantially different RTT, loss, and
capacity.

The initial operational symptom was that application proxy traffic was usable
but some paths appeared much slower than expected. The first question was
whether the bottleneck was the public route, host capacity, or the wg-quic
stack.

## 2. Questions

The field experiment attempted to answer:

1. Is the public path itself slow or asymmetric before wg-quic is involved?
2. Does wg-quic add a material completion-time penalty on a clean path?
3. Is poor tunnel throughput explained by local queue drops or CPU pressure?
4. Do observed QUIC and FEC counters agree with raw public UDP loss?
5. What evidence is missing before changing the congestion controller?

## 3. Initial Method

The experiment used fixed-size, authenticated one-shot listeners rather than a
persistent benchmark daemon. A receiver accepted data only from the expected
source address and only after a one-time token. It accepted bytes and did not
expose command execution. Random high ports were closed when each trial ended.

The measurements were:

- five ICMP echo requests over `mzwq0` for an initial RTT and loss sample;
- one fixed 4 MiB TCP transfer in each direction over the wg-quic interface;
- one fixed 4 MiB TCP transfer in each direction over the public underlay;
- selected bare public UDP sequence tests using 1,200-byte datagrams, about
  2 MiB per direction, and an offered rate near 10 Mbit/s;
- wg-quic status snapshots and host CPU/memory snapshots after the tests.

Approximately 112 MiB of payload was used in total. One tunnel TCP direction
was repeated after the orchestration process lost its result. An attempted
inner UDP test was stopped after its completion acknowledgement timed out. No
firewall policy was persistently changed and all temporary listeners exited.

The 4 MiB TCP runs measure short-transfer completion time. They include startup
and controller ramp behavior and must not be presented as a sustained
bandwidth ceiling.

## 4. Results

### 4.1 Tunnel RTT

Five probes were sent per pair. No ICMP loss was observed in this small sample.

| Source | Destination | Min / average / max RTT |
| --- | --- | ---: |
| `tokyo-1` | `hong-kong-1` | 50.911 / 74.131 / 155.950 ms |
| `tokyo-1` | `los-angeles-1` | 102.021 / 102.688 / 103.660 ms |
| `tokyo-1` | `tokyo-2` | 1.912 / 2.277 / 2.711 ms |
| `hong-kong-1` | `los-angeles-1` | 191.938 / 193.919 / 195.366 ms |
| `hong-kong-1` | `tokyo-2` | 87.061 / 89.854 / 92.225 ms |
| `los-angeles-1` | `tokyo-2` | 109.969 / 188.505 / 497.680 ms |

The Tokyo-to-Hong-Kong and Los-Angeles-to-Tokyo-2 samples show material
jitter. ICMP success is not evidence that UDP/12580 is lossless; the UDP
sequence test below is the relevant initial loss evidence.

### 4.2 Fixed 4 MiB TCP transfer

| Direction | Bare public TCP | Through wg-quic | Bare / tunnel ratio |
| --- | ---: | ---: | ---: |
| `tokyo-1` -> `hong-kong-1` | 35.81 Mbit/s | 3.26 Mbit/s | 11.00 |
| `hong-kong-1` -> `tokyo-1` | 23.95 Mbit/s | 0.67 Mbit/s | 35.75 |
| `tokyo-1` -> `los-angeles-1` | 19.85 Mbit/s | 5.08 Mbit/s | 3.91 |
| `los-angeles-1` -> `tokyo-1` | 1.77 Mbit/s | 0.95 Mbit/s | 1.86 |
| `tokyo-1` -> `tokyo-2` | 364.13 Mbit/s | 66.02 Mbit/s | 5.52 |
| `tokyo-2` -> `tokyo-1` | 343.13 Mbit/s | 40.18 Mbit/s | 8.54 |
| `hong-kong-1` -> `los-angeles-1` | 11.98 Mbit/s | 1.61 Mbit/s | 7.44 |
| `los-angeles-1` -> `hong-kong-1` | 0.97 Mbit/s | 0.69 Mbit/s | 1.41 |
| `hong-kong-1` -> `tokyo-2` | 30.50 Mbit/s | 1.66 Mbit/s | 18.37 |
| `tokyo-2` -> `hong-kong-1` | 19.76 Mbit/s | 6.77 Mbit/s | 2.92 |
| `los-angeles-1` -> `tokyo-2` | 1.21 Mbit/s | 0.91 Mbit/s | 1.33 |
| `tokyo-2` -> `los-angeles-1` | 23.62 Mbit/s | 5.00 Mbit/s | 4.72 |

The public TCP baseline already shows severe directionality. Every tested
Los Angeles outbound direction completed near 1 Mbit/s while reverse
directions were materially faster. This cannot be repaired solely inside a
tunnel controller.

The Tokyo pair is the clean control candidate: low RTT, no observed raw UDP
loss in the initial sample, and a public TCP baseline above 340 Mbit/s. The
short wg-quic transfer was still five to eight times slower. That result does
not yet prove a sustained implementation ceiling, but it does show a
significant short-flow/controller cost worth isolating.

### 4.3 Bare public UDP sequence test

The sender included a monotonically increasing sequence number in each
1,200-byte datagram. The initial runner retained only aggregate results; raw
per-packet arrival timestamps were not preserved.

| Direction | Sent | Received | Loss |
| --- | ---: | ---: | ---: |
| `tokyo-1` -> `hong-kong-1` | 1,748 | 1,724 | 1.373% |
| `hong-kong-1` -> `tokyo-1` | 1,748 | 1,690 | 3.318% |
| `tokyo-1` -> `los-angeles-1` | 1,748 | 1,744 | 0.229% |
| `los-angeles-1` -> `tokyo-1` | 1,748 | 1,748 | 0.000% |
| `tokyo-1` -> `tokyo-2` | 1,748 | 1,748 | 0.000% |

This is the strongest current evidence of real path loss on the Hong Kong
directions. It is still one short observation at one offered rate. The next
run must retain sequence-level evidence and repeat each condition.

The zero-loss 10 Mbit/s UDP result from Los Angeles to Tokyo does not
contradict its poor public TCP completion time. It may indicate direction- or
protocol-specific shaping, different transient conditions, or a TCP-specific
short-flow problem. A controlled rate staircase is required.

### 4.4 Runtime snapshots

All observed wg-quic send queue drop counters were zero. Post-run memory use
was approximately 21--40 MiB. Three nodes had post-run CPU snapshots below 1%.
Los Angeles showed one approximately 31% sample immediately after testing,
which may have been transient and is not sufficient evidence of a CPU
bottleneck.

Selected aggregate runtime values included:

| Node | QUIC/FEC observation |
| --- | --- |
| `tokyo-1` | 431 QUIC packets reported lost; cwnd about 207 KiB; BWE about 190 Mbit/s |
| `hong-kong-1` | cwnd about 64 KiB; BWE about 4.45 Mbit/s; RTT about 191 ms |
| `los-angeles-1` | FEC raw lost 49, recovered 7, unrecovered 42; BWE about 4.96 Mbit/s |
| `tokyo-2` | one QUIC packet reported lost; cwnd about 331 KiB; BWE about 203 Mbit/s |

These values are supporting observations only. Section 6 explains why the
current status model cannot attribute them strictly to one peer.

## 5. Initial Interpretation

The first field run supports a mixed diagnosis.

### 5.1 Public path faults are real

- Hong Kong directions showed 1.37--3.32% raw UDP sequence loss.
- Los Angeles public TCP performance was strongly asymmetric in every tested
  direction.
- Some inter-region RTT samples had substantial jitter.

These conditions can force any congestion-controlled carrier to reduce
goodput. Routing policy should avoid using poor directed links for transit,
even if the tunnel session remains administratively up.

### 5.2 The current stack or controller adds a separate penalty

- The Tokyo control pair had about 2.3 ms RTT, no observed UDP loss, and a
  343--364 Mbit/s bare TCP baseline, while the short tunnel transfers completed
  at 40--66 Mbit/s.
- Several inter-region directions had a bare/tunnel short-transfer ratio well
  above ten.
- `congestion=auto` is the experimental model controller, not an automatic
  comparison or negotiation among controllers.
- The model resets to a four-packet minimum congestion window after a
  retransmission timeout. That is a plausible cause of prolonged recovery on
  high-RTT or lossy paths, but this run did not record the required transition
  events to prove it.

### 5.3 What is not yet established

The field run does not establish:

- the sustained throughput ceiling of `model`, CUBIC, or Reno;
- whether the Tokyo ratio is dominated by short-flow ramp, userspace packet
  processing, QUIC pacing, or another implementation detail;
- where each Hong Kong UDP packet was lost;
- whether FEC repair traffic reduced useful-delivery capacity under the model
  controller;
- whether port-specific or protocol-specific shaping affected UDP/12580;
- statistical distributions across repeated trials.

No production default should be changed solely from the initial samples.

## 6. Attribution Problem in the Tested v0.3.2

The tested `v0.3.2` `telemetry.Stats` object is interface-wide.
`Bind.addQUICStats`
iterates all active sessions, sums counters, congestion windows, in-flight
bytes, bandwidth estimates, and pacing rates, and takes maxima or minima for
several RTT and loss values.

`management.PeerStatus` exposes endpoint, handshake, transfer, reconnect, and
FEC policy fields, but not the QUIC congestion and FEC observations belonging
to that peer.

This means a full-mesh node cannot answer basic field questions such as:

- Which peer caused `quic_packets_lost` to increase?
- Which session reset its congestion window?
- Which direction has the reported queue delay?
- Does a peer's FEC residual loss agree with its own QUIC loss?
- Did traffic to one peer contaminate the metrics collected for another?

During the initial run the tested flow usually dominated the interval, so
aggregate deltas remain useful hints. They are not strict per-link evidence.
Attributable session telemetry was therefore the first prerequisite for
further field controller work. The later `session_telemetry_v1` status schema
addresses that active-session baseline without changing this historical
measurement record.

## 7. Required Per-Peer Telemetry

Add a versioned per-session observation to management status. It should use a
stable peer identity and include enough generation information to distinguish
a reconnected or replaced session.

Suggested identity fields:

```text
peer_public_key
session_id
session_generation
endpoint_generation
configured_endpoint
current_endpoint
session_role
established_at
sampled_at
```

Suggested cumulative counters and gauges:

```text
wire_tx_packets / wire_tx_bytes
wire_rx_packets / wire_rx_bytes
quic_packets_acked / quic_bytes_acked
quic_packets_lost / quic_bytes_lost
quic_spurious_loss_packets
quic_pto_count / quic_rto_count
quic_cwnd_bytes / quic_bytes_in_flight
quic_bandwidth_estimate_bps / quic_pacing_rate_bps
quic_min_rtt_us / latest_rtt_us / smoothed_rtt_us / rttvar_us
quic_path_rtt_us / quic_queue_delay_us
quic_congestion_model_state
send_queue_depth / send_queue_drops
priority_queue_depth / control_queue_depth
datagram_send_queue_depth
fec_data_tx / fec_parity_tx
fec_raw_lost / fec_recovered / fec_unrecovered
fec_current_parity_shards / fec_loss_estimate_ppm
fec_group_expired / fec_feedback_age_us
kernel_rx_queue_overflow
reconnect_attempts / reconnect_failures
```

Interface aggregates may remain for dashboards, but they should be computed
from or presented alongside per-peer values. Aggregating gauges such as RTT,
cwnd, and BWE must document whether the result is a sum, maximum, minimum, or
weighted value.

Linux receive-path instrumentation should expose `SO_RXQ_OVFL` or an
equivalent socket-drop counter. Without it, network loss and kernel receive
queue overflow cannot be separated reliably.

## 8. Required Controller Events

Point-in-time gauges are insufficient to diagnose a collapse that recovers
before the next sample. Add bounded event records for:

- controller state transition and reason;
- slow-start/startup exit and re-entry;
- congestion window reduction, old value, new value, and reason;
- PTO/RTO firing;
- persistent congestion declaration;
- loss event and later spurious-loss classification;
- path RTT baseline update;
- FEC parity/profile transition and reason;
- endpoint migration or session replacement;
- local send queue drop or kernel receive overflow.

Events need both monotonic elapsed time and wall-clock time. They must never
include private keys, preshared keys, application payloads, or unredacted
configuration files.

## 9. Bounded Local Trace Interface

Expose an authenticated local-management operation rather than a public debug
HTTP endpoint. A proposed operator interface is:

```bash
wg-quic-quick trace mzwq0 \
  --peer PUBLIC_KEY \
  --duration 30s \
  --interval 100ms \
  --max-bytes 16M \
  --output /var/lib/wg-quic/traces/TRIAL_ID
```

Required properties:

- available only through the existing privileged local management transport;
- exactly one bounded trace per interface by default;
- explicit peer selection;
- hard duration and output-size limits;
- atomic completion marker and useful partial output after interruption;
- automatic redaction and root-only files;
- no application DATAGRAM payload capture;
- trace collection must not restart the interface or peer session.

Suggested output bundle:

```text
manifest.json
peer-telemetry.csv
controller-events.ndjson
qlog.sqlog
runtime.json
cpu.pprof
host-before.json
host-after.json
COMPLETE
```

The qlog subset should retain packet numbers, acknowledgements, declared loss,
metrics updates, PTO, congestion state, and path events. DATAGRAM payload bytes
must be omitted. A Go CPU profile is preferable to relying only on external
`perf`, because release binaries are currently built with `-s -w`. Release
automation may additionally publish an unstripped symbol artifact associated
with the same version and source commit.

## 10. Host-Level Evidence

The field runner should capture deltas over the exact trial interval for:

- `ip -s link` on the public and tunnel interfaces;
- `tc -s qdisc`;
- UDP/IP counters from `nstat` or `/proc/net/snmp`;
- NIC driver statistics when available;
- socket queue state;
- process CPU, RSS, scheduler switches, faults, and system calls;
- kernel, architecture, container runtime, cgroup limits, and clock-sync state.

Header-only outer packet capture may use a 64- or 96-byte snap length and a hard
packet/file limit. It is useful for packet timing and size distributions but
cannot independently reveal encrypted QUIC packet numbers. The wg-quic qlog is
the authoritative QUIC loss-event record.

UDP `mtr` or traceroute should be retained as route context. Loss reported by
an intermediate router is not proof of forwarding loss because ICMP responses
may be rate limited. Only destination loss and end-to-end sequence evidence
should be used as packet-loss results.

## 11. Reproducible Public Field Matrix

The next run should select three representative bidirectional pairs:

1. `tokyo-1` <-> `tokyo-2`: clean low-latency control.
2. `hong-kong-1` <-> `tokyo-1`: lossy public path.
3. `los-angeles-1` <-> `tokyo-2`: strongly asymmetric path.

For each direction, first run a raw public UDP sequence staircase:

| Offered rate | Duration | Purpose |
| ---: | ---: | --- |
| 1 Mbit/s | 10 s | low-load random-loss baseline |
| 5 Mbit/s | 10 s | moderate-load behavior |
| 10 Mbit/s | 10 s | policer/capacity onset |

Each datagram should carry a trial ID, sequence number, sender monotonic
timestamp, and payload checksum. The receiver must retain received sequence,
arrival monotonic timestamp, duplicate count, reordering distance, and gap
events. One-way delay is valid only when clock synchronization is measured;
loss and reordering do not require synchronized wall clocks.

Repeat the staircase through the tunnel below its first observed local-drop
or saturation point. Record raw outer loss, QUIC loss, FEC recovery, and inner
residual loss in the same time window.

Then compare controllers on an isolated temporary interface and port, without
changing the production `mzwq0` interface:

```text
model + fec off
model + fec auto
cubic + fec off
cubic + fec auto
reno + fec off
```

Use fixed 16--32 MiB TCP transfers or a fixed 20--30 second interval. Preserve
the public baseline, controller trace, CPU profile, useful bytes, outer bytes,
and completion time. Run at least five repetitions before drawing controller
comparisons. Randomize or rotate test order so path drift does not always favor
one controller.

The initial six bidirectional UDP staircases can be kept near 120 MiB of
payload. A complete raw plus tunnel diagnostic run should target a documented
budget of 150--250 MiB unless the operator explicitly raises it.

## 12. Analysis Contract

A final trial report should be able to account for a packet through the
following chain:

```text
sequence sender
  -> local application and send queue
  -> host qdisc and NIC
  -> public UDP path
  -> receiving NIC and kernel socket queue
  -> QUIC acknowledgement/loss/PTO
  -> FEC raw/recovered/unrecovered result
  -> tunnel inner sequence receiver
  -> TCP completion and controller state
```

Interpretation rules:

- sender or qdisc drop: host-offered-load problem;
- receive queue overflow: receiver capacity or socket servicing problem;
- outer sequence gap with no host drop: public path loss evidence;
- QUIC loss without an outer sequence gap: inspect ACK loss, reordering,
  packet-number space, and spurious-loss classification;
- FEC recovered with no inner gap: repair was effective, but its wire cost must
  still be included;
- FEC unrecovered plus inner gap: residual tunnel loss;
- low cwnd/BWE after PTO with available raw capacity: controller recovery
  hypothesis;
- high CPU or batching/syscall hotspot on the clean control: data-path
  implementation hypothesis.

Every report must identify duration, payload, direction, MTU, carrier,
controller, FEC, obfuscation, source commit, host placement, and public link
condition. Median and dispersion across repetitions matter more than the best
run.

## 13. Research Hypotheses

The next instrumentation and experiments should test, not assume, these
hypotheses:

1. A four-packet post-timeout window causes prolonged recovery on high-RTT
   public paths.
2. Short transfers end before the model controller discovers available clean
   path capacity.
3. FEC parity and feedback consume enough of the shared pacing budget to lower
   useful goodput on selected loss patterns.
4. Aggregate telemetry hides a single bad session and misattributes its RTT or
   loss to other peers.
5. Userspace packet handling, pacing timers, or missing UDP batching limit the
   clean-path ceiling.
6. Some providers apply direction-, port-, flow-, or protocol-specific shaping
   that is not visible in a low-rate UDP probe.
7. CUBIC improves clean and moderately lossy field completion time, but may
   trade queue delay or loss resilience against the model controller.

No one hypothesis should be accepted from one short field sample.

## 14. Implementation Order

1. Add per-peer/session telemetry without changing wire protocol behavior.
2. Add PTO, cwnd-transition, FEC-transition, spurious-loss, and socket-overflow
   counters/events.
3. Add the bounded local trace operation and redacted artifact manifest.
4. Add field-runner support for sequence-level raw and inner UDP evidence.
5. Reproduce the three representative public link classes.
6. Run isolated `model`/CUBIC/Reno and FEC comparisons.
7. Change controller behavior only after traces identify a reproducible cause.

## 15. Acceptance Criteria for Diagnostics

- Two concurrently active peers expose independent counters and gauges.
- A transfer to one peer does not materially change another peer's counters.
- Per-peer counter sums reconcile with documented interface aggregates.
- A forced timeout produces a timestamped controller event containing old and
  new cwnd and the transition reason.
- Kernel receive overflow is distinguishable from public sequence loss.
- A 30-second trace terminates automatically and cannot exceed its byte limit.
- Interrupted traces remain parseable and are clearly marked incomplete.
- Trace output contains no private key, preshared key, token, or application
  payload.
- The benchmark artifact identifies the exact source version and configuration
  without exposing secrets.
- The same analysis script can compare clean, lossy, and asymmetric trials.

## 16. Immediate Operational Conclusion

The deployment is functional, and the first field run found no evidence of
local wg-quic send queue overflow. It also found genuine public-path loss and
directional impairment, plus a separate short-transfer penalty on the clean
Tokyo control path.

The correct next step is not a global production controller change. It is to
make telemetry attributable to one peer, preserve bounded traces, and rerun a
small controlled public matrix. That evidence can then drive controller,
FEC, batching, and routing changes without confusing path faults with software
faults.
