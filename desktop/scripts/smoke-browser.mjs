#!/usr/bin/env node
// Exercise the built UI in a real Chromium DOM with a fixed native boundary.
// The packaged smoke separately runs the same interaction checks in WebKit /
// WebView with the actual snapshot and key-generation commands.
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../dist');
const browser = process.env.BROWSER_PATH || [
  '/usr/bin/google-chrome', '/usr/bin/google-chrome-stable', '/usr/bin/chromium',
].find(existsSync);
if (!browser) throw new Error('Set BROWSER_PATH to a Chromium executable');
const profile = mkdtempSync(path.join(tmpdir(), 'wg-quic-browser-'));
const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css' };
const server = createServer((request, response) => {
  const file = path.resolve(root, `.${new URL(request.url, 'http://localhost').pathname === '/' ? '/index.html' : new URL(request.url, 'http://localhost').pathname}`);
  if (!file.startsWith(`${root}${path.sep}`)) { response.writeHead(403).end(); return; }
  try {
    response.setHeader('Content-Type', mime[path.extname(file)] || 'application/octet-stream');
    response.end(readFileSync(file));
  } catch { response.writeHead(404).end(); }
});
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
const child = spawn(browser, [
  '--headless', '--no-sandbox', '--disable-gpu', '--disable-background-networking',
  '--no-first-run', '--remote-debugging-pipe', `--user-data-dir=${profile}`,
], { stdio: ['ignore', 'ignore', 'pipe', 'pipe', 'pipe'] });
let nextID = 0;
let buffer = '';
let stderr = '';
const pending = new Map();
child.stderr.on('data', (chunk) => { stderr = (stderr + chunk).slice(-8192); });
child.stdio[4].on('data', (chunk) => {
  buffer += chunk;
  let end;
  while ((end = buffer.indexOf('\0')) >= 0) {
    const message = JSON.parse(buffer.slice(0, end));
    buffer = buffer.slice(end + 1);
    const waiter = pending.get(message.id);
    if (waiter) {
      pending.delete(message.id);
      clearTimeout(waiter.timer);
      message.error ? waiter.reject(new Error(JSON.stringify(message.error))) : waiter.resolve(message.result);
    }
  }
});
function send(method, params = {}, sessionId) {
  return new Promise((resolve, reject) => {
    const id = ++nextID;
    const timer = setTimeout(() => { pending.delete(id); reject(new Error(`Timed out: ${method}\n${stderr}`)); }, 15000);
    pending.set(id, { resolve, reject, timer });
    child.stdio[3].write(`${JSON.stringify({ id, method, params, sessionId })}\0`);
  });
}
try {
  const { targetId } = await send('Target.createTarget', { url: 'about:blank' });
  const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true });
  const call = (method, params) => send(method, params, sessionId);
  await call('Page.enable');
  await call('Emulation.setDeviceMetricsOverride', { width: 1180, height: 760, deviceScaleFactor: 1, mobile: false });
  await call('Page.addScriptToEvaluateOnNewDocument', { source: `
    localStorage.setItem('wg-quic-language', ${JSON.stringify(process.env.WG_QUIC_SMOKE_LANGUAGE || 'en')});
    window.__TAURI_INTERNALS__ = { invoke: async (command, args) => {
      if (command === 'desktop_smoke_settings') return { mode: 'renderer' };
      if (command === 'snapshot') return {
        backend: { supported: true, platform: 'linux', arch: 'x64', configDirectory: '/tmp/browser-fixture', coreVersion: 'fixture', quickVersion: 'fixture' },
        tunnels: [], refreshedAt: String(Date.now())
      };
      if (command === 'generate_keys') return JSON.stringify({private_key: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=', public_key: 'AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='});
      if (command === 'derive_public_key') return 'AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=';
      if (command === 'complete_desktop_smoke') { window.__smokeResult = args; return; }
      throw new Error('Unexpected native operation: ' + command);
    }};
  ` });
  await call('Page.navigate', { url: `http://127.0.0.1:${server.address().port}/` });
  const deadline = Date.now() + 20000;
  let result;
  while (Date.now() < deadline) {
    const response = await call('Runtime.evaluate', { expression: 'window.__smokeResult', returnByValue: true });
    result = response.result?.value;
    if (result) break;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  if (!result || result.failed) throw new Error(result?.message || `Renderer did not finish\n${stderr}`);
  await send('Browser.grantPermissions', { origin: `http://127.0.0.1:${server.address().port}`, permissions: ['clipboardReadWrite', 'clipboardSanitizedWrite'] });
  await call('Runtime.evaluate', { expression: `document.getElementById('new-tunnel').click()`, userGesture: true });
  await new Promise((resolve) => setTimeout(resolve, 200));
  const saveVisible = await call('Runtime.evaluate', { expression: `(() => { const rect = document.getElementById('form-save').getBoundingClientRect(); return rect.top > 0 && rect.bottom < innerHeight; })()`, returnByValue: true });
  if (!saveVisible.result?.value) throw new Error('Save action is outside the visible editor viewport');
  await call('Runtime.evaluate', { expression: `document.getElementById('form-copy-public-key').click()`, userGesture: true });
  await new Promise((resolve) => setTimeout(resolve, 100));
  const clipboard = await call('Runtime.evaluate', { expression: 'navigator.clipboard.readText()', awaitPromise: true, returnByValue: true, userGesture: true });
  if (clipboard.result?.value !== 'AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=') throw new Error('Copy own public key did not write the expected public key to the clipboard');
  if (process.env.WG_QUIC_SMOKE_SCREENSHOT) {
    const { data } = await call('Page.captureScreenshot', { format: 'png' });
    writeFileSync(process.env.WG_QUIC_SMOKE_SCREENSHOT, Buffer.from(data, 'base64'));
  }
  console.log(result.message);
} finally {
  child.kill();
  await new Promise((resolve) => child.exitCode !== null ? resolve() : child.once('exit', resolve));
  for (const waiter of pending.values()) clearTimeout(waiter.timer);
  server.close();
  rmSync(profile, { recursive: true, force: true });
}
