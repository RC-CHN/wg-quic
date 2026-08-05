#!/usr/bin/env node

import { execFileSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const desktopDir = path.resolve(scriptDir, '..');
const repositoryDir = path.resolve(desktopDir, '..');
const suffix = process.platform === 'win32' ? '.exe' : '';

function run(binary, args) {
  return execFileSync(
    path.join(desktopDir, 'resources', 'bin', `${binary}${suffix}`),
    args,
    {
      cwd: repositoryDir,
      encoding: 'utf8',
      windowsHide: true,
    },
  ).trim();
}

const coreVersion = run('wg-quic', ['version']);
const quickVersion = run('wg-quic-quick', ['version']);
const check = run('wg-quic-quick', [
  'check',
  path.join(repositoryDir, 'tests', 'container', 'a.conf'),
]);

if (
  !coreVersion.startsWith('wg-quic ') ||
  !quickVersion.startsWith('wg-quic-quick ') ||
  !check.includes('configuration is valid')
) {
  throw new Error('bundled native command smoke test returned unexpected output');
}

console.log(`${coreVersion}; ${quickVersion}; configuration check passed`);
