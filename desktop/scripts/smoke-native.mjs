#!/usr/bin/env node

import { execFileSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const desktopDir = path.resolve(scriptDir, '..');
const repositoryDir = path.resolve(desktopDir, '..');
const suffix = process.platform === 'win32' ? '.exe' : '';

function run(binary, args, input) {
  return execFileSync(
    path.join(desktopDir, 'resources', 'bin', `${binary}${suffix}`),
    args,
    {
      cwd: repositoryDir,
      encoding: 'utf8',
      windowsHide: true,
      input,
    },
  ).trim();
}

const coreVersion = run('wg-quic', ['version']);
const quickVersion = run('wg-quic-quick', ['version']);
const check = run('wg-quic-quick', [
  'check',
  path.join(repositoryDir, 'tests', 'container', 'a.conf'),
]);

const keys = JSON.parse(run('wg-quic-quick', ['desktop-genkey']));
if (run('wg-quic', ['pubkey'], keys.private_key) !== keys.public_key) {
  throw new Error('bundled public-key derivation does not match key generation');
}
for (const name of ['peers-before.conf', 'peers-after.conf']) {
  run('wg-quic-quick', ['check', path.join(repositoryDir, 'tests', 'fixtures', 'desktop', name)]);
}
const status = JSON.parse(run('wg-quic-quick', ['desktop-status', 'wgq-smoke']));
if (status.protocol_version !== 1 || !['up', 'prepared', 'inactive', 'unknown'].includes(status.state)) {
  throw new Error('bundled desktop status protocol is incompatible');
}

if (
  !coreVersion.startsWith('wg-quic ') ||
  !quickVersion.startsWith('wg-quic-quick ') ||
  !check.includes('configuration is valid')
) {
  throw new Error('bundled native command smoke test returned unexpected output');
}

console.log(`${coreVersion}; ${quickVersion}; configuration check passed`);
