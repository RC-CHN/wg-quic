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
- browser fixture unit and source-contract tests;
- Linux netstack-client hold behavior and runner argument tests;
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

For host interoperability, launch QEMU user networking with host TCP 10443
forwarded to guest TCP 443 and host UDP 52820 forwarded to guest UDP 52820.
Before starting QEMU, run `prepare-host-interop.sh` so `host-interop.json` is
present on the FAT share. The fixture gives the server `10.77.0.1/24`, leaving
an address pool for the WebUI peer generator, while each peer keeps a `/32`
tunnel address. In the guest:

```sh
/usr/local/bin/php \
  /mnt/wg-quic-share/configure-host-client.php \
  /mnt/wg-quic-share/host-interop.json
configctl template reload OPNsense/WireguardQuic
configctl wireguardquic configure
```

Then run `run-host-interop.sh` on Linux. Set a hold interval when a browser or
human needs to inspect the online peer after the traffic assertions pass:

```sh
WG_QUIC_HOST_INTEROP_HOLD_SECONDS=120 \
  ./scripts/qemu/run-host-interop.sh
```

The hold applies to both the privileged native client and its unprivileged
netstack fallback. The default is zero so unattended interoperability runs
still finish immediately.

OPNsense intentionally blocks traffic on a new VPN interface until a firewall
rule permits it. The protocol interoperability proof below temporarily
disabled PF in the disposable guest after first confirming that the default
rules blocked only the inner ICMP traffic. A real installation must instead
add an ICMP or broader policy rule on `wg-quic (Group)`.

## Real browser peer workflow

The browser fixture uses headless Firefox through WebDriver. Start
`geckodriver` on the host, then provide the disposable guest's WebUI password
through the environment or a private file; do not put it in the process
arguments:

```sh
geckodriver --host 127.0.0.1 --port 4444 \
  > .qemu/geckodriver.log 2>&1 &
read -r -s WG_QUIC_OPNSENSE_PASSWORD
export WG_QUIC_OPNSENSE_PASSWORD
```

With the host forwards described above, provision a peer through the actual
WebUI. The fixture restricts its `AllowedIPs` to the single address under test
instead of installing a default route:

```sh
python3 scripts/qemu/browser-connect.py provision \
  --guest-address 10.77.0.1
```

This writes `.qemu/webui-client.conf` as the exact generated INI profile with
mode `0600`. The screenshot directory has mode `0700` and its files have mode
`0600` because the peer-generator screenshot contains a private key and QR
code. `--password-file /path/to/private-file` is an alternative to the
environment variable. The legacy `--password` option remains accepted only
for compatibility.

In another terminal, run the generated profile and keep the validated peer
online long enough for the WebUI checks:

```sh
.qemu/tools/wg-quic-netstack-client \
  -config .qemu/webui-client.conf \
  -hold 120s
```

After `HOST INTEROP PASSED` and `HOST INTEROP HOLDING` appear, verify the real
Status page, Dashboard widget, and Notice-level Log File rows before the hold
expires:

```sh
python3 scripts/qemu/browser-connect.py verify
```

Use `--guest-address 192.168.1.1` instead when the intended proof is access to
the OPNsense LAN address rather than its wg-quic tunnel address. That path
still requires an appropriate firewall rule on `wg-quic (Group)`.

## Current result

| Target | Result |
|---|---|
| Linux static fixture | Passed |
| FreeBSD/amd64 cross-build | Passed |
| OPNsense 26.1 QEMU | Passed |
| OPNsense 26.7 QEMU | Passed |
| Linux host ↔ OPNsense 26.7 | Passed |
| Browser generator ↔ online Status/Dashboard/Log File | Passed |

Do not treat a cross-build as FreeBSD runtime validation. The table is updated
only after the corresponding command completes against the plugin source in
this monorepo.

The final OPNsense 26.7 run used a fresh QEMU overlay and rebuilt, installed,
uninstalled, and reinstalled the package. It established the WireGuard
handshake over a Salamander, adaptive-FEC QUIC session through QEMU's UDP
forward and received an ICMP echo reply from `10.77.0.1`. The generated client
reported 292 transmitted and 236 received WireGuard bytes before its hold
period. Firefox then observed `browser-e2e` online with increasing transfer
counters in both the Status page and Dashboard widget, plus wg-quic events on
the Log File page at its default Notice severity.

Verified package artifacts:

```text
19b5c8c3a937baf9eb191845200836ac9058e338d2a272ee1e8cb300caa95d9e  os-wg-quic-0.1.0-opnsense-26.1-amd64.pkg
b5effd05d69785c460acb4969a8e65de76c158293bc11e1a44321393ce6a4577  os-wg-quic-0.1.0-opnsense-26.7-amd64.pkg
```
