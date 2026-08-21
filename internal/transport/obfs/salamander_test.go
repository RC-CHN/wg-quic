package obfs

import (
	"bytes"
	"encoding/hex"
	"net"
	"net/netip"
	"syscall"
	"testing"

	"golang.org/x/crypto/curve25519"
)

type quicUDPCapabilities interface {
	net.PacketConn
	SyscallConn() (syscall.RawConn, error)
	SetReadBuffer(int) error
	ReadMsgUDP(b, oob []byte) (n, oobn, flags int, addr *net.UDPAddr, err error)
	WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (n, oobn int, err error)
}

var _ quicUDPCapabilities = (*SalamanderConn)(nil)

func TestSalamanderWireGoldenVector(t *testing.T) {
	privateA := goldenHex(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	publicA := goldenHex(t, "8f40c5adb68f25624ae5b214ea767a6ec94d829d3d7b5e1ad1ba6f3e2138285f")
	privateB := goldenHex(t, "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f")
	publicB := goldenHex(t, "358072d6365880d1aeea329adf9121383851ed21a28e3b75e965d0d2cd166254")
	psk := goldenHex(t, "a0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf")
	wantKey := goldenHex(t, "a5432c4f3449673fc9be625ff0881346cbf1b4172ba6378661e84a9ab2a34ecc")

	keyA, err := DeriveWireGuardKey(privateA, publicB, psk)
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := DeriveWireGuardKey(privateB, publicA, psk)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(keyA[:], wantKey) || keyB != keyA {
		t.Fatalf("Salamander derived keys = %x / %x, want %x", keyA, keyB, wantKey)
	}

	salt := goldenHex(t, "0102030405060708")
	wantHint := goldenHex(t, "f71ea486b3282427")
	wantStream := goldenHex(t, "a0d422732a082a7e9f143eb0dc1cd157eb3619b074ec6a6e3cdc14686d25fe8f")
	if got := packetHint(keyA, salt); !bytes.Equal(got[:], wantHint) {
		t.Fatalf("Salamander packet hint = %x, want %x", got, wantHint)
	}
	if got := packetStream(keyA, salt); !bytes.Equal(got[:], wantStream) {
		t.Fatalf("Salamander packet stream = %x, want %x", got, wantStream)
	}

	plain := goldenHex(t, "c000000001080102030405060708")
	encoded := goldenHex(t, "0102030405060708f71ea486b328242760d422732b002b7c9c103bb6db14")
	output := make([]byte, len(plain))
	connection := &SalamanderConn{
		keyRefs: make(map[Key]int), keyPairs: make(map[Key]*digestPair),
	}
	connection.addReceiveKeyLocked(keyA)
	connection.publishReceiveKeysLocked()
	n, selected, ok := connection.decode(encoded, output)
	if !ok || n != len(plain) || selected != keyA {
		t.Fatalf("decode golden packet: ok=%v n=%d key=%x", ok, n, selected)
	}
	if !bytes.Equal(output, plain) {
		t.Fatalf("decoded Salamander payload = %x, want %x", output, plain)
	}
}

func TestSalamanderReceiveKeyLeaseAddsAndRemovesDecoder(t *testing.T) {
	connection, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := WrapKeyedSalamander(connection, nil)
	if err != nil {
		connection.Close()
		t.Fatal(err)
	}
	defer wrapped.Close()
	key := Key{0x42}
	payload := []byte("dynamic receive key")
	packet := make([]byte, len(payload)+SalamanderHeaderSize)
	if _, err := encode(payload, packet, key); err != nil {
		t.Fatal(err)
	}
	output := make([]byte, len(payload))
	if _, _, ok := wrapped.decode(packet, output); ok {
		t.Fatal("packet decoded before receive key acquisition")
	}
	firstRelease := wrapped.AcquireReceiveKey(key)
	secondRelease := wrapped.AcquireReceiveKey(key)
	if n, decodedKey, ok := wrapped.decode(packet, output); !ok || n != len(payload) ||
		decodedKey != key || !bytes.Equal(output, payload) {
		t.Fatalf("dynamic decode = n:%d key:%x ok:%t output:%q", n, decodedKey, ok, output)
	}
	firstRelease()
	if _, _, ok := wrapped.decode(packet, output); !ok {
		t.Fatal("first reference release removed a multiply-owned key")
	}
	secondRelease()
	if _, _, ok := wrapped.decode(packet, output); ok {
		t.Fatal("last reference release retained the receive key")
	}
	secondRelease()
}

func goldenHex(t testing.TB, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestWireGuardKeyDerivationIsSymmetric(t *testing.T) {
	privateA := bytes.Repeat([]byte{0x11}, 32)
	privateB := bytes.Repeat([]byte{0x22}, 32)
	publicA, err := curve25519.X25519(privateA, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	publicB, err := curve25519.X25519(privateB, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	psk := bytes.Repeat([]byte{0x33}, 32)
	keyA, err := DeriveWireGuardKey(privateA, publicB, psk)
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := DeriveWireGuardKey(privateB, publicA, psk)
	if err != nil {
		t.Fatal(err)
	}
	if keyA != keyB {
		t.Fatal("the two WireGuard peers derived different obfuscation keys")
	}
	withoutPSK, err := DeriveWireGuardKey(privateA, publicB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if withoutPSK == keyA {
		t.Fatal("WireGuard PresharedKey did not affect the obfuscation key")
	}
}

func TestSalamanderWireRoundTripAndPeerSelection(t *testing.T) {
	udp := listenUDP(t)
	defer udp.Close()
	keyA := Key{1}
	keyB := Key{2}
	connection, err := WrapKeyedSalamander(udp, []PeerKey{{Key: keyA}, {Key: keyB}})
	if err != nil {
		t.Fatal(err)
	}
	plain := bytes.Repeat([]byte{0x42}, 1300)
	encoded := make([]byte, EncodedLength(len(plain)))
	n, err := encode(plain, encoded, keyB)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(encoded) || Salt(encoded) == 0 {
		t.Fatalf("invalid encoded record: n=%d salt=%x", n, Salt(encoded))
	}
	decoded := make([]byte, len(plain))
	n, selected, ok := connection.decode(encoded, decoded)
	if !ok || n != len(plain) || selected != keyB {
		t.Fatalf("decode selected wrong peer: ok=%v n=%d key=%x", ok, n, selected[:4])
	}
	if !bytes.Equal(decoded, plain) {
		t.Fatal("Salamander changed the payload")
	}
	encoded[SalamanderSaltSize] ^= 1
	if _, _, ok := connection.decode(encoded, decoded); ok {
		t.Fatal("accepted a packet with an invalid dynamic hint")
	}
}

func TestSalamanderUsesFreshSaltAndHint(t *testing.T) {
	key := Key{9}
	a := make([]byte, 64)
	b := make([]byte, 64)
	if _, err := encode([]byte("same payload"), a, key); err != nil {
		t.Fatal(err)
	}
	if _, err := encode([]byte("same payload"), b, key); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a[:SalamanderHeaderSize], b[:SalamanderHeaderSize]) {
		t.Fatal("two records reused salt and dynamic hint")
	}
}

func TestSalamanderEncodesEachGSOSegmentIndependently(t *testing.T) {
	key := Key{9}
	payload := bytes.Repeat([]byte{0x42}, 2900)
	encoded := make([]byte, EncodedLength(len(payload))+2*SalamanderHeaderSize)
	n, err := encodeSegments(payload, encoded, key, 1200)
	if err != nil {
		t.Fatal(err)
	}
	encoded = encoded[:n]
	segments := []int{1216, 1216, 516}
	offset := 0
	decoded := make([]byte, 0, len(payload))
	udp := listenUDP(t)
	defer udp.Close()
	connection, err := WrapKeyedSalamander(udp, []PeerKey{{Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range segments {
		output := make([]byte, size-SalamanderHeaderSize)
		plain, _, ok := connection.decode(encoded[offset:offset+size], output)
		if !ok {
			t.Fatal("failed to decode GSO segment")
		}
		decoded = append(decoded, output[:plain]...)
		offset += size
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatal("GSO segment round trip changed payload")
	}
}

func TestSalamanderLearnsPassivePeerAddress(t *testing.T) {
	udp := listenUDP(t)
	defer udp.Close()
	key := Key{7}
	connection, err := WrapKeyedSalamander(udp, []PeerKey{{Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	addr := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 10), Port: 443}
	connection.remember(addr, key)
	got, err := connection.keyForAddress(addr)
	if err != nil || got != key {
		t.Fatalf("learned key = %x, %v", got[:4], err)
	}
}

func TestSalamanderConfiguredEndpointSelectsPeer(t *testing.T) {
	udp := listenUDP(t)
	defer udp.Close()
	endpoint := netip.MustParseAddrPort("192.0.2.20:8443")
	key := Key{8}
	connection, err := WrapKeyedSalamander(udp, []PeerKey{{Key: Key{1}}, {Key: key, Endpoint: endpoint}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := connection.keyForAddress(net.UDPAddrFromAddrPort(endpoint))
	if err != nil || got != key {
		t.Fatalf("configured key = %x, %v", got[:4], err)
	}
}

func listenUDP(t testing.TB) *net.UDPConn {
	t.Helper()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	return udp
}

func BenchmarkSalamanderEncode(b *testing.B) {
	pair := newDigestPair(Key{9})
	payload := bytes.Repeat([]byte{0x42}, 1300)
	output := make([]byte, EncodedLength(len(payload)))
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := encodeWithPair(payload, output, pair); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSalamanderDecode(b *testing.B) {
	udp := listenUDP(b)
	defer udp.Close()
	key := Key{9}
	connection, err := WrapKeyedSalamander(udp, []PeerKey{{Key: key}})
	if err != nil {
		b.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0x42}, 1300)
	encoded := make([]byte, EncodedLength(len(payload)))
	if _, err := encode(payload, encoded, key); err != nil {
		b.Fatal(err)
	}
	output := make([]byte, len(payload))
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, _, ok := connection.decode(encoded, output); !ok {
			b.Fatal("decode failed")
		}
	}
}
