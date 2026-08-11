#!/usr/bin/env node

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const desktopDirectory = path.resolve(scriptDirectory, '..');
const repositoryDirectory = path.resolve(desktopDirectory, '..');
const expected = readFileSync(
  path.join(repositoryDirectory, 'VERSION'),
  'utf8',
).trim();
const desktopPackage = JSON.parse(
  readFileSync(path.join(desktopDirectory, 'package.json'), 'utf8'),
);
const tauriConfig = JSON.parse(
  readFileSync(
    path.join(desktopDirectory, 'src-tauri', 'tauri.conf.json'),
    'utf8',
  ),
);
const cargoManifest = readFileSync(
  path.join(desktopDirectory, 'src-tauri', 'Cargo.toml'),
  'utf8',
);
const cargoPackage = cargoManifest.match(
  /^\[package\][\s\S]*?^version\s*=\s*"([^"]+)"/m,
);
if (!cargoPackage) {
  throw new Error('could not read the desktop Cargo package version');
}

const versions = new Map([
  ['VERSION', expected],
  ['desktop/package.json', desktopPackage.version],
  ['desktop/src-tauri/Cargo.toml', cargoPackage[1]],
  ['desktop/src-tauri/tauri.conf.json', tauriConfig.version],
]);
const mismatches = [...versions].filter(([, version]) => version !== expected);
if (mismatches.length > 0) {
  throw new Error(
    `desktop versions do not match ${expected}: ${mismatches
      .map(([source, version]) => `${source}=${JSON.stringify(version)}`)
      .join(', ')}`,
  );
}
console.log(`desktop versions match ${expected}`);
