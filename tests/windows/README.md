# Windows real-peer lifecycle soak

Run `soak-lifecycle.ps1` in an elevated PowerShell on a disposable Windows
machine with a real reachable WireGuard peer. Unlike a service-only test, each
round must deliver ICMP over the tunnel before shutdown.

```powershell
.\tests\windows\soak-lifecycle.ps1 `
  -BinDirectory C:\wgq-test\bin `
  -ConfigPath C:\wgq-test\peer.conf `
  -OutputDirectory C:\wgq-test\results `
  -TunnelName wgqsoak20261003 `
  -TargetAddress 10.89.0.2 `
  -Cycles 20
```

The binary directory must contain matching `wg-quic.exe`,
`wg-quic-quick.exe`, and `wintun.dll`. Use a fresh tunnel name beginning with
`wgqsoak`; import intentionally refuses to overwrite an existing configuration.
Use a split tunnel with test-only addresses and no hooks or DNS overrides.

Add `-UseDesktopBroker` to exercise the desktop's `desktop-client import/up/down`
path. In that mode `BinDirectory` must be the installation used by the running
`wg-quic-manager` service. Stop the service before replacing its binaries and
restart it before testing; a running broker must match the binaries being
reported. This mode can run with a normal desktop user's token if the test
configuration is readable and the output directory is writable by that user.
Emergency fixture repair still uses the direct administrative CLI; if a
limited-token run needs repair, finish cleanup from an elevated shell before
starting another test.

`cycles.json` records startup, first successful ping after startup, shutdown,
and failures. Three more pings verify continuing delivery. The naturally
selected source address and route must both belong to the dedicated tunnel,
so a reachable LAN address or another VPN cannot produce a false pass. Route
inspection runs after ping timing and is recorded separately as
`route_check_ms`; it adds a pause before shutdown, so this fixture is not an
immediate-start/stop timing test. Per-command logs
and `environment.json` retain the exact binaries and Windows build. A first
startup may include driver installation and should be reported separately.
On failures the fixture captures address and route state and attempts repair
only for its dedicated tunnel. It retains the imported test configuration for
diagnosis; remove that configuration after inspecting the results.

For sustained traffic, loss and peer restart tests, use
[`soak-probe`](../network/soak-probe/README.md) while leaving the tunnel running.
Always report the actual Windows version; Windows 10 or Windows Server results
do not establish Windows 11 behavior.

## Native network cleanup integration

Cross-compile the platform tests with `GOOS=windows GOARCH=amd64 go test -c
./internal/platform`, copy `platform.test.exe` beside the signed `wintun.dll`,
and run in an elevated shell on the disposable Windows machine:

```powershell
$env:WG_QUIC_TEST_WINDOWS_NETWORK='1'
.\platform.test.exe -test.v -test.timeout 6m `
  -test.run 'TestWindows(NetworkRollback|NativeAddressDelete|DNS)'
```

The opt-in tests create and remove their own two Wintun adapters. Address and
route tests verify exact interface ownership and repeated cleanup while both
adapters remain open. DNS tests compare native reset with the existing
PowerShell reset for mixed IPv4/IPv6 servers plus a suffix, a suffix alone,
and IPv4 servers alone. They compare effective server/suffix settings and
the corresponding per-interface registry strings, preserve an unrelated
search-list value, and verify that the second adapter's DNS does not change.
Absent and empty registry strings are compared as the same reset value.

Native DNS reset uses version 1 of Microsoft's
[`SetInterfaceDnsSettings`](https://learn.microsoft.com/en-us/windows/win32/api/netioapi/nf-netioapi-setinterfacednssettings),
documented for Windows 10 build 19041 and later. The implementation probes the
API at runtime and retains PowerShell only if the entry point is missing.
Other native failures remain errors. `DNS_SETTING_DOMAIN` corresponds to the
existing connection-specific suffix reset; `DNS_SETTING_SEARCHLIST` is a
different setting and is not changed. See Microsoft's
[`DNS_INTERFACE_SETTINGS`](https://learn.microsoft.com/en-us/windows/win32/api/netioapi/ns-netioapi-dns_interface_settings).
The empty-string reset and architecture-specific GUID calling conventions
were checked against upstream WireGuard's
[`SetDNS`/`FlushDNS`](https://git.zx2c4.com/wireguard-windows/tree/tunnel/winipcfg/luid.go)
and [API binding](https://git.zx2c4.com/wireguard-windows/tree/tunnel/winipcfg/winipcfg.go).
Windows amd64 passes the GUID indirectly; ARM64 passes two 64-bit words.
Compilation for both architectures is required; an amd64 guest does not
establish ARM64 runtime behavior. Network startup still uses its existing
PowerShell batch.
