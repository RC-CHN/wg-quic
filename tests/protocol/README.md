# Standalone protocol conformance

The normative, self-contained document is
[`docs/WG-QUIC-PROTOCOL.md`](../../docs/WG-QUIC-PROTOCOL.md). Its JSON block is
the fixture: independent implementers need only that document.

`check_vectors.py` is an independent calculation of its cryptography and
Reed-Solomon matrix using Python 3 and the `cryptography` package. It imports no
project implementation. `--emit` prints calculated values for review; normal
execution compares them to the embedded document.

```sh
python3 tests/protocol/check_vectors.py
go test ./internal/transport/fec ./third_party/wireguard-go/device -run StandaloneProtocol -count=1
```

The Go tests consume those embedded vectors with the deployed Noise engine
and FEC encoder/decoder, including both handshake messages, key confirmation,
MAC1, replay rejection and reconstruction of two missing source shards.
The device test opens local UDP sockets; it does not require a TUN device.

## Independent Rust network probe

`rust-interop` is a standalone Rust crate. It uses standard Quinn/Rustls for
QUIC/TLS and standard cryptographic primitives; Noise IKpsk2, Salamander,
WGQ1 fragmentation and WGQF/GF(256) reconstruction are implemented from the
wire document. It neither imports Go code nor invokes a WireGuard/wg-quic
binary. `Cargo.lock` fixes its dependency versions. Only the vector unit tests
read the protocol document, not implementation sources or generated fixtures.

The Go test harness is the opposite endpoint under test: it instantiates the
current production bind and WireGuard engine with an in-memory TUN, generates
fresh keys, creates a mode-0600 temporary Rust configuration, and starts the
Rust process. No root, system TUN, route changes, production firewall, or saved
private key is required. Local UDP sockets must be permitted.

```sh
make test-protocol-interop
```

This runs Rust formatting, Clippy, deterministic document-vector tests, then
six actual QUIC + WireGuard network cases:

| Rust role | Outer modes | PSK | Injected loss | Result on 2026-09-28 |
| --- | --- | --- | --- | --- |
| Initiator | none / FEC off | absent | none | Passed |
| Responder | none / FEC off | absent | none | Passed |
| Initiator | Salamander / Go automatic FEC | absent | none | Passed |
| Responder | Salamander / Go automatic FEC | absent | none | Passed |
| Initiator | Salamander / Go automatic FEC | random | first received FEC source shard | Passed; one shard recovered |
| Responder | Salamander / Go automatic FEC | random | first received FEC source shard | Passed; one shard recovered |

Every case verifies QUIC establishment, WireGuard response and key confirmation,
Go-to-Rust authenticated 1280-byte inner IP delivery, Rust-to-Go authenticated
inner IP delivery, and Go's recorded handshake. Rust deliberately emits
64-byte fragment payloads to exercise reassembly. Automatic-FEC cases assert
both source and parity processing; loss cases assert a nonzero recovery count.
Unit vectors separately reconstruct two missing sources and reject timestamp
and transport replays. CI's `protocol-interop` job reruns these checks.

The probe is deliberately limited: one peer, loopback UDP, IPv4 test packets,
raw-only sending with full FEC receiving, and a run shorter than 25 seconds.
It implements cookie consumption but network cookie load shedding, long-lived
rekey, IPv6 inner traffic, roaming, simultaneous dialing, production resource
hardening and automatic FEC sending are not covered by these six cases.
It is an executable interoperability check, not a deployable VPN client or a
claim of full acceptance-checklist completion. No new wire version is introduced.
