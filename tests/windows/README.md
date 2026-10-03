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
