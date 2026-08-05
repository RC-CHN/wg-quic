import {
  copyFileSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  statSync,
} from 'node:fs';
import path from 'node:path';

function listFiles(directory) {
  const files = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const entryPath = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      files.push(...listFiles(entryPath));
    } else if (entry.isFile()) {
      files.push(entryPath);
    }
  }
  return files;
}

function findExactlyOne(files, predicate, description) {
  const matches = files.filter(predicate);
  if (matches.length !== 1) {
    throw new Error(
      `expected exactly one ${description}, found ${matches.length}: ${matches.join(', ')}`,
    );
  }
  return matches[0];
}

function verifyMagic(file, expected, description) {
  const actual = readFileSync(file).subarray(0, expected.length);
  if (!actual.equals(expected)) {
    throw new Error(`${description} has an unexpected file signature: ${file}`);
  }
}

function copyArtifact(source, destination, expectedMagic, description) {
  if (statSync(source).size <= expectedMagic.length) {
    throw new Error(`${description} is empty or truncated: ${source}`);
  }
  verifyMagic(source, expectedMagic, description);
  copyFileSync(source, destination);
  return destination;
}

export function collectReleaseArtifacts({
  platform,
  sourceDirectory,
  outputDirectory,
  version,
}) {
  const files = listFiles(sourceDirectory);
  mkdirSync(outputDirectory, { recursive: true });

  if (platform === 'windows') {
    const installer = findExactlyOne(
      files,
      (file) => file.toLowerCase().endsWith(' setup.exe'),
      'Squirrel Setup.exe',
    );
    return [
      copyArtifact(
        installer,
        path.join(
          outputDirectory,
          `wg-quic-desktop-v${version}-windows-x64-setup.exe`,
        ),
        Buffer.from('MZ'),
        'Windows installer',
      ),
    ];
  }

  if (platform === 'linux') {
    const deb = findExactlyOne(
      files,
      (file) => file.toLowerCase().endsWith('.deb'),
      'Debian package',
    );
    const zip = findExactlyOne(
      files,
      (file) => file.toLowerCase().endsWith('.zip'),
      'Linux ZIP archive',
    );
    return [
      copyArtifact(
        deb,
        path.join(
          outputDirectory,
          `wg-quic-desktop-v${version}-linux-amd64.deb`,
        ),
        Buffer.from('!<arch>\n'),
        'Debian package',
      ),
      copyArtifact(
        zip,
        path.join(
          outputDirectory,
          `wg-quic-desktop-v${version}-linux-amd64.zip`,
        ),
        Buffer.from('PK'),
        'Linux ZIP archive',
      ),
    ];
  }

  throw new Error(`unsupported desktop release platform: ${platform}`);
}
