# Upstream WireGuard behavior coverage

`wg-quic` pins `golang.zx2c4.com/wireguard` to the exact version declared in
`go.mod`. The WireGuard cryptographic state machine, AllowedIPs lookup, replay
window, cookie handling, TUN integration, and peer lifecycle are not forked.

## Exact upstream Go tests

`scripts/test-upstream-wireguard.sh` checks the pinned module version and runs:

```sh
go test -count=1 golang.zx2c4.com/wireguard/...
```

This executes every upstream test selected by Go build constraints for the
current operating system. CI runs it natively on Linux, Windows, and FreeBSD,
so platform-specific test files are selected on their actual target OS.

The pinned upstream revision has one Windows-only test build defect:
`tun/checksum_test.go` refers to `x/sys/unix.IPPROTO_TCP`, although the
identical constant on Windows is `x/sys/windows.IPPROTO_TCP`. The Windows CI
script copies the pinned module to an ephemeral directory, verifies that exact
source shape, substitutes only the platform package name, and then runs
`go test -count=1 ./...`. Neither the module cache nor wg-quic's dependency is
modified.

## Privileged `tests/netns.sh` behavior

The upstream shell script cannot run byte-for-byte against `wg-quic`: it starts
the `wireguard-go` compatibility daemon and reconfigures it dynamically with
the stock `wg(8)` UAPI, while `wg-quic` intentionally starts from a
wg-quick-compatible file and uses a different carrier. The relevant behavior
is therefore ported to `tests/container/test.sh`, with all network mutations
confined to privileged Docker containers.

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
| Source-address stickiness | exact upstream `conn/sticky_linux_test.go` plus the NAT migration test |
| Large configuration split/truncation | 130,560-prefix parser/UAPI serialization regression test |
| Device resource cleanup | upstream device tests plus container teardown and peer restart |

The container suite additionally exercises wg-quic-specific behavior absent
from stock WireGuard: QUIC DATAGRAM transport, key-derived Salamander
obfuscation, fragmentation/reassembly, systematic adaptive FEC, 10% random
loss, and asymmetric delay/loss/reordering/duplication.
