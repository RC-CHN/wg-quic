# wg-quic desktop

This directory contains one Electron UI around the existing wg-quic command
boundary. It is deliberately not a second tunnel implementation.

| Desktop action | Existing source of truth |
|---|---|
| Discover tunnels | platform `.conf` directory |
| Validate | `wg-quic-quick check NAME` |
| Start | `wg-quic-quick up NAME` |
| Stop | `wg-quic-quick down NAME` |
| Runtime status | `wg-quic show NAME --json` |

The Electron renderer is sandboxed and has no Node.js access. Its preload
bridge exposes only the fixed operations above. The main process uses
`execFile` with an argument array; it does not expose a shell or an arbitrary
command API.

## Development

Node.js 22 LTS (22.12 or newer) and Go are used by the current build. Creating
the Windows MSI also requires WiX Toolset 3.14:

```sh
# Windows, from an Administrator shell:
choco install wixtoolset --version=3.14.0

cd desktop
npm ci
npm run check
npm test
npm start
```

`npm start`, `npm run package`, and `npm run make` first compile the two Go
commands into `resources/bin/`. A Windows build also copies the pinned
architecture-matching `wintun.dll`. Generated native programs, Webpack output,
and installers are ignored by Git.

For an unprivileged development fixture, point the desktop shell at a
temporary profile directory:

```sh
WG_QUIC_CONFIG_DIR=/tmp/wg-quic-desktop npm start
```

The application uses the production directories by default:

- Linux: `/etc/wg-quic`
- Windows: `%ProgramData%\wg-quic\interfaces`

## Interaction

The window follows the WireGuard for Windows interaction model: configured
tunnels are listed on the left, the selected tunnel's status and controls stay
on the right, and activation is an explicit stateful action. The current
selection survives status refreshes.

- Import a configuration with the Import button or `Ctrl+O`.
- Refresh immediately with `Ctrl+R`.
- Move through the tunnel list with the up and down arrow keys.
- Closing the window on Windows keeps the app in the notification area; use
  the tray menu to reopen or quit it.

The UI polls status every two seconds while visible. It shows WireGuard
traffic, QUIC RTT/bandwidth/pacing estimates, peer sessions, and FEC recovery
counters without rendering private configuration values.

## Verification

```sh
npm run check
npm test
npm run package
npm run smoke:native
npm run make
```

`npm run smoke:app` additionally starts the packaged application, waits for
the renderer to load a real backend snapshot, verifies the primary controls,
and exits. On headless Linux, run it through
`xvfb-run --auto-servernum npm run smoke:app`.

CI installs the generated Linux Deb before the renderer smoke so Chromium uses
the package's root-owned setuid sandbox helper. The Windows job runs both
`tests/windows/privileged-lifecycle.ps1` against the bundled executables and
`tests/windows/desktop-installed-lifecycle.ps1` against the generated
per-machine WiX MSI. Together they cover installation, UI-driven import and
UAC consent, a real LocalSystem service, Wintun, host network policy,
unprivileged status, teardown, and uninstall.

## Privileges and platform boundary

The desktop shell does not run Electron itself as Administrator and does not
add a second tunnel implementation. On Windows, validation, system-directory
imports, and tunnel start/stop are delegated to the narrow
`wg-quic-quick desktop-helper` command after a UAC consent prompt. Request
values are inherited as data rather than interpolated into PowerShell, and the
helper reports through a one-use local Named Pipe. Runtime status uses a
separate local status-only pipe; mutating core control remains restricted to
LocalSystem and Administrators.

Before a Windows tunnel service is created, the native core, quick helper, and
Wintun DLL are copied into an ACL-restricted, content-addressed runtime under
`%ProgramData%\wg-quic\runtime`. The MSI installs the UI and elevation helper
under ACL-protected Program Files, so an unelevated process cannot replace the
helper before a UAC operation. A running service never points into the
installer's versioned application directory and remains restartable across
desktop upgrades. Configuration and staged runtime data are preserved on
desktop uninstall so uninstalling the UI cannot silently destroy an active
tunnel.

The desktop package intentionally targets only Windows and Linux, matching the
platforms with native `wg-quic-quick` host integration.
