# Public Internet Field Performance and Diagnostics

## Status

- First field observation: 2026-08-23
- Tested release: `wg-quic v0.3.2`
- Tested deployment: four Ubuntu 22.04 backbone nodes on independent public
  networks
- Document purpose: preserve the first complex public-path observations,
  distinguish path faults from implementation limits, and define the telemetry
  required for reproducible controller research
- Follow-up release reviewed: `wg-quic v0.3.3`
- Instrumentation release: `wg-quic v0.3.4`
- Follow-up implementation status: `v0.3.4` supplies active and retained final
  session telemetry, bounded sequenced controller/session events, a
  peer-selecting generation-aware collector, and
  explicit cross-platform receive-overflow availability. This satisfies the
  minimum instrumentation gate for a controlled controller-recovery rerun.
  Bounded qlog/CPU trace capture, sequence-level field evidence, and a new
  public trial are still outstanding; the data still cannot justify changing
  the production congestion-controller default.
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

## 7. Per-Session Telemetry Status and Requirements

### 7.1 Delivered in v0.3.3

`v0.3.3` delivers the first portable attribution layer without changing the
wire protocol:

- versioned observations for every currently active QUIC session;
- process-local session ID and endpoint-local reconnect generation;
- inbound/outbound role, configured/current endpoint, establishment time, and
  common sample time;
- plural configured and WireGuard-authenticated peer associations;
- independent WireGuard, carrier, FEC, local-queue, QUIC, RTT, cwnd, BWE, and
  pacing values;
- cumulative PTO and spurious-loss counters plus RTT variation;
- a 256-session enumeration bound, configured-outbound priority, and an
  explicit omitted-session count;
- capability-based forwarding through the existing portable management
  protocol.

The affected Go unit suite and race-enabled bind/core/quick tests passed during
the 2026-08-25 review. This verifies the implemented status path but does not
replace a privileged container run or a new public field trial.

### 7.2 Target schema

The versioned per-session observation must continue to use a stable peer
identity and enough generation information to distinguish a reconnected or
replaced session.

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
quic_pto_count
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
reconnect_attempts / reconnect_failures
```

Interface aggregates may remain for dashboards, but they should be computed
from or presented alongside per-peer values. Aggregating gauges such as RTT,
cwnd, and BWE must document whether the result is a sum, maximum, minimum, or
weighted value.

Kernel receive overflow belongs to the interface/shared UDP socket rather than
one session. `v0.3.4` exposes a structured
`stats.receive_queue_overflow` object with `supported`, `source`, `platform`,
and cumulative `packets`. Linux uses `SO_RXQ_OVFL`; FreeBSD/OPNsense, Windows,
and unavailable receive paths explicitly report `supported=false` instead of
encoding absence as a zero-loss observation. Without an authoritative
supported counter, network loss and kernel receive queue overflow still cannot
be separated reliably.

### 7.3 Closed-session final snapshots delivered in v0.3.4

`v0.3.3` enumerates only active sessions. `v0.3.4` closes that polling gap with
`recent_session_telemetry_v1`.

The management schema exposes retained records separately as
`recent_sessions`, so an old connection is never mistaken for a usable path.

Required final-state fields are:

```text
state=closed
final_sequence
closed_at
close_reason
error_class
last_error
final_stats
replaced_by_session_id
replaces_session_id
final=true
```

`close_reason` is a stable enum with:

```text
local_shutdown
remote_close
idle_timeout
handshake_timeout
transport_error
endpoint_replaced
configuration_removed
unknown
```

The implementation:

- takes the final connection/controller snapshot before references are removed;
- retains at most 64 final snapshots and expires each after five minutes;
- preserves the same supervisor-epoch, session-ID, and generation identity used
  while active;
- records a redacted error class/message, never configuration secrets;
- exposes cumulative capacity/TTL eviction in
  `recent_sessions_evicted_total` and a monotonic `final_sequence`;
- permits a collector to reconcile the final counter delta before starting the
  replacement generation;
- loses process-local history on a core restart rather than pretending it is
  durable; the supervisor epoch already marks that boundary.

## 8. Bounded Controller Events Delivered in v0.3.4

Point-in-time gauges are insufficient to diagnose a collapse that recovers
before the next sample. `v0.3.4` records:

- controller state transition and reason;
- slow-start/startup exit and re-entry;
- congestion window reduction, old value, new value, and reason;
- QUIC PTO firing (QUIC has PTO, not a TCP-style RTO event);
- loss event and later spurious-loss classification;
- path RTT baseline update;
- FEC parity/profile transition and reason;
- endpoint migration or session replacement;
- local send queue drop or kernel receive overflow.

Events need both monotonic elapsed time and wall-clock time. They must never
include private keys, preshared keys, application payloads, or unredacted
configuration files.

Each collected event record carries:

```text
event_sequence
event_stream_id
supervisor_epoch
session_id
session_generation
event_type
reason
before
after
monotonic_elapsed_ns
wall_time
```

The core event contains the stream/session fields; the portable quick response
and collector record attach `supervisor_epoch` to that process-local stream.

The stable cursor tuple is `(supervisor_epoch, event_stream_id,
event_sequence)`. Sequence is monotonic within one process-local event stream;
a core restart changes the stream ID even if the quick supervisor epoch is
unchanged. The interface ring retains at most 4,096 records, allows at most
1,024 records per query, and reports `first_available_sequence`,
`last_sequence`, and cumulative `events_dropped_total`. A skipped cursor is an
explicit data gap. Event responses also sample the core wall and monotonic
clocks so collector rows can be placed on the same monotonic event timeline.

Typed `before` and `after` snapshots carry cwnd, in-flight bytes, BWE, pacing,
RTT, path RTT, queue delay, model state, PTO/loss/spurious counters, queue
drops, FEC policy, endpoint, and receive overflow as applicable. Production
does not emit one record per ACK. High-volume packet/qlog events and a distinct
persistent-congestion declaration remain part of the future bounded trace
work; the implementation must not fabricate a persistent-congestion event
when the current recovery code made no such declaration.

For the four-packet post-timeout hypothesis, a forced timeout now produces a
PTO record with the typed controller snapshot. Any actual subsequent cwnd or
controller transition, session close, replacement, or recovery is correlated
by the same stream/session identity rather than inferred from log text.

## 9. Bounded Local Trace Interface

This section remains a design requirement. The implemented `collect` command
captures bounded status, derived telemetry, and controller events, but it does
not enable qlog or CPU profiling and must not be described as the trace API
below.

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

The field collector must select a session by authenticated/configured peer key,
not by array position or the interface aggregate. It must:

- record supervisor epoch, session ID, and generation on every row;
- detect a generation change and close the previous series using its retained
  final snapshot;
- refuse an ambiguous association unless the operator explicitly selects one
  session;
- preserve the raw status sample alongside derived CSV rows;
- compute counter deltas only within one epoch/session/generation tuple;
- align trial lifecycle and controller events on monotonic elapsed time;
- report a missing or omitted target session as a failed sample, never as zero
  loss or zero traffic.

`v0.3.4` implements that contract as a bounded portable command:

```bash
wg-quic-quick collect mzwq0 \
  --peer PUBLIC_KEY \
  --duration 30s \
  --interval 100ms \
  --max-bytes 16M \
  --output /var/lib/wg-quic/traces/TRIAL_ID
```

It emits `manifest.json`, `status.ndjson`, `peer-telemetry.csv`,
`controller-events.ndjson`, and `summary.json`. The directory is newly created
with root-only Unix permissions or a protected Windows Administrators/
LocalSystem DACL. `COMPLETE` appears atomically only after success; handled
failures write `INCOMPLETE`, and abrupt termination is detectable by the
absence of `COMPLETE`. Output is hard-limited to 16 MiB by default and accepts
an explicit bound from 64 KiB through 256 MiB. Duration is capped at ten
minutes and the polling interval at 10 ms through one minute.

Selection prefers exactly one WireGuard-authenticated association, falls back
to exactly one configured association, and refuses ambiguity. `--session-id`
can pin an exact active session, but intentionally does not follow its
replacement. Without that pin, a replacement is followed only after the old
tuple's retained final delta is written. Raw status and failed samples are
kept; counter regressions, status omission, event cursor gaps, epoch/stream
changes, and missing final snapshots make the run incomplete rather than
inventing zero values. Sessionless receive-overflow events remain in the
bundle because the UDP socket is interface-scoped.

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

1. **Delivered in v0.3.3:** add active per-peer/session telemetry, cumulative
   PTO/spurious-loss counters, schema versioning, and bounded enumeration.
2. **Delivered in v0.3.4:** retain bounded closed-session final snapshots
   and close reasons.
3. **Delivered in v0.3.4:** add PTO, cwnd-transition, controller-state,
   FEC-transition, and session lifecycle events with a bounded sequence-aware
   ring.
4. **Delivered in v0.3.4:** add a field collector that selects a
   peer/session and follows generation changes without using interface
   aggregates.
5. **Delivered in v0.3.4:** add Linux `SO_RXQ_OVFL` attribution and report
   support/source explicitly on every platform.
6. Add the bounded local trace operation, redacted artifact manifest, qlog
   subset, and on-demand CPU profile.
7. Add field-runner support for sequence-level raw and inner UDP evidence.
8. Reproduce the three representative public link classes.
9. Run isolated `model`/CUBIC/Reno and FEC comparisons.
10. Change controller behavior only after traces identify a reproducible cause.

## 15. Acceptance Criteria for Diagnostics

- Two concurrently active peers on distinct sessions expose independent
  counters and gauges. Peers intentionally sharing one outer session share
  that session's transport metrics and must not be double-counted.
- A transfer on one session does not materially change another session's
  counters.
- Active plus retained-final session deltas reconcile with the documented
  interval-scoped interface counters without summing the same shared session
  once per associated peer. Lifetime interface counters are not compared to an
  active-only snapshot.
- A reconnect retains the old session's final counters, close reason, and
  closure timestamp while exposing the replacement under a new generation.
- A collector polling more slowly than the reconnect still consumes the final
  old-session delta instead of silently losing it.
- A forced timeout produces a timestamped controller event containing old and
  new cwnd and the transition reason.
- Controller events have monotonic sequence numbers, bounded retention, and an
  explicit dropped/omitted count.
- Kernel receive overflow is distinguishable from public sequence loss.
- The benchmark collector follows exactly one selected peer/session and never
  computes a delta across an epoch or generation boundary.
- A 30-second trace terminates automatically and cannot exceed its byte limit.
- Interrupted traces remain parseable and are clearly marked incomplete.
- Trace output contains no private key, preshared key, token, or application
  payload.
- The benchmark artifact identifies the exact source version and configuration
  without exposing secrets.
- The same analysis script can compare clean, lossy, and asymmetric trials.

## 16. v0.3.4 Field-Run Readiness Gate

| Activity | v0.3.4 readiness | Reason |
| --- | --- | --- |
| Inspect one active peer independently | Ready | Active per-session counters and gauges are attributable |
| Controlled controller-recovery rerun | Ready for execution, not yet field-validated | The bounded collector follows one peer/session, consumes final snapshots, and retains controller events |
| Compare steady active-session model/CUBIC/Reno behavior | Ready with caveats | Session-safe CSV/event evidence exists; qlog and CPU profiling are still absent |
| Diagnose reconnect or failure collapse | Ready for a controlled rerun | Final state, close reason, replacement links, and lifecycle/controller events are retained |
| Prove public loss instead of kernel socket overflow | Linux ready; other platforms explicit-unavailable | Linux reports `SO_RXQ_OVFL`; unsupported platforms cannot make this attribution |
| Diagnose clean-path CPU/data-path ceiling | Not ready | Bounded pprof/qlog trace is absent |
| Change the production default controller | Not ready | Root-cause and repeated field evidence are incomplete |

The minimum gate before the next evidence-bearing public research run is
implemented in `v0.3.4`:

1. closed-session final snapshot retention;
2. PTO/cwnd/controller transition events; and
3. a peer-selecting, generation-aware collector.

Linux `SO_RXQ_OVFL` attribution was included in the same implementation batch,
with explicit unavailable semantics elsewhere. Bounded qlog and CPU profiling
may follow if the immediate experiment is limited to controller recovery
rather than clean-path implementation profiling. This readiness statement is
an implementation/test result, not a claim that a new public field matrix has
already run.

## 17. Immediate Operational Conclusion

The deployment is functional, and the first field run found no evidence of
local wg-quic send queue overflow. It also found genuine public-path loss and
directional impairment, plus a separate short-transfer penalty on the clean
Tokyo control path.

The correct next step is not a global production controller change. `v0.3.4`
retains attributable active/final telemetry and controller transitions, while
the bounded collector follows peer generation explicitly. The next action is
to run the small controlled public matrix and use those artifacts to drive
controller, FEC, batching, and routing changes without confusing path faults
with software faults.
