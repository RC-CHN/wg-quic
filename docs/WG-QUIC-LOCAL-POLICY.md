# wg-quic local implementation notes

These are non-normative implementation and tuning notes, originally reviewed
on 11 August 2026. They are not required to implement the protocol. The
standalone [wire specification](WG-QUIC-PROTOCOL.md) takes precedence for
interoperability, including feedback validation and receiver timing.

## Part II: local adaptive policy

Everything below describes the present implementation but is not negotiated
on the wire. Two conforming peers may use different algorithms and still
interoperate if they emit valid v1 records.

## 9. Configuration-to-runtime mapping

The default transport configuration is:

```text
carrier=quic
congestion=auto
fec=auto
obfs=salamander
```

Current accepted values are:

- congestion: `auto`, `model`, `reno`, or `cubic`;
- FEC: `auto` or `off`; and
- obfuscation: `salamander` or `none`.

`congestion=auto` currently maps directly to the experimental `model`
controller. It does not probe or negotiate a controller with the peer. Reno
and CUBIC are benchmark/debug alternatives.

The per-peer directive
`peer.fec-latency=latency|balanced|throughput` selects a local encoder profile.
`balanced` uses the interface values; `latency` caps data shards at four,
starts with interleave one, and caps the base flush window at 1 ms;
`throughput` uses at least interleave two and a base window of at least 4 ms.
The configured interleave is a minimum that automatic burst protection may
increase. Only that automatic increase scales the repair window: the effective
window is `base * active_interleave / configured_interleave`. Source frames
are sent immediately; the window applies to parity and group closure.
Outbound sessions inherit
the configured endpoint's peer policy. Inbound and roamed sessions receive a
policy only after WireGuard authenticates the peer identity. A live change
flushes the old group before reconfiguring the encoder, so it changes no v1
record syntax and never reinterprets an in-flight group.

## 10. Current adaptive FEC sender

The current automatic sender uses these defaults:

| Setting | Current value |
| --- | ---: |
| Default interface data-shard limit | 32 |
| Initial target parity | 1 |
| Partial-group base flush window | 2 ms; up to 8 ms under automatic burst interleaving |
| Controller parity range | 0 through 8 |
| Controller interleave range | 1 through 4 |
| Healthy-path protected probe | one group after 4,096 raw frames |
| Normal parity decrease evidence | 32 groups |
| Interleave decrease evidence | 32 configured groups' worth of healthy source frames |

For a partial group with `k` source shards, emitted parity is bounded by
`min(target_parity, max(1, floor(k/2)))` and by the wire maximum. The sender
increments epoch only at the next group boundary after the target changes.

The controller maintains a loss EWMA from receiver feedback. Its sample weight
is clamped between 1/32 and 1/4. Using the default 32-source interface profile, it
chooses the smallest parity count whose independent-loss estimate gives a
probability of losing more than that many shards in the resulting group of at
most 0.5%, with a current maximum of eight.

At RTT up to 100 ms, an estimated loss at or below 0.1% permits the parity-zero
fast path. Above 100 ms that threshold scales by `100 ms / path RTT`, down to a
floor of 0.01%. A transition from one parity shard to zero also requires
`32 * ceil(RTT / 100 ms)` clean groups on a long-RTT path, capped at 256;
surplus parity above one still drains on the normal 32-group window.

Unrecovered groups raise protection more quickly. The sender also samples
cumulative QUIC sent/lost counters every 32 WGQ1 frames regardless of current
parity, and updates only after at least 128 newly sent QUIC packets. While
parity is zero, two or more losses at a sample rate of at least 0.5%
immediately leave the fast path. A protected probe with even one missing
source shard has the same effect, including when FEC repaired that shard.

At parity target four or higher, four unrecovered sources in a group trigger
additional interleaving. Short groups can also trigger it after two consecutive
groups each lose at least half their sources without recovery. Interleaving
doubles up to four lanes. It decreases only after a healthy source-frame
window, so frequent timer-flushed tiny groups do not prematurely end burst
protection. The repair window grows with the additional lanes to avoid
splitting the same traffic into tiny, disproportionately expensive groups.

These thresholds, the independent-loss model, and the local peer profiles are
implementation policy. They may change without a wire-version change.

## 11. Current capacity and congestion model

The experimental `model` controller is BBR-like but is not BBRv3. It measures
acknowledged congestion-controlled QUIC packet bytes, so source data, parity,
and QUIC packet overhead consume the measured delivery and pacing budget.
UDP/IP headers and the 16-byte Salamander envelope are added below quic-go and
are not explicitly debited from that byte counter. Useful WireGuard bytes are
a separate product metric and are not used to pretend that parity is free.

Current behavior includes:

- delivery samples over windows between 5 and 50 ms, derived from RTT;
- a ten-slot maximum over eligible delivery samples; old peaks expire as new
  capacity-limited samples enter the window. Low application demand does not
  lower the estimate, and ACK-compression samples are bounded by 1.5 times
  in-flight bytes over the path RTT;
- a startup pacing gain of 2.0 and a steady probing gain of 1.10;
- a target congestion window of twice the estimated bandwidth-delay product;
- exit from startup after three capacity-limited rounds without 25% bandwidth
  growth;
- no multiplicative response to random packet loss alone;
- model reductions for explicit ECN, or for loss accompanied by an elevated
  queue-delay estimate;
- a dynamic path-local propagation RTT and queue-delay estimate; and
- reset to a four-packet minimum window after a retransmission timeout.

The standing-queue test requires at least 5 ms of excess smoothed RTT and a
relative threshold of 25%; a more severe 50% threshold permits a model
reduction from ACK feedback alone. Queue and loss responses share a minimum
one-model-RTT interval and a packet-flight recovery boundary. Once a response
records the largest sent packet number, feedback about that flight cannot
compound the reduction, even when delayed beyond an RTT. Validated ECN shares
this flight boundary; new-flight ECN responds without the queue threshold or
RTT timer. The path RTT baseline can move after sustained access-path
changes instead of retaining only the connection-lifetime minimum.

FEC feedback currently supplies recoverable and residual-loss classification
to the model's telemetry. It does **not** directly increase delivery samples,
exempt lost QUIC packets from accounting, or change the current bandwidth/cwnd
formula. Parity selection remains in the separate FEC controller.

## 12. Scheduling, queues, and telemetry

The local send path gives priority to WireGuard handshake initiation,
handshake response, cookie reply, and empty transport keepalive packets. The
default bulk send queue is 1024 items, the priority queue is at least 64 items,
and the FEC feedback queue is 64 items. Admission is bounded; full queues cause
local drops rather than unbounded delay. Priority does not currently change
FEC group membership or QUIC's wire pacing rules.

Status exposes, among other local measurements:

- WireGuard packets/bytes and WGQ1/WGQF QUIC-DATAGRAM payload packets/bytes
  (the latter `wire_*` counters do not include QUIC, UDP/IP, or Salamander
  headers);
- queue depth and drops;
- FEC data/parity, raw missing, recovered, unrecovered, current parity, and
  loss estimate;
- QUIC acknowledged/lost bytes and packets;
- minimum/latest/smoothed/path RTT and estimated queue delay; and
- congestion window, bytes in flight, bandwidth estimate, pacing rate, and
  model state.

Status additionally advertises `session_telemetry_v1` and exposes the same
measurements independently for every active QUIC session. Session observations
include the connection role and generation, configured and current outer
endpoint, authenticated/configured peer associations, RTT variation, cumulative
PTO firings, and packets later classified as spurious loss. A session can be
associated with multiple WireGuard peers; implementations and collectors must
not duplicate its QUIC counters into a separate copy for each peer. These
portable observations use the same schema on Unix and Windows. A
platform-specific host or socket counter must separately report whether its
source is supported instead of using zero to mean both "unavailable" and "no
events". Session enumeration is bounded, prioritizes configured outbound
connections, and reports the excluded count as `session_telemetry_omitted`.

These status values and the local control socket are not wire-protocol fields.
They can change independently of v1 interoperability.

## 13. Current implementation limits

The implemented profile has the following deliberate or known limits:

- UDP is the only carrier; there is no TCP or other fallback for blanket UDP
  blocking.
- There is no padding, packet-size shaping, port hopping, multipath scheduler,
  or application-layer reliable retransmission.
- FEC adapts parity and burst interleave, scaling the repair window with
  additional interleaving. Per-peer policy can change `k`, interleave, and
  the base flush window at a group boundary. The path controller does not
  continuously tune `k`, receiver repair deadlines, or per-packet protection.
- FEC completion and feedback use 3-second expiry, which is not derived from
  a peer latency policy.
- Feedback and malformed-frame diagnostics are incomplete.
- The model controller has no explicit source/repair budget split, confidence
  score, fairness guarantee, or complete BBRv3 state machine.
- Comprehensive parser fuzzing, duplicate-feedback replay tests, and explicit
  `auto`/`off` asymmetric end-to-end tests remain validation gaps. Existing
  tests cover malformed frames, invalid payload/epoch handling, feedback
  counter bounds, and fuzzed late-shard accounting. Golden vectors lock the
  WGQ1, WGQF, and Salamander v1 bytes; recovery, expiry, controller, framing,
  and full WireGuard-over-carrier behavior are also covered.

## 14. Non-normative design intent and validation contract

wg-quic is intended to preserve useful delivery and bounded stalls after
direct userspace WireGuard becomes unstable, heavily rate-limited, or
protocol-discriminated. Winning clean-LAN peak throughput is secondary. The
principal measurements are the impairment usability boundary, interval
goodput floor, P95/P99 and longest stall, recovery after blackout or path
change, residual post-FEC loss, total outer bytes per useful byte, queue delay,
local drops, and fairness to a competing flow.

Performance claims should keep MTU, workload, direction, host placement, path
schedule, and measurement interval fixed; use at least five 30--60 second
repetitions for random or burst loss; report medians and dispersion rather
than the best run; and include outer wire bytes, local drops, TCP capacity,
low-rate UDP, direct userspace WireGuard, no-FEC wg-quic, and adaptive-FEC
wg-quic baselines. The controlled fixture and field interpretation live in
[`tests/benchmark/README.md`](../tests/benchmark/README.md).

Future work should preserve one total congestion-controlled wire budget:

```text
source budget + repair budget + control budget <= total wire budget
```

FEC recovery by itself must not be interpreted as proof that loss is
non-congestive; ECN, queue growth, RTT, delivery collapse, and fairness remain
safety signals. Any future incompatible feedback, coding, or carrier change
must follow the versioning rules in section 7.

## 15. Implementation map

The primary sources reviewed for this specification are:

- QUIC/TLS/ALPN and Datagram carrier:
  `internal/transport/quic/carrier.go`;
- Salamander derivation and UDP envelope:
  `internal/transport/obfs/salamander.go` and platform GSO helpers;
- WGQ1 framing, validation, and reassembly:
  `internal/bind/framing.go`;
- session, fragmentation, FEC dispatch, feedback, and queues:
  `internal/bind/bind.go`;
- WGQF bytes, codec, encoder, decoder, and controller:
  `internal/transport/fec/{wire,codec,encoder,decoder,controller}.go`;
- configuration surface and runtime mapping:
  `internal/config/config.go` and `internal/core/transport.go`;
- custom congestion and capacity estimator:
  `third_party/quic-go/internal/congestion/model_sender.go` and
  `third_party/quic-go/connection.go`; and
- behavior coverage: the corresponding tests under `internal/bind`,
  `internal/transport/{quic,obfs,fec}`, and
  [`tests/WIREGUARD-FORK.md`](../tests/WIREGUARD-FORK.md).
