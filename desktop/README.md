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
Together they cover installation, UI-driven import, the persistent management
broker from a UAC-filtered Administrator token, standard-user rejection, the
one-operation UAC fallback, a real LocalSystem tunnel service, Wintun, network
policy, unprivileged status, teardown, and uninstall. The Windows job also
installs v0.2.0 first and upgrades it while a tunnel remains active. Because
GitHub-hosted Windows disables UAC, its real Tauri/WebView lifecycle runs under
a kernel-created `LUA_TOKEN` in the runner's existing session; the fixture
fails unless that token is limited, non-elevated, and carries the
Administrators SID only for deny checks.

## Privileges and platform boundary

The webview process never runs as Administrator or root. On Windows, the MSI
uses its install-time elevation to register and start the narrow LocalSystem
`wg-quic-manager` service. It authenticates the caller's named-pipe token and
accepts only LocalSystem, a full local Administrator token, or the linked
Administrator identity behind UAC filtering (including the kernel's exact
limited-token deny-only representation). This gives the usual local
Administrator account one-click import and tunnel controls while the desktop
continues to run unelevated. A true standard user falls back to the existing
one-operation UAC helper.

The broker accepts only fixed validate/import/start/stop requests, bounded
configuration bytes, and validated interface names; it never accepts an
arbitrary executable, command line, or source path. Profiles containing
`PreUp`, `PostUp`, `PreDown`, or `PostDown` are deliberately refused by the
persistent broker and use the UAC helper, so passwordless tunnel management
cannot become arbitrary LocalSystem command execution. UAC request values are
sent over a one-use duplex local named pipe rather than inherited through the
elevated environment or interpolated into PowerShell. Runtime status uses a
separate status-only pipe; mutating core control remains restricted to
LocalSystem and Administrators.

On Linux, privileged fixed operations are launched through `pkexec`. Imported
configuration files are copied atomically with mode `0600`; the configuration
directory remains discoverable so the unprivileged UI can enumerate tunnel
names. The status-only control socket permits observation without exposing
activation or mutation.

Before a Windows tunnel service is created, the native core, quick helper, and
Wintun DLL are copied into a fresh unpredictable LocalSystem-owned directory
under `%ProgramData%\wg-quic\runtime`; directory and file handles remain pinned
until SCM reports the service running. ProgramData components are opened
without following reparses or allowing delete-sharing, with a protected
LocalSystem owner/DACL and single-link checks for privileged files. A legacy
root that does not already have trusted provenance is atomically moved to a
`.wg-quic-quarantine-*` directory before a clean root is created. Only valid,
hook-free profiles are copied automatically; skipped profiles remain in the
quarantine and must be reviewed and explicitly imported again.

The MSI installs the UI and helper under ACL-protected Program Files, so an
unelevated process cannot replace the broker or helper. Configuration and
staged runtime data survive desktop uninstall, so uninstalling the UI cannot
silently destroy an active tunnel. Runtimes are removed only after SCM confirms
the corresponding tunnel service was deleted; broker startup also performs a
fail-closed sweep of unreferenced remnants from interrupted operations.

The desktop package intentionally targets only Windows and Linux.
