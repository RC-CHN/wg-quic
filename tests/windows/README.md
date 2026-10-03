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

For real DNS policy validation, run the isolated
[`DNS responder`](../network/dns-probe/README.md) on the configured tunnel DNS
peer and add `-DnsProbeSuffix wgq-native.test.invalid` to the lifecycle fixture.
Each cycle uses a fresh name with the default Windows resolver, records its
answer and elapsed time, and requires the expected test address. Pair that
record with the responder's query log. Omitting the option retains the usual
lifecycle-only checks.

## Native network policy integration

Cross-compile the platform tests with `GOOS=windows GOARCH=amd64 go test -c
./internal/platform`, copy `platform.test.exe` beside the signed `wintun.dll`,
and run in an elevated shell on the disposable Windows machine:

```powershell
$env:WG_QUIC_TEST_WINDOWS_NETWORK='1'
$arguments = @(
  '-test.v', '-test.timeout', '8m', '-test.run',
  'TestWindows(NetworkRollback|NetworkApply|NativeStartup|NativeAddressDelete|DNS)'
)
& .\platform.test.exe @arguments
```

The opt-in tests create and remove their own two Wintun adapters. Address and
route tests verify exact interface ownership and repeated cleanup while both
adapters remain open. DNS tests compare native apply and reset with the existing
PowerShell operations for mixed IPv4/IPv6 servers plus a suffix, a suffix alone,
and each server family alone. They compare effective server/suffix settings and
the corresponding per-interface registry strings, preserve an unrelated
search-list value, and verify that the second adapter's DNS does not change.
Absent and empty registry strings are compared as the same reset value.
PowerShell can pad a `NameServer` registry string with extra trailing NULs;
the apply comparison trims only that padding and retains the original raw
snapshots in its output. Interior NULs and nonzero suffixes remain differences.

Native DNS apply and reset use version 1 of Microsoft's
[`SetInterfaceDnsSettings`](https://learn.microsoft.com/en-us/windows/win32/api/netioapi/nf-netioapi-setinterfacednssettings),
documented for Windows 10 build 19041 and later. The implementation probes the
API at runtime and retains PowerShell only if the entry point is missing.
Other native failures remain errors. `DNS_SETTING_DOMAIN` corresponds to the
existing connection-specific suffix reset; `DNS_SETTING_SEARCHLIST` is a
different setting and is not changed. See Microsoft's
[`DNS_INTERFACE_SETTINGS`](https://learn.microsoft.com/en-us/windows/win32/api/netioapi/ns-netioapi-dns_interface_settings).
Applying servers preserves the listed order within each address family and
leaves any absent family unchanged, matching `Set-DnsClientServerAddress`.
A suffix-only apply preserves both server lists; a servers-only apply preserves
the existing suffix. Every successfully applied native step retains cleanup
ownership if a later step fails or is canceled. The integration comparison
starts from existing dual-stack DNS, suffix and search-list values so these
preservation rules cannot pass merely because the initial settings were empty.
The empty-string reset and architecture-specific GUID calling conventions
were checked against upstream WireGuard's
[`SetDNS`/`FlushDNS`](https://git.zx2c4.com/wireguard-windows/tree/tunnel/winipcfg/luid.go)
and [API binding](https://git.zx2c4.com/wireguard-windows/tree/tunnel/winipcfg/winipcfg.go).
Windows amd64 passes the GUID indirectly; ARM64 passes two 64-bit words.
Compilation for both architectures is required; an amd64 guest does not
establish ARM64 runtime behavior.

Startup uses native IP Helper calls for MTU/DAD, temporary addresses,
active routes and DNS, with the existing DNS cmdlets as a missing-API fallback. The
startup comparison first records fresh Wintun state, then checks native
versus PowerShell DHCP, DAD, MTU, address origins, source-address selection,
route metrics and policy-store behavior. A native address must be Preferred
immediately after creation, before any reference PowerShell calls can hide
a DAD wait. Microsoft documents the native
[`SetIpInterfaceEntry`](https://learn.microsoft.com/en-us/windows/win32/api/netioapi/nf-netioapi-setipinterfaceentry)
and [`CreateUnicastIpAddressEntry`](https://learn.microsoft.com/en-us/windows/win32/api/netioapi/nf-netioapi-createunicastipaddressentry)
contracts: read the current interface before changing MTU/DAD, normalize
IPv4 SitePrefixLength to zero, and initialize each temporary address row.
DHCP parity is measured on fresh Wintun devices; these calls must not be
generalized to change physical adapters.

The Windows CI job runs these integration tests on its Windows Server
runner. That provides recurring native API coverage and does not replace
the separate Windows 11 guest acceptance run.
