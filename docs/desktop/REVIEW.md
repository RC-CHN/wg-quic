# Reproducing the desktop UI review

The design rules are in [DESIGN.md](DESIGN.md). The built application is used for
all browser and native checks. Synthetic snapshots substitute for privileged
network operations in the browser; they do not establish a VPN connection.

## Automated checks

From `desktop/`, using Node 22:

```sh
npm run check
npm test
BROWSER_PATH=/path/to/chromium npm run smoke:browser
npm run package
dbus-run-session -- xvfb-run --auto-servernum npm run smoke:app
```

The existing browser job now also drives actual pointer events through 32
combinations: 1180 × 760 / 920 × 620, English / Chinese, light / dark, and
Overview / Diagnostics / Editor / Source. It checks horizontal overflow and
whether primary controls are visible and unobstructed. The long-name case is
separate. Set `WG_QUIC_REVIEW_SCREENSHOTS` to an output directory to keep images.

The renderer checks also run inside the native WebKit/WebView build. They cover
search, focus, keyboard navigation, tab persistence, generated keys, preservation
of edits and comments, field-level errors, correction of errors, saving/applying,
unknown status, authentication and diagnostic collection. These supplement the
existing installed Linux/Windows service and upgrade tests in CI.

## Optional narrated recording

Recording needs a local Chromium, FFmpeg, a Chinese font (Noto Sans CJK SC), and
Playwright Core. These are review tools, not shipped application dependencies.
Install Playwright separately if it is not already available:

```sh
npm install --prefix /tmp/wg-quic-video playwright-core@1.58.2
# If Playwright's video encoder is absent, install its FFmpeg component:
node /tmp/wg-quic-video/node_modules/playwright-core/cli.js install ffmpeg
```

From the repository root, after `npm --prefix desktop run build:web`:

```sh
PLAYWRIGHT_MODULE=/tmp/wg-quic-video/node_modules/playwright-core/index.mjs \
  BROWSER_PATH=/path/to/chromium node desktop/scripts/record-ui-demo.mjs
python3 desktop/scripts/render-ui-demo.py
```

The output is `dist/desktop-redesign/demo.mp4`, with readable Chinese captions in
a separate band below the application. `demo.srt`, `chapters.json`, raw video,
and light/dark screenshots are retained alongside it. `WG_QUIC_DEMO_DIR` changes
the recorder's output directory; pass that directory to the Python renderer.

The video visibly identifies simulated tunnel data. It records the actual
built renderer with mouse/keyboard input. File dialogs, network transitions,
and configuration writes use an isolated fixture. No production configurations,
credentials or actual traffic appear. The fixture and recording utilities are
not imported by the application bundle.

The final peer-card recording also shows independent send/receive rates and
counters. The fixture advances observations and counters with elapsed time;
measured zero, unknown status and focus retention are separately exercised by
the renderer status smoke test. The recording uses no production tunnel.
