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

Node.js 22 LTS (22.12 or newer) and Go are used by the current build:

```sh
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
the package's root-owned setuid sandbox helper. The Windows job additionally
runs `tests/windows/privileged-lifecycle.ps1` against the bundled executables,
covering a real LocalSystem service, Wintun, host network policy, status, and
teardown.

## Privileges and platform boundary

The desktop shell does not add a privileged desktop daemon. `up`, `down`, and
system-directory imports consequently have exactly the same permissions as
running `wg-quic-quick` in a terminal. A denied operation is reported by the
UI with the original CLI error.

The desktop package intentionally targets only Windows and Linux, matching the
platforms with native `wg-quic-quick` host integration.
