import assert from 'node:assert/strict';
import test from 'node:test';
import {
  buildConf,
  emptyTunnelDraft,
  parseConf,
  validateTunnelDraft,
} from './tunnel-draft';

const PRIVATE = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=';
const PEER = 'BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=';
const PRESHARED = 'CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC=';

test('buildConf renders a minimal valid configuration', () => {
  const draft = emptyTunnelDraft('office');
  draft.privateKey = PRIVATE;
  draft.addresses = '10.0.0.2/32';
  draft.peerPublicKey = PEER;
  draft.endpoint = 'vpn.example.com:51820';
  const conf = buildConf(draft);
  assert.match(conf, /# wg-quic: carrier=quic/);
  assert.match(conf, /\[Interface\]/);
  assert.match(conf, /PrivateKey = AAAA/);
  assert.match(conf, /Address = 10\.0\.0\.2\/32/);
  assert.match(conf, /\[Peer\]/);
  assert.match(conf, /PublicKey = BBBB/);
  assert.match(conf, /AllowedIPs = 0\.0\.0\.0\/0, ::\/0/);
  assert.match(conf, /Endpoint = vpn\.example\.com:51820/);
  assert.match(conf, /PersistentKeepalive = 25/);
  // defaults stay omitted so generated files stay minimal
  assert.doesNotMatch(conf, /congestion=/);
  assert.doesNotMatch(conf, /obfs=/);
});

test('parseConf round-trips a built configuration', () => {
  const draft = emptyTunnelDraft('office');
  draft.privateKey = PRIVATE;
  draft.addresses = '10.0.0.2/32';
  draft.listenPort = '51820';
  draft.dns = '1.1.1.1';
  draft.mtu = '1280';
  draft.peerPublicKey = PEER;
  draft.presharedKey = PRESHARED;
  draft.allowedIPs = '10.0.0.0/24';
  draft.endpoint = 'vpn.example.com:51820';
  draft.keepalive = '25';
  draft.congestion = 'cubic';
  draft.fec = 'off';
  draft.obfs = 'none';
  const parsed = parseConf(buildConf(draft));
  assert.equal(parsed.privateKey, draft.privateKey);
  assert.equal(parsed.addresses, draft.addresses);
  assert.equal(parsed.listenPort, draft.listenPort);
  assert.equal(parsed.dns, draft.dns);
  assert.equal(parsed.mtu, draft.mtu);
  assert.equal(parsed.peerPublicKey, draft.peerPublicKey);
  assert.equal(parsed.presharedKey, draft.presharedKey);
  assert.equal(parsed.allowedIPs, draft.allowedIPs);
  assert.equal(parsed.endpoint, draft.endpoint);
  assert.equal(parsed.keepalive, draft.keepalive);
  assert.equal(parsed.congestion, draft.congestion);
  assert.equal(parsed.fec, draft.fec);
  assert.equal(parsed.obfs, draft.obfs);
});

test('parseConf reads an existing WireGuard-style configuration', () => {
  const conf = [
    '[Interface]',
    `PrivateKey = ${PRIVATE}`,
    'Address = 10.0.0.2/32',
    'ListenPort = 51820',
    '',
    '[Peer]',
    `PublicKey = ${PEER}`,
    'AllowedIPs = 0.0.0.0/0',
    'Endpoint = vpn.example.com:51820',
    'PersistentKeepalive = 25',
    '',
  ].join('\n');
  const draft = parseConf(conf);
  assert.equal(draft.privateKey, PRIVATE);
  assert.equal(draft.addresses, '10.0.0.2/32');
  assert.equal(draft.listenPort, '51820');
  assert.equal(draft.peerPublicKey, PEER);
  assert.equal(draft.allowedIPs, '0.0.0.0/0');
  assert.equal(draft.endpoint, 'vpn.example.com:51820');
  assert.equal(draft.keepalive, '25');
  // transport defaults when no directives present
  assert.equal(draft.carrier, 'quic');
  assert.equal(draft.congestion, 'auto');
  assert.equal(draft.fec, 'auto');
  assert.equal(draft.obfs, 'salamander');
});

test('parseConf accumulates repeated Address lines into a list', () => {
  const conf = [
    '[Interface]',
    `PrivateKey = ${PRIVATE}`,
    'Address = 10.0.0.2/32',
    'Address = fd00::2/128',
    '',
    '[Peer]',
    `PublicKey = ${PEER}`,
    'AllowedIPs = 0.0.0.0/0',
    '',
  ].join('\n');
  const draft = parseConf(conf);
  assert.equal(draft.addresses, '10.0.0.2/32, fd00::2/128');
});

test('validateTunnelDraft flags missing required fields', () => {
  const errors = validateTunnelDraft(emptyTunnelDraft(''), true);
  assert.ok(errors.some((e) => /name/i.test(e)));
  assert.ok(errors.some((e) => /private key/i.test(e)));
  assert.ok(errors.some((e) => /tunnel address/i.test(e)));
  assert.ok(errors.some((e) => /peer public key/i.test(e)));
});

test('validateTunnelDraft accepts a complete draft', () => {
  const draft = emptyTunnelDraft('office');
  draft.privateKey = PRIVATE;
  draft.addresses = '10.0.0.2/32';
  draft.peerPublicKey = PEER;
  draft.allowedIPs = '0.0.0.0/0';
  assert.deepEqual(validateTunnelDraft(draft, true), []);
});

test('validateTunnelDraft rejects an out-of-range port', () => {
  const draft = emptyTunnelDraft('office');
  draft.privateKey = PRIVATE;
  draft.addresses = '10.0.0.2/32';
  draft.peerPublicKey = PEER;
  draft.listenPort = '70000';
  assert.ok(validateTunnelDraft(draft, false).some((e) => /listen port/i.test(e)));
});
