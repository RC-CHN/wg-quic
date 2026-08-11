#!/usr/bin/env node

import { mkdirSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const desktopDir = path.resolve(scriptDir, '..');
const outputDir = path.join(desktopDir, '.test-build');
const tsc = path.join(desktopDir, 'node_modules', 'typescript', 'bin', 'tsc');

function run(args) {
  const result = spawnSync(process.execPath, args, {
    cwd: desktopDir,
    stdio: 'inherit',
  });
  if (result.status !== 0) {
    process.exitCode = result.status || 1;
    return false;
  }
  return true;
}

rmSync(outputDir, { recursive: true, force: true });
try {
  mkdirSync(outputDir, { recursive: true });
  writeFileSync(
    path.join(outputDir, 'package.json'),
    '{"type":"commonjs"}\n',
  );
  if (
    run([tsc, '--project', 'tsconfig.test.json']) &&
    run([
      '--test',
      path.join(outputDir, 'src', 'view-model.test.js'),
      path.join(scriptDir, 'check-version.test.mjs'),
      path.join(scriptDir, 'release-artifacts.test.mjs'),
      path.join(scriptDir, 'wix-management-service.test.mjs'),
    ])
  ) {
    console.log('desktop model tests passed');
  }
} finally {
  rmSync(outputDir, { recursive: true, force: true });
}
