# Pinned WireGuard fork behavior coverage

`wg-quic` maintains its WireGuard userspace core in
`third_party/wireguard-go`. Production code directly imports this directory;
there is no `golang.zx2c4.com/wireguard` module dependency or `replace`
directive.

The initial source and complete Go test suite came from upstream revision
`ecfc5a8d5446`
(`v0.0.0-20260522210424-ecfc5a8d5446`). Exact provenance, license, and local
changes are recorded in `third_party/wireguard-go/ORIGIN.md`.

## In-repository Go tests

The root test command includes every fork package and every test selected by
the current operating system's Go build constraints:

```sh
go test ./...
```

CI executes that same command natively on Linux, Windows, and FreeBSD. The
fork's platform-neutral checksum test uses the numeric TCP protocol constant
`6`, fixing the imported revision's Windows-only test compilation failure
inside the maintained test source itself. No CI-time source patching occurs.

## Privileged `netns.sh` behavior

The imported `third_party/wireguard-go/tests/netns.sh` cannot run byte-for-byte
against `wg-quic`: it starts the stock compatibility daemon and reconfigures it
dynamically with `wg(8)` UAPI, while `wg-quic` intentionally starts from a
wg-quick-compatible file and uses a different carrier. Its relevant behavior
is pinned in `tests/container/test.sh`, with all network mutations confined to
privileged Docker containers.

| Upstream behavior | wg-quic evidence |
| --- | --- |
| Bidirectional IPv4 and IPv6 tunnel traffic | `ping`/`ping6` in both directions |
| IPv4 and IPv6 outer endpoints | independent IPv4-outer and IPv6-outer container pairs |
| TCP over IPv4 and IPv6 | bounded `iperf3` transfers |
| UDP over IPv4 and IPv6 | bounded `iperf3` transfers |
| Large interface MTU | unfragmented 12,000-byte inner ICMP packets |
| Carrier PMTU below inner MTU | the same large packet with outer MTU 1280 |
| RX/TX counters | nonzero bidirectional status assertions |
| AllowedIPs reverse-path filtering | an authenticated packet with an unallowed inner source must time out |
| Endpoint roaming / NAT rebinding | live QUIC path migration after outer source-address SNAT |
| Persistent traffic behind NAT | one-second WireGuard keepalive is configured on the NAT test pair |
| Peer disappearance and reconnect | one peer process is restarted while the other remains running |
| Source-address stickiness | imported `conn/sticky_linux_test.go` plus the NAT migration test |
| Large configuration split/truncation | 130,560-prefix parser/UAPI serialization regression test |
| Device resource cleanup | fork device tests plus container teardown and peer restart |

The container suite additionally exercises wg-quic-specific behavior absent
from stock WireGuard: QUIC DATAGRAM transport, key-derived Salamander
obfuscation, fragmentation/reassembly, systematic adaptive FEC, 10% random
loss, and asymmetric delay/loss/reordering/duplication.

## Which tests use ArmorBind

The imported fork tests intentionally retain their original unit boundaries:
crypto and device tests use fake/channel binds or `StdNetBind`; conn and TUN
tests exercise those fork components directly. They do not masquerade as
wg-quic transport tests.

Two explicit gates cover the production transport:

```sh
make test-transport
./tests/container/test.sh
```

`make test-transport` drives the in-repository WireGuard `Device` through
ArmorBind with QUIC DATAGRAM, adaptive FEC, key-derived Salamander, and
WireGuard PresharedKeys. Its pinned upper-layer behavior matrix covers:

- bidirectional plaintext and nonzero WG, wire, FEC-data, and FEC-parity
  counters;
- IPv4 and IPv6 datagrams from empty payloads through 12,000-byte payloads;
- concurrent bidirectional traffic with paced injection, while a focused Bind
  regression independently forces simultaneous endpoint dialing;
- AllowedIPs reverse-path rejection without wedging the valid path;
- mismatched WireGuard PresharedKeys over a separately verified working
  Salamander transport;
- cryptokey routing from one WireGuard device to two distinct peers.

The dedicated `internal/transport/quic` carrier test additionally asserts that
`quic.Transport.Conn` resolves to the obfuscating PacketConn rather than the
raw UDP socket. The adapter intentionally disables QUIC GSO/GRO and ECN OOB
paths until their segment metadata can be rewritten for Salamander's
per-datagram header. A negative ArmorBind integration test configures different
Salamander keys and requires the QUIC handshake and plaintext delivery to fail.

The extended large-packet and concurrent behavior matrix currently runs on
Linux and FreeBSD. Windows is intentionally behind those targets: its native CI
still executes every Windows-applicable fork test, the ArmorBind round-trip,
simultaneous-dial and abrupt-restart regressions, and the bidirectional
WireGuard-over-QUIC/FEC/Salamander smoke test. It also executes the Windows
ACL Named Pipe status transport, quick SCM state machine, and host-network plan
tests repeatedly, then builds and smokes both amd64 CLIs and cross-builds both
arm64 CLIs. A privileged hosted-runner gate additionally exercises a real
LocalSystem service, Wintun adapter, interface address, MTU and DNS policy,
AllowedIPs route, outer endpoint route lease, Named Pipe status, and full
teardown.
Windows remains behind the two-node targets only for end-to-end traffic across
independent network stacks.

The container suite repeats the production path with four separate Linux
nodes and network namespaces, real TUN devices, and network impairment. Each
node runs `wg-quic-quick` as the host-policy supervisor and a distinct
`wg-quic` core child process.
GitHub Actions names this the `Two-node Linux tunnel interoperability` gate;
the primary A/B pair is checked to have distinct network namespaces before any
tunnel assertion. A6/B6 independently verify IPv6 outer endpoints.

GitHub-hosted jobs do not provide a native rendezvous mechanism that turns two
ephemeral job runners into one private test LAN. A future cross-physical-host
nightly gate can use two restricted self-hosted runners or an externally
managed private overlay. The deterministic per-commit gate intentionally keeps
both isolated nodes inside one disposable hosted runner.
