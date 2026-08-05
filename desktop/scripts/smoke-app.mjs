#!/usr/bin/env node

import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const desktopDir = path.resolve(scriptDir, '..');
const platformDirectory = `wg-quic-${process.platform}-${process.arch}`;
const packageDirectory = path.join(desktopDir, 'out', platformDirectory);
const executable =
  process.env.WG_QUIC_DESKTOP_EXECUTABLE ||
  {
    linux: path.join(packageDirectory, 'wg-quic'),
    win32: path.join(packageDirectory, 'wg-quic.exe'),
  }[process.platform];

if (!executable) {
  throw new Error(`unsupported desktop smoke platform ${process.platform}`);
}

const configDirectory = mkdtempSync(
  path.join(tmpdir(), 'wg-quic-desktop-smoke-'),
);
const integrationSmoke =
  process.env.WG_QUIC_DESKTOP_INTEGRATION_SMOKE === '1';
let output = '';

try {
  const child = spawn(executable, [], {
    cwd: packageDirectory,
    env: {
      ...process.env,
      ELECTRON_ENABLE_LOGGING: '1',
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

  if (
    exitCode !== 0 ||
    !output.includes(
      integrationSmoke
        ? 'wg-quic installed desktop import/UAC/service/status lifecycle passed'
        : 'wg-quic desktop renderer smoke test passed',
    )
  ) {
    throw new Error(
      `packaged desktop app smoke test failed (exit ${exitCode})\n${output}`,
    );
  }
  process.stdout.write(output);
} finally {
  rmSync(configDirectory, { recursive: true, force: true });
}
