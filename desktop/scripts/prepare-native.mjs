#!/usr/bin/env node

import { copyFileSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { spawnSync } from 'node:child_process';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const desktopDir = path.resolve(scriptDir, '..');
const repositoryDir = path.resolve(desktopDir, '..');
const outputDir = path.join(desktopDir, 'resources', 'bin');
const licenseDir = path.join(desktopDir, 'resources', 'licenses');
const targetOS = {
  linux: 'linux',
  win32: 'windows',
}[process.platform];
const targetArch = {
  x64: 'amd64',
  arm64: 'arm64',
}[process.arch];

if (!targetOS || !targetArch) {
  throw new Error(`unsupported desktop build target ${process.platform}/${process.arch}`);
}

const version = readFileSync(path.join(repositoryDir, 'VERSION'), 'utf8').trim();
const suffix = targetOS === 'windows' ? '.exe' : '';

rmSync(outputDir, { recursive: true, force: true });
rmSync(licenseDir, { recursive: true, force: true });
mkdirSync(outputDir, { recursive: true });
mkdirSync(licenseDir, { recursive: true });
copyFileSync(
  path.join(repositoryDir, 'LICENSE'),
  path.join(licenseDir, 'wg-quic-AGPL-3.0.txt'),
);

for (const component of ['quic-go', 'wireguard-go']) {
  const componentDirectory = path.join(licenseDir, component);
  mkdirSync(componentDirectory, { recursive: true });
  for (const file of ['LICENSE', 'ORIGIN.md']) {
    copyFileSync(
      path.join(repositoryDir, 'third_party', component, file),
      path.join(componentDirectory, file),
    );
  }
}

for (const command of ['wg-quic', 'wg-quic-quick']) {
  const output = path.join(outputDir, `${command}${suffix}`);
  const result = spawnSync(
    'go',
    [
      'build',
      '-trimpath',
      '-ldflags',
      `-s -w -X main.version=${version}`,
      '-o',
      output,
      `./cmd/${command}`,
    ],
    {
      cwd: repositoryDir,
      env: {
        ...process.env,
        CGO_ENABLED: '0',
        GOOS: targetOS,
        GOARCH: targetArch,
      },
      stdio: 'inherit',
    },
  );
  if (result.status !== 0) {
    throw new Error(`failed to build ${command} for ${targetOS}/${targetArch}`);
  }
}

if (targetOS === 'windows') {
  copyFileSync(
    path.join(repositoryDir, 'third_party', 'wintun', targetArch, 'wintun.dll'),
    path.join(outputDir, 'wintun.dll'),
  );
  const wintunLicenseDirectory = path.join(licenseDir, 'wintun');
  mkdirSync(wintunLicenseDirectory, { recursive: true });
  for (const file of ['LICENSE.txt', 'ORIGIN.md']) {
    copyFileSync(
      path.join(repositoryDir, 'third_party', 'wintun', file),
      path.join(wintunLicenseDirectory, file),
    );
  }
}

writeFileSync(
  path.join(outputDir, 'native-manifest.json'),
  `${JSON.stringify(
    {
      version,
      os: targetOS,
      arch: targetArch,
      tunnelBackendSupported: targetOS === 'linux' || targetOS === 'windows',
    },
    null,
    2,
  )}\n`,
);

console.log(`prepared wg-quic ${version} for ${targetOS}/${targetArch}`);
