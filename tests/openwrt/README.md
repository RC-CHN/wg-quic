# OpenWrt ARM64 and x86_64 QEMU validation

This fixture validates the installable package and a real encrypted tunnel on
official OpenWrt ARM64 and x86_64 images. It covers dependency installation, TUN
creation, procd/UCI lifecycle, hooks, fw4 policy, reboot recovery, uninstall
cleanup, profile retention, and live peer add/remove through procd reload. It
does not build a LuCI UI.

The fixture is pinned to OpenWrt 25.12.5 `armsr/armv8` and `x86/64`.
Downloaded images and generated keys/configurations live under the gitignored
`.qemu/` directory. The two targets use independent qcow2 overlays.

## Start a clean guest

Install `qemu-system-aarch64` and/or `qemu-system-x86_64`, plus `qemu-img`,
`curl`, and `gzip`. ARM64 remains the default for backward compatibility:

```sh
./tests/openwrt/prepare-qemu.sh
./tests/openwrt/run-qemu.sh
```

Select x86_64 explicitly with:

```sh
./tests/openwrt/prepare-qemu.sh x86_64
./tests/openwrt/run-qemu.sh x86_64
```

On the first serial boot, press Enter and change the default LAN to DHCP so
QEMU user networking can reach it. Install a disposable test public key at the
same time:

```sh
uci set network.lan.proto='dhcp'
uci -q delete network.lan.ipaddr
uci -q delete network.lan.netmask
uci commit network
mkdir -p /etc/dropbear
printf '%s\n' 'YOUR_TEST_PUBLIC_KEY' > /etc/dropbear/authorized_keys
chmod 0600 /etc/dropbear/authorized_keys
/etc/init.d/network restart
```

SSH is then available only on the host loopback address at port 2222. UDP
127.0.0.1:25180 is forwarded to the guest's wg-quic port 51820.

## Package and lifecycle test

Build the package with the matching official SDK as described in
`packaging/openwrt/README.md`, then copy the package and guest test driver:

```sh
scp -O -P 2222 dist/openwrt/wg-quic-0.3.2-r1.apk \
  tests/openwrt/guest-validate.sh root@127.0.0.1:/tmp/
ssh -p 2222 root@127.0.0.1 \
  'chmod 0755 /tmp/guest-validate.sh; /tmp/guest-validate.sh install-smoke /tmp/wg-quic-0.3.2-r1.apk'
```

Use the `armsr-armv8` package in the ARM64 guest and the `x86-64` package in
the x86_64 guest. The package must install `kmod-tun` and `ip-full`
automatically. The smoke test
also proves that direct `wg-quic run` rejects hook-bearing profiles, while
`wg-quic-quick` executes PostUp and PostDown through procd. It then atomically
adds and removes a listening peer with `reload`, verifies generation changes,
and proves the supervisor epoch, TUN, and lifecycle hooks were not restarted.

When iterating on locally cross-built binaries in an already provisioned
guest, install the binaries, init script, and UCI defaults in their package
paths and run `guest-validate.sh runtime-smoke`. Build the binaries with
`CGO_ENABLED=0`, matching the package helper, so an amd64 host build does not
accidentally depend on glibc inside the musl guest. This exercises the same
TUN, hooks, procd, and live-reload assertions without claiming that a
particular APK was installed; release package acceptance must still use
`install-smoke`.

## Encrypted tunnel test

Generate disposable peer keys and both profiles:

```sh
./tests/openwrt/prepare-interop.sh
scp -O -P 2222 tests/openwrt/.qemu/openwrt.conf \
  tests/openwrt/guest-validate.sh root@127.0.0.1:/tmp/
ssh -p 2222 root@127.0.0.1 \
  '/tmp/guest-validate.sh interop-start /tmp/openwrt.conf'
```

Keep the host peer online in one terminal:

```sh
./tests/openwrt/.qemu/tools/wg-quic-netstack-client \
  -config ./tests/openwrt/.qemu/host.conf -hold 30s
```

While it is online, validate and stop the guest side:

```sh
ssh -p 2222 root@127.0.0.1 '/tmp/guest-validate.sh interop-verify'
ssh -p 2222 root@127.0.0.1 '/tmp/guest-validate.sh interop-stop'
```

For reboot coverage, leave the UCI instances enabled, reboot, recopy the guest
driver because `/tmp` is volatile, and run `guest-validate.sh boot-verify`.
Finish with `guest-validate.sh uninstall`; it verifies removal of processes,
interfaces, sockets, and firewall state while retaining `/etc/wg-quic/*.conf`.
