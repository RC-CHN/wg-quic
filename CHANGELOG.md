# Changelog

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
