import './styles.css';
import './tauri-api';
import {
  completeDesktopSmoke,
  desktopSmokeSettings,
  reportDesktopSmoke,
} from './tauri-api';
import type {
  CoreStatus,
  DesktopSnapshot,
  TunnelAction,
  TunnelView,
} from './types';
import {
  chooseSelectedTunnel,
  createSingleFlight,
  formatBitRate,
  formatBytes,
  formatFECRecovery,
  formatRTT,
  managementErrorMessage,
  tunnelDisplayState,
  tunnelStateLabel,
} from './view-model';

const byId = <T extends HTMLElement>(id: string): T => {
  const element = document.getElementById(id);
  if (!element) {
    throw new Error(`missing UI element #${id}`);
  }
  return element as T;
};

const tunnelList = byId<HTMLDivElement>('tunnel-list');
const noTunnels = byId<HTMLDivElement>('no-tunnels');
const detailEmpty = byId<HTMLDivElement>('detail-empty');
const detail = byId<HTMLElement>('tunnel-detail');
const notice = byId<HTMLElement>('notice');
const toast = byId<HTMLDivElement>('toast');
const pending = new Map<string, TunnelAction>();

let current: DesktopSnapshot | null = null;
let selectedName: string | undefined;
let toastTimer: ReturnType<typeof setTimeout> | undefined;
let smokeMode: 'none' | 'renderer' | 'integration' | 'tray' = 'none';

function errorMessage(error: unknown): string {
  const message = error instanceof Error ? error.message : String(error);
  return message
    .replace(/^Error invoking remote method '[^']+':\s*/i, '')
    .replace(/^Error:\s*/i, '')
    .trim();
}

function showToast(message: string, kind: 'ok' | 'error' = 'ok'): void {
  if (toastTimer) {
    clearTimeout(toastTimer);
  }
  toast.textContent = message;
  toast.className = `toast ${kind}`;
  toastTimer = setTimeout(() => {
    toast.classList.add('hidden');
  }, 5000);
}

function statusEndpoint(tunnel: TunnelView): string {
  if (!tunnel.running) {
    return 'Endpoint shown when active';
  }
  return tunnel.status?.peers?.[0]?.endpoint || 'No peer endpoint';
}

function createTunnelItem(tunnel: TunnelView): HTMLButtonElement {
  const item = document.createElement('button');
  const state = tunnelDisplayState(tunnel, pending.get(tunnel.name));
  item.type = 'button';
  item.className = `tunnel-item ${selectedName === tunnel.name ? 'selected' : ''}`;
  item.dataset.name = tunnel.name;
  item.setAttribute('role', 'option');
  item.setAttribute('aria-selected', String(selectedName === tunnel.name));

  const stateDot = document.createElement('span');
  stateDot.className = `state-dot ${state}`;
  stateDot.setAttribute('aria-hidden', 'true');

  const copy = document.createElement('span');
  copy.className = 'tunnel-item-copy';
  const name = document.createElement('strong');
  name.textContent = tunnel.name;
  const endpoint = document.createElement('span');
  endpoint.textContent = statusEndpoint(tunnel);
  copy.append(name, endpoint);

  const label = document.createElement('span');
  label.className = `tunnel-item-state ${state}`;
  label.textContent = tunnelStateLabel(state);
  item.append(stateDot, copy, label);
  item.addEventListener('click', () => {
    selectedName = tunnel.name;
    if (current) {
      render(current);
    }
  });
  return item;
}

function stateDescription(tunnel: TunnelView): string {
  if (!tunnel.running) {
    return 'The tunnel is configured and ready to activate.';
  }
  const sessions = tunnel.status?.stats.active_sessions || 0;
  if (sessions === 0) {
    return 'The interface is active and waiting for a QUIC peer session.';
  }
  return `${sessions} active QUIC ${sessions === 1 ? 'session' : 'sessions'}.`;
}

function setText(id: string, value: string): void {
  byId(id).textContent = value;
}

function refreshedAtDate(value: string): Date {
  return /^\d+$/.test(value) ? new Date(Number(value)) : new Date(value);
}

function renderPeers(status?: CoreStatus): void {
  const peerList = byId<HTMLDivElement>('peer-list');
  const peers = status?.peers || [];
  if (peers.length === 0) {
    const empty = document.createElement('div');
    empty.className = 'peer-empty';
    empty.textContent = status
      ? 'No peers reported by the running interface.'
      : 'Peer status appears when the tunnel is active.';
    peerList.replaceChildren(empty);
    return;
  }

  peerList.replaceChildren(
    ...peers.map((peer) => {
      const row = document.createElement('div');
      row.className = 'peer-row';
      const state = document.createElement('span');
      state.className = `peer-state ${peer.session === 'established' ? 'ready' : ''}`;
      state.textContent = peer.session;
      const copy = document.createElement('div');
      const endpoint = document.createElement('strong');
      endpoint.textContent = peer.endpoint || 'Endpoint pending';
      const key = document.createElement('span');
      key.textContent = `${peer.public_key.slice(0, 16)}…`;
      copy.append(endpoint, key);
      row.append(state, copy);
      return row;
    }),
  );
}

function renderDetail(tunnel?: TunnelView): void {
  detail.classList.toggle('hidden', !tunnel);
  detailEmpty.classList.toggle('hidden', Boolean(tunnel));
  if (!tunnel) {
    setText(
      'detail-empty-title',
      current?.tunnels.length
        ? 'Select a tunnel'
        : 'Import a tunnel configuration',
    );
    setText(
      'detail-empty-copy',
      current?.tunnels.length
        ? 'Choose a tunnel from the list to inspect or control it.'
        : 'wg-quic uses the same .conf files as wg-quic-quick.',
    );
    return;
  }

  const status = tunnel.status;
  const stats = status?.stats;
  const action = pending.get(tunnel.name);
  const state = tunnelDisplayState(tunnel, action);
  const backendSupported = Boolean(current?.backend.supported);

  setText('detail-name', tunnel.name);
  setText('detail-path', tunnel.configPath);
  setText('detail-state', tunnelStateLabel(state));
  setText(
    'detail-state-copy',
    action
      ? action === 'up'
        ? 'Starting wg-quic-quick…'
        : 'Stopping wg-quic-quick…'
      : stateDescription(tunnel),
  );
  byId('detail-state-dot').className = `state-dot large ${state}`;

  const toggle = byId<HTMLButtonElement>('toggle-tunnel');
  toggle.disabled = Boolean(action) || !backendSupported;
  toggle.setAttribute('aria-busy', String(Boolean(action)));
  toggle.textContent = action
    ? tunnelStateLabel(state)
    : tunnel.running
      ? 'Deactivate'
      : 'Activate';
  toggle.className = `button ${tunnel.running ? 'danger' : 'primary'}`;
  toggle.dataset.name = tunnel.name;
  toggle.dataset.action = tunnel.running ? 'down' : 'up';

  const diagnostics = byId<HTMLDetailsElement>('status-diagnostics');
  const hasDiagnostics = Boolean(tunnel.statusDetail && !action);
  diagnostics.classList.toggle('hidden', !hasDiagnostics);
  if (!hasDiagnostics) {
    diagnostics.open = false;
  }
  setText('status-diagnostics-copy', tunnel.statusDetail || '');

  const check = byId<HTMLButtonElement>('check-tunnel');
  check.disabled = Boolean(action);
  check.dataset.name = tunnel.name;

  setText('detail-carrier', status?.carrier.toUpperCase() || 'QUIC');
  setText(
    'detail-modes',
    status
      ? `${status.fec_mode} FEC · ${status.obfs_mode} obfuscation`
      : 'Runtime details unavailable while inactive',
  );
  setText('detail-tx', formatBytes(stats?.wg_tx_bytes));
  setText('detail-rx', formatBytes(stats?.wg_rx_bytes));
  setText('detail-rtt', formatRTT(stats?.quic_smoothed_rtt_us));
  setText(
    'detail-bandwidth',
    formatBitRate(stats?.quic_bandwidth_estimate_bps),
  );
  setText('detail-pacing', formatBitRate(stats?.quic_pacing_rate_bps));
  setText(
    'detail-fec-recovered',
    (stats?.fec_recovered || 0).toLocaleString(),
  );
  setText(
    'detail-fec-loss',
    formatFECRecovery(stats?.fec_recovered, stats?.fec_raw_lost),
  );
  setText(
    'detail-fec-parity',
    `${(stats?.fec_current_parity_shards || 0).toLocaleString()} current parity · ${(stats?.fec_unrecovered || 0).toLocaleString()} residual`,
  );
  renderPeers(status);
}

function setNotice(snapshot: DesktopSnapshot): void {
  const { backend } = snapshot;
  if (!backend.supported) {
    notice.classList.remove('hidden');
    setText('notice-title', 'Tunnel controls unavailable on this platform');
    setText(
      'notice-detail',
      'The interface is available as a preview, but wg-quic-quick host integration currently supports Windows and Linux.',
    );
    return;
  }
  if (backend.error) {
    notice.classList.remove('hidden');
    setText('notice-title', 'Native runtime needs attention');
    setText('notice-detail', backend.error);
    return;
  }
  notice.classList.add('hidden');
}

function render(snapshot: DesktopSnapshot): void {
  current = snapshot;
  selectedName = chooseSelectedTunnel(snapshot.tunnels, selectedName);
  setNotice(snapshot);

  setText(
    'tunnel-count',
    `${snapshot.tunnels.length} ${snapshot.tunnels.length === 1 ? 'tunnel' : 'tunnels'}`,
  );
  setText('config-location', snapshot.backend.configDirectory);
  setText(
    'last-refresh',
    `Updated ${refreshedAtDate(snapshot.refreshedAt).toLocaleTimeString()}`,
  );
  setText(
    'runtime-title',
    snapshot.backend.error
      ? 'Runtime unavailable'
      : snapshot.backend.supported
        ? 'Runtime ready'
        : 'UI preview',
  );
  setText(
    'runtime-version',
    snapshot.backend.quickVersion ||
      `${snapshot.backend.platform}/${snapshot.backend.arch}`,
  );
  byId('runtime-dot').className =
    `runtime-dot ${snapshot.backend.error ? 'error' : snapshot.backend.supported ? 'ready' : 'preview'}`;

  tunnelList.replaceChildren(
    ...snapshot.tunnels.map((tunnel) => createTunnelItem(tunnel)),
  );
  tunnelList.classList.toggle('hidden', snapshot.tunnels.length === 0);
  noTunnels.classList.toggle('hidden', snapshot.tunnels.length !== 0);
  renderDetail(
    snapshot.tunnels.find((tunnel) => tunnel.name === selectedName),
  );
  document.body.dataset.ready = 'true';
}

const refreshSnapshot = createSingleFlight(async (): Promise<void> => {
  byId('refresh').classList.add('spinning');
  try {
    render(await window.wgQuic.snapshot());
  } finally {
    byId('refresh').classList.remove('spinning');
  }
});

interface RefreshResult {
  ok: boolean;
  error?: string;
}

async function refresh(showErrors = true): Promise<RefreshResult> {
  try {
    await refreshSnapshot();
    return { ok: true };
  } catch (error) {
    const message = errorMessage(error);
    if (showErrors) {
      showToast(message, 'error');
    }
    return { ok: false, error: message };
  }
}

async function manageTunnel(
  name: string,
  action: TunnelAction,
): Promise<void> {
  if (pending.has(name)) {
    return;
  }
  pending.set(name, action);
  if (current) {
    render(current);
  }
  try {
    render(await window.wgQuic.manage(name, action));
    showToast(`${name} ${action === 'up' ? 'activated' : 'deactivated'}`);
  } catch (error) {
    showToast(
      `${name}: ${managementErrorMessage(errorMessage(error))}`,
      'error',
    );
  } finally {
    pending.delete(name);
    if (current) {
      render(current);
    }
  }
}

async function checkTunnel(name: string): Promise<void> {
  try {
    const result = await window.wgQuic.check(name);
    showToast(result || `${name} is valid`);
  } catch (error) {
    showToast(`${name}: ${errorMessage(error)}`, 'error');
  }
}

async function importTunnel(): Promise<void> {
  try {
    const result = await window.wgQuic.importConfig();
    if (result.importedName) {
      selectedName = result.importedName;
    }
    render(result.snapshot);
    if (!result.canceled && result.importedName) {
      showToast(`${result.importedName} imported`);
    }
  } catch (error) {
    showToast(
      `${errorMessage(error)} Writing the system configuration directory may require administrator privileges.`,
      'error',
    );
  }
}

byId('refresh').addEventListener('click', () => void refresh());
byId('import-config').addEventListener('click', () => void importTunnel());
byId('empty-import').addEventListener('click', () => void importTunnel());
byId('toggle-tunnel').addEventListener('click', (event) => {
  const button = event.currentTarget as HTMLButtonElement;
  const { name, action } = button.dataset;
  if (name && (action === 'up' || action === 'down')) {
    void manageTunnel(name, action);
  }
});
byId('check-tunnel').addEventListener('click', (event) => {
  const name = (event.currentTarget as HTMLButtonElement).dataset.name;
  if (name) {
    void checkTunnel(name);
  }
});
byId('open-directory').addEventListener('click', async () => {
  const error = await window.wgQuic.openConfigDirectory();
  if (error) {
    showToast(error, 'error');
  }
});

document.addEventListener('keydown', (event) => {
  if (event.ctrlKey && event.key.toLowerCase() === 'o') {
    event.preventDefault();
    void importTunnel();
    return;
  }
  if (event.ctrlKey && event.key.toLowerCase() === 'r') {
    event.preventDefault();
    void refresh();
    return;
  }
  if (
    (event.key === 'ArrowDown' || event.key === 'ArrowUp') &&
    current?.tunnels.length
  ) {
    const index = current.tunnels.findIndex(
      (tunnel) => tunnel.name === selectedName,
    );
    const delta = event.key === 'ArrowDown' ? 1 : -1;
    const next = Math.max(
      0,
      Math.min(current.tunnels.length - 1, index + delta),
    );
    selectedName = current.tunnels[next]?.name;
    render(current);
  }
});

document.addEventListener('visibilitychange', () => {
  if (!document.hidden) {
    void refresh(false);
  }
});

async function start(): Promise<void> {
  const smoke = await desktopSmokeSettings();
  smokeMode = smoke.mode;
  const initialRefresh = await refresh();
  if (!initialRefresh.ok) {
    throw new Error(
      initialRefresh.error || 'desktop could not load its native backend snapshot',
    );
  }
  if (smoke.mode !== 'none') {
    const backend = current?.backend;
    if (!backend || backend.error) {
      throw new Error(
        backend?.error || 'desktop did not return native backend information',
      );
    }
    if (!backend.coreVersion || !backend.quickVersion) {
      throw new Error('desktop did not verify its bundled native versions');
    }
  }
  if (smoke.mode === 'renderer') {
    await completeDesktopSmoke('wg-quic desktop renderer smoke test passed');
    return;
  }
  if (smoke.mode === 'tray') {
    await reportDesktopSmoke('wg-quic desktop tray smoke ready');
    return;
  }
  if (smoke.mode === 'integration') {
    if (!smoke.source || !smoke.name) {
      throw new Error(
        'desktop integration smoke requires a configuration path and tunnel name',
      );
    }
    const imported = await window.wgQuic.importConfigPath(
      smoke.source,
      false,
    );
    if (imported.importedName !== smoke.name) {
      throw new Error(
        `desktop imported ${JSON.stringify(imported.importedName)} instead of ${JSON.stringify(smoke.name)}`,
      );
    }
    let active = false;
    try {
      const running = await window.wgQuic.manage(smoke.name, 'up');
      active = true;
      const tunnel = running.tunnels.find(
        (candidate) => candidate.name === smoke.name,
      );
      if (
        !tunnel?.running ||
        tunnel.status?.interface !== smoke.name ||
        tunnel.status.state !== 'up'
      ) {
        throw new Error(
          `desktop did not observe the active tunnel: ${JSON.stringify(tunnel)}`,
        );
      }
    } finally {
      if (active) {
        await window.wgQuic.manage(smoke.name, 'down');
      }
    }
    const stopped = await window.wgQuic.snapshot();
    const tunnel = stopped.tunnels.find(
      (candidate) => candidate.name === smoke.name,
    );
    if (!tunnel || tunnel.running || tunnel.statusDetail) {
      throw new Error(
        `desktop did not observe a clean inactive tunnel: ${JSON.stringify(tunnel)}`,
      );
    }
    await completeDesktopSmoke(
      'wg-quic installed desktop import/UAC/service/status lifecycle passed',
    );
  }
}

void start().catch((error: unknown) => {
  showToast(errorMessage(error), 'error');
  if (smokeMode !== 'none') {
    void completeDesktopSmoke(
      `wg-quic desktop smoke test failed: ${errorMessage(error)}`,
      true,
    );
  }
});
setInterval(() => {
  if (!document.hidden) {
    void refresh(false);
  }
}, 2_000);
