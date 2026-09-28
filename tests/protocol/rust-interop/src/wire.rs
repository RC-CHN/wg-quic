//! Language-independent framing and GF(256) arithmetic from sections 4 and 5.
use anyhow::{Result, ensure};
use std::{
    collections::{BTreeMap, HashMap, HashSet},
    time::{Duration, Instant},
};
type Decoded = (Vec<Vec<u8>>, Vec<Vec<u8>>);

fn be16(p: &[u8]) -> usize {
    u16::from_be_bytes(p[..2].try_into().unwrap()) as usize
}
fn be64(p: &[u8]) -> u64 {
    u64::from_be_bytes(p[..8].try_into().unwrap())
}
pub fn frame(id: u64, index: u16, count: u16, total: u32, data: &[u8]) -> Vec<u8> {
    [
        b"WGQ1".as_slice(),
        &[1],
        &id.to_be_bytes(),
        &index.to_be_bytes(),
        &count.to_be_bytes(),
        &total.to_be_bytes(),
        data,
    ]
    .concat()
}
pub fn mul(mut a: u16, mut b: u16) -> u8 {
    let mut z = 0;
    while b != 0 {
        if b & 1 != 0 {
            z ^= a
        }
        a <<= 1;
        if a & 256 != 0 {
            a ^= 0x11d
        }
        b >>= 1;
    }
    z as u8
}
fn pow(a: u8, n: usize) -> u8 {
    (0..n).fold(1, |v, _| mul(v as u16, a as u16))
}
fn invert(m: &[Vec<u8>]) -> Result<Vec<Vec<u8>>> {
    let n = m.len();
    let mut a: Vec<Vec<u8>> = m
        .iter()
        .enumerate()
        .map(|(i, row)| [row.clone(), (0..n).map(|j| u8::from(i == j)).collect()].concat())
        .collect();
    for i in 0..n {
        let pivot = (i..n)
            .find(|&j| a[j][i] != 0)
            .ok_or_else(|| anyhow::anyhow!("singular matrix"))?;
        a.swap(i, pivot);
        let scale = pow(a[i][i], 254);
        for x in &mut a[i] {
            *x = mul(*x as u16, scale as u16)
        }
        let pivot_row = a[i].clone();
        for (j, row) in a.iter_mut().enumerate() {
            if j != i {
                let scale = row[i];
                for (cell, pivot) in row.iter_mut().zip(&pivot_row) {
                    *cell ^= mul(scale as u16, *pivot as u16);
                }
            }
        }
    }
    Ok(a.into_iter().map(|row| row[n..].to_vec()).collect())
}
pub fn matrix(k: usize, r: usize) -> Result<Vec<Vec<u8>>> {
    ensure!((1..=32).contains(&k) && r <= 8, "RS dimensions");
    let v: Vec<Vec<u8>> = (0..k + r)
        .map(|row| (0..k).map(|col| pow(row as u8, col)).collect())
        .collect();
    let inv = invert(&v[..k])?;
    Ok(v.iter()
        .map(|row| {
            (0..k)
                .map(|col| (0..k).fold(0, |x, j| x ^ mul(row[j] as u16, inv[j][col] as u16)))
                .collect()
        })
        .collect())
}
struct Fragments {
    born: Instant,
    count: usize,
    total: usize,
    parts: BTreeMap<usize, Vec<u8>>,
}
struct Group {
    born: Instant,
    epoch: u16,
    k: usize,
    r: usize,
    closed: bool,
    data: BTreeMap<usize, Vec<u8>>,
    parity: BTreeMap<usize, Vec<u8>>,
    delivered: HashSet<usize>,
}
#[derive(Default)]
pub struct Decoder {
    fragments: HashMap<u64, Fragments>,
    groups: HashMap<u64, Group>,
    completed: HashSet<u64>,
    pub data_count: u64,
    pub parity_count: u64,
    pub recovered: u64,
    pub drop_first_data: bool,
}
fn feedback(epoch: u16, id: u64, missing: usize, k: usize, recovered: usize) -> Vec<u8> {
    [
        b"WGQF".as_slice(),
        &[1, 3],
        &epoch.to_be_bytes(),
        &id.to_be_bytes(),
        &(missing as u16).to_be_bytes(),
        &(k as u16).to_be_bytes(),
        &(recovered as u16).to_be_bytes(),
        &[0, 0],
    ]
    .concat()
}
fn source(s: &[u8]) -> Result<Vec<u8>> {
    ensure!(s.len() >= 2, "short source");
    let n = be16(s);
    ensure!(
        (22..=4096).contains(&n) && n <= s.len() - 2,
        "source length"
    );
    Ok(s[2..2 + n].to_vec())
}
impl Decoder {
    pub fn expire(&mut self) -> Vec<Vec<u8>> {
        self.fragments
            .retain(|_, g| g.born.elapsed() < Duration::from_secs(3));
        let mut replies = Vec::new();
        self.groups.retain(|id, g| {
            if g.born.elapsed() < Duration::from_secs(3) {
                return true;
            }
            if g.k > 0 {
                let received = (0..g.k).filter(|i| g.data.contains_key(i)).count();
                replies.push(feedback(g.epoch, *id, g.k - received, g.k, 0))
            }
            false
        });
        replies
    }
    pub fn feed(&mut self, p: &[u8]) -> Result<Decoded> {
        let mut replies = self.expire();
        let frames = if p.starts_with(b"WGQF") {
            let (frames, reply) = self.fec(p)?;
            replies.extend(reply);
            frames
        } else {
            vec![p.to_vec()]
        };
        let mut packets = Vec::new();
        for frame in frames {
            if let Some(packet) = self.fragment(&frame)? {
                packets.push(packet)
            }
        }
        Ok((packets, replies))
    }
    fn fragment(&mut self, p: &[u8]) -> Result<Option<Vec<u8>>> {
        ensure!(
            p.len() > 21 && p.len() <= 4096 && p[..5] == *b"WGQ1\x01",
            "frame header"
        );
        let id = be64(&p[5..]);
        let index = be16(&p[13..]);
        let count = be16(&p[15..]);
        let total = u32::from_be_bytes(p[17..21].try_into()?) as usize;
        ensure!(
            (1..=128).contains(&count) && index < count && (1..=65535).contains(&total),
            "frame dimensions"
        );
        if count == 1 {
            ensure!(p.len() - 21 == total, "frame total");
            return Ok(Some(p[21..].to_vec()));
        }
        ensure!(
            self.fragments.len() < 2048 || self.fragments.contains_key(&id),
            "fragment capacity"
        );
        let g = self.fragments.entry(id).or_insert_with(|| Fragments {
            born: Instant::now(),
            count,
            total,
            parts: BTreeMap::new(),
        });
        ensure!(g.count == count && g.total == total, "conflicting frame");
        g.parts.entry(index).or_insert_with(|| p[21..].to_vec());
        if g.parts.len() != count {
            return Ok(None);
        }
        let g = self.fragments.remove(&id).unwrap();
        let out: Vec<u8> = g.parts.into_values().flatten().collect();
        ensure!(out.len() == total, "reassembled total");
        Ok(Some(out))
    }
    fn fec(&mut self, p: &[u8]) -> Result<Decoded> {
        ensure!(p.len() >= 24 && p[4] == 1 && p[5] <= 3, "FEC header");
        let kind = p[5];
        let epoch = be16(&p[6..]) as u16;
        let id = be64(&p[8..]);
        let index = be16(&p[16..]);
        let k = be16(&p[18..]);
        let r = be16(&p[20..]);
        let len = be16(&p[22..]);
        ensure!(len == p.len() - 24 && len <= 4098, "FEC length");
        if kind == 3 {
            ensure!(
                len == 0
                    && ((k == 0 && index <= 1 && r == 0)
                        || (k > 0 && k <= 32 && r <= index && index <= k)),
                "feedback counters"
            );
            return Ok((vec![], vec![]));
        }
        if kind == 0 {
            self.data_count += 1;
            if self.drop_first_data {
                self.drop_first_data = false;
                return Ok((vec![], vec![]));
            }
        } else if kind == 1 {
            self.parity_count += 1
        }
        if self.completed.contains(&id) {
            return Ok((vec![], vec![]));
        }
        ensure!(
            self.groups.len() < 1024 || self.groups.contains_key(&id),
            "FEC capacity"
        );
        let g = self.groups.entry(id).or_insert_with(|| Group {
            born: Instant::now(),
            epoch,
            k: 0,
            r: 0,
            closed: false,
            data: BTreeMap::new(),
            parity: BTreeMap::new(),
            delivered: HashSet::new(),
        });
        ensure!(epoch == g.epoch, "group epoch");
        let mut frames = Vec::new();
        if kind == 0 {
            ensure!(index < 32 && k == 0 && r == 0, "data index");
            let shard = g.data.entry(index).or_insert_with(|| p[24..].to_vec());
            if g.delivered.insert(index) {
                frames.push(source(shard)?)
            }
        } else {
            ensure!(
                (1..=32).contains(&k) && r <= 8 && (g.k == 0 || (g.k == k && g.r == r)),
                "group dimensions"
            );
            g.k = k;
            g.r = r;
            if kind == 1 {
                ensure!(index < r && len > 0, "parity index");
                g.parity.entry(index).or_insert_with(|| p[24..].to_vec());
            } else {
                ensure!(len == 0 && index == 0, "close");
                g.closed = true
            }
        }
        if g.k == 0 {
            return Ok((frames, vec![]));
        }
        let k = g.k;
        let missing = (0..k).filter(|i| !g.data.contains_key(i)).count();
        let available = k - missing + g.parity.len();
        let mut recovered = 0;
        if missing > 0 && available >= k {
            let matrix = matrix(k, g.r)?;
            let selected: Vec<(usize, &Vec<u8>)> = g
                .data
                .iter()
                .filter(|(i, _)| **i < k)
                .map(|(i, v)| (*i, v))
                .chain(g.parity.iter().map(|(i, v)| (k + i, v)))
                .take(k)
                .collect();
            let size = selected.iter().map(|(_, v)| v.len()).max().unwrap();
            let inv = invert(
                &selected
                    .iter()
                    .map(|(i, _)| matrix[*i].clone())
                    .collect::<Vec<_>>(),
            )?;
            for (i, row) in inv.iter().enumerate() {
                if g.data.contains_key(&i) {
                    continue;
                }
                let data: Vec<u8> = (0..size)
                    .map(|b| {
                        selected.iter().enumerate().fold(0, |v, (j, (_, s))| {
                            v ^ mul(row[j] as u16, *s.get(b).unwrap_or(&0) as u16)
                        })
                    })
                    .collect();
                if g.delivered.insert(i) {
                    frames.push(source(&data)?);
                    recovered += 1;
                }
            }
        }
        if recovered == missing && (missing > 0 || g.closed || g.r == 0) {
            let reply = feedback(g.epoch, id, missing, k, recovered);
            self.recovered += recovered as u64;
            self.groups.remove(&id);
            if self.completed.len() >= 4096 {
                self.completed.clear()
            }
            self.completed.insert(id);
            Ok((frames, vec![reply]))
        } else {
            Ok((frames, vec![]))
        }
    }
}
