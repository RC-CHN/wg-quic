# wg-quic desktop

This directory contains a small Tauri 2 shell around the existing
`wg-quic-quick` command boundary. It is deliberately not a second tunnel
implementation.

| Desktop action | Existing source of truth |
|---|---|
| Discover tunnels | platform `.conf` directory |
| Validate | `wg-quic-quick check NAME` |
| Start | `wg-quic-quick up NAME` |
| Stop | `wg-quic-quick down NAME` |
| Runtime status | `wg-quic show NAME --json` |

The webview receives only fixed Tauri commands for those operations. Native
processes are started with argument arrays; neither the frontend nor the Rust
host exposes a shell or an arbitrary command API.

## Development

The build uses Node.js 22 LTS, Go, stable Rust, and the platform Tauri 2
prerequisites. On Linux that includes WebKitGTK 4.1. Windows builds use the
system WebView2 runtime and produce a WiX MSI.

```sh
cd desktop
npm ci
npm run check
npm test
npm start
```

`npm start`, `npm run package`, and `npm run make` compile `wg-quic` and
`wg-quic-quick` into `resources/bin/` first. A Windows build also copies the
pinned architecture-matching `wintun.dll`. Generated frontend assets, native
programs, Cargo output, and installers are ignored by Git.

For an unprivileged development fixture, point the shell at a temporary
profile directory:

```sh
WG_QUIC_CONFIG_DIR=/tmp/wg-quic-desktop npm start
```

Production configuration directories are `/etc/wg-quic` on Linux and
`%ProgramData%\wg-quic\interfaces` on Windows.

## Interaction

The window follows the WireGuard for Windows interaction model: configured
tunnels are listed on the left, and the selected tunnel's status and controls
stay on the right. The current selection survives status refreshes.

- Import a configuration with the Import button or `Ctrl+O`.
- Refresh immediately with `Ctrl+R`.
- Move through the tunnel list with the up and down arrow keys.

The UI polls status every two seconds while visible. It shows WireGuard
traffic, QUIC RTT/bandwidth/pacing estimates, peer sessions, and FEC recovery
counters without rendering private configuration values.

## Verification

```sh
npm run check
npm test
npm run native
cargo test --locked --manifest-path src-tauri/Cargo.toml
npm run make
npm run smoke:native
```

`npm run smoke:app` starts a built or installed application, waits for the
renderer to load a real backend snapshot, and exits through a deterministic
result-file protocol. On headless Linux, run it through `xvfb-run`.

CI builds both supported desktop targets. Linux installs the generated Deb
before its renderer smoke. Windows builds the MSI and then runs both
`tests/windows/privileged-lifecycle.ps1` against the bundled commands and
`tests/windows/desktop-installed-lifecycle.ps1` against the installed app.
Together they cover installation, UI-driven import and UAC consent, a real
LocalSystem service, Wintun, network policy, unprivileged status, teardown,
and uninstall.

## Privileges and platform boundary

The webview process never runs as Administrator or root. On Windows,
validation, imports, and tunnel start/stop are delegated to the narrow
`wg-quic-quick desktop-helper` operation after UAC consent. Request values are
inherited as data rather than interpolated into PowerShell, and the result is
returned through a one-use local named pipe. Runtime status uses a separate
status-only pipe; mutating core control remains restricted to LocalSystem and
Administrators.

On Linux, privileged fixed operations are launched through `pkexec`. Imported
configuration files are copied atomically with mode `0600`; the configuration
directory remains discoverable so the unprivileged UI can enumerate tunnel
names. The status-only control socket permits observation without exposing
activation or mutation.

Before a Windows tunnel service is created, the native core, quick helper,
and Wintun DLL are copied into an ACL-restricted, content-addressed runtime
under `%ProgramData%\wg-quic\runtime`. The MSI installs the UI and helper under
ACL-protected Program Files, so an unelevated process cannot replace the
helper before UAC. Configuration and staged runtime data survive desktop
uninstall, so uninstalling the UI cannot silently destroy an active tunnel.

The desktop package intentionally targets only Windows and Linux.
