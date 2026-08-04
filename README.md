# wg-quic

Repository: <https://github.com/RC-CHN/wg-quic>

`wg-quic` carries complete encrypted WireGuard datagrams over QUIC DATAGRAM
frames. Its WireGuard userspace cryptographic and peer state machine is a
pinned in-repository fork under `third_party/wireguard-go`; production code and
behavior tests no longer download `golang.zx2c4.com/wireguard`.

The usable runtime is currently Linux. A FreeBSD host backend is present and
cross-builds for amd64 and arm64, but still needs QEMU runtime validation and
rc.d packaging. Windows integration is deferred. All platforms share the same
userspace WireGuard, QUIC, FEC, obfuscation, and configuration core.

`wg-quic` accepts ordinary `wg-quick` INI configuration files. Both peers must
run `wg-quic`; it is not wire-compatible with a stock WireGuard UDP endpoint.

By default, every outer QUIC UDP packet uses the built-in Salamander-style
obfuscation profile. Its per-peer key is derived from the existing WireGuard
private/public key pair with X25519. If a peer already has a WireGuard
`PresharedKey`, that key is mixed into the derivation as well. No transport
password or extra configuration field is required.

## Development

```sh
go test ./...
make test-wireguard
make test-transport
./tests/container/test.sh
go build ./cmd/wg-quic
```

The container test leaves the host route table and DNS untouched. It creates
isolated privileged nodes with separate Linux network namespaces and real TUN
devices. The GitHub Actions gate covers IPv4 and IPv6 inner/outer paths,
TCP/UDP, large packets, carrier MTU, loss/FEC, reordering, NAT rebinding, and
peer restart recovery. See
[`tests/WIREGUARD-FORK.md`](tests/WIREGUARD-FORK.md) for the mapping to
the pinned WireGuard fork and its imported test suite.

Check a configuration without changing the host:

```sh
go run ./cmd/wg-quic check /etc/wireguard/wg0.conf
```

Run one tunnel in the foreground:

```sh
sudo go run ./cmd/wg-quic run /etc/wireguard/wg0.conf
```

The implementation and tests are under active development. See the local
`design/architecture.md` when the design checkout is present.
