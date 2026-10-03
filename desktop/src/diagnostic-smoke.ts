import type { DesktopSnapshot } from './types';
import { t } from './i18n';

export async function runDiagnosticInteractionSmoke(actions: {refresh(): Promise<void>; collect(): Promise<void>}): Promise<void> {
  const original = { ...window.wgQuic };
  const fixture = { ...await original.snapshot(), tunnels: [{ name: 'diagnostic-fixture', configPath: '/diagnostic-fixture.conf', running: true, statusState: 'up', status: { carrier: 'quic', peers: [{public_key: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=', generation: 1, session: 'idle'}], stats: {} } }] } as DesktopSnapshot;
  const assert = (ok: boolean, message: string) => { if (!ok) throw new Error(`diagnostic interaction: ${message}`); };
  const button = () => document.getElementById('collect-diagnostics') as HTMLButtonElement;
  const result = () => document.getElementById('diagnostic-result')!.textContent!;
  window.wgQuic.snapshot = async () => fixture;
  try {
    await actions.refresh();
    assert(document.getElementById('detail-quic-wait')!.textContent === '—', 'older cores must not imply a measured empty queue');
    Object.assign(fixture.tunnels[0]!.status!.stats, {
      send_queue_bytes: 1024, quic_datagram_send_queue_bytes: 2048,
      quic_datagram_send_queue_age_us: 0, send_queue_delay_max_us: 250000,
      send_queue_expired: 7,
    });
    await actions.refresh();
    assert(document.getElementById('detail-send-queued')!.textContent === '3.0 KiB', 'local backlog omitted one send layer');
    assert(document.getElementById('detail-quic-wait')!.textContent === '0 ms', 'empty queue shown as unavailable');
    assert(document.getElementById('detail-send-wait')!.textContent === '250 ms', 'queue peak lost its time unit');
    assert(document.getElementById('detail-send-expired')!.textContent === '7', 'expired packet counter not rendered');
    window.wgQuic.collectDiagnostics = async () => ({canceled: true});
    await actions.collect();
    assert(!button().disabled && result() === '', 'cancel claimed a successful export');
    let release!: () => void;
    window.wgQuic.collectDiagnostics = async () => {
      await new Promise<void>((resolve) => { release = resolve; });
      return { canceled: false, complete: false, path: '/partial.zip', detail: 'Runtime restarted' };
    };
    const collecting = actions.collect();
    await actions.refresh();
    assert(button().disabled, 'refresh enabled a duplicate collection');
    release();
    await collecting;
    await actions.refresh();
    assert(!button().disabled && result() === t('Partial diagnostics saved: {0}. {1}', '/partial.zip', 'Runtime restarted'), 'partial result was hidden or shown as complete');
    window.wgQuic.collectDiagnostics = async () => { throw new Error('Synthetic export failure'); };
    await actions.collect();
    assert(!button().disabled && result().includes('Synthetic export failure'), 'failed export is not recoverable');
  } finally { Object.assign(window.wgQuic, original); await actions.refresh(); }
}
