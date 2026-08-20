import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

import { checkVersionConsistency } from './check-version.mjs';

const version = '7.8.9';

function writeFixture(relativePath, contents, repositoryDirectory) {
  const destination = path.join(repositoryDirectory, relativePath);
  mkdirSync(path.dirname(destination), { recursive: true });
  writeFileSync(destination, contents);
}

function makeFixture() {
  const repositoryDirectory = mkdtempSync(
    path.join(os.tmpdir(), 'wg-quic-version-'),
  );
  writeFixture('VERSION', `${version}\n`, repositoryDirectory);
  writeFixture(
    'desktop/package.json',
    JSON.stringify({ version }),
    repositoryDirectory,
  );
  writeFixture(
    'desktop/package-lock.json',
    JSON.stringify({ version, packages: { '': { version } } }),
    repositoryDirectory,
  );
  writeFixture(
    'desktop/src-tauri/Cargo.toml',
    `[package]\nname = "wg-quic-desktop"\nversion = "${version}"\n`,
    repositoryDirectory,
  );
  writeFixture(
    'desktop/src-tauri/Cargo.lock',
    `[[package]]\nname = "some-dependency"\nversion = "0.2.0"\n\n` +
      `[[package]]\nname = "wg-quic-desktop"\nversion = "${version}"\n`,
    repositoryDirectory,
  );
  writeFixture(
    'desktop/src-tauri/tauri.conf.json',
    JSON.stringify({ version }),
    repositoryDirectory,
  );
  return repositoryDirectory;
}

test('accepts synchronized desktop metadata versions', (t) => {
  const repositoryDirectory = makeFixture();
  t.after(() => rmSync(repositoryDirectory, { recursive: true, force: true }));

  const result = checkVersionConsistency(repositoryDirectory);
  assert.equal(result.expected, version);
  assert.equal(result.sources.length, 6);
});

test('reports every drifted desktop lockfile version source', (t) => {
  const repositoryDirectory = makeFixture();
  t.after(() => rmSync(repositoryDirectory, { recursive: true, force: true }));

  writeFixture(
    'desktop/package-lock.json',
    JSON.stringify({
      version: '1.0.0',
      packages: { '': { version: '2.0.0' } },
    }),
    repositoryDirectory,
  );
  writeFixture(
    'desktop/src-tauri/Cargo.lock',
    '[[package]]\nname = "wg-quic-desktop"\nversion = "3.0.0"\n',
    repositoryDirectory,
  );
  assert.throws(
    () => checkVersionConsistency(repositoryDirectory),
    (error) => {
      assert.match(error.message, /package-lock\.json#version/);
      assert.match(error.message, /package-lock\.json#packages\[""\]/);
      assert.match(error.message, /Cargo\.lock#wg-quic-desktop/);
      return true;
    },
  );
});
