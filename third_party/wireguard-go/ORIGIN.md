# Fork origin and maintenance

This directory was imported from:

- project: `wireguard-go`
- upstream module: `golang.zx2c4.com/wireguard`
- upstream revision: `ecfc5a8d5446`
- upstream pseudo-version: `v0.0.0-20260522210424-ecfc5a8d5446`
- imported on: 2026-08-04
- license: MIT; see `LICENSE`

The import contains all Go source files and tests from that revision, together
with the upstream `tests/netns.sh`, README, Makefile, and license. The upstream
`go.mod` and `go.sum` were intentionally removed so this fork is part of the
root `github.com/RC-CHN/wg-quic` module and is covered by `go test ./...`.
The complete imported test inventory is recorded in `IMPORTED_TESTS.md`.

Initial local changes:

1. Internal imports were rewritten from
   `golang.zx2c4.com/wireguard/...` to
   `github.com/RC-CHN/wg-quic/third_party/wireguard-go/...`.
2. `tun/checksum_test.go` now uses the protocol-independent numeric TCP
   protocol constant `6`. The upstream test imported
   `golang.org/x/sys/unix.IPPROTO_TCP` on Windows, which prevented the otherwise
   platform-neutral test from compiling there.
3. `device/pools_test.go` formats `atomic.Uint32.Load()` rather than copying the
   `atomic.Uint32` value into `testing.Errorf`, fixing the Go vet `copylocks`
   diagnostic without changing test behavior.
4. `device.NewDeviceWithOptions` can disable automatic device Up/Down changes
   from TUN events. wg-quic uses this mode so its quick management process can
   prepare endpoint route leases before activating the outer transport.

From this point, wg-quic production code and tests use this directory rather
than downloading `golang.zx2c4.com/wireguard`. Future upstream synchronization
is an explicit code-review operation; behavior changes are made and tested in
this repository.
