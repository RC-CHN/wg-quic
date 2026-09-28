//! Implements section 3 and section 9 of the standalone specification.
use anyhow::{Result, ensure};
use blake2::{
    Blake2b, Blake2bMac, Blake2s256, Blake2sMac, Digest,
    digest::{
        Mac,
        consts::{U16, U32},
    },
};
use chacha20poly1305::{
    ChaCha20Poly1305, KeyInit, XChaCha20Poly1305,
    aead::{Aead, Payload},
};
use hmac::SimpleHmac;
use rand::RngCore;
use x25519_dalek::{PublicKey, StaticSecret};

pub type Key = [u8; 32];
pub fn random<const N: usize>() -> [u8; N] {
    let mut x = [0; N];
    rand::rngs::OsRng.fill_bytes(&mut x);
    x
}
pub fn public(s: &Key) -> Key {
    PublicKey::from(&StaticSecret::from(*s)).to_bytes()
}
pub fn dh(s: &Key, p: &Key) -> Result<Key> {
    let x = StaticSecret::from(*s)
        .diffie_hellman(&PublicKey::from(*p))
        .to_bytes();
    ensure!(x != [0; 32], "zero DH");
    Ok(x)
}
pub fn hash(x: &[u8]) -> Key {
    Blake2s256::digest(x).into()
}
pub fn join(a: &[u8], b: &[u8]) -> Vec<u8> {
    [a, b].concat()
}
pub fn mac(k: &[u8], x: &[u8]) -> [u8; 16] {
    let mut h = <Blake2sMac<U16> as Mac>::new_from_slice(k).unwrap();
    h.update(x);
    h.finalize().into_bytes().into()
}
pub fn hm(k: &[u8], x: &[u8]) -> Key {
    let mut h = <SimpleHmac<Blake2s256> as Mac>::new_from_slice(k).unwrap();
    h.update(x);
    h.finalize().into_bytes().into()
}
pub fn kdf(c: &Key, x: &[u8], n: usize) -> Vec<Key> {
    let t = hm(c, x);
    let mut out = Vec::new();
    let mut previous = Vec::new();
    for i in 1..=n {
        previous.push(i as u8);
        let next = hm(&t, &previous);
        out.push(next);
        previous = next.to_vec();
    }
    out
}
pub fn seal(k: &Key, n: u64, p: &[u8], a: &[u8]) -> Vec<u8> {
    let mut nonce = [0; 12];
    nonce[4..].copy_from_slice(&n.to_le_bytes());
    ChaCha20Poly1305::new(k.into())
        .encrypt((&nonce).into(), Payload { msg: p, aad: a })
        .unwrap()
}
pub fn open(k: &Key, n: u64, c: &[u8], a: &[u8]) -> Result<Vec<u8>> {
    let mut nonce = [0; 12];
    nonce[4..].copy_from_slice(&n.to_le_bytes());
    ChaCha20Poly1305::new(k.into())
        .decrypt((&nonce).into(), Payload { msg: c, aad: a })
        .map_err(|_| anyhow::anyhow!("AEAD authentication"))
}
pub fn salamander_key(s: &Key, p: &Key, psk: Option<&Key>) -> Result<Key> {
    let mut data = b"wg-quic/salamander/key/v1".to_vec();
    data.extend(dh(s, p)?);
    match psk {
        Some(v) => {
            data.push(1);
            data.extend(v)
        }
        None => data.push(0),
    }
    Ok(Blake2b::<U32>::digest(&data).into())
}
fn bmac(k: &Key, x: &[u8]) -> Key {
    let mut h = <Blake2bMac<U32> as Mac>::new_from_slice(k).unwrap();
    h.update(x);
    h.finalize().into_bytes().into()
}
pub fn envelope(k: &Key, salt: &[u8; 8], p: &[u8]) -> Vec<u8> {
    let hint = bmac(k, &join(b"wg-quic/salamander/hint/v1", salt));
    let stream = bmac(k, &join(b"wg-quic/salamander/stream/v1", salt));
    let mut out = join(salt, &hint[..8]);
    out.extend(p.iter().enumerate().map(|(i, b)| b ^ stream[i % 32]));
    out
}
pub fn unwrap(k: &Key, p: &[u8]) -> Result<Vec<u8>> {
    ensure!(p.len() > 16, "short envelope");
    let hint = bmac(k, &join(b"wg-quic/salamander/hint/v1", &p[..8]));
    let mismatch = hint[..8]
        .iter()
        .zip(&p[8..16])
        .fold(0, |acc, (a, b)| acc | (a ^ b));
    ensure!(mismatch == 0, "hint mismatch");
    let stream = bmac(k, &join(b"wg-quic/salamander/stream/v1", &p[..8]));
    Ok(p[16..]
        .iter()
        .enumerate()
        .map(|(i, b)| b ^ stream[i % 32])
        .collect())
}

pub struct Noise {
    pub secret: Key,
    pub peer: Key,
    pub psk: Key,
    pub chain: Key,
    pub transcript: Key,
    pub ephemeral: Key,
    pub local: u32,
    pub remote: u32,
    pub send: Option<Key>,
    pub recv: Option<Key>,
    counter: u64,
    seen: std::collections::BTreeSet<u64>,
    latest: [u8; 12],
    last_mac: [u8; 16],
    cookie: Option<[u8; 16]>,
}
impl Noise {
    pub fn new(secret: Key, peer: Key, psk: Key) -> Self {
        Self {
            secret,
            peer,
            psk,
            chain: [0; 32],
            transcript: [0; 32],
            ephemeral: [0; 32],
            local: rand::random(),
            remote: 0,
            send: None,
            recv: None,
            counter: 0,
            seen: Default::default(),
            latest: [0; 12],
            last_mac: [0; 16],
            cookie: None,
        }
    }
    fn start(&mut self, responder: Key) {
        self.chain = hash(b"Noise_IKpsk2_25519_ChaChaPoly_BLAKE2s");
        self.transcript = hash(&join(
            &hash(&join(&self.chain, b"WireGuard v1 zx2c4 Jason@zx2c4.com")),
            &responder,
        ));
    }
    fn mix_hash(&mut self, x: &[u8]) {
        self.transcript = hash(&join(&self.transcript, x));
    }
    fn mix_key(&mut self, x: &[u8]) {
        self.chain = kdf(&self.chain, x, 1)[0];
    }
    fn key2(&mut self, x: &[u8]) -> Key {
        let keys = kdf(&self.chain, x, 2);
        self.chain = keys[0];
        keys[1]
    }
    fn finish(&mut self, initiator: bool) {
        let keys = kdf(&self.chain, &[], 2);
        self.send = Some(keys[if initiator { 0 } else { 1 }]);
        self.recv = Some(keys[if initiator { 1 } else { 0 }]);
        self.counter = 0;
        self.seen.clear();
    }
    fn add_macs(&mut self, mut p: Vec<u8>) -> Vec<u8> {
        self.last_mac = mac(&hash(&join(b"mac1----", &self.peer)), &p);
        p.extend(self.last_mac);
        p.extend(
            self.cookie
                .map(|cookie| mac(&cookie, &p))
                .unwrap_or([0; 16]),
        );
        p
    }
    fn check_mac(&self, p: &[u8]) -> Result<()> {
        ensure!(p.len() >= 32, "short handshake");
        let n = p.len() - 32;
        ensure!(
            mac(&hash(&join(b"mac1----", &public(&self.secret))), &p[..n]) == p[n..n + 16],
            "MAC1"
        );
        Ok(())
    }
    pub fn initiate(&mut self, e: Key, timestamp: [u8; 12]) -> Result<Vec<u8>> {
        self.start(self.peer);
        self.ephemeral = e;
        let ep = public(&e);
        self.mix_key(&ep);
        self.mix_hash(&ep);
        let key = self.key2(&dh(&e, &self.peer)?);
        let enc = seal(&key, 0, &public(&self.secret), &self.transcript);
        self.mix_hash(&enc);
        let key = self.key2(&dh(&self.secret, &self.peer)?);
        let time = seal(&key, 0, &timestamp, &self.transcript);
        self.mix_hash(&time);
        let p = [
            1u32.to_le_bytes().as_slice(),
            &self.local.to_le_bytes(),
            &ep,
            &enc,
            &time,
        ]
        .concat();
        Ok(self.add_macs(p))
    }
    pub fn respond(&mut self, p: &[u8], e: Key) -> Result<Vec<u8>> {
        ensure!(
            p.len() == 148 && p[..4] == 1u32.to_le_bytes(),
            "initiation layout"
        );
        self.check_mac(p)?;
        self.start(public(&self.secret));
        let remote_ep: Key = p[8..40].try_into()?;
        self.mix_key(&remote_ep);
        self.mix_hash(&remote_ep);
        let key = self.key2(&dh(&self.secret, &remote_ep)?);
        let static_peer = open(&key, 0, &p[40..88], &self.transcript)?;
        ensure!(static_peer == self.peer, "unknown static peer");
        self.mix_hash(&p[40..88]);
        let key = self.key2(&dh(&self.secret, &self.peer)?);
        let timestamp = open(&key, 0, &p[88..116], &self.transcript)?;
        ensure!(
            timestamp.as_slice() > self.latest.as_slice(),
            "timestamp replay"
        );
        self.latest.copy_from_slice(&timestamp);
        self.mix_hash(&p[88..116]);
        self.remote = u32::from_le_bytes(p[4..8].try_into()?);
        self.ephemeral = e;
        let ep = public(&e);
        self.mix_hash(&ep);
        self.mix_key(&ep);
        self.mix_key(&dh(&e, &remote_ep)?);
        self.mix_key(&dh(&e, &self.peer)?);
        let keys = kdf(&self.chain, &self.psk, 3);
        self.chain = keys[0];
        self.mix_hash(&keys[1]);
        let empty = seal(&keys[2], 0, &[], &self.transcript);
        self.mix_hash(&empty);
        self.finish(false);
        let out = [
            2u32.to_le_bytes().as_slice(),
            &self.local.to_le_bytes(),
            &self.remote.to_le_bytes(),
            &ep,
            &empty,
        ]
        .concat();
        Ok(self.add_macs(out))
    }
    pub fn consume_response(&mut self, p: &[u8]) -> Result<()> {
        ensure!(
            p.len() == 92 && p[..4] == 2u32.to_le_bytes() && p[8..12] == self.local.to_le_bytes(),
            "response layout/index"
        );
        self.check_mac(p)?;
        let ep: Key = p[12..44].try_into()?;
        self.mix_hash(&ep);
        self.mix_key(&ep);
        self.mix_key(&dh(&self.ephemeral, &ep)?);
        self.mix_key(&dh(&self.secret, &ep)?);
        let keys = kdf(&self.chain, &self.psk, 3);
        self.chain = keys[0];
        self.mix_hash(&keys[1]);
        ensure!(
            open(&keys[2], 0, &p[44..60], &self.transcript)?.is_empty(),
            "response payload"
        );
        self.mix_hash(&p[44..60]);
        self.remote = u32::from_le_bytes(p[4..8].try_into()?);
        self.finish(true);
        Ok(())
    }
    pub fn cookie(&mut self, p: &[u8]) -> Result<()> {
        ensure!(
            p.len() == 64 && p[4..8] == self.local.to_le_bytes(),
            "cookie index"
        );
        let key = hash(&join(b"cookie--", &self.peer));
        let cookie = XChaCha20Poly1305::new((&key).into())
            .decrypt(
                p[8..32].into(),
                Payload {
                    msg: &p[32..],
                    aad: &self.last_mac,
                },
            )
            .map_err(|_| anyhow::anyhow!("cookie AEAD"))?;
        self.cookie = Some(cookie.as_slice().try_into()?);
        Ok(())
    }
    pub fn transport(&mut self, p: &[u8]) -> Result<Vec<u8>> {
        let key = self.send.ok_or_else(|| anyhow::anyhow!("no send key"))?;
        ensure!(self.counter < ((u64::MAX) - (1 << 13)), "counter exhausted");
        let mut padded = p.to_vec();
        padded.resize(p.len().div_ceil(16) * 16, 0);
        let out = [
            4u32.to_le_bytes().as_slice(),
            &self.remote.to_le_bytes(),
            &self.counter.to_le_bytes(),
            &seal(&key, self.counter, &padded, &[]),
        ]
        .concat();
        self.counter += 1;
        Ok(out)
    }
    pub fn decrypt(&mut self, p: &[u8]) -> Result<Vec<u8>> {
        ensure!(
            p.len() >= 32 && p[..4] == 4u32.to_le_bytes() && p[4..8] == self.local.to_le_bytes(),
            "transport layout/index"
        );
        let counter = u64::from_le_bytes(p[8..16].try_into()?);
        ensure!(counter < u64::MAX - (1 << 13), "counter exhausted");
        ensure!(!self.seen.contains(&counter), "replay");
        if let Some(last) = self.seen.last() {
            ensure!(counter.saturating_add(8192) >= *last, "old counter")
        };
        let key = self.recv.ok_or_else(|| anyhow::anyhow!("no receive key"))?;
        let plain = open(&key, counter, &p[16..], &[])?;
        self.seen.insert(counter);
        let last = *self.seen.last().unwrap();
        self.seen.retain(|x| x.saturating_add(8192) >= last);
        Ok(plain)
    }
}
