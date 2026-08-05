import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { collectReleaseArtifacts } from './release-artifacts.mjs';

function fixture(root, relativePath, contents) {
  const file = path.join(root, relativePath);
  mkdirSync(path.dirname(file), { recursive: true });
  writeFileSync(file, contents);
}

test('collects and renames the Windows installer', () => {
  const root = mkdtempSync(path.join(os.tmpdir(), 'wg-quic-release-'));
  try {
    const source = path.join(root, 'make');
    const output = path.join(root, 'release');
    fixture(
      source,
      'squirrel.windows/x64/wg_quic-0.1.2 Setup.exe',
      Buffer.from('MZinstaller'),
    );
    fixture(
      source,
      'squirrel.windows/x64/wg_quic-0.1.2-full.nupkg',
      Buffer.from('PKpackage'),
    );

    const artifacts = collectReleaseArtifacts({
      platform: 'windows',
      sourceDirectory: source,
      outputDirectory: output,
      version: '0.1.2',
    });

    assert.deepEqual(artifacts, [
      path.join(output, 'wg-quic-desktop-v0.1.2-windows-x64-setup.exe'),
    ]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('collects and renames both Linux distributions', () => {
  const root = mkdtempSync(path.join(os.tmpdir(), 'wg-quic-release-'));
  try {
    const source = path.join(root, 'make');
    const output = path.join(root, 'release');
    fixture(
      source,
      'deb/x64/wg-quic_0.1.2_amd64.deb',
      Buffer.from('!<arch>\ndebian'),
    );
    fixture(
      source,
      'zip/linux/x64/wg-quic-linux-x64-0.1.2.zip',
      Buffer.from('PKarchive'),
    );

    const artifacts = collectReleaseArtifacts({
      platform: 'linux',
      sourceDirectory: source,
      outputDirectory: output,
      version: '0.1.2',
    });

    assert.deepEqual(artifacts, [
      path.join(output, 'wg-quic-desktop-v0.1.2-linux-amd64.deb'),
      path.join(output, 'wg-quic-desktop-v0.1.2-linux-amd64.zip'),
    ]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('rejects ambiguous installer output', () => {
  const root = mkdtempSync(path.join(os.tmpdir(), 'wg-quic-release-'));
  try {
    const source = path.join(root, 'make');
    fixture(source, 'one Setup.exe', Buffer.from('MZone'));
    fixture(source, 'two Setup.exe', Buffer.from('MZtwo'));

    assert.throws(
      () =>
        collectReleaseArtifacts({
          platform: 'windows',
          sourceDirectory: source,
          outputDirectory: path.join(root, 'release'),
          version: '0.1.2',
        }),
      /expected exactly one Squirrel Setup\.exe, found 2/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
