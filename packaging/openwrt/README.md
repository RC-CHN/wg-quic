# OpenWrt package

The OpenWrt package installs both static executables, depends on `kmod-tun`
and `ip-full`, and supervises one procd instance per tunnel. It supports the
current apk-based OpenWrt releases and older opkg-based SDKs through the same
package source.

Do not use `wg-quic run` for a normal OpenWrt tunnel. That command is the
low-level data plane and intentionally does not assign addresses, install
routes, or execute `PreUp`, `PostUp`, `PreDown`, or `PostDown`. Use
`wg-quic-quick` or the procd service.

## Install and configure

Install the package produced for the exact OpenWrt release and target. The
package manager installs the TUN kernel module and full iproute2 command:

```sh
apk add --allow-untrusted ./wg-quic-0.3.5-r1.apk
# OpenWrt 24.10 and older:
# opkg install ./wg-quic_0.3.5-r1_aarch64_generic.ipk
```

Profiles remain ordinary wg-quick-style files under `/etc/wg-quic/` and must
be readable only by root:

```sh
install -d -m 0700 /etc/wg-quic
chmod 0600 /etc/wg-quic/aws.conf
wg-quic-quick check aws
wg-quic-quick up aws
wg-quic-quick show aws --json
wg-quic-quick down aws
```

`up` and `down` select the OpenWrt procd service automatically. A profile can
be started manually without UCI. To enable it at boot, add a UCI instance:

```sh
uci set wg-quic.aws='instance'
uci set wg-quic.aws.enabled='1'
uci set wg-quic.aws.config='/etc/wg-quic/aws.conf'
uci commit wg-quic
/etc/init.d/wg-quic enable
/etc/init.d/wg-quic reload
```

The UCI instance list, procd instance names, and the existing redacted JSON
from `wg-quic-quick show --json` form the backend boundary intended for a
future LuCI application. LuCI is deliberately not part of this package yet.

OpenWrt does not ship systemd-resolved or resolvconf by default. Keep DNS
policy in OpenWrt's network/dnsmasq configuration for now instead of adding a
`DNS =` line to a wg-quic profile.

OpenWrt's fw4 input chain can still reject packets after an `accept` verdict
in a separate nftables base chain. Prefer a persistent `/etc/config/firewall`
device rule for the tunnel. If the policy must follow the profile lifecycle,
insert the rule into fw4's own input chain and remove its comment-tagged handle
on shutdown, for example:

```ini
PostUp = nft insert rule inet fw4 input iifname "%i" accept comment "wg-quic-%i"
PostDown = for handle in $(nft -a list chain inet fw4 input | awk '/wg-quic-%i/ { print $NF }'); do nft delete rule inet fw4 input handle "$handle"; done
```

Hooks are shell commands executed as root. Keep each profile mode `0600`, use a
unique comment per interface, and validate commands on the matching OpenWrt
release.

## Build in an SDK

Use the SDK matching the target firmware. The helper cross-builds the two
static Go executables and lets the official OpenWrt package rules produce an
apk or ipk:

```sh
# Release SDKs omit many base package definitions. Index the base feed pinned
# by the SDK so the ip-full runtime dependency can be recorded accurately.
cd /path/to/openwrt-sdk
./scripts/feeds update base
make defconfig
./scripts/feeds install ip-full
cd /path/to/wg-quic

./packaging/openwrt/build-package.sh \
  /path/to/openwrt-sdk \
  0.3.5 arm64
```

The helper uses the SDK's `NO_DEPS` single-package mode: `kmod-tun` and
`ip-full` remain mandatory package metadata, but are installed from OpenWrt's
matching binary repositories instead of being rebuilt locally. See the
repeatable ARM64 test procedure in `tests/openwrt/README.md`.

For the two release targets, a pinned wrapper downloads and verifies the exact
official SDK, prepares its base feed, builds the package, verifies its
architecture/dependencies, and gives each artifact a collision-free name:

```sh
./packaging/openwrt/build-release-target.sh arm64
./packaging/openwrt/build-release-target.sh x86_64
```

These produce OpenWrt 25.12.5 packages for `armsr/armv8`
(`aarch64_generic`) and `x86/64` (`x86_64`). An APK must still match the
firmware target and kernel package repository; the architecture-neutral
configuration and procd files do not make kernel dependencies portable across
OpenWrt releases.
