// A form projection of one peer and the interface. Existing documents retain
// their original text; saving patches only edited fields, including repeated
// list fields, without losing comments, other peers, hooks or future settings.

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
  sourceText?: string;
  peerIndex?: number;
  peerCount?: number;
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

type DraftField = Exclude<keyof TunnelDraft, 'sourceText' | 'peerIndex' | 'peerCount' | 'name'>;

const interfaceFields: Record<string, DraftField> = {
  privatekey: 'privateKey', address: 'addresses', listenport: 'listenPort',
  dns: 'dns', mtu: 'mtu',
};
const peerFields: Record<string, DraftField> = {
  publickey: 'peerPublicKey', presharedkey: 'presharedKey', allowedips: 'allowedIPs',
  endpoint: 'endpoint', persistentkeepalive: 'keepalive',
};
const transportFields: Record<string, DraftField> = {
  carrier: 'carrier', congestion: 'congestion', fec: 'fec', obfs: 'obfs',
};
const fieldNames: Partial<Record<DraftField, string>> = {
  privateKey: 'PrivateKey', addresses: 'Address', listenPort: 'ListenPort',
  dns: 'DNS', mtu: 'MTU', peerPublicKey: 'PublicKey', presharedKey: 'PresharedKey',
  allowedIPs: 'AllowedIPs', endpoint: 'Endpoint', keepalive: 'PersistentKeepalive',
};

function documentLines(text: string, peerIndex: number) {
  let section = '';
  let peer = -1;
  return (text.match(/[^\n]*\n|[^\n]+$/g) || []).map((raw) => {
    const line = raw.trim();
    if (/^\[.*\]$/.test(line)) {
      section = line.slice(1, -1).trim().toLowerCase();
      if (section === 'peer') peer++;
      return { raw, section, peer, header: true, field: undefined, value: '' };
    }
    const directive = line.startsWith('# wg-quic:');
    const setting = directive ? line.slice('# wg-quic:'.length).trim() : line;
    const eq = setting.indexOf('=');
    const key = setting.slice(0, eq).trim();
    const value = setting.slice(eq + 1).trim();
    const field = eq < 0 || (!directive && /^[#;]/.test(line)) ? undefined
      : directive ? transportFields[key]
      : section === 'interface' ? interfaceFields[key.toLowerCase()]
      : section === 'peer' && peer === peerIndex ? peerFields[key.toLowerCase()]
      : undefined;
    return { raw, section, peer, header: false, field, value };
  });
}

export function parseConf(text: string, peerIndex = 0): TunnelDraft {
  const draft = emptyTunnelDraft('');
  draft.allowedIPs = '';
  draft.keepalive = '';
  const lines = documentLines(text, peerIndex);
  for (const {field, value} of lines) {
    if (!field) continue;
    draft[field] = ['addresses', 'dns', 'allowedIPs'].includes(field)
      ? appendListField(draft[field], value) : value;
  }
  draft.sourceText = text;
  draft.peerIndex = peerIndex;
  draft.peerCount = lines.filter((line) => line.header && line.section === 'peer').length;
  return draft;
}

function patchConf(draft: TunnelDraft): string {
  const source = draft.sourceText!;
  const peerIndex = draft.peerIndex ?? 0;
  const original = parseConf(source, peerIndex);
  const fields = [...Object.values(interfaceFields), ...Object.values(peerFields), ...Object.values(transportFields)];
  const changed = new Set(fields.filter((field) => draft[field].trim() !== original[field].trim()));
  if (changed.size === 0) return source;
  const newline = source.includes('\r\n') ? '\r\n' : '\n';
  const lines = documentLines(source, peerIndex);
  const present = new Set(lines.flatMap((line) => line.field ? [line.field] : []));
  const written = new Set<DraftField>();
  const setting = (field: DraftField): string => {
    written.add(field);
    const value = draft[field].trim();
    if (!value) return '';
    return `${fieldNames[field] ? `${fieldNames[field]} = ` : `# wg-quic: ${field}=`}${value}${newline}`;
  };
  let output = Object.values(transportFields)
    .filter((field) => changed.has(field) && !present.has(field)).map(setting).join('');
  for (const line of lines) {
    if (line.field && changed.has(line.field)) {
      if (!written.has(line.field)) output += setting(line.field);
      continue;
    }
    output += line.raw;
    if (line.header) {
      const sectionFields = line.section === 'interface' ? interfaceFields
        : line.section === 'peer' && line.peer === peerIndex ? peerFields : {};
      const added = Object.values(sectionFields)
        .filter((field) => changed.has(field) && !present.has(field) && !written.has(field));
      if (added.length && !output.endsWith('\n')) output += newline;
      output += added.map(setting).join('');
    }
  }
  // A profile without peers remains byte-for-byte unchanged until a user
  // explicitly fills in a peer (or adds one in the source editor).
  const addedPeer = Object.values(peerFields).filter((field) => changed.has(field) && !written.has(field));
  if (addedPeer.length) {
    if (!output.endsWith('\n')) output += newline;
    output += `[Peer]${newline}${addedPeer.map(setting).join('')}`;
  }
  return output;
}

// buildConf renders the draft back into wg-quic configuration text. Transport
// directives only emit when they differ from the defaults so generated files
// stay minimal and match what Check/Validate accept.
export function buildConf(draft: TunnelDraft): string {
  if (draft.sourceText !== undefined) return patchConf(draft);
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
