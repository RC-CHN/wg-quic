# wg-quic

Repository: <https://github.com/RC-CHN/wg-quic>

[简体中文](README_CN.md)

`wg-quic` carries complete encrypted WireGuard datagrams over QUIC DATAGRAM
frames. It keeps the WireGuard interface and configuration model, but replaces
the outer WireGuard UDP transport with QUIC, optional adaptive FEC, and
Salamander-style packet obfuscation.

> [!IMPORTANT]
> Both ends of a tunnel must run `wg-quic`. A stock WireGuard endpoint cannot
> connect to a `wg-quic` endpoint even though both use familiar WireGuard keys
> and `wg-quick`-style configuration files.

The current public release is
[`v0.3.3`](https://github.com/RC-CHN/wg-quic/releases/tag/v0.3.3).

## Platform status

| Platform | Available packages | Service or UI | Current validation boundary |
|---|---|---|---|
| Linux | CLI archives for amd64 and arm64; desktop Deb for amd64 | systemd; optional Tauri desktop | Native tests, race tests, and privileged two-node TUN interoperability on amd64; arm64 is cross-built |
| Windows | CLI bundles for amd64 and arm64; desktop MSI for x64 | Wintun, one SCM service per tunnel, and a Tauri desktop | Installed x64 MSI, LocalSystem service, Wintun, address/MTU/DNS/routes, upgrade, status, and cleanup; arm64 is cross-built |
| FreeBSD | CLI archives for amd64 and arm64 | rc.d | Native FreeBSD 14 amd64 tests; arm64 is cross-built |
| OPNsense | Private packages for 26.1/FreeBSD 14 and 26.7/FreeBSD 15, amd64 | `VPN > wg-quic`, Dashboard widget, configd, and `quicN` interfaces | Both versions are package-validated and QEMU runtime-tested; Linux-to-OPNsense traffic is also exercised |
| OpenWrt | OpenWrt 25.12.5 APK workflow artifacts for `armsr/armv8` and `x86/64` | procd and UCI multi-instance service | Both exact CI APKs have full QEMU install, traffic, live reload, reboot, hooks, and uninstall coverage |
| macOS, Android, iOS | None | None | Not currently supported |

Starting with `v0.3.1`, the release workflow publishes both OpenWrt APKs in
addition to the `release-openwrt-armsr-armv8` and
`release-openwrt-x86-64` workflow artifacts.

## Choose the right command

The project deliberately has two executables:

- `wg-quic` is the low-level userspace data plane. It owns WireGuard, QUIC,
  the TUN device, and the local status interface.
- `wg-quic-quick` is the normal entry point. It owns addresses, routes, MTU,
  DNS, hooks, endpoint route pinning, and platform service management.

Use `wg-quic-quick` for a normal tunnel. Running `wg-quic run` directly does
not apply `Address`, routes, DNS, `PreUp`, `PostUp`, `PreDown`, or `PostDown`.
Direct core startup rejects hook-bearing profiles instead of silently ignoring
their host policy.

The common commands are:

```sh
wg-quic-quick check wg0
sudo wg-quic-quick run wg0       # foreground, complete tunnel lifecycle
sudo wg-quic-quick up wg0        # start through the platform service manager
wg-quic-quick show wg0
wg-quic-quick show wg0 --json
sudo wg-quic-quick down wg0
```

### Runtime peer and DDNS management

Release `v0.3.3` supports live peer reconciliation and automatic DDNS across
the platform service adapters listed above.

Start by inspecting the running supervisor. On Unix, use root for the detailed
quick status because its management socket is mode `0600`:

```sh
sudo wg-quic-quick show wg0 --json
```

The `capabilities` array must contain `peer_reconcile_v1` for live peer changes
and `endpoint_refresh_v1` for administrative DDNS refresh. Status also reports
`supervisor_epoch`, `desired_generation`, `persistent_drift`, recovery state,
and, for every peer, the configured hostname, selected numeric endpoint, DNS
candidates, refresh time, resolution error, endpoint generation, authenticated
endpoint, session, and traffic counters.

When `capabilities` contains `session_telemetry_v1`, the top-level `sessions`
array exposes connection-scoped WireGuard, QUIC, FEC, and queue observations.
Each entry has a process-unique session ID, an endpoint-local reconnect
generation, role, configured/current endpoint, sample time, and zero or more
peer associations. Associations distinguish configuration-derived ownership
from a WireGuard-authenticated path; one QUIC session may legitimately list
multiple peers. PTO and spurious-loss counters, RTT variation, cwnd, pacing,
and bandwidth estimates are independent per session. Session counters start at
connection creation and leave the active set when that connection closes, so
collectors must key deltas by supervisor epoch, session ID, and generation.
The schema and semantics are identical on Linux/OpenWrt, FreeBSD/OPNsense, and
Windows. Enumeration is bounded and prioritizes configured outbound sessions;
`session_telemetry_omitted` reports any additional active sessions that were
not included.

The CLI is the reference client for the privileged local management API:

| CLI | Protocol operation | Required capability | Effect |
| --- | --- | --- | --- |
| `show --json` | `status` | `management_protocol_v1` | Inspect runtime, persistence, peers, DDNS, recovery, and transactions |
| `reload` | `reload` | `peer_reconcile_v1` | Re-read the canonical full profile and reconcile it |
| `reconcile` | `reconcile` | `peer_reconcile_v1` | Validate and apply a protected candidate using epoch/generation CAS |
| `transaction-status` | `transaction_status` | management protocol | Recover the result of an accepted request ID |
| `refresh-endpoints` | `refresh_endpoints` | `endpoint_refresh_v1` | Refresh all hostname peers or one public key immediately |

This is not a remote HTTP API. Linux/OpenWrt use
`/run/wg-quic/<interface>.manage.sock`, FreeBSD/OPNsense use
`/var/run/wg-quic/<interface>.manage.sock`, and Windows uses the ACL-protected
`\\.\pipe\wg-quic-quick-<interface>` named pipe. Each connection carries one
bounded protocol-v1 JSON request and one response. Controllers should normally
invoke the CLI or use the typed Go client instead of exposing these endpoints.

#### Add, update, or remove peers

Every input is a **complete desired profile**, not a peer patch. Add a `[Peer]`
section to add it, edit that section to update it, or omit it to remove it.
The normal administrator workflow is to replace the canonical file atomically
and call `reload`. This Linux/OpenWrt example stages the file in the same
root-owned directory so the rename is atomic:

```sh
sudo install -o root -g root -m 0600 ./wg0.next.conf \
  /etc/wg-quic/.wg0.conf.next
sudo mv /etc/wg-quic/.wg0.conf.next /etc/wg-quic/wg0.conf
sudo wg-quic-quick reload wg0 --json
```

Use `/usr/local/etc/wg-quic/` on FreeBSD/OPNsense. The Windows manager performs
the protected candidate and canonical-file operations under
`%ProgramData%\wg-quic\interfaces`. `reload` reads status itself, supplies the
current CAS tuple, and generates a request ID. If the file is valid but runtime
commit fails, `show --json` reports `persistent_drift=true`; it never pretends
that the canonical file was rolled back.

An automation controller that needs runtime commit before file promotion uses
the candidate workflow. Keep the candidate under a root-only path, retain one
request ID across timeout retries, and promote those exact bytes only after a
`committed` or `no_op` result:

```sh
sudo wg-quic-quick show wg0 --json
# Copy supervisor_epoch and desired_generation from the response.

sudo install -d -o root -g root -m 0700 /etc/wg-quic/.candidates
sudo install -o root -g root -m 0600 ./wg0.next.conf \
  /etc/wg-quic/.candidates/wg0.peer-change-01.conf

sudo wg-quic-quick reconcile wg0 \
  /etc/wg-quic/.candidates/wg0.peer-change-01.conf \
  --expected-epoch EPOCH --expected-generation N \
  --request-id peer-change-01 --json

# Use the same ID after a lost response; do not submit changed content under it.
sudo wg-quic-quick transaction-status wg0 \
  --request-id peer-change-01 --json

# After success, promote the exact candidate atomically on the same filesystem.
sudo mv /etc/wg-quic/.candidates/wg0.peer-change-01.conf \
  /etc/wg-quic/wg0.conf
```

Live mutable fields are peer add/remove, ordinary `AllowedIPs`, `Endpoint`,
`PersistentKeepalive`, and `peer.fec-latency`. Adding a new peer may include a
`PresharedKey`; changing one for an active peer requires restart. Interface
keys/addresses/listen port/fwmark/MTU/DNS/table/hooks/global transport policy,
automatic full-tunnel transitions, and active preshared-key rotation return
`restart_required` before mutation. A durable full profile should retain all
of its secrets even though an omitted existing preshared key can be inherited
for the current live transaction. `SaveConfig = true` remains rejected.

#### DDNS behavior and manual refresh

A hostname in `Endpoint`, for example `Endpoint = edge.example.com:443`, makes
that peer dynamic. The supervisor resolves all usable A/AAAA candidates and
refreshes automatically. The current system resolver does not expose TTLs, so
it uses a conservative one-minute base interval with jitter (bounded by the
30-second/30-minute policy); route changes and repeated transport recovery
failures can trigger an earlier refresh or candidate rotation.

```sh
# Refresh every dynamic peer now.
sudo wg-quic-quick refresh-endpoints wg0 --json

# Refresh one peer identified by its complete base64 public key.
sudo wg-quic-quick refresh-endpoints wg0 \
  --peer 'PEER_PUBLIC_KEY_BASE64' --json

# Inspect selected_endpoint, dns_candidates, next_refresh_at, and errors.
sudo wg-quic-quick show wg0 --json
```

DNS answer reordering does not move a healthy peer. Timeout, SERVFAIL, and
NXDOMAIN retain the last selected endpoint and expose an error in status. A new
address is published only after its route is prepared and WireGuard traffic
authenticates the new endpoint generation; failure rolls the candidate and its
route lease back. DDNS changes `endpoint_generation`, not
`desired_generation`, and never rewrites the profile.

See [`docs/RUNTIME-PEER-RECONCILIATION.md`](docs/RUNTIME-PEER-RECONCILIATION.md)
for the complete JSON, transaction, security, rollback, and controller contract.

“All-platform” here means the same protocol and transaction semantics, not an
undifferentiated claim that every CPU has already completed native acceptance:

| Runtime family | Live peer path | Crash-persistent ownership | Claim boundary |
|---|---|---|---|
| Linux/systemd or OpenRC/direct supervisor | Unix management socket plus incremental `ip` routes | ordinary peer routes disappear with the TUN; automatic policy-routing transitions require restart | amd64 privileged lifecycle; arm64 requires native/emulated lifecycle for a runtime claim |
| OpenWrt ARM64/x86_64 | the same Linux runtime through procd reload | the same TUN boundary | each installed APK must pass its own QEMU reload/reboot/traffic fixture |
| FreeBSD/OPNsense | Unix socket plus incremental `route` operations | root-owned/checksummed outer endpoint-route ledger; TUN peer routes disappear with the interface | rc.d/configd and each carried FreeBSD release train are tested separately |
| Windows amd64/arm64 | ACL-protected named pipe, typed core transaction, and IP Helper peer routes | protected endpoint ledger plus a per-tunnel before/after/phase journal keyed by compartment and interface LUID | x64 installed SCM/MSI lifecycle; arm64 remains build/unit-only until a native service fixture passes |

The current development tree contains all four adapters. Release notes must use
`build-supported`, `unit-verified`, `runtime-verified`, or
`integration-verified` per exact OS/architecture; cross-compilation alone never
raises that label.

The data-plane child does not depend on systemd for process ownership. On
Linux/OpenWrt and FreeBSD/OPNsense, quick gives core a private inherited
lifetime pipe; killing quick closes the pipe, so core exits and releases the
TUN even under OpenRC, procd, rc.d, or a direct supervisor. FreeBSD also uses a
parent-death signal, while Windows uses a kill-on-close Job Object. Service
manager cgroups/process groups remain defense in depth.

The current development binaries have passed the OpenWrt 25.12.5
`runtime-smoke` fixture in both `armsr/armv8` and `x86/64` QEMU guests: TUN
creation, procd lifecycle, hook ordering, peer add/remove, generation advance,
and unchanged supervisor epoch. Because that run installed locally built
binaries into the package paths instead of installing a newly built APK, the
per-target APK install/traffic/reboot fixture remains the release claim gate.

## Create a first profile

Generate a private/public keypair on each peer. Keep the private key file
secret:

```sh
umask 077
wg-quic genkey > private.key
wg-quic pubkey < private.key > public.key
```

A minimal profile for peer A looks like this:

```ini
[Interface]
PrivateKey = PEER_A_PRIVATE_KEY
Address = 10.203.0.1/32
ListenPort = 51820
MTU = 1280

[Peer]
PublicKey = PEER_B_PUBLIC_KEY
AllowedIPs = 10.203.0.2/32
Endpoint = PEER_B_PUBLIC_IP_OR_NAME:51820
PersistentKeepalive = 25
```

On peer B, use its private key, `10.203.0.2/32`, peer A's public key,
`10.203.0.1/32`, and peer A's reachable endpoint. Start with narrow `/32`
`AllowedIPs`; test a default route only after a split tunnel works. If a
`PresharedKey` is used, place the same value in both matching `[Peer]`
sections.

QUIC, adaptive FEC, and Salamander obfuscation are enabled by the default
transport profile, so this basic configuration needs no separate transport
password. Salamander keys are derived from the WireGuard key agreement and,
when present, the WireGuard preshared key.

Configuration locations are platform-specific:

| Platform | Profile location |
|---|---|
| Linux and OpenWrt | `/etc/wg-quic/<name>.conf` |
| FreeBSD | `/usr/local/etc/wg-quic/<name>.conf` |
| Windows | `%ProgramData%\wg-quic\interfaces\<name>.conf` |
| OPNsense | `/usr/local/etc/wg-quic/quicN.conf`, generated by the plugin |

On Unix-like systems, keep the directory private and each profile readable
only by root:

```sh
sudo install -d -m 0700 /etc/wg-quic
sudo install -m 0600 wg0.conf /etc/wg-quic/wg0.conf
sudo wg-quic-quick check wg0
```

## Linux

Download the archive matching the host architecture from
[Releases](https://github.com/RC-CHN/wg-quic/releases). For example, on amd64:

```sh
curl -LO https://github.com/RC-CHN/wg-quic/releases/download/v0.3.3/wg-quic-v0.3.3-linux-amd64.tar.gz
curl -LO https://github.com/RC-CHN/wg-quic/releases/download/v0.3.3/SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
tar -xzf wg-quic-v0.3.3-linux-amd64.tar.gz
cd wg-quic-v0.3.3-linux-amd64

sudo install -m 0755 wg-quic wg-quic-quick /usr/local/bin/
sudo install -m 0644 wg-quic@.service /etc/systemd/system/
sudo systemctl daemon-reload
```

Put `wg0.conf` in `/etc/wg-quic/`, then start it and optionally enable it for
boot:

```sh
sudo wg-quic-quick check wg0
sudo wg-quic-quick up wg0
sudo systemctl enable wg-quic@wg0.service
wg-quic-quick show wg0
sudo wg-quic-quick down wg0
```

The amd64 desktop Deb is an alternative for Linux desktop users:

```sh
sudo apt install ./wg-quic-desktop-v0.3.3-linux-amd64.deb
```

The desktop imports profiles into `/etc/wg-quic/` with mode `0600` and uses
`pkexec` only for fixed privileged operations. It is a UI over the same
`wg-quic-quick` service and configuration model, not a separate tunnel
implementation.

Linux requires a working TUN device and `CAP_NET_ADMIN`. The packaged systemd
unit grants the required capability and access to `/dev/net/tun`.

The runtime does not depend on systemd. Under OpenRC, runit, s6, dinit, SysV,
or a custom supervisor, run `wg-quic-quick run wg0` as root and use the same
`show`, `reload`, `reconcile`, and `refresh-endpoints` CLI against its root-only
Unix socket. systemd is only one lifecycle adapter; its `ExecReload` invokes
the same manager-neutral CLI. Starting with `v0.3.2`, Linux release archives
also include an OpenRC instance script:

```sh
sudo install -m 0755 wg-quic.openrc /etc/init.d/wg-quic
sudo ln -s wg-quic /etc/init.d/wg-quic.wg0
sudo rc-update add wg-quic.wg0 default
sudo rc-service wg-quic.wg0 start
sudo rc-service wg-quic.wg0 reload
```

For runit/s6/dinit, supervise `wg-quic-quick run wg0` in the foreground and
invoke `wg-quic-quick reload wg0 --json` from the service's control hook. The
supervisor must run it as root (or supply equivalent TUN and network-policy
privileges), forward termination signals, and restart only after an actual
process failure—not when reload returns `restart_required`.

## Windows

For x64 Windows, the recommended installation is
`wg-quic-desktop-v0.3.3-windows-x64.msi` from
[Releases](https://github.com/RC-CHN/wg-quic/releases). The per-machine MSI
asks for elevation once, installs the UI under Program Files, and registers the
restricted `wg-quic-manager` LocalSystem service. Use **Import** in the desktop
application to add a profile and then start or stop it from the tunnel list.

The desktop itself remains unelevated. Local Administrator accounts use the
authenticated management service for hook-free profiles; standard users and
profiles containing lifecycle hooks fall back to a one-operation UAC helper.

For CLI-only use, download the amd64 or arm64 ZIP. Keep `wg-quic.exe`,
`wg-quic-quick.exe`, and the architecture-matching signed `wintun.dll`
together. From an elevated PowerShell terminal:

```powershell
New-Item -ItemType Directory -Force "$env:ProgramData\wg-quic\interfaces"
Copy-Item .\wg0.conf "$env:ProgramData\wg-quic\interfaces\wg0.conf"
.\wg-quic-quick.exe check wg0
.\wg-quic-quick.exe up wg0
.\wg-quic-quick.exe show wg0
.\wg-quic-quick.exe down wg0
```

Use `wg-quic-quick.exe debug wg0` for foreground diagnostics. Redacted logs
are written under `%ProgramData%\wg-quic\logs`. If an interrupted stop leaves
an exact managed service, adapter, or route lease behind, the explicit
recovery command is:

```powershell
.\wg-quic-quick.exe down wg0 --repair
```

When this peer accepts incoming sessions, allow its outer UDP `ListenPort` in
Windows Firewall. See [`packaging/windows/README.md`](packaging/windows/README.md)
for the complete LAN test and recovery procedure.

## FreeBSD

Download the amd64 or arm64 FreeBSD archive and install its two programs and
rc.d script:

```sh
tar -xzf wg-quic-v0.3.3-freebsd-amd64.tar.gz
cd wg-quic-v0.3.3-freebsd-amd64
install -m 0755 wg-quic wg-quic-quick /usr/local/bin/
install -m 0755 wg_quic /usr/local/etc/rc.d/wg_quic
install -d -m 0700 /usr/local/etc/wg-quic
install -m 0600 /path/to/wg0.conf /usr/local/etc/wg-quic/wg0.conf
```

Enable one or more interfaces and start the service:

```sh
sysrc wg_quic_enable=YES
sysrc 'wg_quic_interfaces=wg0'
service wg_quic start
wg-quic-quick show wg0
service wg_quic stop
```

After the rc.d script is installed, `wg-quic-quick up wg0` and
`wg-quic-quick down wg0` use the same service boundary.

## OPNsense 26.1 and 26.7

Use the package whose OPNsense version exactly matches the firewall:

- `os-wg-quic-0.3.3-opnsense-26.1-amd64.pkg`
- `os-wg-quic-0.3.3-opnsense-26.7-amd64.pkg`

Copy it to the firewall and install it from a console or SSH session. For
OPNsense 26.7:

```sh
pkg add -f /tmp/os-wg-quic-0.3.3-opnsense-26.7-amd64.pkg
```

Then open `VPN > wg-quic`:

1. Add or generate peers.
2. Create an instance and assign the desired peer(s).
3. Apply the configuration and check the **Status** page.
4. Add an explicit pass rule on `wg-quic (Group)` for the intended inner
   traffic. As with every new OPNsense VPN interface, traffic is not
   automatically allowed by installation alone.

The plugin creates `quicN` interfaces and owns the generated profiles,
configd actions, service lifecycle, CARP/XML-RPC integration, API, logs, and
Dashboard widget. It is separate from OPNsense's stock WireGuard integration.

Route installation is disabled by default. Before enabling broad
`AllowedIPs` remotely, confirm they cannot replace the route used to
administer the firewall and keep a console or other recovery path available.
Remove the package with `pkg delete os-wg-quic`.

See [`wg-quic-opnsense/README.md`](wg-quic-opnsense/README.md) for build and
QEMU details.

## OpenWrt 25.12.5

The current OpenWrt workflow builds these exact 64-bit targets:

- `armsr/armv8` with package architecture `aarch64_generic`;
- `x86/64` with package architecture `x86_64`.

Download the matching artifact from the
[`openwrt-package` workflow](https://github.com/RC-CHN/wg-quic/actions/workflows/openwrt-package.yml),
or build both packages with the pinned official SDK wrappers:

```sh
./packaging/openwrt/build-release-target.sh arm64
./packaging/openwrt/build-release-target.sh x86_64
```

Do not install an APK built for a different OpenWrt release or target. Kernel
packages such as `kmod-tun` must match the running firmware. Install the APK
on the router:

```sh
apk add --allow-untrusted ./wg-quic-0.3.3-r1-openwrt-25.12.5-armsr-armv8.apk
```

The package pulls in `kmod-tun` and `ip-full`, installs both executables, and
registers a procd multi-instance service. Profiles live in `/etc/wg-quic/`:

```sh
install -d -m 0700 /etc/wg-quic
chmod 0600 /etc/wg-quic/aws.conf
wg-quic-quick check aws
wg-quic-quick up aws
wg-quic-quick show aws --json
wg-quic-quick down aws
```

To start the profile at boot, add one UCI instance:

```sh
uci set wg-quic.aws='instance'
uci set wg-quic.aws.enabled='1'
uci set wg-quic.aws.config='/etc/wg-quic/aws.conf'
uci commit wg-quic
/etc/init.d/wg-quic enable
/etc/init.d/wg-quic reload
```

If `/dev/net/tun` does not exist, first confirm that the package was built for
the exact firmware target and that its dependency installed successfully:

```sh
apk add kmod-tun ip-full
test -c /dev/net/tun
```

Do not work around this by running the low-level `wg-quic run` command; it
still cannot apply addresses, routes, or hooks. Use `wg-quic-quick` or procd.

OpenWrt does not normally provide `systemd-resolved` or `resolvconf`. Keep DNS
policy in OpenWrt network/dnsmasq configuration instead of adding `DNS =` to a
profile. Prefer persistent `/etc/config/firewall` rules for tunnel traffic.
Lifecycle `PostUp`/`PostDown` hooks are supported through the supervised quick
or procd path and run as root, so keep profiles mode `0600` and review every
hook carefully.

The UCI/procd and redacted JSON status boundary is ready for a future LuCI
application, but **no LuCI UI is included yet**. See
[`packaging/openwrt/README.md`](packaging/openwrt/README.md) for firewall-hook,
SDK, and package details, and
[`tests/openwrt/README.md`](tests/openwrt/README.md) for the ARM64 and x86_64
QEMU fixtures.

## Architecture and protocol notes

The pinned userspace WireGuard implementation lives under
[`third_party/wireguard-go`](third_party/wireguard-go), and the complete pinned
quic-go source lives under [`third_party/quic-go`](third_party/quic-go).
Production builds do not download or substitute either implementation from a
module cache.

`wg-quic-quick` parses and validates a profile once, resolves host policy, and
sends an immutable configuration snapshot to the supervised core through
standard input. Private and preshared keys do not enter process arguments, and
the supervised core does not reread the profile path.

The wire format, transport directives, security boundaries, adaptive FEC
policy, and current limitations are documented in
[`docs/WG-QUIC-PROTOCOL.md`](docs/WG-QUIC-PROTOCOL.md).

## Development and verification

The normal local checks are:

```sh
go test ./...
make test-wireguard
make test-transport
make test-quic
./tests/container/test.sh
make build
```

The privileged container fixture keeps TUN devices, routes, DNS, and network
emulation inside isolated Linux namespaces. CI covers IPv4 and IPv6
inner/outer paths, TCP and UDP, large packets, MTU, loss/FEC, reordering, NAT
rebinding, and peer restart recovery. See
[`tests/WIREGUARD-FORK.md`](tests/WIREGUARD-FORK.md) for the pinned WireGuard
test mapping.

Performance and controlled-loss fixtures are available through:

```sh
make benchmark-smoke
make benchmark-transports
make benchmark-ceiling
make benchmark-loss
make benchmark-profiles
make benchmark-bandwidth
make benchmark-protocol
```

See [`tests/benchmark/README.md`](tests/benchmark/README.md) for the available
LAN, Wi-Fi, cellular, satellite, FEC, obfuscation, CPU, and protocol-signature
measurements.

The Windows and Linux desktop source and tests live under
[`desktop/`](desktop/README.md). The OPNsense plugin is maintained in the
[`wg-quic-opnsense/`](wg-quic-opnsense/README.md) monorepo subtree.

## Release archives

Every `v*` tag is gated on Linux, Windows, FreeBSD, the privileged Linux tunnel
matrix, desktop packages, OPNsense packages, and the OpenWrt package matrix.
The release job publishes checksums in one top-level `SHA256SUMS` file.

The root [`VERSION`](VERSION) file is the canonical release version. Packaging
scripts and a manually dispatched release workflow read it automatically; the
optional workflow input only asserts that the requested version matches it.
Desktop npm/Cargo/Tauri manifests still require literal versions for their
native tooling, and `npm run version:check --prefix desktop` detects drift.

Build and validate the six portable CLI archives locally with:

```sh
make release-artifacts VERSION=0.3.3
./scripts/check-release-archive.sh \
  dist/wg-quic-v0.3.3-linux-amd64.tar.gz linux amd64 0.3.3
```

OpenWrt and OPNsense packages must additionally match their exact target
firmware and packaging framework; a generic CPU architecture match is not
sufficient.
