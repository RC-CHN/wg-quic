# OPNsense validation

This document records only checks run against the monorepo
`wg-quic-opnsense` plugin. Results from the separate wg-mix plugin are not
carried over.

## Coverage

Linux-side checks:

- XML parsing;
- Python compilation;
- JavaScript and rendered Volt script syntax;
- shellcheck;
- monorepo source/binary layout assertions; and
- static FreeBSD/amd64 cross-builds of `wg-quic` and `wg-quic-quick`.

GitHub Actions additionally creates both ABI-specific packages with the
official OPNsense `stable/26.1` and `stable/26.7` plugin frameworks in
FreeBSD 14.3 and 15.1 VMs. `verify-package.sh` checks the resulting manifest,
package scripts, ABI annotations, and each installed file hash before the
artifact is uploaded.

The disposable OPNsense guest test additionally covers:

- native `make lint` and `make package`;
- package ABI, installation, manifest integrity, and hooks;
- PHP/configd/model/template loading;
- configuration permissions and `wg-quic-quick check`;
- supervised `quic0` startup and a second `quic1` process;
- an actual wg-quic-to-wg-quic QUIC session on loopback;
- optional Linux-host-to-OPNsense traffic through a QEMU UDP forward;
- isolation from `/usr/bin/wg` and the standard WireGuard UAPI;
- authenticated General, Peer, Instance, status, version, and Dashboard APIs;
- the `VPN > wg-quic` configuration and Status pages;
- Dashboard widget registration under the visible title `wg-quic`; and
- process, interface, socket, profile, UI, and registration cleanup on
  uninstall, followed by reinstall.

## Reusing the sibling QEMU environment

The large immutable assets remain in:

```text
/workspace/projects/wg-mix-opnsense/.qemu/images
/workspace/projects/wg-mix-opnsense/.qemu/disks
```

Create a new qcow2 overlay backed by the appropriate clean disk and attach a
new FAT share prepared under this plugin's ignored `.qemu/` directory. Never
boot the sibling clean or verified disk writable.

The guest expects the share as the second VirtIO block device and mounts it at
`/mnt/wg-quic-share`. Run one of:

```sh
/bin/sh /mnt/wg-quic-share/guest-validate.sh 26.1
/bin/sh /mnt/wg-quic-share/guest-validate.sh 26.7
```

Generated packages can then be collected and verified on the host:

```sh
./scripts/collect-artifacts.sh
./scripts/verify-artifacts.sh
```

For host interoperability, launch QEMU user networking with host UDP 52820
forwarded to guest UDP 52820. Before starting QEMU, run
`prepare-host-interop.sh` so `host-interop.json` is present on the FAT share.
In the guest:

```sh
/usr/local/bin/php \
  /mnt/wg-quic-share/configure-host-client.php \
  /mnt/wg-quic-share/host-interop.json
configctl template reload OPNsense/WireguardQuic
configctl wireguardquic configure
```

Then run `run-host-interop.sh` on Linux.

OPNsense intentionally blocks traffic on a new VPN interface until a firewall
rule permits it. The protocol interoperability proof below temporarily
disabled PF in the disposable guest after first confirming that the default
rules blocked only the inner ICMP traffic. A real installation must instead
add an ICMP or broader policy rule on `wg-quic (Group)`.

## Current result

| Target | Result |
|---|---|
| Linux static fixture | Passed |
| FreeBSD/amd64 cross-build | Passed |
| OPNsense 26.1 QEMU | Passed |
| OPNsense 26.7 QEMU | Passed |
| Linux host ↔ OPNsense 26.7 | Passed |

Do not treat a cross-build as FreeBSD runtime validation. The table is updated
only after the corresponding command completes against the plugin source in
this monorepo.

The Linux-host runs established the WireGuard handshake over a Salamander,
adaptive-FEC QUIC session through QEMU's UDP forward and received ICMP echo
replies from `10.77.0.1` in 3.774–5.108 ms. The client reported 292
transmitted and 204 received WireGuard bytes; outer transport counters
advanced in both directions.

Verified package artifacts:

```text
19b5c8c3a937baf9eb191845200836ac9058e338d2a272ee1e8cb300caa95d9e  os-wg-quic-0.1.0-opnsense-26.1-amd64.pkg
efc6c675f68eec08673b78037d8072f35040dc512cfe205ca7f0c45ff0713e19  os-wg-quic-0.1.0-opnsense-26.7-amd64.pkg
```
