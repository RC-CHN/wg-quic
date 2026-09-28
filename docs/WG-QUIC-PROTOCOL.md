# wg-quic protocol v1

Status: normative specification of the deployed `wg-quic/1` protocol, checked
against release 0.3.7 on 28 September 2026. This is a specification clarification,
not a new wire version and not an IETF standard.

This document is self-contained at the application protocol level. It requires
no repository, programming language, runtime, package, source file, private
constants, generated fixture or vendor-specific QUIC extension. Implementers
may use any implementation of the public cryptographic and QUIC standards
listed in section 12. Those standards define the underlying general-purpose
primitives; all wg-quic-specific values, framing, algorithms, state transitions
and test inputs/outputs are given here, including the inner WireGuard handshake.

**MUST**, **MUST NOT**, **SHOULD** and **MAY** express protocol requirements.
A requirement concerns emitted bytes or interoperable semantics unless marked
as a local policy. Local scheduling, congestion estimation and UI behavior are
not part of the wire protocol. There are no proposed future extensions in this
specification. An implementation claiming full v1 compatibility must receive
both raw and FEC records even if it sends only raw records.

### 0.1 Provisioning and a minimal implementation path

Out of band, each side receives its own 32-byte X25519 static private key, the
other side's 32-byte public key, the same optional 32-byte PSK, and allowed inner
IP prefixes. At least one side needs a reachable UDP IP address and port for the
other side. There is no discovery service, HTTP request, password login, fixed
UDP port, certificate enrollment or key exchange outside those described below.
Both sides may listen and dial. The QUIC client role and WireGuard initiator role
are separate roles; a completed connection carries traffic in both directions.

Default deployment settings are `congestion=auto`, `fec=auto`,
`obfs=salamander`, and an inner interface MTU of 1280. Congestion and FEC sender
choices may differ between sides. Obfuscation mode and key material MUST agree.
A minimal interoperable sender may use standard QUIC Reno or CUBIC and raw
WGQ1 without FEC, while implementing the complete WGQF receiver in section 5.
No proprietary congestion controller is needed to complete either handshake.

The three transport settings belong to a local interface/instance and apply to
its peers. `peer.fec-latency` is a local per-peer scheduling preference, not a
negotiated field. Their textual spellings do not appear in any network packet.
If implementing profile import, recognize `# wg-quic: congestion = VALUE`,
`# wg-quic: fec = VALUE`, and `# wg-quic: obfs = VALUE` under `[Interface]`;
accepted values are respectively `auto|model|reno|cubic`, `auto|off`, and
`salamander|none`. Under `[Peer]`, `# wg-quic: peer.fec-latency = VALUE` accepts
`latency|balanced|throughput`, default `balanced`. A profile reader must not
silently discard these comment directives. WireGuard keys in profiles use
standard padded Base64; all wire operations use the decoded bytes.

### 0.2 Notation

`||` means byte concatenation; `x[a:b]` includes offset `a` and excludes `b`.
All offsets and lengths count octets. Text constants are exact ASCII without a
trailing zero or newline. Hex strings encode bytes, not ASCII hex on the wire.
`u16_be`/`u32_le` etc. specify unsigned fixed-width integer byte order. There is
no structure alignment or implicit padding. Application headers are big-endian;
inner WireGuard headers are little-endian. QUIC uses its standard varints.

## 1. Scope and invariants

wg-quic retains the WireGuard protocol and replaces only its UDP bind. Both
ends therefore implement WireGuard, and carry each complete,
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

The only implemented carrier is UDP. The deployed endpoint supports QUIC
v1 (RFC 9000) and QUIC v2 (RFC 9369), with v1 offered first. Implementing QUIC
v1 is sufficient for interoperability. Peers need at
least one QUIC version in common.

An implementation MUST negotiate QUIC DATAGRAM support (RFC 9221). Advertise
transport parameter `max_datagram_frame_size` (ID `0x20`) with a nonzero value;
65535 is a suitable value. A sender still obeys the peer's limit and the path
packet budget. Both DATAGRAM frame types `0x30` (remaining bytes) and `0x31`
(explicit QUIC-varint length) carry the identical application payload. There is
no HTTP/3, WebTransport session ID, DATAGRAM flow ID, prefix or stream handshake
between QUIC DATAGRAM and the WGQ1/WGQF magic. Application
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
bidirectional and unidirectional streams, and does not use 0-RTT. The current handshake idle timeout is 4 seconds, connection idle
timeout 15 seconds, and keepalive period 5 seconds. These are
current local settings; only successful DATAGRAM and ALPN negotiation is
required for application interoperability.

### 2.2 Outer TLS is not peer identity

TLS 1.3 is required. The current listener creates a random Ed25519 self-signed
certificate when the carrier is opened; it is valid for 24 hours. A compatible dialer must support the Ed25519 TLS signature scheme `0x0807`
and a standard TLS 1.3 cipher suite (for example `TLS_AES_128_GCM_SHA256`,
`0x1301`). The dialer
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
symmetric between the two peers. Apply RFC 7748 scalar clamping inside X25519:
clear private byte 0 bits 0..2, clear byte 31 bit 7, set byte 31 bit 6. Reject
an all-zero shared result. Public coordinates are 32-byte little-endian values.
BLAKE2b-256 means digest length 32 set in the BLAKE2 parameter block; it is
**not** the first half of a BLAKE2b-512 digest. The same distinction applies to
BLAKE2s-128 MACs in section 9.

An absent PSK and an explicitly configured all-zero PSK produce **different
Salamander keys** because of the marker. Do not normalize them to the same
input. Inner WireGuard, by contrast, uses 32 zero bytes when no PSK is present.

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

The payload of a QUIC DATAGRAM sent without FEC is one WGQ1 frame. A protected FEC
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

Define field addition as bytewise XOR. Field multiplication is carryless
polynomial multiplication reduced modulo `x^8+x^4+x^3+x^2+1` (`0x11d`):

```text
mul(a,b):
    z = 0
    while b != 0:
        if (b AND 1) != 0: z = z XOR a
        a = a << 1
        if (a AND 256) != 0: a = a XOR 0x11d
        b = b >> 1
    return z
```

`pow(a,0)=1`, including `pow(0,0)=1`; subsequent powers use `mul`.
The inverse of a nonzero element `a` is `pow(a,254)`. Construct a
`(k+r) x k` Vandermonde matrix `V[row,column]=pow(row,column)`, with row and
column indices starting at zero. Let `T` be its first `k` rows. Compute
`M=V*T^-1` entirely in this field. Matrix inversion uses Gaussian elimination:
augment `T` with the identity, swap in a nonzero pivot for each column, multiply
the pivot row by the pivot's inverse, then XOR a scaled pivot row into every
other row to zero that column. The resulting right half is `T^-1`.

The top `k` rows of `M` are the identity. For each byte position `b` and parity
index `j`, emit `P[j,b] = XOR_i mul(M[k+j,i], D[i,b])`. There is no integer
carry, logarithm-base ambiguity, first-parity XOR shortcut, Cauchy matrix,
transposed matrix, or special case for `r=1`. For reconstruction select any `k`
available distinct rows of `M` to form `A`; the corresponding received bytes
form `Y`. Original source bytes are `D=A^-1*Y`. This fully defines the codec
without reference to a library or its defaults.

The wire decoder accepts `1 <= k <= 32` and `0 <= r <= 8`. A parity index MUST
be smaller than `r`. Every parity payload has the padded shard length. On
reconstruction, the two-byte source length removes padding and must describe a
non-empty WGQ1 frame that fits in the reconstructed shard.

A receiver MUST support the full 32-data / 8-parity range. Smaller local
sender group choices do not narrow the accepted wire range.

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
have delivered received systematic shards. Ordinary timer expiry without known dimensions emits no feedback. A newer
group can instead trigger the unknown-dimensions feedback described below. QUIC transport-loss counters are the current sender's secondary
signal for this case.

### 5.5 Feedback

On successful completion or expiration of a group whose `k` is known, the
receiver may send one feedback record:

- `total` is the group's `k`;
- `missing` is the number of source shards absent when the group was resolved;
- `recovered` is the subset reconstructed successfully; and
- `epoch` and group ID copy the data group.

For dimensioned groups, `1 <= total <= 32` and
`0 <= recovered <= missing <= total`. There is also an existing zero-total
form: `total=0`, `recovered=0`, and `missing=0` or `1`. The form `(0,1,0)`
means dimensions never arrived and a bounded unrecovered-loss indication is
being reported; it does not mean a one-source group. Receivers MUST accept
these forms and reject other counter relationships. Empty payload and zero
unused fields remain canonical. Feedback is
best-effort, is not retransmitted, and may itself be lost. The sender applies
feedback only when its epoch matches the current FEC epoch.

The current receiver keeps at most 1,024 incomplete FEC groups per QUIC
session, expires them after 3 seconds, polls expiration every 500 ms, and keeps
up to 4,096 just-completed groups while deferring feedback for a 10 ms
reordering grace period (actual emission may wait until the next packet or
500 ms poll). During that grace, late originals already recovered are not
redelivered, and their missing/recovered counts are reduced. The completed
entry is removed when feedback is emitted; duplicate suppression is not
permanent. WireGuard must still reject replayed transport counters.

An existing receiver may discard an incomplete group when the newest observed
group ID is at least 4 greater, emitting dimensioned loss feedback or the
`(total=0,missing=1,recovered=0)` form. QUIC DATAGRAMs can in fact reorder;
this is an aggressive deployed receiver heuristic, not an ordering guarantee.
Senders targeting this receiver SHOULD have at most four concurrently open
groups, emit group IDs in increasing order at allocation, and close groups
promptly. A receiver may use the full 3-second timeout instead. The local
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

The deployed receiver validates feedback counters as specified in section 5.5.
Some unused header fields remain lenient. A sender MUST emit canonical records;
implementations MUST NOT depend on another receiver ignoring invalid fields.

## 9. Inner WireGuard handshake and transport

Implementing the outer QUIC handshake alone is insufficient. This section
specifies the inner messages carried as the *entire* payload reconstructed from
WGQ1. An existing standard WireGuard engine may supply/consume these messages;
a new implementation can use the algorithms below. There is no extra wg-quic
signature, public-key announcement, challenge, connection ID or login packet.

### 9.1 Primitives and state

Let `S_i,s_i` and `S_r,s_r` be static public/private keys of initiator and
responder, and `E_i,e_i` / `E_r,e_r` their fresh ephemeral keypairs. Uppercase
names denote public keys. Static keys are provisioned; each handshake uses new
cryptographically random 32-byte ephemeral private keys. `DH(s,P)` is X25519
as defined in section 3. Reject all-zero DH results.

Define:

- `H(x)`: unkeyed BLAKE2s with 32-byte output.
- `MAC(k,x)`: keyed BLAKE2s with **16-byte output parameter**, not HMAC and not
  truncated BLAKE2s-256.
- `HM(k,x)`: HMAC using BLAKE2s-256 and a 64-byte hash block size. For a key
  longer than 64 bytes replace it with `H(key)`; zero-pad shorter keys to 64.
  `HM(k,x)=H((k XOR 0x5c*64) || H((k XOR 0x36*64) || x))`.
- `KDF(c,x,n)`: `t=HM(c,x)`; `t1=HM(t,01)`;
  `t2=HM(t,t1||02)`; `t3=HM(t,t2||03)`. Return the first `n` outputs.
  The bytes `01`, `02`, `03` are single bytes, not characters.
- `Seal(k,n,p,a)`: RFC 8439 ChaCha20-Poly1305 with 32-byte key, nonce
  `00000000 || u64_le(n)`, plaintext `p`, associated data `a`, returning
  ciphertext followed by its 16-byte tag. `Open` authenticates before returning
  plaintext. A failure MUST discard the message without advancing state.
- `PSK`: provisioned PSK, or 32 zero bytes if absent.

The chain key `c`, transcript hash `h`, AEAD keys and session keys are 32 bytes.
An index is a randomly chosen local `u32` not in use by another retained
handshake/session. Index zero has no special wire meaning. Keep separate
handshake state per attempt; do not confuse WireGuard receiver indices with
QUIC connection IDs or WGQ1 packet IDs.

### 9.2 Message layouts

All integer fields in this table are **little-endian**. The type is a single
byte followed by three zero bytes (equivalently the indicated `u32_le`).

| Message | Exact layout, `offset:length` |
| --- | --- |
| Initiation, type 1, 148 bytes | `0:4 type`, `4:4 sender index I`, `8:32 E_i`, `40:48 encrypted S_i`, `88:28 encrypted timestamp`, `116:16 mac1`, `132:16 mac2` |
| Response, type 2, 92 bytes | `0:4 type`, `4:4 sender index R`, `8:4 receiver I`, `12:32 E_r`, `44:16 encrypted empty`, `60:16 mac1`, `76:16 mac2` |
| Cookie reply, type 3, 64 bytes | `0:4 type`, `4:4 receiver`, `8:24 XChaCha nonce`, `32:32 encrypted cookie` |
| Transport, type 4, at least 32 bytes | `0:4 type`, `4:4 receiver`, `8:8 counter`, `16:rest encrypted inner packet plus tag` |

Reject unsupported types, nonzero reserved bytes, wrong fixed lengths and
truncated transport messages. Outer fragmentation does not alter these bytes.

### 9.3 Initiation

The initiator initializes and sends:

```text
c = H("Noise_IKpsk2_25519_ChaChaPoly_BLAKE2s")
h = H(c || "WireGuard v1 zx2c4 Jason@zx2c4.com")
h = H(h || S_r)
E_i = X25519(e_i, basepoint_9)
c = KDF(c, E_i, 1)[0]
h = H(h || E_i)
(c, key) = KDF(c, DH(e_i, S_r), 2)
enc_static = Seal(key, 0, S_i, h)
h = H(h || enc_static)
(c, key) = KDF(c, DH(s_i, S_r), 2)
enc_timestamp = Seal(key, 0, timestamp, h)
h = H(h || enc_timestamp)
body = u32_le(1) || u32_le(I) || E_i || enc_static || enc_timestamp
mac1 = MAC(H("mac1----" || S_r), body)
mac2 = 16 zero bytes, unless a live cookie exists (section 9.7)
initiation = body || mac1 || mac2
```

`basepoint_9` is `09` followed by 31 zero bytes. The 12-byte timestamp is
`u64_be(0x400000000000000a + Unix_seconds) || u32_be(nanoseconds)`.
Nanoseconds are in `[0,999999999]`. The deployed sender clears the low 24 bits
of nanoseconds to reduce timing precision. Ensure a later attempt has a later
timestamp; do not use the deterministic vector's timestamp in a live network.

The responder verifies `mac1` using its own static public key before expensive
work and may require `mac2` under load. It repeats the transcript/chain steps,
using `DH(s_r,E_i)` to decrypt the initiator static key. It then looks up that
static key in its configured peers; unknown peers MUST be rejected. Next use
`DH(s_r,S_i)` to authenticate/decrypt the timestamp. The timestamp MUST be
lexicographically greater than the last accepted timestamp for this peer. A
current responder also rate-limits accepted initiations from the same peer to
at most 50 per second. Only after these checks commit `c,h,E_i,I` and the latest
timestamp. An invalid packet must not replace authenticated peer state.

### 9.4 Response and confirmation

Starting from the committed initiation state, the responder computes:

```text
E_r = X25519(e_r, basepoint_9)
h = H(h || E_r)
c = KDF(c, E_r, 1)[0]
c = KDF(c, DH(e_r, E_i), 1)[0]
c = KDF(c, DH(e_r, S_i), 1)[0]
(c, tau, key) = KDF(c, PSK, 3)
h = H(h || tau)
enc_empty = Seal(key, 0, empty_bytes, h)
h = H(h || enc_empty)
body = u32_le(2) || u32_le(R) || u32_le(I) || E_r || enc_empty
mac1 = MAC(H("mac1----" || S_i), body)
response = body || mac1 || mac2
(K_i_to_r, K_r_to_i) = KDF(c, empty_bytes, 2)
```

`mac2` is zero or calculated from a cookie as below. The initiator finds the
outstanding handshake by receiver index `I`, checks `mac1`, performs the same
steps using `DH(e_i,E_r)` and `DH(s_i,E_r)`, and authenticates `enc_empty`.
Only on successful verification does it install the two transport keys. Each
direction's send counter starts at zero. Erase the ephemeral private keys,
chain key, transcript hash and temporary AEAD keys when no longer needed.

The initiator immediately sends an authenticated type-4 message, using the
new initiator-to-responder key and receiver index `R`. If there is no pending
IP packet, send an empty keepalive. This is key confirmation: the responder
holds new keys as pending and MUST wait for a valid type-4 message using them
before treating that session as established for outbound traffic. It can use
a still-valid previous keypair while waiting. Successful TLS establishment is
not a substitute for this confirmation.

### 9.5 Transport messages and inner delivery

To send an IP packet `p`:

```text
counter = next_send_counter; next_send_counter += 1
padded = p || zero padding to a multiple of 16 bytes
header = u32_le(4) || u32_le(remote_receiver_index) || u64_le(counter)
packet = header || Seal(direction_key, counter, padded, empty_bytes)
```

The 16-byte transport header is **not** AEAD associated data. The receiver
index selects the key; the counter supplies the nonce. The deployed sender
caps padding at the configured inner MTU, so a receiver MUST also accept
non-multiple-of-16 plaintext lengths. An empty keepalive has no plaintext and
is exactly 32 bytes including header/tag. Never reuse a counter under a key.

The receiver authenticates before committing replay-window changes. Keep a
sliding replay window per receive key, supporting out-of-order counters while
rejecting duplicates and counters too old for the window. The deployed window
is approximately 8192 counters. Reject counter values at or above
`2^64 - 2^13 - 1` and keys aged 180 seconds or more. A new keypair starts fresh
counters and a fresh replay window. Keep the current/previous pair and a
pending responder pair only as long as needed for orderly rekeying.

After decryption, empty plaintext is a keepalive. Otherwise inspect the IP
version nibble: IPv4's total length at offsets 2..3 (big-endian) or IPv6's
payload length at offsets 4..5 plus its 40-byte header determines the delivered
length. Verify minimum header size and length within decrypted plaintext;
strip padding using the IP length, never by trimming zero bytes. Reject unknown
versions, invalid lengths, and an inner source address outside the authenticated
peer's allowed prefixes. Outbound destination-to-peer selection is local
routing policy. It MUST NOT replace inbound source validation.

An outer remote-address change can update the peer's endpoint only after
WireGuard authentication and replay checks. Keep QUIC path validation active.
Reconnecting QUIC does not by itself reset WireGuard keys/counters; conversely,
WireGuard rekeying can happen inside an existing QUIC connection.

### 9.6 Loss, timers and simultaneous handshakes

Handshake initiation/response/cookie and transport messages all use the same
WGQ1/WGQF path; none is sent directly as plain UDP or as a reliable QUIC stream.
Do not add an application ACK, duplicate a Noise handshake deliberately, or
wait for FEC feedback before passing a received systematic shard to WireGuard.

For interoperable WireGuard behavior: retry an unanswered initiation after
5 seconds plus random jitter 0..333 ms, with fresh ephemeral key and timestamp.
Stop retrying after 90 seconds until new outbound work arrives. Rekey after
sending `2^60` messages or, as handshake initiator, when a key is 120 seconds
old and sending data; an initiator receiving traffic should rekey by 105
seconds. Reject keys at 180 seconds, and erase unused key material after 540
seconds without replacement. These intervals are seconds, not milliseconds.

After receiving data, if no outgoing packet follows within 10 seconds, send an
empty keepalive. After sending data with no reply for 15 seconds, attempt a new
handshake. A separately configured persistent-keepalive interval may maintain
NAT state; it is not a wire field. QUIC's own keepalive is distinct.

Allow inbound handshakes even while an outbound attempt is pending. Use receiver
indices and transcript state to choose the right attempt, never outer UDP
address alone. Multiple QUIC connections may transiently carry the same peer;
WireGuard replay protection remains per key, not per QUIC connection.

### 9.7 Cookies under load

A recipient with a valid `mac1` but no acceptable `mac2` may send a type-3 cookie
reply rather than doing a handshake. A cookie is a 16-byte secret tied to the
source endpoint. A responder can derive it as `MAC(rotating_secret, source)`
with a random 32-byte secret rotated every 120 seconds. `source` is its local
binary representation of the observed IP and UDP port; the other endpoint never
recomputes this value, so that private representation is not an interop field.

Cookie reply fields:

```text
receiver = sender index of the triggering initiation or response
cookie_key = H("cookie--" || cookie_sender_static_public_key)
nonce = 24 cryptographically random bytes
encrypted_cookie = XSeal(cookie_key, nonce, cookie, triggering_mac1)
reply = u32_le(3) || u32_le(receiver) || nonce || encrypted_cookie
```

`XSeal` is XChaCha20-Poly1305: derive a subkey with HChaCha20 using the original
key and first 16 nonce bytes, then use RFC 8439 AEAD with that subkey and nonce
`00000000 || nonce[16:24]`. HChaCha20 initializes ChaCha's 16 little-endian
32-bit words with the four constants `61707865 3320646e 79622d32 6b206574`,
eight key words and four nonce words; run the standard 20 ChaCha rounds,
without the final addition of the initial state, and serialize words
`0,1,2,3,12,13,14,15` little-endian as the subkey. The AEAD associated data is
the 16-byte `mac1` of the triggering message, not the entire handshake.

A cookie receiver locates the outstanding attempt using `receiver`, derives
`cookie_key` from its configured remote public key and decrypts using its saved
last sent `mac1`. On success cache the cookie for at most 120 seconds. When
sending the next handshake message to that peer, compute:

```text
mac2 = MAC(cookie, body || mac1)
```

This cookie mechanism is inside the WireGuard datagram and unrelated to QUIC
Retry tokens. The cookie reply itself is wrapped in WGQ1/FEC/QUIC/Salamander.
A full implementation must handle cookie replies even if its own responder
never enables cookie-based load shedding.

## 10. Embedded interoperability vectors

All strings in this JSON are hexadecimal bytes; matrix entries are ordinary
integer byte values. Concatenate a string's digits without whitespace. These
are synthetic public test secrets only; NEVER use them as deployment keys.
No external fixture file is needed.

- `salamander` uses private/static keys A and B, the shown PSK and salt. Both
  directions derive `key`. `key_absent_psk` and `key_zero_psk` intentionally
  differ. `plain` is a short artificial QUIC-shaped input for testing the
  envelope, **not** a complete valid QUIC Initial.
- `noise` reuses the static keys and PSK from `salamander`, with the specified
  fresh ephemeral inputs, initiator index `0x01020304`, responder index
  `0x05060708`, Unix timestamp 1700000000 and zero nanoseconds. Both MAC2 fields
  are zero. `keepalive` is the first initiator-to-responder transport message,
  counter zero. `framed_initiation` is one WGQ1 frame, packet ID 1.
- `fragments` wraps dummy payload `010203040506070809` into three fragments;
  accept order 2,0,1 and reconstruct the original bytes. Dummy payloads in this
  and the FEC vector test outer framing, not WireGuard authentication.
- `fec` is epoch 1, group 1, `k=3,r=2`. Data shard lengths differ and `shards`
  includes zero padding for coding. `data` contains unpadded on-wire data
  records. Feed only `data[0]`, `parity[0]`, `parity[1]`: recover frames 1 and 2
  without redelivering frame 0, and emit the shown dimensioned `feedback` after
  grace. `close` is the complete close record. The zero-total feedback vector
  demonstrates the separate unknown-dimensions form.

```json
{
  "salamander": {
    "private_a": "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
    "public_a": "8f40c5adb68f25624ae5b214ea767a6ec94d829d3d7b5e1ad1ba6f3e2138285f",
    "private_b": "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f",
    "public_b": "358072d6365880d1aeea329adf9121383851ed21a28e3b75e965d0d2cd166254",
    "shared": "9663aa1da97e848a914a436d04163dfbb89178f107f1b5b77ed3854203382854",
    "psk": "a0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf",
    "key": "a5432c4f3449673fc9be625ff0881346cbf1b4172ba6378661e84a9ab2a34ecc",
    "key_absent_psk": "2b08a7e0e61d73525db836a38cb892254b0c3204e85931b3b656c978e10e514b",
    "key_zero_psk": "f61f5c3e5848cc4ffca6047f229c0554974773051e0841808884861838a5acfa",
    "salt": "0102030405060708",
    "hint": "f71ea486b3282427",
    "stream": "a0d422732a082a7e9f143eb0dc1cd157eb3619b074ec6a6e3cdc14686d25fe8f",
    "plain": "c000000001080102030405060708",
    "wire": "0102030405060708f71ea486b328242760d422732b002b7c9c103bb6db14"
  },
  "noise": {
    "ephemeral_private_a": "404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f",
    "ephemeral_public_a": "79a631eede1bf9c98f12032cdeadd0e7a079398fc786b88cc846ec89af85a51a",
    "ephemeral_private_b": "606162636465666768696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f",
    "ephemeral_public_b": "675dd574ed7789310b3d2e7681f3790b466c773b1521fecf36577958371ea52f",
    "timestamp": "400000006553f10a00000000",
    "initiation": "010000000403020179a631eede1bf9c98f12032cdeadd0e7a079398fc786b88cc846ec89af85a51af0225eb421886af8f54fb31289d49dcd82b0f6f3279d40d7cff913fece0c64ad226eef3320755b393b72f16842c8d7ee78a74e937e3f2a828ff4ecc11547a9c7d1ee1b22fedf3178ecbaddb325e375e3dd7bb6b996692092d3d0deba00000000000000000000000000000000",
    "chain_after_initiation": "5b3722a25c7c69706cfbbef606c70681a400fd1b6dab82533b1266606d6ede42",
    "hash_after_initiation": "99419dec0d190b62da00339f2bf2675ea547e09e6e9971aa475f595ccf8f8f3c",
    "response": "020000000807060504030201675dd574ed7789310b3d2e7681f3790b466c773b1521fecf36577958371ea52f095f43d1e4ebc37fc2dfe187cffb0cae163bf89f3d35d219316b75498184af4900000000000000000000000000000000",
    "chain_final": "5bf7a0954c8634f08c37053098a30f50b64ba5aec671da1527472be2079a8fff",
    "hash_final": "db980f6a7dcde6d67276e39ab4e621eb48ffad8421bf06cd302f6b892993b153",
    "sending_key_a": "5f5adf1120d2983a88f9a4a510b2b5d4ab7f26c4d0ff3ed13bebc5221a65e1a6",
    "sending_key_b": "335840fe731665d85a1772dd9f3bea6702ce640b81af6b36d49594927251ec92",
    "keepalive": "04000000080706050000000000000000f6ad3e0ef17f7c448ee031cd18296c80",
    "framed_initiation": "574751310100000000000000010000000100000094010000000403020179a631eede1bf9c98f12032cdeadd0e7a079398fc786b88cc846ec89af85a51af0225eb421886af8f54fb31289d49dcd82b0f6f3279d40d7cff913fece0c64ad226eef3320755b393b72f16842c8d7ee78a74e937e3f2a828ff4ecc11547a9c7d1ee1b22fedf3178ecbaddb325e375e3dd7bb6b996692092d3d0deba00000000000000000000000000000000"
  },
  "fragments": [
    "574751310101020304050607080000000300000009010203",
    "574751310101020304050607080001000300000009040506",
    "574751310101020304050607080002000300000009070809"
  ],
  "fec": {
    "matrix": [
      [
        1,
        0,
        0
      ],
      [
        0,
        1,
        0
      ],
      [
        0,
        0,
        1
      ],
      [
        1,
        1,
        1
      ],
      [
        15,
        8,
        6
      ]
    ],
    "frames": [
      "574751310100000000000000010000000100000001aa",
      "574751310100000000000000020000000100000002bbcc",
      "574751310100000000000000030000000100000003ddeeff"
    ],
    "shards": [
      "0016574751310100000000000000010000000100000001aa0000",
      "0017574751310100000000000000020000000100000002bbcc00",
      "0018574751310100000000000000030000000100000003ddeeff"
    ],
    "data": [
      "5747514601000001000000000000000100000000000000180016574751310100000000000000010000000100000001aa",
      "5747514601000001000000000000000100010000000000190017574751310100000000000000020000000100000002bbcc",
      "57475146010000010000000000000001000200000000001a0018574751310100000000000000030000000100000003ddeeff"
    ],
    "parity": [
      "57475146010100010000000000000001000000030002001a0019574751310100000000000000000000000100000000cc22ff",
      "57475146010100010000000000000001000100030002001a003a5747513101000000000000001500000001000000150d7038"
    ],
    "close": "574751460102000100000000000000010000000300020000",
    "feedback": "574751460103000100000000000000010002000300020000",
    "unknown_dimensions_feedback": "574751460103000100000000000000010001000000000000"
  }
}
```

### 10.1 Negative vectors derived from the positive cases

Each transformation below must fail at the indicated layer, without delivery
to an inner network interface and without an unbounded allocation:

| Input transformation | Required outcome |
| --- | --- |
| Flip one bit of Salamander hint | No matching key; drop before QUIC |
| Flip one bit of the obfuscated ciphertext | QUIC authentication normally fails; Salamander alone has no payload MAC |
| Offer only ALPN `wg-quic/2` | No compatible ALPN; handshake fails |
| Set WGQ1 version to 2, count to 0, index equal to count, or total to 65536 | Drop malformed WGQ1 |
| Change WGQF payload-length field without changing payload | Drop as malformed WGQF, not as WGQ1 |
| Parity `k=33`, `r=9`, or index equal to `r` | Reject the dimensions/index |
| Same FEC group with a different epoch or inconsistent announced dimensions | Reject the conflicting record |
| Feedback `(total,missing,recovered)=(3,4,0)` or `(0,1,1)` | Reject invalid feedback |
| Flip initiation MAC1, encrypted static or timestamp bytes | Reject before committing peer handshake state |
| Use a different inner PSK in the response | `enc_empty` authentication fails |
| Replay the initiation after it was accepted | Reject non-increasing timestamp |
| Replay an authenticated type-4 counter after delivery | Drop as replay; no second inner packet |
| Valid encrypted IP with an unauthorized source prefix | Reject inner packet despite valid AEAD |

## 11. Independent implementation acceptance procedure

A successful test means both handshakes **and** bidirectional authenticated
inner delivery, not just a UDP socket, TLS connection or "connected" label.

1. Implement primitives and compare every value in section 10, including the
   matrix and recovery from two missing source shards. Check the negative
   cases before attempting network interoperability.
2. Provision fresh static keys A/B and reciprocal peer public keys. Use A inner
   IP `10.200.0.1/32`, B `10.200.0.2/32`, matching allowed peer prefixes, MTU
   1280, reachable UDP endpoints, initially `obfs=none`, `fec=off` for sending.
   Each receiver still supports WGQF. A may be an existing endpoint and B the
   new implementation. Begin with QUIC v1 and ALPN `wg-quic/1`.
3. Complete QUIC/TLS, send a freshly generated WireGuard initiation in WGQ1,
   receive a response, verify it, send key confirmation, and exchange valid
   inner IPv4 ICMP echo or UDP request/reply packets. Verify decrypted inner
   addresses/payloads, not just a recent handshake timestamp. Reverse which
   endpoint initiates and repeat.
4. Enable `obfs=salamander` at both ends. Test no PSK, then the same randomly
   generated nonzero PSK at both ends. Verify both envelope directions and full
   inner delivery. Deliberately mismatch obfuscation mode and PSK: neither may
   yield authenticated inner traffic. Restore agreement and verify recovery.
5. Enable an automatic-FEC sender at the existing endpoint while the independent
   sender remains raw. Accept raw, systematic, parity, close and feedback
   records interleaved in both directions. Induce source-shard loss within the
   FEC capacity and verify exact reconstruction; induce greater loss and verify
   bounded expiry with no fabricated plaintext or stalled unrelated groups.
6. Force WGQ1 fragmentation by choosing a smaller DATAGRAM payload budget; drop,
   reorder and duplicate fragments. Verify no partial packet delivery and no
   cross-connection reassembly. Repeat with FEC around individual fragments.
7. Exercise cookies, simultaneous dialing, idle periods, outer reconnect and
   WireGuard rekey. Keep traffic running beyond 180 seconds. Change the UDP
   source port/address and verify authenticated migration. Replay old transport
   packets and inject an authenticated packet with an unauthorized inner source.
8. Record version/ALPN, provisioned modes, packet size limits, which roles were
   tested, matching vector checks, bidirectional payload counts and observed
   failure behavior. Record any unimplemented capability explicitly. Do not
   call a WGQ1-only receiver or a client that ignores cookies fully compatible.

QUIC connection IDs, TLS randomness, certificates, packet numbers and timing
make a universal fixed full-network transcript inappropriate. The deterministic
vectors stop at the QUIC application/UDP-envelope boundaries; RFC-conforming
QUIC provides the transport between those boundaries. No vendor fork is needed.

## 12. Public standards and algorithm identities

These references identify general-purpose primitives and transport standards;
all application-specific constants and choices are specified in this document.

- [RFC 9000 — QUIC v1](https://www.rfc-editor.org/rfc/rfc9000.html),
  [RFC 9001 — TLS for QUIC](https://www.rfc-editor.org/rfc/rfc9001.html), and
  [RFC 9002 — QUIC loss detection and congestion control](https://www.rfc-editor.org/rfc/rfc9002.html).
- [RFC 9221 — QUIC DATAGRAM](https://www.rfc-editor.org/rfc/rfc9221.html);
  [RFC 9369 — QUIC v2](https://www.rfc-editor.org/rfc/rfc9369.html) is optional.
- [RFC 8446 — TLS 1.3](https://www.rfc-editor.org/rfc/rfc8446.html),
  [RFC 7748 — X25519](https://www.rfc-editor.org/rfc/rfc7748.html),
  [RFC 7693 — BLAKE2](https://www.rfc-editor.org/rfc/rfc7693.html), and
  [RFC 8439 — ChaCha20-Poly1305](https://www.rfc-editor.org/rfc/rfc8439.html).
- [WireGuard protocol overview](https://www.wireguard.com/protocol/) and
  [technical paper](https://www.wireguard.com/papers/wireguard.pdf) describe the
  standard inner protocol. Section 9 supplies the exact inner layouts and
  calculations needed here; no WireGuard implementation language is required.
