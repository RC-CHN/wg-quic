import assert from 'node:assert/strict';
import test from 'node:test';
import {
  windowsElevationEnvironment,
  windowsElevationError,
  windowsElevationScript,
  windowsPowerShellPath,
} from './windows-elevation';

test('uses the system Windows PowerShell executable', () => {
  assert.equal(
    windowsPowerShellPath('D:\\Windows'),
    'D:\\Windows\\System32\\WindowsPowerShell\\v1.0\\powershell.exe',
  );
});

test('preserves replace and UAC cancellation semantics', () => {
  const exists = windowsElevationError(
    'wg-quic-quick: file already exists',
    { action: 'import', name: 'office' },
  );
  assert.equal(exists.code, 'EEXIST');

  const canceled = windowsElevationError(
    'The operation was canceled by the user. (1223)',
    { action: 'up', name: 'office' },
  );
  assert.equal(canceled.message, 'Administrator approval was canceled.');
});

test('keeps untrusted request values out of the PowerShell program', () => {
  const name = "office'; Remove-Item C:\\important; '";
  const source = "C:\\Users\\me\\vpn'; calc.exe; '.conf";
  const environment = windowsElevationEnvironment(
    'C:\\Program Files\\wg-quic\\wg-quic-quick.exe',
    {
      action: 'import',
      name,
      source,
      overwrite: true,
    },
    '\\\\.\\pipe\\wg-quic-desktop-test',
  );

  assert.equal(environment.WG_QUIC_DESKTOP_ACTION, 'import');
  assert.equal(environment.WG_QUIC_DESKTOP_NAME, name);
  assert.equal(environment.WG_QUIC_DESKTOP_SOURCE, source);
  assert.equal(environment.WG_QUIC_DESKTOP_OVERWRITE, '1');
  assert.equal(
    environment.WG_QUIC_DESKTOP_RESULT_PIPE,
    '\\\\.\\pipe\\wg-quic-desktop-test',
  );
  assert.equal(windowsElevationScript.includes(name), false);
  assert.equal(windowsElevationScript.includes(source), false);
  assert.match(windowsElevationScript, /-Verb RunAs/);
  assert.match(windowsElevationScript, /desktop-helper/);
  assert.doesNotMatch(windowsElevationScript, /RedirectStandard/);
});
