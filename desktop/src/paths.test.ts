import assert from 'node:assert/strict';
import test from 'node:test';
import {
  defaultConfigDirectory,
  isSupportedPlatform,
  isValidInterfaceName,
} from './paths';

test('platform support follows the existing wg-quic-quick host boundary', () => {
  assert.equal(isSupportedPlatform('linux'), true);
  assert.equal(isSupportedPlatform('win32'), true);
  assert.equal(isSupportedPlatform('darwin'), false);
});

test('configuration directories mirror platformenv', () => {
  assert.equal(defaultConfigDirectory('linux'), '/etc/wg-quic');
  assert.equal(
    defaultConfigDirectory('win32', 'D:\\ProgramData'),
    'D:\\ProgramData\\wg-quic\\interfaces',
  );
  assert.throws(
    () => defaultConfigDirectory('darwin'),
    /unsupported desktop platform/,
  );
});

test('interface validation rejects traversal and platform-invalid names', () => {
  assert.equal(isValidInterfaceName('wg0', 'linux'), true);
  assert.equal(isValidInterfaceName('../wg0', 'linux'), false);
  assert.equal(isValidInterfaceName('sixteen-characters', 'linux'), false);
  assert.equal(isValidInterfaceName('office tunnel', 'win32'), true);
  assert.equal(isValidInterfaceName('office.', 'win32'), false);
  assert.equal(isValidInterfaceName('office/tunnel', 'win32'), false);
});
