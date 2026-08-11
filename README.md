# wg-quic

Repository: <https://github.com/RC-CHN/wg-quic>

`wg-quic` carries complete encrypted WireGuard datagrams over QUIC DATAGRAM
frames. Its WireGuard userspace cryptographic and peer state machine is a
pinned in-repository fork under `third_party/wireguard-go`; production code and
behavior tests no longer download `golang.zx2c4.com/wireguard`.
The complete quic-go v0.61.0 source and tests are likewise pinned under
`third_party/quic-go`. The root product module uses that nested upstream module
through a local `replace` and `go.work`; production builds do not substitute a
module-cache or `references/` copy.

The broadest exercised runtime is currently Linux. The FreeBSD data plane and
`wg-quic-quick` host-policy layer are also QEMU-validated through the OPNsense
plugin on FreeBSD 14 and 15, and an rc.d service script is included. Windows
has a Wintun, host-network, Named Pipe, and per-tunnel SCM implementation with
a Tauri desktop shell; hosted CI validates the installed UI's elevation
boundary plus its privileged Wintun, LocalSystem service, address, MTU, DNS,
route, status, and cleanup lifecycle. All platforms share the same userspace
WireGuard, QUIC, FEC,
obfuscation, and configuration core.

The command boundary mirrors WireGuard's daemon/`wg-quick` split:

- `wg-quic` owns only the TUN-backed userspace data plane, local status socket,
  configuration validation, and key utilities;
- `wg-quic-quick` owns addresses, routes, DNS, hooks, and platform service
  management. It starts and supervises the separate `wg-quic` executable; it
  does not import or embed the core package.

The supervised startup configuration also has one owner:
`wg-quic-quick` parses and validates the `.conf` once, resolves automatic host
policy, and sends a versioned immutable configuration snapshot to the core over
the child's standard input. Private and preshared keys do not enter process
arguments, and the supervised core does not reread the path.

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
make test-quic
./tests/container/test.sh
make build
```

Controlled performance and loss measurements use a separate two-node fixture
that keeps all TUN, route, and `tc netem` state inside containers:

```sh
make benchmark-smoke
make benchmark-transports
make benchmark-ceiling
make benchmark-loss
make benchmark-profiles
make benchmark-bandwidth
make benchmark-protocol
```

It compares direct userspace WireGuard with all four wg-quic FEC/obfuscation
combinations, and supports asymmetric custom links, built-in
LAN/Wi-Fi/cellular/satellite profiles, runtime link changes, outer-path
baselines, per-interval controller sampling, protocol signature drop/police
controls, and FEC/QUIC/CPU/wire counters. See
[`tests/benchmark/README.md`](tests/benchmark/README.md). The implemented wire
format, versioning, security boundaries, local adaptive policy, product goals,
and current limitations are recorded in
[`docs/WG-QUIC-PROTOCOL.md`](docs/WG-QUIC-PROTOCOL.md).

The container test leaves the host route table and DNS untouched. It creates
isolated privileged nodes with separate Linux network namespaces and real TUN
devices. The GitHub Actions gate covers IPv4 and IPv6 inner/outer paths,
TCP/UDP, large packets, carrier MTU, loss/FEC, reordering, NAT rebinding, and
peer restart recovery. See
[`tests/WIREGUARD-FORK.md`](tests/WIREGUARD-FORK.md) for the mapping to
the pinned WireGuard fork and its imported test suite.

The OPNsense plugin lives in
[`wg-quic-opnsense/`](wg-quic-opnsense/README.md) as a separate monorepo
subtree. Its Web UI, Dashboard widget, package, services, and `quicN`
interfaces are isolated from OPNsense's built-in WireGuard integration.

The Windows and Linux desktop shell lives in
[`desktop/`](desktop/README.md). It is a constrained Tauri webview over the
existing `wg-quic-quick check/up/down` and `wg-quic show --json` commands;
there is no separate desktop tunnel implementation or configuration model.

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

Windows preserves the same two-program boundary:
`wg-quic.exe` owns the Wintun data plane, while `wg-quic-quick.exe` owns host
`%ProgramData%\wg-quic\interfaces\`. The per-machine MSI asks for elevation
once and installs the `wg-quic-manager` LocalSystem service. An unelevated
desktop running under a local Administrator account can then validate, import,
start, and stop hook-free profiles through that narrow authenticated broker.
Standard users, unavailable/incompatible broker versions, and profiles with
`PreUp`, `PostUp`, `PreDown`, or `PostDown` retain the one-operation UAC helper;
the desktop/WebView itself is never elevated. The equivalent elevated-terminal
commands are:

```powershell
wg-quic-quick.exe check wg0
wg-quic-quick.exe up wg0
wg-quic.exe show wg0
wg-quic-quick.exe down wg0
# Explicit recovery only, if a previous stop left residual state:
wg-quic-quick.exe down wg0 --repair
```

The quick service runs as LocalSystem, supervises a separate sibling
`wg-quic.exe`, and exposes an Administrators/System-only control Named Pipe
plus a separate local status-only pipe using the same JSON request/response
protocol as Unix sockets. Desktop-started services use a fresh, unpredictable,
LocalSystem-owned native runtime under `%ProgramData%\wg-quic\runtime`, rather
than the installer's replaceable application directory. Privileged paths are
opened without following reparses, and legacy permissive roots are isolated
before a bounded batch of validated hook-free profiles is migrated. An active
runtime survives desktop upgrade or uninstall; once SCM confirms a tunnel has
stopped and its service record is gone, its retired runtime is reclaimed. The
desktop itself is installed per-machine under ACL-protected Program Files by a
WiX MSI. Shutdown uses bounded cleanup contexts, reports SCM checkpoints and
wait hints for the current cleanup stage, and contains the core in a Windows
Job Object. Normal `down` never force-terminates the service. The explicit
`down --repair` path gives it a final graceful-stop window, may terminate only
that tunnel's stuck service process, then removes the exact named residual
Wintun adapter and reconciles only dead, provably managed route leases. Routes
with live owners and ambiguous kernel routes are left untouched.

Before installing AllowedIPs, its endpoint supervisor asks the
Windows route manager for a lease on every resolved endpoint. The manager uses
per-interface `GetBestRoute2`, excludes Wintun, compares prefix length and
effective route cost, and owns pins through a persistent reference-counted
ledger. It never approximates selection by taking the first default route or
uses a magic metric as ownership. Hosted CI validates Wintun creation, SCM,
address/MTU/DNS and split-route policy, endpoint pinning, status, and cleanup; a
two-host Windows traffic test remains a separate integration boundary.

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
make release-artifacts VERSION=0.2.1
./scripts/check-release-archive.sh \
  dist/wg-quic-v0.2.1-linux-amd64.tar.gz linux amd64 0.2.1
```

The implementation and tests are under active development. See the local
`design/architecture.md` when the design checkout is present.
