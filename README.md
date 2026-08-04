# wg-quic

Repository: <https://github.com/RC-CHN/wg-quic>

`wg-quic` carries complete encrypted WireGuard datagrams over QUIC DATAGRAM
frames. Its WireGuard userspace cryptographic and peer state machine is a
pinned in-repository fork under `third_party/wireguard-go`; production code and
behavior tests no longer download `golang.zx2c4.com/wireguard`.

The fully exercised runtime is currently Linux. The `wg-quic-quick` host-policy
layer also builds and runs natively on FreeBSD and includes an rc.d service
script, but the FreeBSD data plane still needs QEMU runtime validation. Windows
now has a CLI-only Wintun, host-network, Named Pipe, and per-tunnel SCM
implementation; its privileged data plane still needs Windows VM integration
testing. All platforms share the same userspace WireGuard, QUIC, FEC,
obfuscation, and configuration core.

The command boundary mirrors WireGuard's daemon/`wg-quick` split:

- `wg-quic` owns only the TUN-backed userspace data plane, local status socket,
  configuration validation, and key utilities;
- `wg-quic-quick` owns addresses, routes, DNS, hooks, and platform service
  management. It starts and supervises the separate `wg-quic` executable; it
  does not import or embed the core package.

QUIC is also isolated below the WireGuard bind adapter:
`internal/transport/quic` owns quic-go, outer TLS, UDP sockets, socket marks,
and Salamander PacketConn wiring, while `internal/bind` sees only opaque
datagram connections.

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
make build
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
go run ./cmd/wg-quic-quick check /etc/wg-quic/wg0.conf
```

Run one complete tunnel in the foreground:

```sh
sudo go run ./cmd/wg-quic-quick run /etc/wg-quic/wg0.conf
```

For service-managed Linux installations:

```sh
sudo wg-quic-quick up wg0
wg-quic show wg0
sudo wg-quic-quick down wg0
```

`wg-quic run` is the deliberately lower-level core entry point. It creates and
starts the userspace device but does not assign addresses, install routes,
configure DNS, or run hooks.

`SaveConfig=true` is rejected by `wg-quic-quick` instead of being silently
ignored. There is no runtime configuration mutation to persist yet.

Bare interface names resolve under `/etc/wg-quic/` on Linux and
`/usr/local/etc/wg-quic/` on FreeBSD. Explicit paths remain supported, but the
defaults are intentionally separate from stock WireGuard because the on-wire
protocols are not compatible.

On FreeBSD, install `packaging/freebsd/wg_quic` under
`/usr/local/etc/rc.d/`, put profiles in `/usr/local/etc/wg-quic/`, and set
`wg_quic_interfaces` in `rc.conf`. The same quick commands then use rc.d:

```sh
sudo wg-quic-quick up wg0
sudo wg-quic-quick down wg0
```

Windows remains CLI-only and preserves the same two-program boundary:
`wg-quic.exe` owns the Wintun data plane, while `wg-quic-quick.exe` owns host
policy and a per-tunnel Windows service. Put profiles under
`%ProgramData%\wg-quic\interfaces\` and run from an elevated terminal:

```powershell
wg-quic-quick.exe check wg0
wg-quic-quick.exe up wg0
wg-quic.exe show wg0
wg-quic-quick.exe down wg0
```

The quick service runs as LocalSystem, supervises a separate sibling
`wg-quic.exe`, and reports readiness through an Administrators/System-only
Named Pipe. Its route plan pins every resolved QUIC endpoint through the
pre-tunnel default route before installing AllowedIPs, including full-tunnel
defaults. Windows Wintun, SCM, route, DNS, and cleanup behavior is not yet
claimed VM-validated.

`make build-windows` creates self-contained amd64 and arm64 test directories
under `build/`. Each contains both executables, an unmodified official signed
Wintun 0.14.1 DLL, its original license, checksums, and a LAN test guide.
GitHub Actions uploads the same directories as the `wg-quic-windows` artifact.
On Windows, `wg-quic-quick debug wg0` runs the tunnel in the foreground and
writes a key-redacted diagnostic log under `%ProgramData%\wg-quic\logs`.

## Release archives

Every `v*` tag is gated again on native Linux, Windows, and FreeBSD tests plus
the privileged Linux tunnel matrix. CD then publishes amd64 and arm64 archives
for Linux, FreeBSD, and Windows together with one top-level `SHA256SUMS`.
Windows archives include the matching official Wintun DLL and its license.

To build and validate the same six archives locally:

```sh
make release-artifacts VERSION=0.1.0
./scripts/check-release-archive.sh \
  dist/wg-quic-v0.1.0-linux-amd64.tar.gz linux amd64 0.1.0
```

The implementation and tests are under active development. See the local
`design/architecture.md` when the design checkout is present.
