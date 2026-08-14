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
import {
  buildConf,
  emptyTunnelDraft,
  parseConf,
  validateTunnelDraft,
  type TunnelDraft,
} from './tunnel-draft';

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
const tunnelForm = byId<HTMLElement>('tunnel-form');
const notice = byId<HTMLElement>('notice');
const toast = byId<HTMLDivElement>('toast');
const pending = new Map<string, TunnelAction>();
const pendingSince = new Map<string, number>();

let current: DesktopSnapshot | null = null;
let selectedName: string | undefined;
let formDraft: TunnelDraft | null = null;
let formMode: 'new' | 'edit' = 'new';
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

function tunnelSummary(tunnel: TunnelView): string {
  const status = tunnel.status;
  if (!status) {
    return '—';
  }
  const address = status.addresses?.[0];
  const peerEndpoint = status.peers?.find((peer) => peer.endpoint)?.endpoint;
  if (address && peerEndpoint) {
    return `${address} → ${peerEndpoint}`;
  }
  return address || peerEndpoint || '—';
}

function renderDetail(tunnel?: TunnelView): void {
  const inForm = formDraft !== null;
  tunnelForm.classList.toggle('hidden', !inForm);
  detail.classList.toggle('hidden', inForm || !tunnel);
  detailEmpty.classList.toggle('hidden', inForm || Boolean(tunnel));
  if (inForm) {
    renderForm();
    return;
  }
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
  setText('detail-summary', tunnelSummary(tunnel));
  setText('detail-path', tunnel.configPath);
  setText('detail-state', tunnelStateLabel(state));
  const startedAt = pendingSince.get(tunnel.name);
  setText(
    'detail-state-copy',
    action
      ? actionProgressDescription(
          tunnel,
          action,
          startedAt ? (Date.now() - startedAt) / 1000 : 0,
        )
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

  const deleteButton = byId<HTMLButtonElement>('delete-tunnel');
  deleteButton.disabled = Boolean(action) || !backendSupported;
  deleteButton.dataset.name = tunnel.name;

  const editButton = byId<HTMLButtonElement>('edit-tunnel');
  editButton.disabled = Boolean(action) || !backendSupported;
  editButton.dataset.name = tunnel.name;

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

// === Tunnel form (new/edit) ===

function fillFormFromDraft(draft: TunnelDraft): void {
  byId<HTMLInputElement>('form-name').value = draft.name;
  byId<HTMLInputElement>('form-addresses').value = draft.addresses;
  byId<HTMLInputElement>('form-listen-port').value = draft.listenPort;
  byId<HTMLInputElement>('form-peer-public-key').value = draft.peerPublicKey;
  byId<HTMLInputElement>('form-endpoint').value = draft.endpoint;
  byId<HTMLInputElement>('form-allowed-ips').value = draft.allowedIPs;
  byId<HTMLInputElement>('form-keepalive').value = draft.keepalive;
  byId<HTMLInputElement>('form-private-key').value = draft.privateKey;
  byId<HTMLInputElement>('form-preshared-key').value = draft.presharedKey;
  byId<HTMLInputElement>('form-dns').value = draft.dns;
  byId<HTMLInputElement>('form-mtu').value = draft.mtu;
  byId<HTMLSelectElement>('form-congestion').value = draft.congestion;
  byId<HTMLSelectElement>('form-fec').value = draft.fec;
  byId<HTMLSelectElement>('form-obfs').value = draft.obfs;
}

function readFormIntoDraft(): TunnelDraft {
  return {
    name: byId<HTMLInputElement>('form-name').value.trim(),
    addresses: byId<HTMLInputElement>('form-addresses').value,
    listenPort: byId<HTMLInputElement>('form-listen-port').value,
    peerPublicKey: byId<HTMLInputElement>('form-peer-public-key').value,
    endpoint: byId<HTMLInputElement>('form-endpoint').value,
    allowedIPs: byId<HTMLInputElement>('form-allowed-ips').value,
    keepalive: byId<HTMLInputElement>('form-keepalive').value,
    privateKey: byId<HTMLInputElement>('form-private-key').value,
    presharedKey: byId<HTMLInputElement>('form-preshared-key').value,
    dns: byId<HTMLInputElement>('form-dns').value,
    mtu: byId<HTMLInputElement>('form-mtu').value,
    carrier: 'quic',
    congestion: byId<HTMLSelectElement>('form-congestion').value,
    fec: byId<HTMLSelectElement>('form-fec').value,
    obfs: byId<HTMLSelectElement>('form-obfs').value,
  };
}

function renderForm(): void {
  if (!formDraft) {
    return;
  }
  setText('form-mode-label', formMode === 'new' ? 'NEW TUNNEL' : 'EDIT TUNNEL');
  setText(
    'form-title',
    formMode === 'new' ? 'Create tunnel' : `Edit ${formDraft.name}`,
  );
  fillFormFromDraft(formDraft);
  byId<HTMLInputElement>('form-name').disabled = formMode === 'edit';
  byId('form-errors').classList.add('hidden');
}

function showFormErrors(errors: string[]): void {
  const box = byId('form-errors');
  box.innerHTML = '';
  const list = document.createElement('ul');
  for (const error of errors) {
    const item = document.createElement('li');
    item.textContent = error;
    list.appendChild(item);
  }
  box.appendChild(list);
  box.classList.remove('hidden');
}

async function startNewTunnel(): Promise<void> {
  formMode = 'new';
  formDraft = emptyTunnelDraft('');
  if (current) {
    render(current);
  }
  try {
    const keys = await window.wgQuic.generateKeys();
    const field = byId<HTMLInputElement>('form-private-key');
    if (formDraft && formMode === 'new' && !field.value) {
      field.value = keys.private_key;
    }
  } catch (error) {
    showToast(`Generate keys failed: ${errorMessage(error)}`, 'error');
  }
}

async function startEditTunnel(name: string): Promise<void> {
  try {
    const conf = await window.wgQuic.readTunnel(name);
    formMode = 'edit';
    formDraft = parseConf(conf);
    formDraft.name = name;
    if (current) {
      render(current);
    }
  } catch (error) {
    showToast(`Read tunnel failed: ${errorMessage(error)}`, 'error');
  }
}

function cancelForm(): void {
  formDraft = null;
  if (current) {
    render(current);
  }
}

async function generateKeyIntoForm(): Promise<void> {
  try {
    const keys = await window.wgQuic.generateKeys();
    byId<HTMLInputElement>('form-private-key').value = keys.private_key;
  } catch (error) {
    showToast(`Generate keys failed: ${errorMessage(error)}`, 'error');
  }
}

async function saveForm(): Promise<void> {
  if (!formDraft) {
    return;
  }
  const draft = readFormIntoDraft();
  if (formMode === 'edit') {
    draft.name = formDraft.name;
  }
  const errors = validateTunnelDraft(draft, formMode === 'new');
  if (errors.length > 0) {
    showFormErrors(errors);
    return;
  }
  const conf = buildConf(draft);
  const saveButton = byId<HTMLButtonElement>('form-save');
  saveButton.disabled = true;
  try {
    const snapshot = await window.wgQuic.writeTunnel(
      draft.name,
      conf,
      formMode === 'edit',
    );
    const savedName = draft.name;
    const wasNew = formMode === 'new';
    formDraft = null;
    current = snapshot;
    selectedName = savedName;
    render(current);
    showToast(
      wasNew ? `Tunnel ${savedName} created` : `Tunnel ${savedName} updated`,
    );
  } catch (error) {
    showFormErrors([errorMessage(error)]);
  } finally {
    saveButton.disabled = false;
  }
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
  const management = managementServiceDisplay(
    backend.platform,
    backend.managementStatus,
  );
  if (management.needsAttention) {
    notice.classList.remove('hidden');
    setText('notice-title', management.label);
    setText(
      'notice-detail',
      backend.managementStatus === 'incompatible'
        ? 'The installed service does not match this desktop version. Repair or reinstall wg-quic; administrator approval will be used as a fallback.'
        : 'Repair or reinstall wg-quic to restore one-click tunnel controls. Administrator approval will be used as a fallback.',
    );
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

  const management = managementServiceDisplay(
    snapshot.backend.platform,
    snapshot.backend.managementStatus,
  );
  const managementLine = byId<HTMLDivElement>('management-line');
  managementLine.classList.toggle('hidden', management.state === 'hidden');
  setText('management-label', management.label);
  byId('management-dot').className = `management-dot ${management.state}`;

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
  pendingSince.set(name, Date.now());
  if (current) {
    render(current);
  }
  // While the privileged command runs, track the core status socket at a
  // fine cadence so the progress copy follows real state transitions
  // (interface up, QUIC session established) instead of a static label.
  const progressPoll = setInterval(() => {
    void refresh(false);
  }, 500);
  try {
    render(await window.wgQuic.manage(name, action));
    showToast(`${name} ${action === 'up' ? 'activated' : 'deactivated'}`);
  } catch (error) {
    showToast(
      `${name}: ${managementErrorMessage(errorMessage(error))}`,
      'error',
    );
    await refresh(false);
  } finally {
    clearInterval(progressPoll);
    pending.delete(name);
    pendingSince.delete(name);
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

async function deleteTunnel(name: string): Promise<void> {
  try {
    const result = await window.wgQuic.deleteTunnel(name);
    if (result.canceled) {
      return;
    }
    if (selectedName === name) {
      selectedName = undefined;
    }
    render(result.snapshot);
    showToast(`${name} deleted`);
  } catch (error) {
    showToast(`${name}: ${errorMessage(error)}`, 'error');
    await refresh(false);
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

function applyTheme(theme: 'dark' | 'light'): void {
  document.documentElement.dataset.theme = theme;
  byId<HTMLSpanElement>('theme-icon').textContent =
    theme === 'light' ? '☀' : '☾';
}

function currentTheme(): 'dark' | 'light' {
  return document.documentElement.dataset.theme === 'light' ? 'light' : 'dark';
}

function initTheme(): void {
  const saved = localStorage.getItem('wg-quic-theme');
  const initial =
    saved === 'light' || saved === 'dark'
      ? saved
      : window.matchMedia('(prefers-color-scheme: light)').matches
        ? 'light'
        : 'dark';
  applyTheme(initial);
}

initTheme();

byId('theme-toggle').addEventListener('click', () => {
  const next = currentTheme() === 'light' ? 'dark' : 'light';
  localStorage.setItem('wg-quic-theme', next);
  applyTheme(next);
});

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
byId('delete-tunnel').addEventListener('click', (event) => {
  const name = (event.currentTarget as HTMLButtonElement).dataset.name;
  if (name) {
    void deleteTunnel(name);
  }
});
byId('new-tunnel').addEventListener('click', () => void startNewTunnel());
byId('edit-tunnel').addEventListener('click', (event) => {
  const name = (event.currentTarget as HTMLButtonElement).dataset.name;
  if (name) {
    void startEditTunnel(name);
  }
});
byId('form-cancel').addEventListener('click', () => cancelForm());
byId('form-generate-key').addEventListener('click', () =>
  void generateKeyIntoForm(),
);
byId('tunnel-form-fields').addEventListener('submit', (event) => {
  event.preventDefault();
  void saveForm();
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
    if (
      current?.backend.platform === 'win32' &&
      current.backend.managementStatus !== 'ready'
    ) {
      throw new Error(
        `installed management service is not ready: ${JSON.stringify(current.backend)}`,
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
    const storedConfiguration = await window.wgQuic.readTunnel(smoke.name);
    if (
      !/^\[Interface\]\s*$/m.test(storedConfiguration) ||
      !/^PrivateKey\s*=/m.test(storedConfiguration) ||
      !/^\[Peer\]\s*$/m.test(storedConfiguration) ||
      !/^Endpoint\s*=\s*192\.0\.2\.200:/m.test(storedConfiguration)
    ) {
      throw new Error(
        'desktop read returned an unexpected installed configuration',
      );
    }
    const checked = await window.wgQuic.check(smoke.name);
    if (!/configuration is valid for wg-quic-quick/i.test(checked)) {
      throw new Error(
        `desktop returned an unexpected configuration check result: ${JSON.stringify(checked)}`,
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
      'wg-quic installed desktop import/broker/service/status lifecycle passed',
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
