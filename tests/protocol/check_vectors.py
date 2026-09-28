#!/usr/bin/env python3
"""Independent arithmetic/crypto oracle for the JSON embedded in the wire spec.
Requires Python 3 and cryptography; imports no project implementation.
"""
import hashlib
import hmac
import json
from pathlib import Path
import re
import struct
from cryptography.hazmat.primitives.asymmetric.x25519 import X25519PrivateKey, X25519PublicKey
from cryptography.hazmat.primitives.ciphers.aead import ChaCha20Poly1305


def public(secret):
    return X25519PrivateKey.from_private_bytes(secret).public_key().public_bytes_raw()


def dh(secret, peer):
    return X25519PrivateKey.from_private_bytes(secret).exchange(X25519PublicKey.from_public_bytes(peer))


def h(data):
    return hashlib.blake2s(data).digest()


def mac(key, data):
    return hashlib.blake2s(data, key=key, digest_size=16).digest()


def kdf(chain, data, count=1):
    temp = hmac.digest(chain, data, 'blake2s')
    outputs = []
    previous = b''
    for i in range(1, count + 1):
        previous = hmac.digest(temp, previous + bytes([i]), 'blake2s')
        outputs.append(previous)
    return outputs


def aead(key, counter, data, aad=b''):
    return ChaCha20Poly1305(key).encrypt(b'\0'*4 + struct.pack('<Q', counter), data, aad)


def mul(a, b):
    result = 0
    while b:
        if b & 1:
            result ^= a
        a <<= 1
        if a & 256:
            a ^= 0x11d
        b >>= 1
    return result


def power(a, n):
    result = 1
    for _ in range(n):
        result = mul(result, a)
    return result


def inverse(matrix):
    n = len(matrix)
    rows = [row[:] + [int(i == j) for j in range(n)] for i, row in enumerate(matrix)]
    for i in range(n):
        pivot = next(j for j in range(i, n) if rows[j][i])
        rows[i], rows[pivot] = rows[pivot], rows[i]
        scale = power(rows[i][i], 254)
        rows[i] = [mul(x, scale) for x in rows[i]]
        for j in range(n):
            if i != j:
                scale = rows[j][i]
                rows[j] = [x ^ mul(scale, y) for x, y in zip(rows[j], rows[i])]
    return [row[n:] for row in rows]


def rs_matrix(k, r):
    v = [[power(i, j) for j in range(k)] for i in range(k+r)]
    inv = inverse(v[:k])
    return [[xor(mul(v[i][j], inv[j][c]) for j in range(k)) for c in range(k)] for i in range(k+r)]


def xor(values):
    result = 0
    for x in values:
        result ^= x
    return result


def frame(packet_id, index, count, total, data):
    return b'WGQ1' + struct.pack('>BQHHI', 1, packet_id, index, count, total) + data


def record(kind, index, k, r, data=b''):
    return b'WGQF' + struct.pack('>BBHQHHHH', 1, kind, 1, 1, index, k, r, len(data)) + data


def vectors():
    sa, sb = bytes(range(32)), bytes(range(32, 64))
    ea, eb = bytes(range(64, 96)), bytes(range(96, 128))
    psk = bytes(range(160, 192))
    pa, pb = public(sa), public(sb)
    salt = bytes(range(1, 9))
    shared = dh(sa, pb)
    def obfs_key(optional):
        data = b'wg-quic/salamander/key/v1' + shared + (b'\0' if optional is None else b'\1' + optional)
        return hashlib.blake2b(data, digest_size=32).digest()
    key = obfs_key(psk)
    hint = hashlib.blake2b(b'wg-quic/salamander/hint/v1'+salt, key=key, digest_size=32).digest()[:8]
    stream = hashlib.blake2b(b'wg-quic/salamander/stream/v1'+salt, key=key, digest_size=32).digest()
    plain = bytes.fromhex('c000000001080102030405060708')
    wire = salt + hint + bytes(x ^ stream[i % 32] for i, x in enumerate(plain))
    obfs = dict(private_a=sa, public_a=pa, private_b=sb, public_b=pb, shared=shared, psk=psk,
                key=key, key_absent_psk=obfs_key(None), key_zero_psk=obfs_key(bytes(32)), salt=salt,
                hint=hint, stream=stream, plain=plain, wire=wire)
    # Independently compute a complete deterministic Noise IKpsk2 exchange.
    chain = h(b'Noise_IKpsk2_25519_ChaChaPoly_BLAKE2s')
    transcript = h(h(chain+b'WireGuard v1 zx2c4 Jason@zx2c4.com') + pb)
    ae = public(ea)
    chain, = kdf(chain, ae)
    transcript = h(transcript+ae)
    chain, cipher = kdf(chain, dh(ea, pb), 2)
    encrypted_static = aead(cipher, 0, pa, transcript)
    transcript = h(transcript+encrypted_static)
    chain, cipher = kdf(chain, dh(sa, pb), 2)
    timestamp = struct.pack('>QI', 0x400000000000000a + 1700000000, 0)
    encrypted_time = aead(cipher, 0, timestamp, transcript)
    transcript = h(transcript+encrypted_time)
    initiation = struct.pack('<II', 1, 0x01020304)+ae+encrypted_static+encrypted_time
    initiation += mac(h(b'mac1----'+pb), initiation) + bytes(16)
    chain_init, hash_init = chain, transcript
    be = public(eb)
    transcript = h(transcript+be)
    chain, = kdf(chain, be)
    chain, = kdf(chain, dh(eb, ae))
    chain, = kdf(chain, dh(eb, pa))
    chain, tau, cipher = kdf(chain, psk, 3)
    transcript = h(transcript+tau)
    empty = aead(cipher, 0, b'', transcript)
    transcript = h(transcript+empty)
    response = struct.pack('<III', 2, 0x05060708, 0x01020304)+be+empty
    response += mac(h(b'mac1----'+pa), response)+bytes(16)
    send_a, send_b = kdf(chain, b'', 2)
    keepalive = struct.pack('<IIQ', 4, 0x05060708, 0)+aead(send_a, 0, b'')
    noise = dict(ephemeral_private_a=ea, ephemeral_public_a=ae, ephemeral_private_b=eb, ephemeral_public_b=be,
                 timestamp=timestamp, initiation=initiation, chain_after_initiation=chain_init,
                 hash_after_initiation=hash_init, response=response, chain_final=chain, hash_final=transcript,
                 sending_key_a=send_a, sending_key_b=send_b, keepalive=keepalive,
                 framed_initiation=frame(1, 0, 1, len(initiation), initiation))
    fragments = [frame(0x0102030405060708,i,3,9,bytes(range(1+3*i,4+3*i))).hex() for i in range(3)]
    frames = [frame(i+1,0,1,len(data),data) for i,data in enumerate([b'\xaa',b'\xbb\xcc',b'\xdd\xee\xff'])]
    shards = [struct.pack('>H',len(f))+f for f in frames]
    padded = [s.ljust(max(map(len,shards)),b'\0') for s in shards]
    matrix = rs_matrix(3,2)
    parity = [bytes(xor(mul(c,x) for c,x in zip(row,column)) for column in zip(*padded)) for row in matrix[3:]]
    fec = dict(matrix=matrix, frames=[f.hex() for f in frames], shards=[s.hex() for s in padded],
               data=[record(0,i,0,0,s).hex() for i,s in enumerate(shards)],
               parity=[record(1,i,3,2,s).hex() for i,s in enumerate(parity)],
               close=record(2,0,3,2).hex(), feedback=record(3,2,3,2).hex(),
               unknown_dimensions_feedback=record(3,1,0,0).hex())
    return dict(salamander={k:v.hex() for k,v in obfs.items()}, noise={k:v.hex() for k,v in noise.items()}, fragments=fragments, fec=fec)


if __name__ == '__main__':
    import sys
    actual = vectors()
    if '--emit' in sys.argv:
        print(json.dumps(actual, indent=2))
    else:
        document = Path(__file__).resolve().parents[2] / 'docs/WG-QUIC-PROTOCOL.md'
        expected = json.loads(re.search(r'```json\n(.*?)\n```', document.read_text(), re.S)[1])
        assert actual == expected, 'Protocol vectors differ from independent computation'
        print('Independent X25519, Salamander, Noise handshake, WGQ1 and RS vectors match the standalone specification')
