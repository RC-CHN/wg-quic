import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  managementServiceComponentId,
  renderManagementServiceFragment,
} from './wix-management-service.mjs';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const desktopDir = path.resolve(scriptDir, '..');

test('management service fragment uses the broker executable and escapes its absolute source', () => {
  const source = path.resolve(
    path.parse(desktopDir).root,
    'Program Files',
    'wg-quic & "<tools>',
    'wg-quic-quick.exe',
  );
  const fragment = renderManagementServiceFragment(source);

  assert.match(fragment, /<DirectoryRef Id="INSTALLDIR">/);
  assert.match(fragment, /<Component Id="WgQuicManagementService"/);
  assert.match(
    fragment,
    /Source="[^"]*wg-quic &amp; &quot;&lt;tools&gt;[^"]*"/,
  );
  assert.match(fragment, /Name="wg-quic-manager.exe"/);
  assert.match(fragment, /<ServiceInstall[\s\S]*Name="wg-quic-manager"/);
  assert.match(fragment, /Account="LocalSystem"/);
  assert.match(fragment, /Start="auto"/);
  assert.match(fragment, /Arguments="broker-service"/);
  assert.match(fragment, /<ServiceControl[\s\S]*Start="install"/);
  assert.match(fragment, /Stop="both"/);
  assert.match(fragment, /Remove="uninstall"/);
});

test('management service fragment rejects relative source paths', () => {
  assert.throws(
    () => renderManagementServiceFragment('resources/bin/wg-quic-quick.exe'),
    /must be absolute/,
  );
});

test('Windows Tauri configuration includes the management service fragment', () => {
  const config = JSON.parse(
    readFileSync(
      path.join(desktopDir, 'src-tauri', 'tauri.windows.conf.json'),
      'utf8',
    ),
  );
  const wix = config.bundle.windows.wix;

  assert.deepEqual(wix.fragmentPaths, [
    'target/wix-fragments/management-service.wxs',
  ]);
  assert.deepEqual(wix.componentRefs, [managementServiceComponentId]);
});
