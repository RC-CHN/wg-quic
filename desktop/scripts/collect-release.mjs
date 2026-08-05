#!/usr/bin/env node

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { collectReleaseArtifacts } from './release-artifacts.mjs';

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const desktopDirectory = path.resolve(scriptDirectory, '..');
const repositoryDirectory = path.resolve(desktopDirectory, '..');
const platform = process.argv[2];
const version = readFileSync(
  path.join(repositoryDirectory, 'VERSION'),
  'utf8',
).trim();
const desktopPackage = JSON.parse(
  readFileSync(path.join(desktopDirectory, 'package.json'), 'utf8'),
);

if (desktopPackage.version !== version) {
  throw new Error(
    `desktop version ${desktopPackage.version} does not match VERSION ${version}`,
  );
}

const artifacts = collectReleaseArtifacts({
  platform,
  sourceDirectory: path.join(desktopDirectory, 'out', 'make'),
  outputDirectory: path.join(
    repositoryDirectory,
    'dist',
    'release-desktop',
  ),
  version,
});

for (const artifact of artifacts) {
  console.log(path.relative(repositoryDirectory, artifact));
}
