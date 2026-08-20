# OpenWrt ARM64 QEMU validation

This fixture validates the installable package and a real encrypted tunnel on
an official OpenWrt ARM64 image. It covers dependency installation, TUN
creation, procd/UCI lifecycle, hooks, fw4 policy, reboot recovery, uninstall
cleanup, and profile retention. It does not build a LuCI UI.

The fixture is pinned to OpenWrt 25.12.5 `armsr/armv8`. Downloaded images and
generated keys/configurations live under the gitignored `.qemu/` directory.

## Start a clean guest

Install `qemu-system-aarch64`, `qemu-img`, `curl`, and `gzip`, then run:

```sh
./tests/openwrt/prepare-qemu.sh
./tests/openwrt/run-qemu.sh
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
scp -O -P 2222 dist/openwrt/wg-quic-0.3.1-r1.apk \
  tests/openwrt/guest-validate.sh root@127.0.0.1:/tmp/
ssh -p 2222 root@127.0.0.1 \
  'chmod 0755 /tmp/guest-validate.sh; /tmp/guest-validate.sh install-smoke /tmp/wg-quic-0.3.1-r1.apk'
```

The package must install `kmod-tun` and `ip-full` automatically. The smoke test
also proves that direct `wg-quic run` rejects hook-bearing profiles, while
`wg-quic-quick` executes PostUp and PostDown through procd.

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
