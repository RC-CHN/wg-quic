# Degraded-link design goals

Status: implemented experimental baseline and remaining design direction,
August 2026.

## Mission

wg-quic is not intended to be a faster implementation of ordinary WireGuard.
Its reason to exist is to keep encrypted WireGuard traffic usable after a
direct UDP tunnel has become unstable, heavily rate-limited, or effectively
unusable.

The primary product goal is therefore:

> Preserve useful application delivery, connection continuity, and bounded
> stalls on degraded paths, even when doing so costs peak throughput on a clean
> path.

The comparison baseline is the pinned userspace `wireguard-go` implementation,
using the same hosts, MTU, workload, outer path, and measurement interval.
Claims should describe a measured operating envelope, not imply that wg-quic
wins on every network called "Wi-Fi", "cellular", or "satellite".

## Non-goals and guardrails

- Winning clean-LAN throughput is not a primary goal.
- FEC is not required to be enabled continuously. A healthy path should pay
  little or no parity cost after the controller converges.
- Random loss must not simply be ignored. The sender must remain paced, respond
  to real congestion, include parity in its wire-rate budget, and avoid
  persistent queue growth or starvation of competing traffic.
- The tunnel must not turn unordered WireGuard datagrams into an
  ordered/reliable byte stream with head-of-line blocking.
- Obfuscation is allowed to cost some throughput, but its cost must be measured
  independently from FEC and congestion-control behavior.

## What success means

Peak bit rate is a secondary diagnostic. The primary measurements are:

1. **Usability boundary:** the loss, burst, RTT, and rate-change combinations
   under which the tunnel still delivers useful traffic.
2. **Goodput floor:** delivered application bits per second during the worst
   intervals, not only the whole-run average.
3. **Tail stall:** the longest zero-delivery interval and P95/P99 request or
   packet latency.
4. **Recovery time:** time to return to useful delivery after a blackout, NAT
   rebinding, or a large bandwidth change.
5. **Residual loss:** loss visible above the tunnel after FEC, separated from
   raw outer loss and local queue drops.
6. **Wire cost:** total outer bytes, including parity and control traffic, per
   useful application byte.
7. **Safety:** queue delay, local drops, and fairness against a conventional
   flow sharing the bottleneck.

The following are initial engineering targets, not release claims. They should
be recalibrated after repeated 30–60 second measurements are available.

| Condition | Initial target |
| --- | --- |
| Clean 1 Gbit/s LAN versus direct `wireguard-go` | At least 50% median goodput |
| Clean path, adaptive FEC | At least 80% of the matching no-FEC wg-quic mode after convergence |
| 0–0.5% random loss | No more than 10% regression from no-FEC wg-quic |
| 2–5% random or correlated loss, 40–100 ms RTT | At least 1.5x direct `wireguard-go` median goodput, with lower P99 stall |
| 0.1–0.5% random loss, 300–600 ms RTT | At least 1.5x direct `wireguard-go` median short-flow goodput and fewer zero-delivery intervals |
| 5–10% loss or periodic short bursts | Maintain useful interactive delivery and cut residual loss by at least 50% |
| 200–1000 ms path blackout | Resume useful delivery within `max(3 * RTT, 2s)` after the path returns |
| Abrupt bandwidth reduction | Converge without sustained local queue drops or growing standing delay |
| UDP discrimination or throttling | Remain usable in at least one documented condition where direct WireGuard is not |

A major milestone is the **crossover point**: the least severe impairment at
which wg-quic has higher median goodput and lower tail stall than direct
WireGuard. Work should move this point toward milder impairment without
creating unsafe bottleneck behavior.

## Implemented baseline

The current data path:

1. accepts encrypted WireGuard datagrams from the pinned `wireguard-go` device;
2. waits for QUIC path-MTU discovery and fragments each datagram against the
   current maximum DATAGRAM payload, reserving room for FEC framing;
3. optionally groups frames into systematic Reed-Solomon FEC blocks;
4. sends data, parity, close, and feedback messages as QUIC DATAGRAM frames;
5. optionally applies Salamander-style packet obfuscation to the outer UDP
   socket.

Current FEC defaults to eight data shards, starts with one parity shard, flushes
a partial group after 2 ms, and adjusts between zero and four parity shards
from receiver feedback and QUIC packet-loss counters. The target is the
smallest parity count whose independent-loss group-failure probability is at
most 0.5%, with fast increase and slow decrease. The loss threshold for
entering the raw fast path scales from 0.1% at 100 ms down to a floor of 0.01%
on long-RTT paths, because one unrecovered shard is much more expensive there.
The evidence window for disabling the last parity shard scales with RTT as
well, up to eight times the normal 32-group window. Surplus parity above that
minimum still drains on the normal window after a burst. This avoids claiming
a high-cost path is healthy from too small a sample without retaining an
overly expensive protection level. After a sufficiently healthy interval it
uses a raw no-FEC fast path and sends one protected probe per 4096 frames.
Reed-Solomon codecs are cached by `(k, r)`.

`congestion=auto` now selects an experimental delivery-model controller. It
samples acknowledged outer bytes, estimates delivery capacity, maintains a
path-local propagation RTT plus queue delay, and owns the pacing and in-flight
budget for every QUIC packet. Data, parity, feedback, and QUIC control traffic
therefore share one congestion-controlled wire budget. Random loss without
standing queue or ECN does not trigger Reno-style multiplicative collapse;
loss with persistent queue growth does reduce the model. Reno and CUBIC remain
available as benchmark/debug selections. Startup bandwidth-plateau rounds are
counted only while at least half of the congestion window is in flight, so
application-limited tunnel setup and keepalives cannot make a high-RTT
connection exit Startup before the bulk workload begins. An application-limited
delivery sample is still allowed to raise, but never lower, the bandwidth
estimate when it exceeds the current model; a flight-over-path-RTT bound limits
ACK-compression artifacts.

The path RTT baseline is dynamic. A sustained latency increase is accepted once
the sender has drained to a small window, while an implausibly large decrease
must persist for 50 ms. This prevents a route or qdisc transition from
poisoning the controller with one transient sample, and lets a
fiber-to-cellular-to-fiber schedule re-characterize the active path in both
directions.

Salamander preserves UDP batching: outgoing GSO segments are obfuscated
independently and their segment size is rewritten; incoming `ReadBatch`
datagrams are decoded before QUIC sees them. This removes the former fixed
syscall ceiling while retaining per-datagram salt and hints.

Status output now exposes FEC parity and loss estimates; QUIC acknowledged and
lost bytes/packets; minimum, latest, smoothed, and controller path RTT; window,
in-flight, bandwidth, pacing, queue delay, FEC-classified loss, and model
state. The fixture samples these at 0.5-second intervals into
`controller.csv`.

## August 2026 acceptance evidence

These are short engineering acceptance runs on one host, not release-wide
performance claims. Every compared row used the same containers, MTU 1280,
direction, workload, qdisc, and interval. Random-loss and clean-LAN values are
three-run medians.

| Condition | direct `wireguard-go` | FEC plain | FEC obfs | Result |
| --- | ---: | ---: | ---: | --- |
| 1 Gbit/s LAN, ~0.4 ms RTT, TCP | 906.52 | — | 644.15 | obfs is 71.1% of direct |
| 100 Mbit/s, ~40 ms RTT, 5% loss each direction, TCP | 1.57 | 9.61 | 11.28 | 6.1x / 7.2x direct |
| Unshaped container ceiling, ~30 µs RTT, TCP | 6482.37 | 458.56 | 384.11 | known peak-throughput gap |

Values are Mbit/s. The 1 Gbit/s clean-link target is met, and the intended
degraded-link crossover is demonstrated at 5% symmetric random loss. The
unshaped result is deliberately retained: on a multi-gigabit, near-zero-RTT
virtual path, direct WireGuard's GSO ceiling is far higher, so wg-quic does not
meet a blanket “half of direct at every clean-path speed” claim.

In a 28-second `fiber -> cellular -> fiber` run, the controller path RTT moved
from about 8.7 ms to 55.7 ms and back to 8.9 ms. FEC moved from zero to one or
two parity shards and returned to zero. On the cellular section FEC-obfs
maintained roughly 2–7 Mbit/s after the transition, while direct WireGuard had
several whole one-second zero-delivery intervals. Direct retained the larger
whole-run average because it entered the impaired section with a much larger
existing window; tail stall, not that aggregate, is the reason for keeping
both interval traces.

A targeted high-RTT check used the fixture's 25/5 Mbit/s, approximately 600 ms
RTT, 0.2% independent loss profile and five fresh 10-second TCP runs per mode.
The final FEC-obfs median was 1.18 Mbit/s versus 1.09 Mbit/s for direct
`wireguard-go`, only 1.09x. Its P10 goodput was 0.926 versus 0.553 Mbit/s, or
1.67x, and it recovered all 18 observed missing shards. Both modes had a median
of two whole-second zero-delivery intervals. The controller has improved the
bad-run floor but this condition does **not** yet meet the proposed 1.5x median
and lower-stall target. The short duration also makes this evidence about
startup resilience, not capacity; the next gate remains 30–60 second runs with
at least five repetitions.

The owned-DATAGRAM handoff was checked separately before treating it as a
throughput optimization. Its queue-only microbenchmark moved from roughly
680 ns, 1312 B, and two allocations per 1200-byte DATAGRAM to 82 ns, 32 B, and
one allocation. Three matched 8-second LAN trials moved from a 708.2 Mbit/s
median to 725.9 Mbit/s, but their ranges overlapped; this proves the removed
copy, not a statistically stable end-to-end throughput gain. The new runtime
telemetry found about 590 MiB/s of remaining allocation churn at a 725 Mbit/s
LAN goodput, with the ArmorBind queue reaching 774/1024 and the quic-go
DATAGRAM queue reaching 32/32. Those observations make framing/packet-builder
allocation and send-loop backpressure the next clean-path profiling targets.

The next copy-reduction pass combined the required WireGuard queue-lifetime
copy with the common one-fragment carrier frame: the destination allocation now
reserves and fills the 21-byte framing header instead of copying the payload
again in the session send loop. On receive, quic-go's parser-owned DATAGRAM
payload transfers directly into its receive queue instead of being cloned a
second time. The framing microbenchmark moved from about 1.33 us, 2840 B, and
three allocations to 0.63 us, 1408 B, and one allocation. In three matched
8-second LAN trials against `c1ed524`, median goodput moved from 753.8 to
805.7 Mbit/s, combined allocation rate from 608 to 410 MiB/s, GC cycles from
273 to 178, and GC pause CPU from 5.55 to 3.86 seconds. The send queues still
reached their limits, so the result is attributable to less copying rather
than hidden queue growth. A cellular FEC-obfs regression delivered 9.30 Mbit/s,
recovered 114 of 115 missing shards, and had no local queue drops.

One post-change 10-second-per-cell diagnostic matrix produced the following
directional sample:

| Mode | LAN | Fiber | Wi-Fi | Cellular | Satellite |
| --- | ---: | ---: | ---: | ---: | ---: |
| direct `wireguard-go` | 906.35 | 277.43 | 9.21 | 2.29 | 1.38 |
| no-FEC, plain | 725.31 | 125.69 | 4.29 | 1.25 | 1.88 |
| no-FEC, obfuscated | 705.54 | 194.49 | 4.81 | 1.35 | 0.99 |
| FEC, plain | 746.16 | 114.37 | 12.23 | 5.29 | 1.09 |
| FEC, obfuscated | 687.88 | 127.35 | 5.90 | 10.56 | 1.58 |

Values are Mbit/s. All 25 trials completed. ArmorBind queue drops were zero
except for LAN no-FEC obfuscated (4), Fiber no-FEC obfuscated (1), and LAN FEC
obfuscated (6). FEC improved the single Wi-Fi and cellular samples, but mode
rankings varied substantially from earlier short runs. The three-second outer
TCP calibration also underestimated several high-RTT trials. This matrix
validates instrumentation and identifies candidate conditions; it is not new
acceptance evidence.

The IPv4 protocol-policy fixture also establishes three synthetic DPI/QoS
controls:

| Policy | direct/plain result | disguised result |
| --- | --- | --- |
| Drop WireGuard message types 1–4 | direct tunnel unavailable | plain wg-quic about 663 Mbit/s |
| Police WireGuard signatures to 1 Mbit/s | direct 0.92 Mbit/s | QUIC mode about 725 Mbit/s |
| Drop QUIC v1/v2 long-header handshakes | plain wg-quic unavailable; 40 matched drops | obfs wg-quic 373.91 Mbit/s; zero matches |

These filters demonstrate resistance to protocol discrimination, not to a
blanket UDP ban. The current carrier is UDP-only; if all UDP is dropped or
severely throttled, a TCP/TLS or other non-UDP fallback is still required.

The original 5-by-5 sample at commit `b81b7fc` is retained as historical
motivation. It used one 10-second run per cell and was later found to combine a
fixed-fragment/FEC cost with an offload setup that could hide configured
per-packet loss. It must not be used as current performance evidence:

| Mode | LAN | Fiber | Wi-Fi | Cellular | Satellite |
| --- | ---: | ---: | ---: | ---: | ---: |
| direct `wireguard-go` | 912.77 | 452.72 | 35.03 | 2.71 | 1.48 |
| no-FEC, plain | 493.22 | 64.51 | 3.45 | 0.73 | 0.50 |
| no-FEC, obfuscated | 410.14 | 71.73 | 3.46 | 1.14 | 0.26 |
| FEC, plain | 68.30 | 54.04 | 4.23 | 0.92 | 0.30 |
| FEC, obfuscated | 67.89 | 60.53 | 3.50 | 0.90 | 0.37 |

Values are Mbit/s. The gap in that table motivated dynamic PMTU fragmentation,
healthy-path FEC bypass, codec caching, Salamander GSO/ReadBatch support,
offload-calibrated netem, and the model controller described above.

## Dynamic control architecture

The production policy should be dynamic by default, but it should not consist
of several independent adaptive algorithms. One per-path model should feed
three coordinated controllers:

```text
QUIC ACK/loss/ECN ─┐
FEC feedback ──────┼─> Path model ──> congestion controller ──> total wire budget
queue/application ─┘       │                    │
                           ├─> protection controller ──> k, r, flush, interleave
                           └─> scheduler ──────────────> priority, deadlines, drops
```

The path model owns a coherent view of:

- `max_delivery_rate`: a filtered estimate from acknowledged **outer** bytes;
- `min_rtt`, current RTT, and estimated queue delay;
- raw outer loss, ECN, reordering, and recent loss-burst distribution;
- post-FEC residual loss, useful/late/useless repair ratios, and feedback age;
- application delivery rate, offered rate, and local queue age;
- confidence and a path generation that changes after migration or blackout.

There must be two distinct rate measurements. Outer acknowledged bytes,
including useful and useless parity, estimate bottleneck capacity. Delivered
WireGuard/application bytes measure product utility. Counting only useful data
in the capacity estimate would incorrectly make parity appear free.

The congestion controller owns one total wire pacing rate and one total
in-flight limit. The protection controller divides that budget:

```text
source_budget + repair_budget + control_budget <= total_wire_budget
```

If the repair fraction rises on a full bottleneck, the source rate must fall.
Repair traffic must never be appended above the congestion controller's rate.
This joint budget follows the architecture recommended for FEC within a
transport by [RFC 9265](https://www.rfc-editor.org/info/rfc9265/).

### Different loops need different time scales

"Dynamic" does not mean every output changes after every packet:

| Loop | Typical cadence | Responsibility |
| --- | --- | --- |
| Measurement | each ACK/event | Update delivery samples, RTT, ECN, loss, queue, and FEC observations |
| Pacing | packet/send opportunity | Enforce the current total wire budget |
| Congestion | about once per round | Update bandwidth/in-flight model and probing phase |
| Protection | 2–8 rounds | Change FEC dimensions only with enough feedback and hysteresis |
| Path lifecycle | event plus seconds | Reset confidence, refresh minimum RTT, handle blackout/migration |

Immediate safety events are exceptions: sustained ECN, rapid RTT inflation,
queue deadline violations, or path failure can reduce rate/protection state
without waiting for a normal control interval.

### FEC-aware BBR direction

The leading candidate is not "BBR plus more packets". It is a BBR-style path
model with FEC-aware accounting and loss classification:

1. Estimate bottleneck bandwidth from acknowledged outer bytes and propagation
   delay from a windowed minimum RTT. Track application-limited samples so they
   do not lower the capacity estimate.
2. Pace all source, repair, and control datagrams under the same rate and
   in-flight limits.
3. Use FEC feedback to estimate the loss process and choose protection, not to
   inflate the delivery-rate sample.
4. Distinguish likely non-congestive loss from congestion using several
   signals. Raw loss with stable RTT, no ECN, and stable delivery rate can
   justify maintaining rate and adding bounded repair. Loss accompanied by
   RTT inflation, ECN, queue growth, or delivery collapse must reduce the total
   rate.
5. Keep a post-FEC utility model: if extra parity is mostly late or unused,
   reduce it even when raw loss remains high.
6. Fall back to conservative Reno-like behavior when model confidence is low,
   ACKs are too sparse, or a new path has not been characterized.

The implemented `model` controller is the first experimental version of this
direction, and `auto` currently selects it. It is intentionally described as
BBR-like, not as BBRv3: it has delivery-rate, propagation-RTT, pacing, and
in-flight models, but does not implement the complete BBRv3 state machine.
Shared-bottleneck fairness, ECN integration coverage, and long-duration
bandwidth-step tests remain promotion gates.
The IETF BBR work models a path from delivery rate, RTT, and loss and controls
both pacing rate and in-flight volume; it is a useful base, not a drop-in
answer for this tunnel. See
[draft-ietf-ccwg-bbr](https://datatracker.ietf.org/doc/draft-ietf-ccwg-bbr/)
and the generic
[delivery-rate estimation draft](https://datatracker.ietf.org/doc/html/draft-cheng-iccrg-delivery-rate-estimation).

The QUIC DATAGRAM specification requires datagrams to use the connection's
congestion controller and explicitly permits dropping expired datagrams
instead of waiting indefinitely. The proposed scheduler and deadline policy
should use that model rather than bypassing congestion control; see
[RFC 9221](https://www.rfc-editor.org/rfc/rfc9221.html).

### Dynamic policy surface

Normal users should choose intent, not transport constants:

- `fec=auto` controls off/probe/lossy/burst/recovery states;
- `congestion=auto` selects the validated controller and safe fallback;
- `peer.fec-latency=latency|balanced|throughput` maps to latency budget,
  maximum repair fraction, and controller aggressiveness;
- obfuscation remains an explicit compatibility/policy choice.

Fixed `k`, `r`, pacing rate, and controller modes should remain available only
as debug and benchmark overrides. Automatic decisions must be visible in
status output so an operator can tell which state is active and why.

## Implementation status and next changes

### 1. Make the controller observable before making it clever

**Baseline implemented.** Status and `controller.csv` cover the core RTT,
window, delivery-rate, pacing, FEC, and model-state fields. ECN-CE, queue age,
loss-run histograms, path generation, and explicit decision-reason events are
still missing.

Add interval or event telemetry for:

- smoothed, latest, and minimum RTT;
- congestion window, bytes in flight, pacing rate, and delivery-rate estimate;
- raw QUIC loss and ECN-CE events;
- FEC group dimensions, parity ratio, group completion delay, recovered and
  unrecovered shards, and late parity;
- loss-run or burst-length histogram;
- send, control, and receive queue occupancy, enqueue age, and drops;
- application-delivery rate and outer wire rate over the same interval;
- path generation and controller state transitions.

The benchmark should capture these values at one-second or finer intervals.
Every adaptive decision should be explainable from recorded inputs and state.

This is priority zero: without it, improvements can easily be caused by random
loss variation, an overloaded local queue, or an overly aggressive sender.

### 2. Replace the parity counter with a path-state controller

**Partially implemented.** Parity is now derived from a smoothed raw-loss
estimate with QUIC-loss wake-up, protected probes, and hysteresis. It is not
yet the richer burst/deadline/capacity state machine below.

Use a small state machine with hysteresis, for example:

- `healthy`: FEC off or probe-level parity;
- `lossy`: enough parity to cover the observed short loss distribution;
- `burst`: shorter groups or interleaving plus higher temporary parity;
- `capacity_limited`: reduce parity and data pacing together;
- `recovering`: conservative pacing and temporary protection after a blackout
  or path change.

Controller inputs should combine:

- raw and post-FEC loss over multiple time windows;
- recovered/unrecovered ratio and whether parity arrived in time;
- burst length rather than only average loss;
- RTT trend and queue delay;
- estimated delivery capacity and current wire-rate headroom;
- application traffic rate and latency class;
- feedback age and confidence.

The controller output should include data-shard count, parity-shard count,
flush deadline, interleaving depth, and a total data-plus-parity pacing budget.
Changes need bounds, hysteresis, and cooldown periods to prevent oscillation.

Feedback should be versioned and extended rather than changing the existing
wire record in place. Unknown feedback versions must fail safely.

### 3. Make FEC packet-aware and deadline-aware

**PMTU portion implemented.** The sender now fragments after QUIC establishes
the active DATAGRAM payload limit, so a normal MTU-1280 WireGuard message uses
one protected carrier datagram when the path permits. Datagram-boundary group
policy, unequal protection, interleaving, and repair deadlines remain.

FEC still protects transport fragments when a path cannot fit the encrypted
WireGuard datagram, so one unrecovered fragment still discards the complete
WireGuard datagram. Continue investigating:

- avoiding fragmentation when path MTU permits;
- grouping shards on WireGuard datagram boundaries;
- protecting small control, handshake, and keepalive packets separately;
- unequal protection for latency-sensitive packets and bulk data;
- dropping parity that can no longer arrive before the reassembly deadline;
- interleaving across short loss bursts without adding excessive latency.

The sender already prioritizes WireGuard handshakes and keepalives. That
priority should extend through FEC grouping, pacing, and queue admission rather
than ending at the local send channel.

### 4. Remove avoidable FEC implementation cost

**Two copy-reduction passes implemented.** Codec caching, the parity-zero raw
path, dynamic single-datagram framing, Salamander GSO, decoded `ReadBatch`, and
ownership transfer through both quic-go DATAGRAM queues are active. The
owned-buffer API removes quic-go's per-DATAGRAM send copy, while the receive
parser transfers its already-owned payload into the application queue. These
are in-process zero-copy handoffs, not kernel/NIC zero-copy. The initial
wireguard-go bind buffer still has to be copied because its lifetime ends when
`Send` returns, but that allocation now includes carrier framing headroom and
avoids another copy. Broader buffer reuse and parallel/batched Reed-Solomon
work remain profiling-driven follow-ups.

Profile before changing the code, then evaluate:

- caching Reed-Solomon codecs by `(k, r)`;
- reusing shard and packet buffers;
- avoiding the remaining framing, padding, and receive-side copies;
- batching encode/decode and QUIC DATAGRAM submission;
- keeping small groups on a low-overhead path;
- moving expensive work away from the single session send loop while
  preserving packet order where required.

The clean-path target is not to match direct WireGuard. It is to make adaptive
FEC cheap enough that enabling the feature does not force a permanently low
ceiling or create local queue loss before the network is stressed.

### 5. Add capacity estimation and FEC-aware pacing

**Experimental baseline implemented.** The model estimates acknowledged outer
delivery, exposes bandwidth/path-RTT/queue state, and paces all QUIC packets
under one budget. Application-limited classification, confidence/path
generation, explicit repair-budget allocation, and schedule/fairness
validation remain.

Estimate the bottleneck delivery rate from acknowledged bytes over time, with
minimum RTT and RTT inflation as separate signals. The estimate must:

- reset or reduce confidence after endpoint/path changes and blackouts;
- distinguish application-limited samples from capacity-limited samples;
- count parity and control bytes against the outer budget;
- expose its value and confidence in telemetry;
- operate in shadow mode before it controls sending.

The current integration already gives data and parity the same QUIC
congestion-controlled wire budget. A future explicit source/repair allocator
should make the division observable and enforce application latency policy.

### 6. Evaluate loss-tolerant congestion control behind an interface

**Interface and first model implemented.** `reno`, `cubic`, and `model` are
selectable, and `auto` selects `model`. The remaining work is comparative
long-duration safety testing, not wiring the selection boundary.

Reno treats most loss as congestion, which is a poor fit for recoverable radio
loss. Introduce a narrow congestion-control selection boundary in the pinned
QUIC fork, then compare:

- the existing Reno behavior;
- CUBIC as another loss-based reference;
- a delivery-rate/model-based controller in the BBR family;
- a hybrid that uses delivery rate and minimum RTT while treating ECN and
  persistent queue delay as strong congestion signals.

FEC recovery must not directly suppress congestion response without another
credible congestion signal. Any loss-tolerant mode needs shared-bottleneck
fairness and standing-queue tests before it can become `auto`.

Do not call the current model BBRv3 or freeze it as the production policy until
its estimate survives longer bandwidth steps, reverse-path loss,
application-limited traffic, ECN, and competing-flow tests.

### 7. Bound queues and discard stale work

On a collapsing link, a large FIFO turns loss into seconds of latency. Add:

- queue occupancy and age limits derived from RTT or an explicit latency
  budget;
- early rejection or replacement of stale bulk datagrams;
- reserved capacity for FEC feedback, WireGuard handshakes, and keepalives;
- controller backpressure before the fixed queue reaches overflow;
- separate counters for deadline drops, admission drops, and network loss.

This work is necessary for the tail-stall goal even when average throughput
does not change.

### 8. Keep obfuscation orthogonal

Salamander now rewrites outgoing GSO metadata and supplies a decoded
`ReadBatch` path. Continue measuring plain and obfuscated modes separately,
and do not allow an obfuscation optimization to change FEC or congestion policy
implicitly.

## Experimental contract

Every performance claim must use controlled variables:

- at least five repetitions for random-loss or burst-loss cases;
- 30–60 seconds after tunnel establishment, longer for very high RTT;
- median plus P10/P90 or confidence interval, never only the best result;
- identical MTU, path schedule, workload, direction, and host placement;
- outer wire bytes and local queue drops reported with application goodput;
- both TCP capacity traffic and low-rate UDP probes;
- direct `wireguard-go`, no-FEC wg-quic, and adaptive-FEC wg-quic baselines.

The adverse-link suite should scan:

1. independent loss from 0 through 10%;
2. correlated/Gilbert-Elliott loss with equal average loss but different burst
   lengths;
3. 200, 500, and 1000 ms periodic blackouts;
4. asymmetric forward and reverse loss;
5. bandwidth steps such as `50 -> 2 -> 20 Mbit/s`;
6. RTT from 20 through 600 ms with jitter and reordering;
7. UDP throttling or classification where it can be reproduced safely;
8. a competing conventional flow for fairness and queue-delay checks.

Results should include a curve showing impairment severity against goodput and
tail stall. The direct/wg-quic crossover and the right-hand goodput floor are
the headline results.

## Delivery sequence

Completed in the current experimental baseline:

1. core controller/FEC/RTT/delivery/pacing telemetry and 0.5-second fixture
   sampling;
2. direct WireGuard and five-mode controlled baselines with calibrated netem;
3. codec caching, healthy bypass, dynamic PMTU fragmentation, and obfuscation
   batching;
4. adaptive parity driven by FEC feedback plus transport-loss observations;
5. selectable Reno, CUBIC, and model controllers with a shared QUIC wire
   budget;
6. dynamic link schedules and synthetic protocol block/police policies;
7. application-limited Startup filtering plus RTT-aware FEC bypass thresholds
   and decrease evidence windows.

Next gates:

1. 30–60 second, at least five-repeat random, correlated, asymmetric, blackout,
   bandwidth-step, and high-RTT matrices;
2. explicit stall/P95/P99, path-generation, ECN, queue-age, and decision-reason
   telemetry;
3. source-versus-repair budget and deadline-aware/packet-aware protection;
4. competing-flow fairness and standing-queue tests before treating `auto` as
   production-stable;
5. a non-UDP fallback if generic UDP blocking is in scope;
6. multi-gigabit and ultra-low-RTT optimization only after the safety gates.

Each stage should land with benchmark evidence. If a change raises average
throughput but worsens P99 stalls, local drops, or fairness, it has not met the
design goal.
