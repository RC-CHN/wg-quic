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

`cycles.json` records startup, first successful ping after startup, shutdown,
and failures. Three more pings verify continuing delivery. Per-command logs
and `environment.json` retain the exact binaries and Windows build. A first
startup may include driver installation and should be reported separately.
On failures the fixture captures address and route state and attempts repair
only for its dedicated tunnel. It retains the imported test configuration for
diagnosis; remove that configuration after inspecting the results.

For sustained traffic, loss and peer restart tests, use
[`soak-probe`](../network/soak-probe/README.md) while leaving the tunnel running.
Always report the actual Windows version; Windows 10 or Windows Server results
do not establish Windows 11 behavior.
