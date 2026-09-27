import type { CoreStatus, DesktopSnapshot } from './types';
import { t } from './i18n';

export async function runApplicationInteractionSmoke(actions: {
  refresh(): Promise<void>; edit(name: string): Promise<void>; save(): Promise<void>;
  apply(name: string): Promise<void>; restart(name: string): Promise<void>;
}): Promise<void> {
  const original = { ...window.wgQuic };
  const fixture: DesktopSnapshot = { ...await original.snapshot(), tunnels: [{
    name: 'apply-fixture', configPath: '/apply-fixture.conf', running: true, statusState: 'up',
    status: { interface: 'apply-fixture', state: 'up', carrier: 'quic', fec_mode: 'auto', obfs_mode: 'none', stats: {active_sessions: 0} } as unknown as CoreStatus,
  }] };
  const configuration = '[Interface]\nPrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nAddress = 10.0.0.1/24\n[Peer]\nPublicKey = AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nAllowedIPs = 10.0.0.2/32\n';
  const assert = (ok: boolean, message: string) => { if (!ok) throw new Error(`apply interaction: ${message}`); };
  window.wgQuic.snapshot = async () => fixture;
  window.wgQuic.readTunnel = async () => configuration;
  window.wgQuic.writeTunnel = async () => fixture;
  const commands: string[] = [];
  window.wgQuic.manage = async (_name, action) => {
    commands.push(action);
    fixture.tunnels[0]!.running = action === 'up';
    fixture.tunnels[0]!.statusState = action === 'up' ? 'up' : 'inactive';
    return fixture;
  };
  window.wgQuic.confirmRestart = async () => false;
  try {
    await actions.refresh();
    await actions.edit('apply-fixture');
    await actions.save();
    assert(!document.getElementById('configuration-state')!.classList.contains('hidden'), 'save does not explain application');
    assert(commands.length === 0, 'saving restarted the tunnel without consent');
    window.wgQuic.apply = async () => ({state: 'restart_required', restart_reasons: ['Interface MTU changed']});
    await actions.apply('apply-fixture');
    assert(!document.getElementById('restart-tunnel')!.classList.contains('hidden'), 'restart requirement has no action');
    await actions.restart('apply-fixture');
    assert(commands.length === 0, 'restart ignored Keep running');
    window.wgQuic.confirmRestart = async () => true;
    await actions.restart('apply-fixture');
    assert(commands.join(',') === 'down,up', 'restart order is incorrect');
    assert(document.getElementById('configuration-state')!.classList.contains('hidden'), 'successful restart left changes unapplied');

    await actions.edit('apply-fixture');
    await actions.save();
    const id = '0123456789abcdef0123456789abcdef';
    const requestIDs: Array<string | undefined> = [];
    window.wgQuic.apply = async (_name, requestID) => {
      requestIDs.push(requestID);
      return requestID ? {state: 'applied'} : {state: 'unknown', request_id: id};
    };
    await actions.apply('apply-fixture');
    assert(document.getElementById('apply-config')!.textContent === t('Check application result'), 'unknown result offers a fresh retry');
    await actions.apply('apply-fixture');
    assert(requestIDs.length === 2 && requestIDs[0] === undefined && requestIDs[1] === id, 'result check did not reuse the transaction ID');
    assert(document.getElementById('configuration-state')!.classList.contains('hidden'), 'applied state remained pending');
  } finally {
    Object.assign(window.wgQuic, original);
    await actions.refresh();
  }
}
