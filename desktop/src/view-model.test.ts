import assert from 'node:assert/strict';
import test from 'node:test';
import type { TunnelView } from './types';
import {
  chooseSelectedTunnel,
  formatBitRate,
  formatBytes,
  formatFECRecovery,
  formatRTT,
  managementErrorMessage,
  tunnelDisplayState,
  tunnelStateLabel,
} from './view-model';

const tunnel = (name: string, running = false): TunnelView => ({
  name,
  running,
  configPath: `/etc/wg-quic/${name}.conf`,
});

test('selection survives refresh and falls back deterministically', () => {
  const tunnels = [tunnel('alpha'), tunnel('beta')];
  assert.equal(chooseSelectedTunnel(tunnels, 'beta'), 'beta');
  assert.equal(chooseSelectedTunnel(tunnels, 'missing'), 'alpha');
  assert.equal(chooseSelectedTunnel([], 'alpha'), undefined);
});

test('pending commands override observed state labels', () => {
  assert.equal(tunnelDisplayState(tunnel('alpha', true)), 'active');
  assert.equal(tunnelDisplayState(tunnel('alpha'), 'up'), 'activating');
  assert.equal(tunnelDisplayState(tunnel('alpha', true), 'down'), 'deactivating');
  assert.equal(tunnelStateLabel('deactivating'), 'Deactivating…');
});

test('telemetry formatters preserve byte and bit-rate units', () => {
  assert.equal(formatBytes(1536), '1.5 KiB');
  assert.equal(formatBitRate(8_000_000), '8.0 Mbps');
  assert.equal(formatRTT(12_400), '12 ms');
  assert.equal(formatFECRecovery(8, 10), '80.0% recovered');
  assert.equal(formatFECRecovery(0, 0), 'No observed loss');
});

test('management errors add privilege guidance only when relevant', () => {
  assert.equal(
    managementErrorMessage('endpoint resolution failed'),
    'endpoint resolution failed',
  );
  assert.equal(
    managementErrorMessage('Access is denied.'),
    'Access is denied. Administrator privileges may be required.',
  );
  assert.equal(
    managementErrorMessage('administrator approval was canceled'),
    'administrator approval was canceled',
  );
});
