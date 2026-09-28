# Standalone protocol conformance

The normative, self-contained document is
[`docs/WG-QUIC-PROTOCOL.md`](../../docs/WG-QUIC-PROTOCOL.md). Its JSON block is
the fixture: independent implementers need only that document.

`check_vectors.py` is an independent calculation of its cryptography and
Reed-Solomon matrix using Python 3 and the `cryptography` package. It imports no
project implementation. `--emit` prints calculated values for review; normal
execution compares them to the embedded document.

```sh
python3 tests/protocol/check_vectors.py
go test ./internal/transport/fec ./third_party/wireguard-go/device -run StandaloneProtocol -count=1
```

The Go tests consume those embedded vectors with the deployed Noise engine
and FEC encoder/decoder, including both handshake messages, key confirmation,
MAC1, replay rejection and reconstruction of two missing source shards.
The device test opens local UDP sockets; it does not require a TUN device.
