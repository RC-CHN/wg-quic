package obfs

import (
	"bytes"
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

func listenUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	return udp
}
