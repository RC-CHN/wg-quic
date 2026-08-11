#!/usr/bin/env node

import { spawn } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const desktopDir = path.resolve(scriptDir, '..');
const packageDirectory = path.join(desktopDir, 'src-tauri', 'target', 'release');
const executable =
  process.env.WG_QUIC_DESKTOP_EXECUTABLE ||
  {
    linux: path.join(packageDirectory, 'wg-quic-desktop'),
    win32: path.join(packageDirectory, 'wg-quic-desktop.exe'),
  }[process.platform];

if (!executable) {
  throw new Error(`unsupported desktop smoke platform ${process.platform}`);
}

const configDirectory = mkdtempSync(
  path.join(tmpdir(), 'wg-quic-desktop-smoke-'),
);
const integrationSmoke =
  process.env.WG_QUIC_DESKTOP_INTEGRATION_SMOKE === '1';
const resultPath = path.join(configDirectory, 'result.txt');
let output = '';

try {
  const child = spawn(executable, [], {
    cwd: packageDirectory,
    env: {
      ...process.env,
      WG_QUIC_DESKTOP_SMOKE_RESULT: resultPath,
      ...(integrationSmoke
        ? {}
        : {
            WG_QUIC_CONFIG_DIR: configDirectory,
            WG_QUIC_DESKTOP_SMOKE: '1',
          }),
    },
    windowsHide: true,
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  child.stdout.setEncoding('utf8');
  child.stderr.setEncoding('utf8');
  child.stdout.on('data', (chunk) => {
    output += chunk;
  });
  child.stderr.on('data', (chunk) => {
    output += chunk;
  });

  const exitCode = await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      child.kill();
      reject(
        new Error(
          `packaged desktop app did not exit within ${integrationSmoke ? 180 : 20} seconds`,
        ),
      );
    }, integrationSmoke ? 180_000 : 20_000);
    child.once('error', (error) => {
      clearTimeout(timeout);
      reject(error);
    });
    child.once('exit', (code, signal) => {
      clearTimeout(timeout);
      if (signal) {
        reject(
          new Error(
            `packaged desktop app exited via ${signal}\n${output}`,
          ),
        );
        return;
      }
      resolve(code);
    });
  });

  const expected = integrationSmoke
    ? 'wg-quic installed desktop import/broker/service/status lifecycle passed'
    : 'wg-quic desktop renderer smoke test passed';
  const result = readFileSync(resultPath, 'utf8').trim();
  if (exitCode !== 0 || result !== expected) {
    throw new Error(
      `packaged desktop app smoke test failed (exit ${exitCode}, result ${JSON.stringify(result)})\n${output}`,
    );
  }
  console.log(result);
  if (output) {
    process.stdout.write(output);
  }
} finally {
  rmSync(configDirectory, { recursive: true, force: true });
}
