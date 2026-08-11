#!/usr/bin/env node

import { rmSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const desktopDirectory = path.resolve(scriptDir, '..');

for (const outputDirectory of [
  path.join(desktopDirectory, 'out'),
  path.join(desktopDirectory, 'dist'),
  path.join(desktopDirectory, 'src-tauri', 'target', 'release', 'bundle'),
]) {
  rmSync(outputDirectory, { recursive: true, force: true });
}
