import type { CoreStatus, DesktopSnapshot } from './types';

export async function runStatusInteractionSmoke(refresh: () => Promise<void>): Promise<void> {
  const original = window.wgQuic.snapshot;
  const base = await original();
  const fixture: DesktopSnapshot = { ...base, tunnels: [{
    name: 'status-fixture', configPath: '/fixture.conf', running: false,
    statusState: 'unknown', statusCode: 'permission_denied', statusDetail: 'Synthetic permission failure',
  }] };
  window.wgQuic.snapshot = async () => fixture;
  const assert = (condition: boolean, message: string) => {
    if (!condition) throw new Error(`status interaction: ${message}`);
  };
  try {
    await refresh();
    assert(document.getElementById('detail-state')!.textContent === 'Status unavailable', 'unknown status looks stopped');
    assert((document.getElementById('toggle-tunnel') as HTMLButtonElement).disabled, 'unknown status offers activation');
    assert(!document.getElementById('retry-status')!.classList.contains('hidden'), 'no recovery action for unknown status');
    const tunnel = fixture.tunnels[0]!;
    tunnel.running = true;
    tunnel.statusState = 'up';
    tunnel.statusDetail = undefined;
    tunnel.status = {
      interface: tunnel.name, state: 'up', carrier: 'quic', fec_mode: 'auto', obfs_mode: 'salamander',
      stats: { active_sessions: 1 },
      peers: [{ public_key: 'peer', session: 'established', generation: 1 }],
      sessions: [{ session_id: 1, session_generation: 1, state: 'established', peers: [] }],
    } as unknown as CoreStatus;
    await refresh();
    assert(document.getElementById('detail-state')!.textContent === 'Authenticating…', 'QUIC alone looks authenticated');
    tunnel.status.sessions![0]!.peers = [{ public_key: 'peer', authenticated: true }];
    await refresh();
    assert(document.getElementById('detail-state')!.textContent === 'Connected', 'authenticated peer not connected');
    tunnel.running = false;
    tunnel.statusState = 'inactive';
    tunnel.status = undefined;
    await refresh();
    assert(document.getElementById('detail-state')!.textContent === 'Inactive', 'inactive state not shown');
    assert(!(document.getElementById('toggle-tunnel') as HTMLButtonElement).disabled, 'inactive tunnel cannot activate');
  } finally {
    window.wgQuic.snapshot = original;
    await refresh();
  }
}
