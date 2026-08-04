# Wintun prebuilt binaries

This directory contains unmodified, official prebuilt Wintun 0.14.1 binaries
used to make wg-quic Windows release bundles self-contained.

- Source archive: <https://www.wintun.net/builds/wintun-0.14.1.zip>
- Archive SHA-256:
  `07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51`
- Imported architectures: amd64 and arm64
- Imported on: 2026-08-04

The DLLs are byte-for-byte copies from the archive. Do not rebuild, rename, or
modify them. `LICENSE.txt` is the license shipped in the same archive and must
remain alongside every distributed DLL.

The Go binding remains the pinned `golang.zx2c4.com/wintun` module declared in
the root `go.mod`; this directory supplies its required runtime DLL only.
