# Changelog

## Unreleased

## v0.3.0 - 2026-08-14

Adaptive FEC, desktop tunnel management, and data-plane performance release.

- Interleaved FEC groups across the wire and adapted interleave depth to
  observed burst loss, so bursty links recover packets that single sequential
  groups would lose; reordered shards are absorbed through a completion grace
  window, and groups abandoned by a newer group expire immediately instead of
  occupying the decoder.
- Raised the FEC data-shard ceiling and parity cap, and exposed the shard
  layout to the benchmark fixture, which also gained a stateful burst-loss
  model for asymmetric link profiles.
- Stopped duplicating WireGuard handshake packets across FEC groups while
  still duplicating other priority datagrams, keeping handshake latency
  off the parity path.
- Pooled datagram send, reassembly, and FEC marshal buffers across the bind,
  QUIC, and FEC layers; released pooled send buffers on queue-full drops, cut
  receive-path copies, disabled GSO on the DATAGRAM-only carrier, and made
  Salamander obfuscation word-wise with cached keyed BLAKE2b states.
- Restored idle peers autonomously at the transport layer, retried network
  path reconciliation after endpoint changes, and tracked migrated peer
  endpoints so NAT rebinding keeps sessions authenticated; the status schema
  now exposes per-peer authenticated receive/send activity.
- Added a desktop inline new/edit tunnel form with key generation and
  validation, tunnel deletion, a light theme, peer endpoint display, and
  broker-mediated configuration reads on Windows; activation and deactivation
  now follow live core status transitions with elapsed-time indication
  instead of a static pending label.
- Installed the OPNsense status helper as an executable and exercised client
  address rebinding in the QEMU plugin tests.

## v0.2.3 - 2026-08-12

Windows custom-install trust and MSI hardening release.

- Accepted per-machine Windows installations outside Program Files only when
  the LocalSystem manager, fixed `bin\wg-quic.exe` child, and their containing
  directories have trusted owners, non-writable ACLs, no reparse points, and
  single-link executable identities pinned during runtime staging.
- Secured custom MSI installation roots and `bin` directories before service
  startup with protected, inheritable SDDL: LocalSystem and Administrators
  retain full access while ordinary users receive read and execute access.
- Preserved the stricter portable-executable boundary and surfaced the
  validation reason for each rejected core candidate instead of returning only
  a generic trusted-core lookup error.
- Added a native Windows installed-MSI lifecycle rooted at a custom system-drive
  path containing spaces, covering ACLs, the LocalSystem broker, desktop
  import/check/up/down, Wintun, routes, DNS, upgrade, uninstall, and cleanup.

## v0.2.2 - 2026-08-11

Windows install-once privilege and service-recovery release.

- Explicitly marked the desktop executable `asInvoker`, keeping the WebView
  unelevated while the MSI-installed LocalSystem manager handles fixed,
  authenticated tunnel operations after the one install-time UAC consent.
- Bounded Windows tunnel-service startup; on rollback, retained cleanup state
  until SCM confirmed teardown, and completed failed-start recovery without
  deleting live or ambiguous service resources.
- Captured staged-service startup diagnostics before rollback, normalized
  synchronous Named Pipe cancellation, and hardened secure ProgramData DACL
  validation and migration leases against conflicting delete access.
- Expanded installed-MSI coverage to combine a real desktop
  import/check/up/down lifecycle with a separate genuine UAC-filtered local
  Administrator broker lifecycle, while preserving broker rejection of
  standard users and the explicit UAC path for hook-bearing profiles.
- Updated the privileged Wintun fixture to install configurations through the
  same protected, atomic import path used by the desktop and verified the full
  service, adapter, address, route, DNS, failure, repair, and cleanup cycle.

## v0.2.1 - 2026-08-11

Windows desktop privilege and inactive-state repair release.

- Added an MSI-installed, narrowly scoped LocalSystem management service so
  local administrators can import, validate, activate, and deactivate tunnels
  from the unelevated desktop after the install-time consent; true standard
  users retain the per-operation UAC fallback, and profiles with lifecycle
  hooks always require that explicit approval.
- Authenticated the broker from the named-pipe client token, removed
  client-side pipe-instance creation rights, bounded the authorization phase,
  and prevented automatic retries after an operation may have been dispatched.
- Hardened ProgramData configuration and runtime staging with known-folder
  resolution, no-follow handles, LocalSystem-owned protected ACLs, single-link
  checks, fresh random service runtimes, and quarantine migration for legacy
  roots. Retired runtimes are reclaimed after confirmed SCM teardown, with a
  fail-closed orphan sweep for interrupted lifecycle operations.
- Hardened the desktop elevation IPC across filtered and elevated Windows
  tokens, and coalesced overlapping startup refreshes to avoid stale UI state.
- Normalized missing Windows control pipes into a stable inactive state so an
  idle tunnel no longer exposes raw Named Pipe errors while genuine failures
  remain visible.
- Polished inactive, pending-action, and management-service status in the
  desktop UI and expanded native Windows package coverage around the installed
  privilege boundary, including a live v0.2.0 upgrade plus installed WebView
  and limited-Administrator broker coverage.

## v0.2.0 - 2026-08-11

Protocol freeze, native desktop, and appliance observability release.

- Froze the v1 wire contract for QUIC, WGQ1 fragmentation, WGQF FEC, and
  Salamander obfuscation, with golden compatibility vectors and explicit
  versioning rules.
- Replaced the Electron shell with a smaller Tauri desktop for Windows and
  Linux while retaining `wg-quic-quick` as the single tunnel lifecycle
  implementation.
- Added per-machine WiX MSI and Debian packages, constrained desktop operation
  helpers, narrow UAC elevation, protected Windows runtime staging, and
  read-only status endpoints for unprivileged clients.
- Exposed live peer/session state consistently on Windows, Linux, FreeBSD, and
  OPNsense, including WebUI status counters and notice-level service logs.
- Hardened shutdown and crash cleanup for Windows services, Unix supervisors,
  systemd control groups, FreeBSD child processes, endpoint routes, and
  recoverable network-policy ledgers.
- Added installed-package lifecycle coverage on native Windows and Linux
  runners, Windows-to-Linux Proton interoperability over QUIC/FEC/Salamander,
  and browser-driven OPNsense peer provisioning and traffic verification in
  QEMU.

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
