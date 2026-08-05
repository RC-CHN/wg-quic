#!/usr/bin/env node

import { rmSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const outputDirectory = path.resolve(scriptDir, '..', 'out');

rmSync(outputDirectory, { recursive: true, force: true });
