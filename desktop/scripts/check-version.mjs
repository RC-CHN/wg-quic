#!/usr/bin/env node

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptFile = fileURLToPath(import.meta.url);
const scriptDirectory = path.dirname(scriptFile);
const desktopDirectory = path.resolve(scriptDirectory, '..');
const defaultRepositoryDirectory = path.resolve(desktopDirectory, '..');

function readText(repositoryDirectory, relativePath) {
  return readFileSync(path.join(repositoryDirectory, relativePath), 'utf8');
}

function requiredMatch(contents, pattern, description) {
  const match = contents.match(pattern);
  if (!match) {
    throw new Error(`could not read ${description}`);
  }
  return match[1];
}

function cargoPackageVersion(contents, packageName) {
  const packageBlocks = contents.split(/^\[\[package\]\]\s*$/m).slice(1);
  const matchingBlocks = packageBlocks.filter((block) => {
    const name = block.match(/^name\s*=\s*"([^"]+)"/m);
    return name?.[1] === packageName;
  });
  if (matchingBlocks.length !== 1) {
    throw new Error(
      `expected one ${packageName} package in desktop/src-tauri/Cargo.lock, ` +
        `found ${matchingBlocks.length}`,
    );
  }
  return requiredMatch(
    matchingBlocks[0],
    /^version\s*=\s*"([^"]+)"/m,
    `${packageName} version in desktop/src-tauri/Cargo.lock`,
  );
}

export function collectVersionSources(
  repositoryDirectory = defaultRepositoryDirectory,
) {
  const expected = readText(repositoryDirectory, 'VERSION').trim();
  const desktopPackage = JSON.parse(
    readText(repositoryDirectory, 'desktop/package.json'),
  );
  const packageLock = JSON.parse(
    readText(repositoryDirectory, 'desktop/package-lock.json'),
  );
  const tauriConfig = JSON.parse(
    readText(repositoryDirectory, 'desktop/src-tauri/tauri.conf.json'),
  );
  const cargoManifest = readText(
    repositoryDirectory,
    'desktop/src-tauri/Cargo.toml',
  );
  const cargoLock = readText(
    repositoryDirectory,
    'desktop/src-tauri/Cargo.lock',
  );
  const opnsenseMakefile = readText(
    repositoryDirectory,
    'wg-quic-opnsense/net/wg-quic/Makefile',
  );
  const opnsenseBuildScript = readText(
    repositoryDirectory,
    'wg-quic-opnsense/scripts/build-wg-quic.sh',
  );

  const cargoManifestVersion = requiredMatch(
    cargoManifest,
    /^\[package\][\s\S]*?^version\s*=\s*"([^"]+)"/m,
    'desktop Cargo package version',
  );
  const opnsensePluginVersion = requiredMatch(
    opnsenseMakefile,
    /^PLUGIN_VERSION\s*=\s*([^\s#]+)\s*$/m,
    'OPNsense plugin version',
  );
  const opnsenseFallbackVersion = requiredMatch(
    opnsenseBuildScript,
    /^version="\$\{WG_QUIC_VERSION:-([^}]+)\}"\s*$/m,
    'OPNsense build fallback version',
  );

  return {
    expected,
    sources: [
      {
        source: 'desktop/package.json',
        actual: desktopPackage.version,
        wanted: expected,
      },
      {
        source: 'desktop/package-lock.json#version',
        actual: packageLock.version,
        wanted: expected,
      },
      {
        source: 'desktop/package-lock.json#packages[""]',
        actual: packageLock.packages?.['']?.version,
        wanted: expected,
      },
      {
        source: 'desktop/src-tauri/Cargo.toml',
        actual: cargoManifestVersion,
        wanted: expected,
      },
      {
        source: 'desktop/src-tauri/Cargo.lock#wg-quic-desktop',
        actual: cargoPackageVersion(cargoLock, 'wg-quic-desktop'),
        wanted: expected,
      },
      {
        source: 'desktop/src-tauri/tauri.conf.json',
        actual: tauriConfig.version,
        wanted: expected,
      },
      {
        source: 'wg-quic-opnsense/net/wg-quic/Makefile',
        actual: opnsensePluginVersion,
        wanted: expected,
      },
      {
        source: 'wg-quic-opnsense/scripts/build-wg-quic.sh',
        actual: opnsenseFallbackVersion,
        wanted: `${expected}-opnsense`,
      },
    ],
  };
}

export function checkVersionConsistency(
  repositoryDirectory = defaultRepositoryDirectory,
) {
  const result = collectVersionSources(repositoryDirectory);
  const mismatches = result.sources.filter(
    ({ actual, wanted }) => actual !== wanted,
  );
  if (mismatches.length > 0) {
    throw new Error(
      `release versions do not match ${result.expected}: ${mismatches
        .map(
          ({ source, actual, wanted }) =>
            `${source}=${JSON.stringify(actual)} (expected ${JSON.stringify(wanted)})`,
        )
        .join(', ')}`,
    );
  }
  return result;
}

function main() {
  const { expected, sources } = checkVersionConsistency();
  console.log(
    `release versions match ${expected} across ${sources.length} metadata sources`,
  );
}

if (process.argv[1] && path.resolve(process.argv[1]) === scriptFile) {
  main();
}
