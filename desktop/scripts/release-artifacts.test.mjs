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
      'wix/x64/wg-quic.msi',
      Buffer.concat([
        Buffer.from([0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1]),
        Buffer.from('installer'),
      ]),
    );

    const artifacts = collectReleaseArtifacts({
      platform: 'windows',
      sourceDirectory: source,
      outputDirectory: output,
      version: '0.1.2',
    });

    assert.deepEqual(artifacts, [
      path.join(output, 'wg-quic-desktop-v0.1.2-windows-x64.msi'),
    ]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('collects and renames the Linux Debian package', () => {
  const root = mkdtempSync(path.join(os.tmpdir(), 'wg-quic-release-'));
  try {
    const source = path.join(root, 'make');
    const output = path.join(root, 'release');
    fixture(
      source,
      'deb/x64/wg-quic_0.1.2_amd64.deb',
      Buffer.from('!<arch>\ndebian'),
    );
    const artifacts = collectReleaseArtifacts({
      platform: 'linux',
      sourceDirectory: source,
      outputDirectory: output,
      version: '0.1.2',
    });

    assert.deepEqual(artifacts, [
      path.join(output, 'wg-quic-desktop-v0.1.2-linux-amd64.deb'),
    ]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('rejects ambiguous installer output', () => {
  const root = mkdtempSync(path.join(os.tmpdir(), 'wg-quic-release-'));
  try {
    const source = path.join(root, 'make');
    fixture(source, 'one.msi', Buffer.from('one'));
    fixture(source, 'two.msi', Buffer.from('two'));

    assert.throws(
      () =>
        collectReleaseArtifacts({
          platform: 'windows',
          sourceDirectory: source,
          outputDirectory: path.join(root, 'release'),
          version: '0.1.2',
        }),
      /expected exactly one WiX MSI, found 2/,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
