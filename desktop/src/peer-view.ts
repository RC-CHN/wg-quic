import { t } from './i18n';
import type { TunnelView } from './types';
import { PeerRates } from './peer-rates';
import { formatBitRate, formatBytes } from './view-model';
import { copyText } from './clipboard';

const rates = new PeerRates();
export function renderPeers(tunnel: TunnelView, notify: (text: string, kind?: 'ok' | 'error') => void): void {
  const list = document.getElementById('peer-list')!;
  const status = tunnel.status;
  const observations = rates.observe(tunnel);
  const existing = new Map(Array.from(list.querySelectorAll<HTMLElement>('[data-peer]')).map((row) => [row.dataset.peer!, row]));
  list.querySelector('.peer-empty')?.remove();
  const peers = status?.peers || [];
  for (const [index, peer] of peers.entries()) {
    let row = existing.get(peer.public_key);
    existing.delete(peer.public_key);
    if (!row) {
      row = document.createElement('div');
      row.className = 'peer-row';
      row.dataset.peer = peer.public_key;
      const state = document.createElement('span');
      const copy = document.createElement('div');
      copy.className = 'peer-copy';
      copy.append(document.createElement('strong'), document.createElement('code'), document.createElement('span'), document.createElement('span'));
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'button secondary';
      button.textContent = t('Copy public key');
      button.addEventListener('click', () => void copyText(peer.public_key).then(() => notify(t('Public key copied')), (error) => notify(String(error), 'error')));
      row.append(state, copy, button);
    }
    const authenticated = tunnel.statusState !== 'unknown' && status?.sessions?.some((session) => session.state === 'established' && session.peers?.some((association) => association.public_key === peer.public_key && association.authenticated));
    row.children[0]!.className = `peer-state ${authenticated ? 'ready' : ''}`;
    row.children[0]!.textContent = tunnel.statusState === 'unknown' ? t('Status unavailable') : authenticated ? t('Connected') : peer.session === 'established' ? t('Authenticating…') : peer.session === 'reconnecting' ? t('Reconnecting…') : peer.session === 'dialing' ? t('Connecting…') : t('Waiting');
    row.children[2]!.textContent = t('Copy public key');
    const copy = row.children[1]!;
    copy.children[0]!.textContent = peer.endpoint || t('Endpoint pending');
    copy.children[1]!.textContent = `${peer.public_key.slice(0, 16)}…`;
    copy.children[1]!.setAttribute('title', peer.public_key);
    const rate = observations.get(peer.public_key);
    copy.children[2]!.textContent = `↓ ${rate?.rx === undefined ? '—' : formatBitRate(rate.rx)} · ↑ ${rate?.tx === undefined ? '—' : formatBitRate(rate.tx)} · ${formatBytes(peer.transfer_rx)} / ${formatBytes(peer.transfer_tx)}`;
    copy.children[3]!.textContent = peer.session === 'reconnecting' && peer.next_reconnect
      ? t("Retry in {0} s · {1} attempts", Math.max(0, Math.ceil(peer.next_reconnect - Date.now() / 1000)), peer.reconnect_attempts || 0)
      : peer.latest_handshake ? t("Last handshake: {0}", new Date(peer.latest_handshake * 1000).toLocaleString()) : t('No WireGuard handshake yet');
    if (list.children[index] !== row) list.insertBefore(row, list.children[index] || null);
  }
  for (const row of existing.values()) row.remove();
  if (!peers.length) {
    const empty = document.createElement('div');
    empty.className = 'peer-empty';
    empty.textContent = status ? t('No peers reported by the running interface.') : t('Peer status appears when the tunnel is active.');
    list.replaceChildren(empty);
  }
  const history = document.getElementById('session-history-list')!;
  const reasons: Record<string, string> = {
    authentication_timeout: t('WireGuard authentication timed out. Check both public keys and the preshared key.'),
    idle_timeout: t('The peer stopped responding. Check connectivity, firewall rules and the peer service.'),
    remote_close: t('The remote peer closed the connection.'),
    local_shutdown: t('The local tunnel was stopped.'),
    endpoint_replaced: t('A replacement connection took over.'),
    configuration_removed: t('The peer was removed from the configuration.'),
    handshake_timeout: t('QUIC handshake timed out. Check the endpoint and UDP connectivity.'),
  };
  const sessions = [...status?.recent_sessions || []].slice(-8).reverse();
  history.replaceChildren(...sessions.map((session) => {
    const row = document.createElement('p');
    row.textContent = `${new Date(session.closed_at).toLocaleTimeString()} · ${session.current_endpoint || ''} · ${reasons[session.close_reason] || session.close_reason}`;
    if (session.last_error) row.title = session.last_error;
    return row;
  }));
  if (!sessions.length) history.textContent = t('No recent disconnections reported.');
}
