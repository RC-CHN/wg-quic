import assert from 'node:assert/strict';
import test from 'node:test';
import type { CoreStatus, TunnelView } from './types';
import {
  actionProgressDescription,
  chooseSelectedTunnel,
  createSingleFlight,
  formatBitRate,
  formatBytes,
  formatFECRecovery,
  formatRTT,
  managementErrorMessage,
  managementServiceDisplay,
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

test('action progress copy follows observed core state', () => {
  const running = (sessions: number): TunnelView => ({
    ...tunnel('alpha', true),
    status: {
      stats: { active_sessions: sessions },
    } as unknown as CoreStatus,
  });

  assert.equal(
    actionProgressDescription(tunnel('alpha'), 'up', 1),
    'Starting wg-quic-quick and creating the interface',
  );
  assert.equal(
    actionProgressDescription(tunnel('alpha'), 'up', 4),
    'Starting wg-quic-quick and creating the interface (4s)',
  );
  assert.equal(
    actionProgressDescription(running(0), 'up', 1),
    'Interface is up; establishing QUIC session',
  );
  assert.equal(
    actionProgressDescription(running(1), 'up', 1),
    'QUIC session established; finishing activation',
  );
  assert.equal(
    actionProgressDescription(running(0), 'down', 1),
    'Stopping the service and cleaning up host state',
  );
  assert.equal(
    actionProgressDescription(tunnel('alpha'), 'down', 6),
    'Finishing deactivation (6s)',
  );
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
  assert.equal(
    managementErrorMessage('management operation outcome is unknown'),
    'management operation outcome is unknown Tunnel status was refreshed; verify it before retrying.',
  );
});

test('management service state distinguishes one-click control from UAC fallback', () => {
  assert.deepEqual(managementServiceDisplay('linux'), {
    label: '',
    state: 'hidden',
    needsAttention: false,
  });
  assert.equal(managementServiceDisplay('win32', 'ready').state, 'ready');
  assert.equal(
    managementServiceDisplay('win32', 'unauthorized').state,
    'fallback',
  );
  assert.equal(
    managementServiceDisplay('win32', 'unavailable').needsAttention,
    true,
  );
});

test('single-flight callers share an active refresh and allow the next one', async () => {
  let calls = 0;
  let release: (() => void) | undefined;
  const run = createSingleFlight(
    () =>
      new Promise<number>((resolve) => {
        calls += 1;
        release = () => resolve(calls);
      }),
  );

  const first = run();
  const overlapping = run();
  assert.strictEqual(overlapping, first);
  assert.equal(calls, 1);
  release?.();
  assert.equal(await first, 1);
  assert.equal(await overlapping, 1);

  const next = run();
  assert.equal(calls, 2);
  release?.();
  assert.equal(await next, 2);
});

test('single-flight callers share failures and retry after settlement', async () => {
  const expected = new Error('snapshot failed');
  let calls = 0;
  const run = createSingleFlight(async () => {
    calls += 1;
    if (calls === 1) {
      throw expected;
    }
    return 'recovered';
  });

  const first = run();
  const overlapping = run();
  assert.strictEqual(overlapping, first);
  await assert.rejects(first, expected);
  await assert.rejects(overlapping, expected);
  assert.equal(await run(), 'recovered');
  assert.equal(calls, 2);
});
