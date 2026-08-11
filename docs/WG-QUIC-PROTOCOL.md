# wg-quic protocol v1

Status: v1 wire contract frozen for `v0.1.2` and later; local adaptive policy
remains experimental. Last reviewed 11 August 2026.

This document describes the protocol implemented by this repository. It is
split deliberately into two parts:

- **Wire protocol** defines the bytes and behavior another implementation must
  reproduce to interoperate.
- **Local adaptive policy** describes the current sender algorithms and
  defaults. Those choices are observable, but are not negotiated and are not
  wire compatibility requirements.

The words **MUST**, **MUST NOT**, **SHOULD**, and **MAY** define the v1
interoperability contract beginning with `v0.1.2`. This is not an IETF
standard. A disagreement between this document, implementation, and tests is a
release-blocking specification bug; it is not permission to change v1 wire
behavior silently. The sources named in
[Implementation map](#implementation-map) provide the executable conformance
reference for the affected release.

## 1. Scope and invariants

wg-quic retains the WireGuard protocol and replaces only its UDP bind. Both
ends therefore run a WireGuard userspace device, but carry each complete,
already encrypted WireGuard datagram through a separate QUIC session. A stock
WireGuard UDP endpoint does not implement this outer protocol and cannot
interoperate with wg-quic.

The outer transport preserves these properties:

- every item delivered to WireGuard is one complete WireGuard datagram;
- loss, duplication, and reordering are allowed;
- a lost datagram does not hold later datagrams behind a reliable ordered
  stream;
- fragmentation and FEC are removed before delivery to WireGuard; and
- WireGuard remains responsible for peer authentication, replay protection,
  AllowedIPs, key rotation, and inner-packet confidentiality.

The implemented stack, from inner to outer, is:

```text
inner IP packet
  -> WireGuard encrypted datagram
  -> WGQ1 fragment frame
  -> raw WGQ1 or WGQF systematic-FEC record
  -> QUIC DATAGRAM frame
  -> TLS-protected QUIC packet
  -> optional Salamander record
  -> UDP/IP
```

There is no application control stream and no separate wg-quic peer-authentication
message. FEC feedback is itself an unreliable QUIC DATAGRAM.

## Part I: wire protocol

## 2. UDP, QUIC, and TLS profile

### 2.1 UDP and QUIC

The only implemented carrier is UDP. The current pinned quic-go supports QUIC
v1 (RFC 9000) and QUIC v2 (RFC 9369), with v1 offered first. Peers need at
least one QUIC version in common.

An implementation MUST negotiate QUIC DATAGRAM support (RFC 9221). Application
payloads are sent as DATAGRAM frames and MUST NOT be converted to QUIC streams.
Every data, parity, close, and feedback record is consequently unreliable and
unordered. QUIC ACK, handshake, path-validation, and PMTU packets remain
ordinary QUIC transport traffic.

The application ALPN is exactly:

```text
wg-quic/1
```

The current implementation uses an initial QUIC packet size of 1200 bytes,
enables path MTU discovery when the platform supports it, disables incoming
bidirectional and unidirectional streams, and does not use 0-RTT. Those are
current local settings; only successful DATAGRAM and ALPN negotiation is
required for application interoperability.

### 2.2 Outer TLS is not peer identity

TLS 1.3 is required. The current listener creates a random Ed25519 self-signed
certificate when the carrier is opened; it is valid for 24 hours. The dialer
does not validate that certificate or a server name, and the listener does not
request a client certificate.

Consequently, outer TLS supplies QUIC packet confidentiality and integrity but
does **not** authenticate the configured WireGuard peer. The receiver passes a
reassembled candidate datagram to WireGuard, which performs the actual peer
authentication and replay checks. An implementation MUST NOT treat successful
QUIC establishment alone as proof that a configured WireGuard peer is online.

With `obfs=none`, an active endpoint can establish an anonymous QUIC session
and consume pre-WireGuard parsing resources. It still cannot forge a valid
WireGuard packet. The default Salamander profile adds a key-derived prefilter,
but its security boundary is described separately below.

### 2.3 Sessions and congestion accounting

An outbound QUIC session is created on the first WireGuard send to a numeric
peer endpoint. A listener also accepts inbound sessions. Simultaneous dialing
can therefore leave two valid sessions, and no on-wire session identifier is
added by wg-quic.

All congestion-controlled QUIC packets share the connection's pacing and
in-flight budget. This includes application data, FEC parity and feedback, and
ack-eliciting TLS and QUIC control traffic. ACK-only packets remain subject to
QUIC's normal rules and need not count as bytes in flight. FEC MUST NOT be sent
on a side channel outside the QUIC congestion controller.

## 3. Optional Salamander UDP envelope

Salamander is below QUIC and therefore wraps every UDP payload emitted by
QUIC, including Initial, Handshake, ACK-only, DATAGRAM, path-validation, and
PMTU-probe packets. It is an explicit deployment choice:

- `obfs=none` sends the QUIC UDP payload unchanged;
- `obfs=salamander` applies the profile in this section.

There is no on-wire capability negotiation. Both endpoints MUST select the
same mode. A mismatch normally appears as a QUIC handshake timeout.

This profile borrows a construction from Hysteria 2 but uses wg-quic-specific
key derivation and is not a claim of Hysteria interoperability.

### 3.1 Per-peer key derivation

All WireGuard keys below are their decoded 32-byte values. For local WireGuard
private key `sk`, remote WireGuard public key `pk`, and optional 32-byte
WireGuard preshared key `psk`, calculate:

```text
shared = X25519(sk, pk)

K = BLAKE2b-256(
      "wg-quic/salamander/key/v1" ||
      shared ||
      psk_marker ||
      optional_psk
    )
```

`psk_marker` is the single byte `0x00` and `optional_psk` is empty when no PSK
is configured. Otherwise `psk_marker` is `0x01` and `optional_psk` is the 32
PSK bytes. BLAKE2b is unkeyed in this derivation. X25519 makes the result
symmetric between the two peers.

### 3.2 Record format

Each QUIC UDP payload is encoded independently:

| Offset | Size | Field |
| ---: | ---: | --- |
| 0 | 8 | cryptographically random `salt` |
| 8 | 8 | key-selection `hint` |
| 16 | rest | XOR-obfuscated QUIC UDP payload |

Define keyed BLAKE2b-256 as `BLAKE2b-256(key=K, data=...)`:

```text
hint = first_8_bytes(
  BLAKE2b-256(key=K,
    data="wg-quic/salamander/hint/v1" || salt)
)

stream = BLAKE2b-256(key=K,
  data="wg-quic/salamander/stream/v1" || salt)

encoded[16+i] = plain[i] XOR stream[i mod 32]
```

The salt is an opaque byte string; it has no numeric byte order on the wire.
The plaintext MUST be non-empty. The encoded UDP payload MUST fit the maximum
UDP payload of 65,507 bytes, so this envelope adds exactly 16 bytes and accepts
at most 65,491 plaintext bytes.

When UDP GSO is used, each segment is a separate Salamander record with its
own salt and hint; it is not one record spanning all coalesced segments.

### 3.3 Selection, mobility, and failure behavior

A receiver tests the 8-byte hint against its configured peer keys using a
constant-time comparison, then XOR-decodes with the matching key. A packet
that is too short, has no matching hint, or cannot fit the destination buffer
is silently discarded before QUIC sees it.

After a matching packet arrives, the implementation remembers the source
address-to-key association for replies and roaming. Configured endpoint
associations take precedence. The learned association cache is bounded to
1024 entries and is cleared when full; that cache policy is local and does not
change the bytes above.

Salamander is obfuscation, not an independent authenticated-encryption layer:

- its payload transform is repeating-key-stream XOR and has no separate MAC;
- the 64-bit hint selects a key but is not an identity certificate;
- salts are not kept in a replay cache; and
- QUIC packet protection is expected to reject modified decoded packets.

WireGuard remains the end-to-end security boundary. The derived secret also
makes blind construction of a packet accepted by the default outer prefilter
harder than with `obfs=none`, but operators MUST NOT treat Salamander as a
replacement for WireGuard authentication.

## 4. WGQ1 carrier frame

The payload of an unprotected QUIC DATAGRAM is one WGQ1 frame. A protected FEC
data shard also contains exactly one WGQ1 frame after its source-length prefix.
All multibyte integers in wg-quic application headers are unsigned and
big-endian.

### 4.1 Header

The fixed header is 21 bytes:

| Offset | Size | Type | Field | v1 meaning |
| ---: | ---: | --- | --- | --- |
| 0 | 4 | bytes | magic | ASCII `WGQ1` (`57 47 51 31`) |
| 4 | 1 | `u8` | version | `1` |
| 5 | 8 | `u64` | packet ID | identifier of the original WireGuard datagram |
| 13 | 2 | `u16` | fragment index | zero-based index |
| 15 | 2 | `u16` | fragment count | total number of fragments |
| 17 | 4 | `u32` | total length | original WireGuard datagram length |
| 21 | rest | bytes | fragment data | one contiguous portion of the datagram |

There is no fragment-payload-length field; the QUIC DATAGRAM boundary supplies
it. A sender MUST use a nonzero fragment count, an index smaller than that
count, and the same packet ID, count, and total length for every fragment of a
datagram. It MUST NOT reuse a packet ID on the same session while an earlier
datagram with that ID can still be incomplete. The current sender uses a
bind-lifetime monotonically increasing `u64` counter.

### 4.2 Limits and reassembly

The current v1 implementation enforces:

| Item | Limit |
| --- | ---: |
| Original WireGuard datagram | 1 through 65,535 bytes |
| Fragment data | 1 through 4,075 bytes |
| Fragment count | 1 through 128 |
| Incomplete reassemblies | 2,048 across the open bind |
| Reassembly lifetime | 3 seconds from the first fragment |

The sender chooses fragment data size from the QUIC connection's current
maximum DATAGRAM payload. It subtracts 21 bytes for WGQ1 and, when the local
FEC encoder exists, another 26 bytes for WGQF data framing. It also caps the
result at 4,075 bytes, keeping a complete WGQ1 frame at or below the 4,096-byte
FEC frame limit.

Fragments may arrive out of order. The first copy of a duplicate fragment
index wins. A receiver delivers only after concatenating all indices in order
and verifying that the result equals `total length`. A one-fragment frame is
accepted only when its payload length equals `total length`.

Malformed frames and expired or inconsistent reassemblies are discarded. A
single malformed application datagram does not close the QUIC session in the
current implementation.

## 5. WGQF FEC records

FEC is systematic: original WGQ1 frames are sent as data shards and can be
delivered without waiting for parity. A QUIC DATAGRAM is classified as WGQF
only when its first four bytes are ASCII `WGQF`. Any other datagram is passed
to the WGQ1 parser. A datagram beginning with `WGQF` but containing a malformed
or unsupported WGQF record is consumed and discarded; it is not retried as
WGQ1.

### 5.1 Common header

The fixed WGQF header is 24 bytes:

| Offset | Size | Type | Field |
| ---: | ---: | --- | --- |
| 0 | 4 | bytes | magic: ASCII `WGQF` (`57 47 51 46`) |
| 4 | 1 | `u8` | FEC wire version: `1` |
| 5 | 1 | `u8` | kind |
| 6 | 2 | `u16` | epoch |
| 8 | 8 | `u64` | group ID |
| 16 | 2 | `u16` | index / missing |
| 18 | 2 | `u16` | `k` / total |
| 20 | 2 | `u16` | `r` / recovered |
| 22 | 2 | `u16` | payload length |
| 24 | rest | bytes | payload |

The payload-length field MUST equal the bytes remaining in this QUIC
DATAGRAM. The receiver rejects payloads larger than 4,098 bytes. The meanings
of the overloaded fields are:

| Kind | Value | `index` | `k` field | `r` field | Payload |
| --- | ---: | --- | --- | --- | --- |
| data | 0 | source-shard index | `0` | `0` | length-prefixed WGQ1 frame |
| parity | 1 | parity-shard index | data count `k` | parity count `r` | RS parity shard |
| close | 2 | `0` | data count `k` | parity count `r` | empty |
| feedback | 3 | missing source count | total source count | recovered source count | empty |

Conforming v1 senders use zero in fields marked zero and send no close or
feedback payload. Current receivers are lenient about some unused fields, but
new senders MUST NOT rely on that leniency.

### 5.2 Data shards

A data payload is:

```text
source_length:u16_be || WGQ1_frame
```

`source_length` is the WGQ1 frame length and MUST be in the range 22 through
4,096: the 21-byte header plus non-empty fragment data. Data indices start at
zero, MUST be unique within the group, and MUST cover `0` through `k-1` for
the eventual `k`. Data records do not announce `k` or `r`; a parity or close
record supplies those dimensions later. The receiver may deliver a valid
systematic WGQ1 frame immediately and remembers that it has already done so to
avoid a second delivery after reconstruction.

### 5.3 Reed-Solomon parity

For one group, pad every length-prefixed source shard with trailing zero bytes
to the length of the largest source shard. Encode byte positions independently
with systematic Reed-Solomon over GF(2^8), primitive polynomial `0x11d` and
generator `2`.

The coding matrix is the default matrix used by
`github.com/klauspost/reedsolomon` v1.14.1: construct a `(k+r) x k`
Vandermonde matrix `V` where `V[row][column] = row^column` in GF(2^8), then
right-multiply it by the inverse of its top `k x k` submatrix. The top `k`
rows are consequently the identity matrix; wire parity index `j` is matrix
row `k+j`. `WithAutoGoroutines` is only an execution optimization and does not
change the code words.

The wire decoder accepts `1 <= k <= 32` and `0 <= r <= 8`. A parity index MUST
be smaller than `r`. Every parity payload has the padded shard length. On
reconstruction, the two-byte source length removes padding and must describe a
non-empty WGQ1 frame that fits in the reconstructed shard.

The current automatic sender normally uses at most eight data shards and four
parity shards, but those are local-policy limits, not smaller wire fields.

### 5.4 Close and group completion

Close announces the final `k` and `r` for a partial or full group. It is not a
QUIC connection close. When source shards are missing, a receiver can complete
before close after learning valid dimensions from parity and collecting any
`k` reconstructable shards from the `k+r` data and parity set. If all `k`
source shards arrive and `r` is nonzero, the current receiver has already
delivered those systematic frames but waits for close or expiry before it
completes the group and emits feedback.

Data, parity, and close are all unreliable. In particular, if every parity
record and close are lost, the receiver never learns `k`, even though it may
have delivered received systematic shards. That group expires without FEC
feedback. QUIC transport-loss counters are the current sender's secondary
signal for this case.

### 5.5 Feedback

On successful completion or expiration of a group whose `k` is known, the
receiver may send one feedback record:

- `total` is the group's `k`;
- `missing` is the number of source shards absent when the group was resolved;
- `recovered` is the subset reconstructed successfully; and
- `epoch` and group ID copy the data group.

Thus `0 <= recovered <= missing <= total` for canonical feedback. Feedback is
best-effort, is not retransmitted, and may itself be lost. The sender applies
feedback only when its epoch matches the current FEC epoch.

The current receiver keeps at most 1,024 incomplete FEC groups per QUIC
session, expires them after 3 seconds, polls expiration every 500 ms, and keeps
up to 4,096 recently completed group IDs for duplicate suppression. The local
feedback queue holds 64 records; excess feedback is dropped. These bounds are
defensive implementation limits, not negotiated values.

### 5.6 Epoch and group identifiers

The current sender starts at epoch 1 and increments the nonzero `u16` epoch at
a group boundary whenever its target parity changes. It starts group ID at 1
and increments it for each protected or probe group. Raw healthy-path WGQ1
frames do not consume a group ID.

A receiver scopes FEC group IDs to one QUIC session. It rejects an epoch change
within a group. Epoch is a controller-generation filter, not cryptographic
freshness or replay protection.

## 6. FEC mode compatibility and lack of negotiation

There is no FEC capability handshake. The v1 receiver always includes both a
WGQF decoder and a raw WGQ1 path:

- `fec=off` disables only the local outbound encoder. It still decodes inbound
  WGQF and sends feedback.
- `fec=auto` may send raw WGQ1 during healthy bypass and WGQF during protected
  groups. It accepts both forms inbound.

Current `auto` and `off` implementations can therefore be configured
asymmetrically. Congestion-controller choices can also differ by direction.
This does **not** make an implementation that understands only WGQ1 fully
compatible with ALPN `wg-quic/1`: an `auto` sender will eventually emit WGQF.

## 7. Versioning and compatibility

wg-quic currently has several independent version markers:

| Layer | Current marker | Failure on mismatch |
| --- | --- | --- |
| Application over QUIC | ALPN `wg-quic/1` | TLS/QUIC handshake fails |
| Carrier fragment | `WGQ1`, version byte 1 | datagram is discarded |
| FEC record and feedback | `WGQF`, version byte 1 | datagram is discarded |
| Salamander derivation | three `/v1` domains | hint mismatch and timeout |

There is no capability exchange, downgrade, or parameter negotiation after
the QUIC handshake. Starting with `v0.1.2`, the bytes and required semantics in
Part I are frozen as v1. Any incompatible frame layout, limit, FEC codec or
matrix, required record kind, Salamander transform, or security semantic MUST
use a new ALPN and the appropriate new inner version marker, or first add an
explicit capability mechanism under a compatible protocol revision. Merely
changing the WGQF version is not a graceful downgrade: a peer cannot know in
advance that it should send raw WGQ1 instead.

The WireGuard UAPI line `protocol_version=1` is part of WireGuard peer
configuration and is unrelated to the wg-quic ALPN, WGQ1 version, or WGQF
version.

### 7.1 Existing release compatibility

The WGQF v1 byte layout and default RS matrix are unchanged from repository
tags `v0.1.0` and `v0.1.1`. Salamander's wire transform and the ALPN are also
unchanged; later batching and congestion work is local.

WGQ1's byte layout is unchanged, but its accepted fragment-data limit was
raised from 1,000 to 4,075 bytes without changing the ALPN. A current receiver
accepts old senders. A current sender can, after consulting the QUIC DATAGRAM
limit, emit a fragment larger than 1,000 bytes that a `v0.1.0` or `v0.1.1`
receiver drops. Compatibility with those tags is therefore conditional on
every emitted fragment remaining at most 1,000 bytes; typical current
single-fragment MTU-1280 traffic does not satisfy that condition.

Until a capability or ALPN revision resolves this, releases MUST NOT claim
unqualified bidirectional interoperability with the two earlier tags merely
because all three advertise `wg-quic/1`.

## 8. Error handling and security bounds

The current error policy is fail-closed per application datagram:

- invalid Salamander records are silently discarded before QUIC;
- QUIC version, TLS, ALPN, or DATAGRAM negotiation failures prevent session
  establishment;
- malformed or unknown WGQF records are discarded as WGQF;
- malformed WGQ1 frames and inconsistent reassemblies are discarded; and
- these application parsing errors do not currently close an otherwise valid
  QUIC session.

Connection-level QUIC errors close the session. A later WireGuard send to a
configured endpoint creates a new outbound session. The implementation does
not reliably expose malformed-frame counters or reasons today, so silence in
logs does not prove that no invalid records arrived.

Implementations MUST enforce the frame, group, payload, and incomplete-state
limits in this document before allocating unbounded memory. Fields learned
from the wire are not trusted. In particular, FEC feedback is protected only
by the established QUIC connection; because outer TLS does not authenticate a
WireGuard peer, transport-control feedback does not have the same end-to-end
identity guarantee as an authenticated WireGuard packet. Feedback can affect
local FEC policy and telemetry but never bypasses WireGuard packet validation.

The current feedback parser does not reject every noncanonical relationship
such as `missing > total`, does not deduplicate feedback at the sender, and
ignores some unused fields. A new implementation SHOULD emit only canonical
records and SHOULD validate these relationships even when interoperating with
the current lenient receiver.

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

The parser accepts per-peer
`peer.fec-latency=latency|balanced|throughput`, but the runtime does not yet
read that value. It currently changes neither wire records nor local FEC
behavior and MUST be described as reserved/no-op in user interfaces.

## 10. Current adaptive FEC sender

The current automatic sender uses these defaults:

| Setting | Current value |
| --- | ---: |
| Maximum data shards per group | 8 |
| Initial target parity | 1 |
| Partial-group flush deadline | 2 ms |
| Controller parity range | 0 through 4 |
| Healthy-path protected probe | one group after 4,096 raw frames |
| Normal decrease evidence | 32 groups |

For a partial group with `k` source shards, emitted parity is bounded by
`min(target_parity, max(1, floor(k/2)))` and by the wire maximum. The sender
increments epoch only at the next group boundary after the target changes.

The controller maintains a loss EWMA from receiver feedback. Its sample weight
is clamped between 1/32 and 1/4. Using the default eight-source group, it
chooses the smallest parity count whose independent-loss estimate gives a
probability of losing more than that many shards in the resulting group of at
most 0.5%, with a current maximum of four.

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

These thresholds, the independent-loss model, and the fixed group/flush values
are implementation policy. They may change without a wire-version change.

## 11. Current capacity and congestion model

The experimental `model` controller is BBR-like but is not BBRv3. It measures
acknowledged congestion-controlled QUIC packet bytes, so source data, parity,
and QUIC packet overhead consume the measured delivery and pacing budget.
UDP/IP headers and the 16-byte Salamander envelope are added below quic-go and
are not explicitly debited from that byte counter. Useful WireGuard bytes are
a separate product metric and are not used to pretend that parity is free.

Current behavior includes:

- delivery samples over windows between 5 and 50 ms, derived from RTT;
- a ten-slot delivery-sample window used to raise the bandwidth estimate;
  ordinary samples do not lower the estimate, and ACK-compression samples are
  bounded by 1.5 times in-flight bytes over the path RTT;
- a startup pacing gain of 2.0 and a steady probing gain of 1.10;
- a target congestion window of twice the estimated bandwidth-delay product;
- exit from startup after three capacity-limited rounds without 25% bandwidth
  growth;
- no multiplicative response to random packet loss alone;
- model reductions for ECN or loss accompanied by standing queue growth;
- a dynamic path-local propagation RTT and queue-delay estimate; and
- reset to a four-packet minimum window after a retransmission timeout.

The standing-queue test requires at least 5 ms of excess smoothed RTT and a
relative threshold of 25%; a more severe 50% threshold triggers repeated
model reduction. The path RTT baseline can move after sustained access-path
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

These status values and the local control socket are not wire-protocol fields.
They can change independently of v1 interoperability.

## 13. Current implementation limits

The implemented profile has the following deliberate or known limits:

- UDP is the only carrier; there is no TCP or other fallback for blanket UDP
  blocking.
- There is no padding, packet-size shaping, port hopping, multipath scheduler,
  or application-layer reliable retransmission.
- FEC adapts parity only. It does not yet adapt `k`, the 2 ms flush deadline,
  interleaving, repair deadlines, or per-packet protection.
- `peer.fec-latency` is not implemented beyond configuration parsing.
- FEC completion and feedback use 3-second expiry, which is not derived from
  a peer latency policy.
- Feedback and malformed-frame diagnostics are incomplete.
- The model controller has no explicit source/repair budget split, confidence
  score, fairness guarantee, or complete BBRv3 state machine.
- Malformed-record tables/fuzzing, duplicate-feedback tests, and explicit
  `auto`/`off` asymmetric interoperability tests are still missing. Golden
  vectors lock the WGQ1, WGQF, and Salamander v1 bytes, and recovery, expiry,
  controller, framing, and full WireGuard-over-carrier behavior are covered.

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
