# Changelog

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
