# Changelog

## Unreleased

- Fixed Windows desktop imports and tunnel controls by delegating only the
  privileged operation to a narrow UAC helper while leaving Electron
  unelevated.
- Replaced the per-user Squirrel package with a per-machine WiX MSI so the
  elevation helper is installed under ACL-protected Program Files.
- Added a local status-only Named Pipe so the desktop can display live tunnel
  state without opening mutating control operations to unelevated callers.
- Staged Windows service binaries in an ACL-restricted, content-addressed
  ProgramData runtime so desktop upgrades and UI removal cannot invalidate an
  installed tunnel service.
- Added a Windows CI lifecycle that installs the generated MSI,
  drives import/start/status/stop through the desktop, and uninstalls it.

## v0.1.2 - 2026-08-05

Degraded-link control, applications, and Windows lifecycle hardening release.

- Added adaptive degraded-link congestion/FEC control, high-RTT tuning, and
  telemetry-backed benchmark fixtures with a direct wireguard-go baseline.
- Removed avoidable QUIC framing, receive, and queue copies and pooled received
  datagrams.
- Added the `wg-quic` OPNsense plugin, official-framework packaging, and
  FreeBSD 14/15 CI coverage.
- Added the Windows and Linux Electron desktop shell around the existing
  `wg-quic-quick` lifecycle, including native packaging and application smoke
  tests.
- Added a privileged Windows CI lifecycle covering a real LocalSystem service,
  Wintun, address/MTU/DNS policy, AllowedIPs, endpoint route leases, runtime
  status, and complete cleanup.
- Bounded endpoint, host-policy, core-process, and SCM shutdown; added
  StopPending checkpoints, wait hints, stage diagnostics, and a kill-on-close
  Windows Job Object for the supervised core.
- Added explicit `wg-quic-quick down INTERFACE --repair` recovery. Normal
  `down` never force-terminates; repair preserves live-owner and ambiguous
  routes and may terminate only the exact stuck tunnel service after a final
  graceful-stop window.

## v0.1.1 - 2026-08-04

Routing, endpoint lifecycle, and architecture hardening release.

- Added a Windows endpoint route lease manager using the native best-route API,
  persistent ownership ledger, named-mutex liveness, shared leases, precise
  cleanup, and crash reconciliation.
- Added dynamic DNS endpoint supervision and live core endpoint updates without
  restarting the tunnel.
- Added endpoint route migration after DNS changes, Windows network changes, or
  disappearance of a managed route.
- Added endpoint session-readiness and route-lease state to control-plane
  reporting.
- Self-hosted and pinned quic-go v0.61.0 under `third_party`, with repository
  resolution checks and upstream tests included in CI.
- Separated device lifecycle, endpoint policy, and host networking capabilities
  between `wg-quic` and `wg-quic-quick`.
- Replaced mutable launch arguments with one versioned immutable configuration
  snapshot over standard input, keeping key material out of the process command
  line.
- Centralized endpoint parsing, activation, MTU policy, DNS policy, and host
  policy projections.
- Fixed retry behavior for failed Windows and FreeBSD route-lease releases and
  made Windows Named Pipe listener shutdown persistent under cancellation races.
- Expanded Linux tunnel interoperability, Windows stress, FreeBSD native, pinned
  WireGuard behavior, and architecture-boundary coverage.

## v0.1.0 - 2026-08-04

Initial experimental release.

- Userspace WireGuard data plane maintained as an in-repository fork.
- QUIC DATAGRAM carrier with congestion control, fragmentation/reassembly, and
  bounded priority queues.
- Adaptive systematic Reed-Solomon FEC with feedback and exposed counters.
- Key-derived Salamander-style packet obfuscation; an existing WireGuard
  `PresharedKey` is mixed in without adding configuration fields.
- WireGuard-compatible `wg-quick` configuration syntax. Both peers must run
  wg-quic; the outer protocol is not compatible with stock WireGuard.
- Separate `wg-quic` core and `wg-quic-quick` host-policy executables.
- Linux host backend and service, validated with privileged multi-node tunnel
  tests.
- FreeBSD host backend and rc.d service. Native tests pass; privileged
  TUN/routing VM validation remains experimental.
- Windows Wintun, Named Pipe, PowerShell host-policy, and per-tunnel SCM
  backend, plus a key-redacted foreground debug log. Native userspace tests
  pass; privileged Wintun/routing integration remains experimental.
- Self-contained amd64 and arm64 release archives for Linux, FreeBSD, and
  Windows. Windows bundles include the matching official Wintun 0.14.1 DLL,
  provenance, license, and checksums.
