# os-wg-quic

`os-wg-quic` is the OPNsense integration shipped inside the wg-quic
monorepo. The package and every user-facing page use the name `wg-quic`; it is
independent from both OPNsense's built-in WireGuard plugin and the separate
wg-mix project.

The initial 0.1.0 plugin provides:

- `VPN > wg-quic` pages for Instances, Peers, Peer generator, Status, and logs;
- a native Lobby Dashboard widget named `wg-quic`;
- multiple supervised `quicN` userspace interfaces;
- OPNsense service, interface registration, CARP, XML-RPC, API, template, and
  package lifecycle integration;
- controls for congestion mode, adaptive FEC, obfuscation, and per-peer FEC
  latency policy; and
- generated client profiles containing the matching wg-quic transport
  directives.

Both ends of a tunnel must run wg-quic. A stock WireGuard endpoint is not
on-wire compatible.

## Runtime layout

| Component | Location |
|---|---|
| Data plane and control CLI | `/usr/local/sbin/wg-quic` |
| Host-policy supervisor | `/usr/local/sbin/wg-quic-quick` |
| Generated profiles | `/usr/local/etc/wg-quic/quicN.conf` |
| Local control sockets | `/var/run/wg-quic/quicN.sock` |
| Interfaces | `quicN` |
| Web UI | `VPN > wg-quic` |

The plugin launches `wg-quic-quick run` as the foreground service.
`wg-quic-quick` validates the profile, starts the sibling data plane, resolves
endpoint hostnames, pins endpoint routes, and owns address, route, MTU, DNS,
and cleanup policy. The plugin adds OPNsense interface registration and CARP
behavior around that lifecycle.

The status page reads wg-quic's private JSON control interface. It shows QUIC
session state and aggregate transfer counters without exposing keys. The
current core schema does not expose WireGuard handshake timestamps or
per-peer byte counters. Aggregate counters are therefore shown for the
interface and, when an instance has exactly one peer, for that peer.

## Build

From this directory, cross-build the two static FreeBSD/amd64 programs from
the monorepo parent:

```sh
./scripts/build-wg-quic.sh
./scripts/check-static.sh
```

The resulting executables are intentionally ignored by Git:

```text
net/wg-quic/src/sbin/wg-quic
net/wg-quic/src/sbin/wg-quic-quick
```

The OPNsense package itself must be built on the target OPNsense branch so its
ABI metadata matches the guest:

```sh
cd /usr/plugins/net/wg-quic
make lint
make package
```

Version 0.2.0 targets OPNsense 26.1/FreeBSD 14 amd64 and
OPNsense 26.7/FreeBSD 15 amd64. Install a matching private package with:

```sh
pkg add -f /tmp/os-wg-quic-0.2.0-opnsense-26.7-amd64.pkg
```

Remove it with `pkg delete os-wg-quic`.

## CI and release packages

GitHub Actions runs the official OPNsense plugin framework in FreeBSD VMs:
OPNsense 26.1 is packaged on FreeBSD 14.3 and OPNsense 26.7 on FreeBSD 15.1.
Each job clones the matching `stable/26.1` or `stable/26.7` branches of
`opnsense/plugins` and `opnsense/core`, runs `make lint` and `make package`,
then verifies the package ABI, hooks, manifest, and every payload hash on the
Linux runner.

The `opnsense-package` workflow uploads both `.pkg` files as CI artifacts.
For a `v*` tag, the release workflow also publishes them alongside the normal
wg-quic archives and includes them in `SHA256SUMS`.

The same packaging entry point is available in a matching FreeBSD VM:

```sh
./scripts/build-package-freebsd.sh 26.7
```

## QEMU validation

The fixture can reuse the official images and clean qcow2 bases already kept
by the sibling `wg-mix-opnsense` checkout. It creates its own overlay and FAT
share; it does not modify those reference assets.

Prepare the share after every source change:

```sh
./scripts/qemu/prepare-shared.sh
```

Inside a disposable guest, run the branch-matching validation:

```sh
/bin/sh /mnt/wg-quic-share/guest-validate.sh 26.7
```

The test builds and installs the package, validates the model and rendered
profile, starts two real wg-quic peers, requires established QUIC sessions,
checks API/UI/widget registration and standard-WireGuard isolation, then
tests uninstall cleanup and reinstall. See [TESTING.md](TESTING.md) for the
fixture details and recorded results.

To test the actual monorepo Linux implementation through a QEMU UDP forward,
prepare a one-time key/config fixture, configure the guest with the generated
JSON, and run:

```sh
./scripts/qemu/prepare-host-interop.sh
./scripts/qemu/run-host-interop.sh
```

The runner uses a native host TUN when passwordless `sudo` is available and
falls back to the in-repository unprivileged netstack client otherwise. As
with every new OPNsense VPN interface, add an explicit pass rule on
`wg-quic (Group)` for the inner traffic being tested. The runner requires an
ICMP reply, an established WireGuard/QUIC path, and non-zero bidirectional
wg-quic counters.

## Safety

Route installation is disabled by default. Before enabling it remotely,
verify that Allowed IPs cannot replace the route used to administer the
firewall, and keep a console or another recovery path available.
