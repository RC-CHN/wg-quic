import { t, currentLanguage, setLanguage, localizeDocument } from './i18n';
import './styles.css';
import './tauri-api';
import { ConfigurationApplications } from './config-application';
import { ObservationClock } from './observation-clock';
import { renderPeers } from './peer-view';
import { copyText } from './clipboard';
import { errorFields, routesAllTraffic } from './editor-guidance';
import {
  completeDesktopSmoke,
  desktopSmokeSettings,
  reportDesktopSmoke,
  quitDesktop,
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

localizeDocument();

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
const applying = new Set<string>();
const collecting = new Set<string>();
const diagnosticResults = new Map<string, string>();
const applications = new ConfigurationApplications(localStorage);
const observations = new ObservationClock();
let forceRefresh = false;

let current: DesktopSnapshot | null = null;
let selectedName: string | undefined;
let formDraft: TunnelDraft | null = null;
let formMode: 'new' | 'edit' = 'new';
let formSourceMode = false;
let formOriginal = '';
let formOriginalName = '';
let formRevision = 0;
let formBusy = false;
let formErrors: string[] = [];
let publicKeyRevision = 0;
let toastTimer: ReturnType<typeof setTimeout> | undefined;
let detailTab: 'overview' | 'diagnostics' = 'overview';
let displayedName: string | undefined;

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
  if (tunnel.statusState === 'unknown') return t('Status unavailable');
  if (!tunnel.running) {
    return t('Endpoint shown when active');
  }
  return tunnel.status?.peers?.[0]?.endpoint || t('No peer endpoint');
}

function createTunnelItem(tunnel: TunnelView, existing?: HTMLButtonElement): HTMLButtonElement {
  const item = existing || document.createElement('button');
  const state = tunnelDisplayState(tunnel, pending.get(tunnel.name));
  item.type = 'button';
  item.className = `tunnel-item ${selectedName === tunnel.name ? 'selected' : ''}`;
  item.dataset.name = tunnel.name;
  item.title = tunnel.name;
  item.setAttribute('role', 'option');
  item.setAttribute('aria-selected', String(selectedName === tunnel.name));
  item.tabIndex = selectedName === tunnel.name ? 0 : -1;

  const stateDot = item.children[0] || document.createElement('span');
  stateDot.className = `state-dot ${state}`;
  stateDot.setAttribute('aria-hidden', 'true');

  const copy = item.children[1] || document.createElement('span');
  copy.className = 'tunnel-item-copy';
  const name = copy.children[0] || document.createElement('strong');
  name.textContent = tunnel.name;
  const endpoint = copy.children[1] || document.createElement('span');
  endpoint.textContent = statusEndpoint(tunnel);
  if (!existing) copy.append(name, endpoint);

  const label = item.children[2] || document.createElement('span');
  label.className = `tunnel-item-state ${state}`;
  label.textContent = tunnelStateLabel(state);
  if (!existing) item.append(stateDot, copy, label);
  if (!existing) item.addEventListener('click', async () => {
    if (formDraft && !await canLeaveForm()) return;
    dismissForm();
    selectedName = tunnel.name;
    if (current) {
      render(current);
    }
    void refresh(false);
  });
  return item;
}

function stateDescription(tunnel: TunnelView): string {
  const state = tunnelDisplayState(tunnel);
  if (state === 'unknown') return t('Status could not be read. The tunnel may still be running; refresh to check again.');
  if (state === 'connected') return t('QUIC transport is established and WireGuard has authenticated the peer.');
  if (state === 'partial') return t('Some peers are connected. Check the remaining peers below.');
  if (state === 'authenticating') return t('QUIC is connected; waiting for WireGuard authentication.');
  if (state === 'reconnecting') return t('The interface is up and the transport is reconnecting automatically.');
  if (state === 'connecting') return t('The interface is up; connecting to the QUIC peer.');
  if (state === 'activating') return t('The interface is being prepared; host network settings are not yet ready.');
  if (!tunnel.running) {
    return t('The tunnel is configured and ready to activate.');
  }
  const sessions = tunnel.status?.stats.active_sessions || 0;
  if (sessions === 0) {
    return t('The interface is active and waiting for a QUIC peer session.');
  }
  return t("{0} active QUIC {1}.", sessions, sessions === 1 ? 'session' : 'sessions');
}

function setText(id: string, value: string): void {
  byId(id).textContent = value;
}

function refreshedAtDate(value: string): Date {
  return /^\d+$/.test(value) ? new Date(Number(value)) : new Date(value);
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
    return;
  }
  if (!tunnel) {
    setText(
      'detail-empty-title',
      current?.tunnels.length
        ? t('Select a tunnel')
        : t('Add your first tunnel'),
    );
    setText(
      'detail-empty-copy',
      current?.tunnels.length
        ? t('Choose a tunnel from the list to inspect or control it.')
        : t('Have a configuration file? Import it to get started. You can also create a tunnel with the details from your administrator.'),
    );
    return;
  }

  if (displayedName !== tunnel.name) {
    displayedName = tunnel.name;
    selectDetailTab('overview');
    byId<HTMLDetailsElement>('tunnel-menu').open = false;
    byId('tunnel-detail').querySelector('.detail-scroll')!.scrollTop = 0;
  }
  const status = tunnel.status;
  const stats = tunnel.statusState === 'unknown' ? undefined : status?.stats;
  const action = pending.get(tunnel.name);
  const busy = Boolean(action) || applying.has(tunnel.name);
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
  toggle.disabled = busy || !backendSupported || state === 'unknown' || tunnel.statusState === 'prepared';
  toggle.setAttribute('aria-busy', String(Boolean(action)));
  toggle.textContent = action
    ? tunnelStateLabel(state)
    : tunnel.running
      ? t('Disconnect')
      : t('Connect');
  toggle.className = `button ${tunnel.running ? 'secondary' : 'primary'}`;
  toggle.dataset.name = tunnel.name;
  toggle.dataset.action = tunnel.running ? 'down' : 'up';

  const diagnostics = byId<HTMLDetailsElement>('status-diagnostics');
  const hasDiagnostics = Boolean(tunnel.statusDetail && !action);
  diagnostics.classList.toggle('hidden', !hasDiagnostics);
  if (!hasDiagnostics) {
    diagnostics.open = false;
  }
  setText('status-diagnostics-copy', tunnel.statusDetail || '');
  byId('retry-status').classList.toggle('hidden', state !== 'unknown');

  const check = byId<HTMLButtonElement>('check-tunnel');
  check.disabled = busy;
  check.dataset.name = tunnel.name;

  const deleteButton = byId<HTMLButtonElement>('delete-tunnel');
  deleteButton.disabled = busy || !backendSupported;
  deleteButton.dataset.name = tunnel.name;

  const editButton = byId<HTMLButtonElement>('edit-tunnel');
  editButton.disabled = busy || !backendSupported;
  editButton.dataset.name = tunnel.name;
  renderConfigurationState(tunnel, busy);

  setText('detail-carrier', status?.carrier.toUpperCase() || 'QUIC');
  setText(
    'detail-modes',
    status
      ? t("{0} FEC · {1} obfuscation", status.fec_mode, status.obfs_mode)
      : t('Runtime details unavailable while inactive'),
  );
  setText('detail-tx', stats ? formatBytes(stats.wg_tx_bytes) : '—');
  setText('detail-rx', stats ? formatBytes(stats.wg_rx_bytes) : '—');
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
    t("{0} current parity · {1} residual", (stats?.fec_current_parity_shards || 0).toLocaleString(), (stats?.fec_unrecovered || 0).toLocaleString()),
  );
  setText('detail-public-key', status?.public_key || '—');
  byId<HTMLButtonElement>('copy-public-key').disabled = !status?.public_key;
  renderPeers(tunnel, showToast);
  setText('peer-count', t('{0} configured', status?.peers?.length || 0));
  const peerSelect = byId<HTMLSelectElement>('diagnostic-peer');
  const previousPeer = peerSelect.value;
  const peers = status?.peers || [];
  const signature = JSON.stringify(peers.map((peer) => [peer.public_key, peer.endpoint]));
  if (peerSelect.dataset.peers !== signature) {
    peerSelect.dataset.peers = signature;
    peerSelect.replaceChildren(...peers.map((peer) => {
      const option = document.createElement('option');
      option.value = peer.public_key;
      option.textContent = `${peer.endpoint || t('Peer')} · ${peer.public_key.slice(0, 12)}…`;
      return option;
    }));
    if (peers.some((peer) => peer.public_key === previousPeer)) peerSelect.value = previousPeer;
  }
  peerSelect.disabled = collecting.has(tunnel.name);
  const collect = byId<HTMLButtonElement>('collect-diagnostics');
  collect.disabled = collecting.has(tunnel.name) || !tunnel.running || tunnel.statusState === 'unknown' || !peers.length;
  collect.textContent = collecting.has(tunnel.name) ? t('Collecting (about 10 s)…') : t('Collect and export…');
  setText('diagnostic-result', diagnosticResults.get(tunnel.name) || '');
}

async function collectDiagnostics(): Promise<void> {
  const name = selectedName;
  const peer = byId<HTMLSelectElement>('diagnostic-peer').value;
  if (!name || !peer || collecting.has(name)) return;
  collecting.add(name);
  diagnosticResults.delete(name);
  if (current) render(current);
  try {
    const result = await window.wgQuic.collectDiagnostics(name, peer);
    if (!result.canceled) diagnosticResults.set(name, result.complete
      ? t("Diagnostics saved: {0}", result.path)
      : t("Partial diagnostics saved: {0}. {1}", result.path, result.detail || ''));
  } catch (error) { diagnosticResults.set(name, errorMessage(error)); }
  finally { collecting.delete(name); if (current) render(current); }
}

function renderConfigurationState(tunnel: TunnelView, busy: boolean): void {
  const saved = applications.get(tunnel.configPath);
  byId('configuration-state').classList.toggle('hidden', !saved);
  if (!saved) return;
  let message = t('Configuration saved. Apply it to the running tunnel when ready.');
  if (tunnel.statusState === 'unknown') message = t('Configuration saved. Current runtime state could not be checked.');
  else if (!tunnel.running) message = t('Configuration saved. It will be used on the next activation.');
  else if (saved.state === 'restart_required') message = t('Saved changes require a restart. The current connection is still using the previous settings.');
  else if (saved.state === 'failed') message = t('The saved configuration was not fully applied. Review the result before retrying.');
  else if (saved.state === 'unknown') message = t('The application result is not yet known. Check the original transaction before retrying.');
  if (applying.has(tunnel.name)) message = saved.state === 'unknown' ? t('Checking the application result…') : t('Applying saved configuration…');
  setText('configuration-state-copy', message);
  setText('configuration-result', [saved.message, ...(saved.restart_reasons || []), saved.request_id ? t("Request: {0}", saved.request_id) : ''].filter(Boolean).join('\n'));
  const apply = byId<HTMLButtonElement>('apply-config');
  apply.dataset.name = tunnel.name;
  apply.textContent = saved.state === 'unknown' ? t('Check application result') : t('Apply saved changes');
  apply.disabled = busy || !tunnel.running || tunnel.statusState === 'unknown' || saved.state === 'restart_required' || (saved.state === 'unknown' && !saved.request_id);
  apply.classList.toggle('hidden', !tunnel.running || saved.state === 'restart_required');
  const restart = byId<HTMLButtonElement>('restart-tunnel');
  restart.dataset.name = tunnel.name;
  restart.classList.toggle('hidden', saved.state !== 'restart_required' && saved.state !== 'unknown');
  restart.disabled = busy || !tunnel.running || tunnel.statusState === 'unknown';
}

async function applyConfiguration(name: string): Promise<void> {
  const tunnel = current?.tunnels.find((item) => item.name === name);
  if (!tunnel || applying.has(name) || pending.has(name)) return;
  const saved = applications.get(tunnel.configPath);
  if (saved?.state === 'unknown' && !saved.request_id) return;
  applying.add(name);
  render(current!);
  try {
    const result = await window.wgQuic.apply(name, saved?.state === 'unknown' ? saved.request_id : undefined);
    applications.set(tunnel.configPath, result);
    if (result.state === 'applied') showToast(result.cleanup_pending ? t('Configuration applied; host cleanup is still pending.') : t('Saved configuration applied'));
  } catch (error) {
    // A failed IPC/elevation command does not prove the supervisor rejected
    // the mutation. Keep a known transaction ID, and never retry blindly.
    applications.set(tunnel.configPath, { state: 'unknown', request_id: saved?.request_id, message: errorMessage(error) });
  } finally {
    observations.invalidate();
    applying.delete(name);
    await refresh(false);
    if (current) render(current);
  }
}

async function restartTunnel(name: string): Promise<void> {
  const tunnel = current?.tunnels.find((item) => item.name === name);
  if (!tunnel || pending.has(name) || applying.has(name) || !await window.wgQuic.confirmRestart(name)) return;
  pending.set(name, 'down');
  pendingSince.set(name, Date.now());
  render(current!);
  try {
    renderMutation(await window.wgQuic.manage(name, 'down'));
    pending.set(name, 'up');
    render(current!);
    renderMutation(await window.wgQuic.manage(name, 'up'));
    applications.clear(tunnel.configPath);
    showToast(t("{0} restarted with the saved configuration", name));
  } catch (error) {
    showToast(`${name}: ${managementErrorMessage(errorMessage(error))}`, 'error');
    await refresh(false);
  } finally {
    pending.delete(name);
    pendingSince.delete(name);
    if (current) render(current);
  }
}

// === Tunnel form (new/edit) ===

function preserveSelectValue(id: string, value: string): void {
  const select = byId<HTMLSelectElement>(id);
  select.querySelectorAll('[data-preserved]').forEach((option) => option.remove());
  if (!Array.from(select.options).some((option) => option.value === value)) {
    const option = new Option(value || t('Not specified'), value);
    option.dataset.preserved = 'true';
    select.add(option);
  }
  select.value = value;
}

function updateEditorGuidance(): void {
  if (!formDraft) return;
  setText('form-draft-state', formBusy ? t('Saving…') : formIsDirty() ? t('Unsaved changes') : formMode === 'new' ? t('New configuration') : t('No changes'));
  byId('form-draft-state').classList.toggle('dirty', formIsDirty());
  setText('form-route-hint', routesAllTraffic(byId<HTMLInputElement>('form-allowed-ips').value)
    ? t('All IPv4 and IPv6 traffic will use this peer.') : t('Only traffic to these networks uses this peer.'));
  setText('form-fec-hint', byId<HTMLSelectElement>('form-fec').value === 'off'
    ? t('This tunnel will not send FEC repair packets.')
    : t('Automatic FEC adapts to packet loss. Both peers must use compatible transport settings.'));
}

function revealField(id: string): void {
  const field = byId(id);
  let parent: HTMLElement | null = field.parentElement;
  while (parent) {
    if (parent instanceof HTMLDetailsElement) parent.open = true;
    parent = parent.parentElement;
  }
  field.scrollIntoView({ block: 'center' });
  field.focus({ preventScroll: true });
}

function clearFieldErrors(): void {
  tunnelForm.querySelectorAll('[aria-invalid]').forEach((field) => { field.removeAttribute('aria-invalid'); field.removeAttribute('aria-errormessage'); });
  tunnelForm.querySelectorAll('.field-error').forEach((field) => field.remove());
}

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
  preserveSelectValue('form-congestion', draft.congestion);
  preserveSelectValue('form-fec', draft.fec);
  preserveSelectValue('form-obfs', draft.obfs);
}

function readFormIntoDraft(): TunnelDraft {
  return {
    ...formDraft,
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
    carrier: formDraft?.carrier || 'quic',
    congestion: byId<HTMLSelectElement>('form-congestion').value,
    fec: byId<HTMLSelectElement>('form-fec').value,
    obfs: byId<HTMLSelectElement>('form-obfs').value,
  };
}

function renderForm(): void {
  if (!formDraft) {
    return;
  }
  setText('form-mode-label', formMode === 'new' ? t('NEW TUNNEL') : t('EDIT TUNNEL'));
  setText(
    'form-title',
    formMode === 'new' ? t('Create tunnel') : t("Edit {0}", formDraft.name),
  );
  fillFormFromDraft(formDraft);
  byId<HTMLTextAreaElement>('form-source').value = buildConf(formDraft);
  byId('form-structured').classList.toggle('hidden', formSourceMode);
  document.querySelectorAll('.structured-only').forEach((element) => element.classList.toggle('hidden', formSourceMode));
  byId('form-source-field').classList.toggle('hidden', !formSourceMode);
  byId<HTMLButtonElement>('form-source-toggle').textContent = formSourceMode ? t('Use form') : t('Edit source');
  const peers = byId<HTMLSelectElement>('form-peer-select');
  peers.replaceChildren(...Array.from({ length: formDraft.peerCount || 1 }, (_, index) => {
    const option = document.createElement('option');
    option.value = String(index);
    option.textContent = t("Peer {0}", index + 1);
    return option;
  }));
  peers.value = String(formDraft.peerIndex || 0);
  peers.disabled = (formDraft.peerCount || 1) < 2;
  byId('form-peer-selection').classList.toggle('hidden', peers.disabled);
  byId<HTMLInputElement>('form-name').disabled = formMode === 'edit';
  byId('form-errors').classList.add('hidden');
  formErrors = [];
  clearFieldErrors();
  updateEditorGuidance();
  void updatePublicKey();
}

function setPublicKey(key: string): void {
  publicKeyRevision++;
  byId<HTMLInputElement>('form-public-key').value = key;
  byId<HTMLButtonElement>('form-copy-public-key').disabled = !key;
}

async function updatePublicKey(): Promise<void> {
  const privateKey = byId<HTMLInputElement>('form-private-key').value.trim();
  setPublicKey('');
  const revision = publicKeyRevision;
  if (!privateKey || !formDraft) return;
  try {
    const publicKey = await window.wgQuic.derivePublicKey(privateKey);
    if (formDraft && revision === publicKeyRevision && byId<HTMLInputElement>('form-private-key').value.trim() === privateKey) setPublicKey(publicKey.trim());
  } catch { /* Invalid input keeps copying disabled while the user edits. */ }
}

async function copyPublicKey(key: string): Promise<void> {
  try { await copyText(key); showToast(t('Public key copied')); }
  catch (error) { showToast(errorMessage(error), 'error'); }
}

function formContents(): string {
  return formSourceMode ? byId<HTMLTextAreaElement>('form-source').value : buildConf(readFormIntoDraft());
}

function formIsDirty(): boolean {
  return formDraft !== null && (formContents() !== formOriginal ||
    byId<HTMLInputElement>('form-name').value.trim() !== formOriginalName);
}

async function canLeaveForm(): Promise<boolean> {
  return !formBusy && (!formIsDirty() || await window.wgQuic.confirmDiscard());
}

async function canQuit(): Promise<boolean> {
  if (pending.size || applying.size || collecting.size || formBusy) {
    showToast(t('An operation is in progress. Wait for it to finish before quitting.'), 'error');
    return false;
  }
  return canLeaveForm();
}

function toggleFormSource(): void {
  if (!formDraft || formBusy) return;
  const name = byId<HTMLInputElement>('form-name').value.trim();
  formDraft = parseConf(formContents(), formDraft.peerIndex || 0);
  if ((formDraft.peerIndex || 0) >= (formDraft.peerCount || 1)) {
    formDraft = parseConf(formDraft.sourceText!);
  }
  formDraft.name = name;
  formSourceMode = !formSourceMode;
  renderForm();
  revealField(formSourceMode ? 'form-source' : 'form-addresses');
}

function selectFormPeer(index: number): void {
  if (!formDraft || formBusy) return;
  const name = byId<HTMLInputElement>('form-name').value.trim();
  formDraft = parseConf(formContents(), index);
  formDraft.name = name;
  renderForm();
}

function showFormErrors(errors: string[], focus = true): void {
  formErrors = errors;
  clearFieldErrors();
  const box = byId('form-errors');
  box.replaceChildren();
  const heading = document.createElement('strong');
  heading.textContent = t('Review these fields before saving');
  const list = document.createElement('ul');
  let firstField: string | undefined;
  for (const error of errors) {
    const item = document.createElement('li');
    const fieldId = Object.entries(errorFields).find(([message]) => error === message || error === t(message))?.[1];
    if (fieldId && !formSourceMode) {
      firstField ||= fieldId;
      const link = document.createElement('button');
      link.type = 'button';
      link.textContent = t(error);
      link.addEventListener('click', () => revealField(fieldId));
      item.append(link);
      const field = byId(fieldId);
      const hint = document.createElement('span');
      hint.className = 'field-error';
      hint.id = `${fieldId}-error`;
      hint.textContent = t(error);
      field.closest('.form-field')!.append(hint);
      field.setAttribute('aria-invalid', 'true');
      field.setAttribute('aria-errormessage', hint.id);
    } else item.textContent = t(error);
    list.append(item);
  }
  box.append(heading, list);
  box.classList.remove('hidden');
  if (focus) revealField(firstField || 'form-errors');
}

async function startNewTunnel(): Promise<void> {
  if (!await canLeaveForm()) return;
  const revision = ++formRevision;
  formMode = 'new';
  formSourceMode = false;
  formDraft = emptyTunnelDraft('');
  formOriginal = buildConf(formDraft);
  formOriginalName = '';
  renderForm();
  if (current) {
    render(current);
  }
  byId<HTMLInputElement>('form-name').focus();
  try {
    const keys = await window.wgQuic.generateKeys();
    const field = byId<HTMLInputElement>('form-private-key');
    if (formDraft && formRevision === revision && !formSourceMode && !field.value) {
      field.value = keys.private_key;
      setPublicKey(keys.public_key);
      // Only the generated default is clean; keep any edits made during IPC dirty.
      formOriginal = buildConf({ ...emptyTunnelDraft(''), privateKey: keys.private_key });
      updateEditorGuidance();
    }
  } catch (error) {
    showToast(t("Generate keys failed: {0}", errorMessage(error)), 'error');
  }
}

async function startEditTunnel(name: string): Promise<void> {
  if (!await canLeaveForm()) return;
  const revision = ++formRevision;
  try {
    const conf = await window.wgQuic.readTunnel(name);
    if (revision !== formRevision) return;
    formMode = 'edit';
    formSourceMode = false;
    formDraft = parseConf(conf);
    formDraft.name = name;
    formOriginal = conf;
    formOriginalName = name;
    renderForm();
    if (current) {
      render(current);
    }
    revealField('form-addresses');
  } catch (error) {
    showToast(t("Read tunnel failed: {0}", errorMessage(error)), 'error');
  }
}

async function cancelForm(): Promise<void> {
  if (!await canLeaveForm()) return;
  dismissForm();
  if (current) render(current);
  (selectedName ? byId('edit-tunnel') : byId('new-tunnel')).focus();
}

function dismissForm(): void {
  formRevision++;
  formDraft = null;
  clearFormSecrets();
}

function clearFormSecrets(): void {
  formOriginal = '';
  setPublicKey('');
  for (const id of ['form-private-key', 'form-preshared-key', 'form-source']) {
    byId<HTMLInputElement | HTMLTextAreaElement>(id).value = '';
  }
}

async function generateKeyIntoForm(): Promise<void> {
  const revision = formRevision;
  const previous = byId<HTMLInputElement>('form-private-key').value;
  if (previous && !await window.wgQuic.confirmRegenerate()) return;
  if (!formDraft || revision !== formRevision || formBusy) return;
  try {
    const keys = await window.wgQuic.generateKeys();
    const field = byId<HTMLInputElement>('form-private-key');
    if (formDraft && revision === formRevision && !formBusy && !formSourceMode && field.value === previous) {
      field.value = keys.private_key;
      setPublicKey(keys.public_key);
      updateEditorGuidance();
    }
  } catch (error) {
    showToast(t("Generate keys failed: {0}", errorMessage(error)), 'error');
  }
}

async function saveForm(): Promise<void> {
  if (!formDraft || formBusy) {
    return;
  }
  const draft = readFormIntoDraft();
  if (formMode === 'edit') {
    draft.name = formDraft.name;
  }
  const errors = formSourceMode
    ? (!draft.name ? [t('Tunnel name is required.')] : [])
    : validateTunnelDraft(draft, formMode === 'new');
  if (errors.length > 0) {
    showFormErrors(errors);
    return;
  }
  const conf = formContents();
  const saveButton = byId<HTMLButtonElement>('form-save');
  saveButton.disabled = true;
  formBusy = true;
  saveButton.textContent = t('Saving…');
  saveButton.setAttribute('aria-busy', 'true');
  byId<HTMLButtonElement>('form-source-toggle').disabled = true;
  updateEditorGuidance();
  byId<HTMLFieldSetElement>('form-controls').disabled = true;
  byId<HTMLButtonElement>('form-cancel').disabled = true;
  try {
    const snapshot = await window.wgQuic.writeTunnel(
      draft.name,
      conf,
      formMode === 'edit',
    );
    const savedName = draft.name;
    const wasNew = formMode === 'new';
    const savedTunnel = snapshot.tunnels.find((item) => item.name === savedName);
    if (savedTunnel) applications.set(savedTunnel.configPath, { state: 'saved' });
    formDraft = null;
    formRevision++;
    clearFormSecrets();
    observations.invalidate();
    current = snapshot;
    selectedName = savedName;
    render(current);
    showToast(
      wasNew ? t("Tunnel {0} created", savedName) : t("Configuration saved for {0}", savedName),
    );
  } catch (error) {
    showFormErrors([errorMessage(error)]);
  } finally {
    saveButton.disabled = false;
    formBusy = false;
    saveButton.textContent = t('Save & validate');
    saveButton.setAttribute('aria-busy', 'false');
    byId<HTMLButtonElement>('form-source-toggle').disabled = false;
    updateEditorGuidance();
    byId<HTMLFieldSetElement>('form-controls').disabled = false;
    byId<HTMLButtonElement>('form-cancel').disabled = false;
  }
}

function setNotice(snapshot: DesktopSnapshot): void {
  const { backend } = snapshot;
  if (!backend.supported) {
    notice.classList.remove('hidden');
    setText('notice-title', t('Tunnel controls unavailable on this platform'));
    setText(
      'notice-detail',
      t('The interface is available as a preview, but wg-quic-quick host integration currently supports Windows and Linux.'),
    );
    return;
  }
  if (backend.error) {
    notice.classList.remove('hidden');
    setText('notice-title', t('Native runtime needs attention'));
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
        ? t('The installed service does not match this desktop version. Repair or reinstall wg-quic; administrator approval will be used as a fallback.')
        : t('Repair or reinstall wg-quic to restore one-click tunnel controls. Administrator approval will be used as a fallback.'),
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
    t(snapshot.tunnels.length === 1 ? '{0} tunnel' : '{0} tunnels', snapshot.tunnels.length),
  );
  setText('config-location', snapshot.backend.configDirectory);
  setText(
    'last-refresh',
    t("Updated {0}", refreshedAtDate(snapshot.refreshedAt).toLocaleTimeString()),
  );
  setText(
    'runtime-title',
    snapshot.backend.error
      ? t('Runtime unavailable')
      : snapshot.backend.supported
        ? t('Runtime ready')
        : t('UI preview'),
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

  const existing = new Map(Array.from(tunnelList.querySelectorAll<HTMLButtonElement>('.tunnel-item')).map((item) => [item.dataset.name!, item]));
  for (const [index, tunnel] of snapshot.tunnels.entries()) {
    const item = createTunnelItem(tunnel, existing.get(tunnel.name));
    existing.delete(tunnel.name);
    if (tunnelList.children[index] !== item) tunnelList.insertBefore(item, tunnelList.children[index] || null);
  }
  for (const item of existing.values()) item.remove();
  filterTunnelList();
  tunnelList.classList.toggle('hidden', snapshot.tunnels.length === 0);
  noTunnels.classList.toggle('hidden', snapshot.tunnels.length !== 0);
  renderDetail(
    snapshot.tunnels.find((tunnel) => tunnel.name === selectedName),
  );
  document.body.dataset.ready = 'true';
}

function renderMutation(snapshot: DesktopSnapshot): void {
  observations.invalidate();
  render(snapshot);
}

const refreshSnapshot = createSingleFlight(async (): Promise<void> => {
  byId('refresh').classList.add('spinning');
  const revision = observations.capture();
  const force = forceRefresh;
  forceRefresh = false;
  try {
    const snapshot = await window.wgQuic.snapshot(selectedName, force);
    if (observations.accepts(revision)) render(snapshot);
  } catch (error) {
    if (current && observations.accepts(revision)) render({ ...current, tunnels: current.tunnels.map((tunnel) => ({ ...tunnel, statusState: 'unknown', statusCode: 'snapshot_failed', statusDetail: errorMessage(error) })) });
    throw error;
  } finally {
    byId('refresh').classList.remove('spinning');
  }
});

interface RefreshResult {
  ok: boolean;
  error?: string;
}

async function refresh(showErrors = true): Promise<RefreshResult> {
  forceRefresh ||= showErrors;
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
  if (pending.has(name) || applying.has(name)) {
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
    renderMutation(await window.wgQuic.manage(name, action));
    if (action === 'up') {
      const tunnel = current?.tunnels.find((item) => item.name === name);
      if (tunnel?.running) applications.clear(tunnel.configPath);
    }
    showToast(t(action === 'up' ? 'Tunnel {0} activated' : 'Tunnel {0} deactivated', name));
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
    showToast(result || t("{0} is valid", name));
  } catch (error) {
    showToast(`${name}: ${errorMessage(error)}`, 'error');
  }
}

async function deleteTunnel(name: string): Promise<void> {
  if (pending.has(name) || applying.has(name)) return;
  const tunnel = current?.tunnels.find((item) => item.name === name);
  try {
    const result = await window.wgQuic.deleteTunnel(name);
    if (result.canceled) {
      return;
    }
    if (tunnel) applications.clear(tunnel.configPath);
    if (selectedName === name) {
      selectedName = undefined;
    }
    renderMutation(result.snapshot);
    showToast(t("{0} deleted", name));
  } catch (error) {
    showToast(`${name}: ${errorMessage(error)}`, 'error');
    await refresh(false);
  }
}

async function importTunnel(): Promise<void> {
  if (!await canLeaveForm()) return;
  try {
    const result = await window.wgQuic.importConfig();
    if (result.importedName) {
      dismissForm();
      selectedName = result.importedName;
      const tunnel = result.snapshot.tunnels.find((item) => item.name === result.importedName);
      if (tunnel) applications.set(tunnel.configPath, { state: 'saved' });
    }
    renderMutation(result.snapshot);
    if (!result.canceled && result.importedName) {
      showToast(t("{0} imported", result.importedName));
    }
  } catch (error) {
    showToast(
      t("{0} Writing the system configuration directory may require administrator privileges.", errorMessage(error)),
      'error',
    );
  }
}

function filterTunnelList(): void {
  const query = byId<HTMLInputElement>('tunnel-search').value.trim().toLocaleLowerCase();
  const items = Array.from(tunnelList.querySelectorAll<HTMLButtonElement>('.tunnel-item'));
  for (const item of items) item.classList.toggle('hidden', !item.dataset.name!.toLocaleLowerCase().includes(query));
  const visible = items.filter((item) => !item.classList.contains('hidden'));
  // Preserve selection while filtering, but keep a keyboard entry into results.
  for (const item of items) item.tabIndex = item.dataset.name === selectedName && visible.includes(item) ? 0 : -1;
  if (!visible.some((item) => item.tabIndex === 0) && visible[0]) visible[0].tabIndex = 0;
  byId('no-results').classList.toggle('hidden', !items.length || visible.length > 0);
}

function selectDetailTab(tab: 'overview' | 'diagnostics', focus = false): void {
  detailTab = tab;
  for (const name of ['overview', 'diagnostics'] as const) {
    const button = byId<HTMLButtonElement>(`tab-${name}`);
    button.setAttribute('aria-selected', String(name === tab));
    button.tabIndex = name === tab ? 0 : -1;
    byId(`panel-${name}`).classList.toggle('hidden', name !== tab);
  }
  if (focus) byId(`tab-${tab}`).focus();
}
byId('tunnel-search').addEventListener('input', filterTunnelList);
byId('tunnel-search').addEventListener('keydown', (event) => {
  if (event.key === 'Escape') { byId<HTMLInputElement>('tunnel-search').value = ''; filterTunnelList(); }
  if (event.key === 'ArrowDown') { tunnelList.querySelector<HTMLButtonElement>('.tunnel-item:not(.hidden)')?.focus(); event.preventDefault(); }
});
for (const tab of ['overview', 'diagnostics'] as const) {
  byId(`tab-${tab}`).addEventListener('click', () => selectDetailTab(tab));
  byId(`tab-${tab}`).addEventListener('keydown', (event) => {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    selectDetailTab(event.key === 'Home' ? 'overview' : event.key === 'End' ? 'diagnostics' : detailTab === 'overview' ? 'diagnostics' : 'overview', true);
  });
}
const tunnelMenu = byId<HTMLDetailsElement>('tunnel-menu');
document.addEventListener('click', (event) => {
  if (event.target instanceof Node && (!tunnelMenu.contains(event.target) || event.target instanceof HTMLButtonElement)) tunnelMenu.open = false;
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && tunnelMenu.open) { tunnelMenu.open = false; tunnelMenu.querySelector('summary')!.focus(); event.preventDefault(); }
});

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
byId<HTMLSelectElement>('language-select').value = currentLanguage();
byId('language-select').addEventListener('change', () => {
  setLanguage(byId<HTMLSelectElement>('language-select').value === 'zh' ? 'zh' : 'en');
  localizeDocument();
  // Change copy only: switching language must never rebuild an active draft.
  if (formDraft) {
    setText('form-mode-label', formMode === 'new' ? t('NEW TUNNEL') : t('EDIT TUNNEL'));
    setText('form-title', formMode === 'new' ? t('Create tunnel') : t('Edit {0}', formDraft.name));
    setText('form-source-toggle', formSourceMode ? t('Use form') : t('Edit source'));
    for (const option of Array.from(byId<HTMLSelectElement>('form-peer-select').options)) option.textContent = t('Peer {0}', Number(option.value) + 1);
    if (formErrors.length) showFormErrors(formErrors, false);
    updateEditorGuidance();
  }
  if (current) render(current);
});
byId('collect-diagnostics').addEventListener('click', () => void collectDiagnostics());
byId('copy-public-key').addEventListener('click', () => {
  const key = current?.tunnels.find((tunnel) => tunnel.name === selectedName)?.status?.public_key;
  if (key) void copyPublicKey(key);
});
byId('form-copy-public-key').addEventListener('click', () => void copyPublicKey(byId<HTMLInputElement>('form-public-key').value));
let keyInputTimer: ReturnType<typeof setTimeout> | undefined;
byId('form-private-key').addEventListener('input', () => {
  setPublicKey('');
  clearTimeout(keyInputTimer);
  keyInputTimer = setTimeout(() => void updatePublicKey(), 250);
});
byId('retry-status').addEventListener('click', () => void refresh());
byId('apply-config').addEventListener('click', () => {
  const name = byId('apply-config').dataset.name;
  if (name) void applyConfiguration(name);
});
byId('restart-tunnel').addEventListener('click', () => {
  const name = byId('restart-tunnel').dataset.name;
  if (name) void restartTunnel(name);
});
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
byId('empty-new').addEventListener('click', () => void startNewTunnel());
byId('edit-tunnel').addEventListener('click', (event) => {
  const name = (event.currentTarget as HTMLButtonElement).dataset.name;
  if (name) {
    void startEditTunnel(name);
  }
});
byId('form-cancel').addEventListener('click', () => void cancelForm());
byId('form-source-toggle').addEventListener('click', toggleFormSource);
byId('form-peer-select').addEventListener('change', (event) => {
  selectFormPeer(Number((event.currentTarget as HTMLSelectElement).value));
});
byId('form-generate-key').addEventListener('click', () =>
  void generateKeyIntoForm(),
);
byId('tunnel-form-fields').addEventListener('input', updateEditorGuidance);
byId('tunnel-form-fields').addEventListener('change', updateEditorGuidance);
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
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's' && formDraft) { event.preventDefault(); void saveForm(); return; }
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
  if (!formDraft && event.target instanceof HTMLElement && tunnelList.contains(event.target) &&
      ['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
    const items = Array.from(tunnelList.querySelectorAll<HTMLButtonElement>('.tunnel-item:not(.hidden)'));
    const index = items.findIndex((item) => item.dataset.name === selectedName);
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 :
      Math.max(0, Math.min(items.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)));
    const item = items[next];
    if (item) { item.click(); item.focus(); }
    event.preventDefault();
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
  if (smoke.mode === 'none') {
    const { getCurrentWindow } = await import('@tauri-apps/api/window');
    await getCurrentWindow().listen('desktop-quit-requested', async () => {
      if (await canQuit()) await quitDesktop();
    });
    await getCurrentWindow().onCloseRequested(async (event) => {
      // Windows closes to the tray and retains the draft. Linux closes the
      // application, so ask before discarding or interrupting a pending save.
      if (current?.backend.platform === 'win32' || !await canQuit()) {
        event.preventDefault();
      }
    });
  }
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
    const { runEditorInteractionSmoke } = await import('./editor-smoke');
    await runEditorInteractionSmoke({
      startNewTunnel, startEditTunnel, refresh: () => refreshSnapshot(),
      saveForm, cancelForm, toggleFormSource, selectFormPeer, formIsDirty,
      canQuit,
    });
    const { runStatusInteractionSmoke } = await import('./status-smoke');
    await runStatusInteractionSmoke(refreshSnapshot);
    const { runWorkspaceInteractionSmoke } = await import('./workspace-smoke');
    await runWorkspaceInteractionSmoke(refreshSnapshot);
    const { runApplicationInteractionSmoke } = await import('./application-smoke');
    await runApplicationInteractionSmoke({ refresh: refreshSnapshot, edit: startEditTunnel, save: saveForm, apply: applyConfiguration, restart: restartTunnel });
    const { runDiagnosticInteractionSmoke } = await import('./diagnostic-smoke');
    await runDiagnosticInteractionSmoke({ refresh: refreshSnapshot, collect: collectDiagnostics });
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
