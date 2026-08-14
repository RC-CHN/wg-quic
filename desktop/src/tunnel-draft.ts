// TunnelDraft is the editable form state for one tunnel. The desktop UI keeps
// exactly one peer (the common case) while the parser preserves a single-peer
// projection of an existing config; multi-peer editing is intentionally left
// as a future extension point rather than designed out.

export interface TunnelDraft {
  name: string;
  privateKey: string;
  addresses: string; // comma-separated CIDRs
  listenPort: string; // empty = auto
  dns: string; // comma-separated
  mtu: string; // empty = default
  peerPublicKey: string;
  presharedKey: string;
  allowedIPs: string; // comma-separated CIDRs
  endpoint: string; // host:port, empty allowed
  keepalive: string; // seconds, empty/0 = off
  carrier: string;
  congestion: string;
  fec: string;
  obfs: string;
}

export function emptyTunnelDraft(name: string): TunnelDraft {
  return {
    name,
    privateKey: '',
    addresses: '',
    listenPort: '',
    dns: '',
    mtu: '',
    peerPublicKey: '',
    presharedKey: '',
    allowedIPs: '0.0.0.0/0, ::/0',
    endpoint: '',
    keepalive: '25',
    carrier: 'quic',
    congestion: 'auto',
    fec: 'auto',
    obfs: 'salamander',
  };
}

function appendListField(current: string, value: string): string {
  if (!current) {
    return value;
  }
  return `${current}, ${value}`;
}

// parseConf projects an installed configuration onto the editable draft. It
// mirrors internal/config parsing: `# wg-quic:` directives carry transport
// settings, Interface/Peer sections carry the rest. Only the first peer is
// projected (single-peer MVP).
export function parseConf(text: string): TunnelDraft {
  const draft = emptyTunnelDraft('');
  draft.allowedIPs = '';
  draft.keepalive = '';
  let section = '';
  let sawPeer = false;
  for (const rawLine of text.split('\n')) {
    const line = rawLine.trim();
    if (!line) {
      continue;
    }
    if (line.startsWith('# wg-quic:')) {
      const directive = line.slice('# wg-quic:'.length).trim();
      const eq = directive.indexOf('=');
      if (eq < 0) {
        continue;
      }
      const key = directive.slice(0, eq).trim();
      const value = directive.slice(eq + 1).trim();
      if (section !== 'peer') {
        switch (key) {
          case 'carrier':
            draft.carrier = value;
            break;
          case 'congestion':
            draft.congestion = value;
            break;
          case 'fec':
            draft.fec = value;
            break;
          case 'obfs':
            draft.obfs = value;
            break;
        }
      }
      continue;
    }
    if (line.startsWith('#') || line.startsWith(';')) {
      continue;
    }
    if (line.startsWith('[') && line.endsWith(']')) {
      section = line.slice(1, -1).trim().toLowerCase();
      if (section === 'peer') {
        sawPeer = true;
      }
      continue;
    }
    const eq = line.indexOf('=');
    if (eq < 0) {
      continue;
    }
    const key = line.slice(0, eq).trim().toLowerCase();
    const value = line.slice(eq + 1).trim();
    if (section === 'interface') {
      switch (key) {
        case 'privatekey':
          draft.privateKey = value;
          break;
        case 'address':
          draft.addresses = appendListField(draft.addresses, value);
          break;
        case 'listenport':
          draft.listenPort = value;
          break;
        case 'dns':
          draft.dns = appendListField(draft.dns, value);
          break;
        case 'mtu':
          draft.mtu = value;
          break;
      }
    } else if (section === 'peer' && sawPeer) {
      // Only project the first peer; later peers are out of MVP scope.
      switch (key) {
        case 'publickey':
          if (!draft.peerPublicKey) {
            draft.peerPublicKey = value;
          }
          break;
        case 'presharedkey':
          if (!draft.presharedKey) {
            draft.presharedKey = value;
          }
          break;
        case 'allowedips':
          draft.allowedIPs = appendListField(draft.allowedIPs, value);
          break;
        case 'endpoint':
          if (!draft.endpoint) {
            draft.endpoint = value;
          }
          break;
        case 'persistentkeepalive':
          if (!draft.keepalive) {
            draft.keepalive = value;
          }
          break;
      }
    }
  }
  return draft;
}

// buildConf renders the draft back into wg-quic configuration text. Transport
// directives only emit when they differ from the defaults so generated files
// stay minimal and match what Check/Validate accept.
export function buildConf(draft: TunnelDraft): string {
  const lines: string[] = [];
  const carrier = draft.carrier || 'quic';
  lines.push(`# wg-quic: carrier=${carrier}`);
  if (draft.congestion && draft.congestion !== 'auto') {
    lines.push(`# wg-quic: congestion=${draft.congestion}`);
  }
  if (draft.fec && draft.fec !== 'auto') {
    lines.push(`# wg-quic: fec=${draft.fec}`);
  }
  if (draft.obfs && draft.obfs !== 'salamander') {
    lines.push(`# wg-quic: obfs=${draft.obfs}`);
  }
  lines.push('');
  lines.push('[Interface]');
  lines.push(`PrivateKey = ${draft.privateKey.trim()}`);
  lines.push(`Address = ${draft.addresses.trim()}`);
  if (draft.listenPort.trim()) {
    lines.push(`ListenPort = ${draft.listenPort.trim()}`);
  }
  if (draft.dns.trim()) {
    lines.push(`DNS = ${draft.dns.trim()}`);
  }
  if (draft.mtu.trim()) {
    lines.push(`MTU = ${draft.mtu.trim()}`);
  }
  lines.push('');
  lines.push('[Peer]');
  lines.push(`PublicKey = ${draft.peerPublicKey.trim()}`);
  if (draft.presharedKey.trim()) {
    lines.push(`PresharedKey = ${draft.presharedKey.trim()}`);
  }
  lines.push(`AllowedIPs = ${draft.allowedIPs.trim()}`);
  if (draft.endpoint.trim()) {
    lines.push(`Endpoint = ${draft.endpoint.trim()}`);
  }
  const keepalive = draft.keepalive.trim();
  if (keepalive && keepalive !== '0' && keepalive.toLowerCase() !== 'off') {
    lines.push(`PersistentKeepalive = ${keepalive}`);
  }
  return `${lines.join('\n')}\n`;
}

const KEY_PATTERN = /^[A-Za-z0-9+/]{43}=$/;

function isValidPrefixList(value: string): boolean {
  return value
    .split(',')
    .map((part) => part.trim())
    .filter((part) => part.length > 0)
    .every((part) => /^\d{1,3}(\.\d{1,3}){3}\/\d{1,2}$/.test(part) || /^[0-9a-fA-F:]+\/\d{1,3}$/.test(part));
}

// validateTunnelDraft runs the cheap client-side checks before handing the
// rendered config to the backend's authoritative Check.
export function validateTunnelDraft(draft: TunnelDraft, isNew: boolean): string[] {
  const errors: string[] = [];
  if (isNew && !draft.name.trim()) {
    errors.push('Tunnel name is required.');
  }
  if (!KEY_PATTERN.test(draft.privateKey.trim())) {
    errors.push('Private key must be a base64 WireGuard key.');
  }
  if (!draft.addresses.trim() || !isValidPrefixList(draft.addresses)) {
    errors.push('Tunnel address must be a CIDR like 10.0.0.2/32.');
  }
  if (draft.listenPort.trim()) {
    const port = Number(draft.listenPort.trim());
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      errors.push('Listen port must be between 1 and 65535.');
    }
  }
  if (draft.mtu.trim()) {
    const mtu = Number(draft.mtu.trim());
    if (!Number.isInteger(mtu) || mtu < 576 || mtu > 65535) {
      errors.push('MTU must be between 576 and 65535.');
    }
  }
  if (!KEY_PATTERN.test(draft.peerPublicKey.trim())) {
    errors.push('Peer public key must be a base64 WireGuard key.');
  }
  if (draft.presharedKey.trim() && !KEY_PATTERN.test(draft.presharedKey.trim())) {
    errors.push('Preshared key must be a base64 WireGuard key.');
  }
  if (!draft.allowedIPs.trim() || !isValidPrefixList(draft.allowedIPs)) {
    errors.push('Allowed IPs must be CIDRs like 0.0.0.0/0.');
  }
  const keepalive = draft.keepalive.trim();
  if (keepalive && keepalive.toLowerCase() !== 'off') {
    const seconds = Number(keepalive);
    if (!Number.isInteger(seconds) || seconds < 0 || seconds > 65535) {
      errors.push('Keepalive must be 0-65535 seconds or off.');
    }
  }
  return errors;
}
