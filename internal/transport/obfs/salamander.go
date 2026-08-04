// Package obfs implements the packet obfuscation layer below QUIC.
//
// The Salamander profile borrows the salt-and-BLAKE2b construction used by
// Hysteria 2, but derives a distinct key from the existing WireGuard key
// material. It is obfuscation, not a second security boundary: WireGuard and
// QUIC still provide authentication and encryption.
package obfs

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/curve25519"
)

const (
	SalamanderSaltSize   = 8
	SalamanderHintSize   = 8
	SalamanderHeaderSize = SalamanderSaltSize + SalamanderHintSize
	maxUDPPayload        = 65507
	maxLearnedEndpoints  = 1024
)

var (
	derivationDomain = []byte("wg-quic/salamander/key/v1")
	hintDomain       = []byte("wg-quic/salamander/hint/v1")
	streamDomain     = []byte("wg-quic/salamander/stream/v1")
)

// Key is a domain-separated obfuscation key. It must never be written to logs.
type Key [blake2b.Size256]byte

// PeerKey associates a derived key with a configured endpoint. Endpoint may be
// invalid for a passive peer; a successfully received packet teaches the
// wrapper that peer's current source address.
type PeerKey struct {
	Key      Key
	Endpoint netip.AddrPort
}

// DeriveWireGuardKey derives the same obfuscation key at both peers from the
// local WireGuard private key and the remote WireGuard public key. An existing
// WireGuard PresharedKey is mixed in when configured.
func DeriveWireGuardKey(localPrivate, remotePublic, preshared []byte) (Key, error) {
	var key Key
	if len(localPrivate) != curve25519.ScalarSize {
		return key, fmt.Errorf("WireGuard private key must be %d bytes", curve25519.ScalarSize)
	}
	if len(remotePublic) != curve25519.PointSize {
		return key, fmt.Errorf("WireGuard public key must be %d bytes", curve25519.PointSize)
	}
	if len(preshared) != 0 && len(preshared) != blake2b.Size256 {
		return key, fmt.Errorf("WireGuard preshared key must be %d bytes", blake2b.Size256)
	}
	shared, err := curve25519.X25519(localPrivate, remotePublic)
	if err != nil {
		return key, fmt.Errorf("derive WireGuard shared secret: %w", err)
	}
	hash, err := blake2b.New256(nil)
	if err != nil {
		return key, err
	}
	_, _ = hash.Write(derivationDomain)
	_, _ = hash.Write(shared)
	if len(preshared) == 0 {
		_, _ = hash.Write([]byte{0})
	} else {
		_, _ = hash.Write([]byte{1})
		_, _ = hash.Write(preshared)
	}
	copy(key[:], hash.Sum(nil))
	return key, nil
}

// SalamanderConn provides UDP PacketConn and OOB-capable methods. ArmorBind
// currently gives quic-go a narrow PacketConn view because GSO/GRO metadata
// must be rewritten when obfuscation changes each datagram's length.
type SalamanderConn struct {
	*net.UDPConn

	keys     []Key
	outbound map[netip.AddrPort]Key

	cacheMu sync.RWMutex
	learned map[netip.AddrPort]Key

	readMu   sync.Mutex
	readBuf  []byte
	writeMu  sync.Mutex
	writeBuf []byte
}

// WrapKeyedSalamander wraps a UDP socket without adding configuration secrets.
// At least one peer key is required to exchange packets, but an empty key list
// is accepted so an interface with no peers can still be brought up.
func WrapKeyedSalamander(connection *net.UDPConn, peers []PeerKey) (*SalamanderConn, error) {
	if connection == nil {
		return nil, errors.New("Salamander requires a UDP connection")
	}
	result := &SalamanderConn{
		UDPConn:  connection,
		outbound: make(map[netip.AddrPort]Key),
		learned:  make(map[netip.AddrPort]Key),
		readBuf:  make([]byte, maxUDPPayload),
		writeBuf: make([]byte, maxUDPPayload),
	}
	seen := make(map[Key]struct{}, len(peers))
	for _, peer := range peers {
		if _, ok := seen[peer.Key]; !ok {
			seen[peer.Key] = struct{}{}
			result.keys = append(result.keys, peer.Key)
		}
		if peer.Endpoint.IsValid() {
			result.outbound[canonicalAddrPort(peer.Endpoint)] = peer.Key
		}
	}
	return result, nil
}

func (c *SalamanderConn) ReadFrom(output []byte) (int, net.Addr, error) {
	n, addr, err := c.readFromUDP(output)
	return n, addr, err
}

func (c *SalamanderConn) ReadFromUDP(output []byte) (int, *net.UDPAddr, error) {
	return c.readFromUDP(output)
}

func (c *SalamanderConn) ReadFromUDPAddrPort(output []byte) (int, netip.AddrPort, error) {
	n, addr, err := c.readFromUDP(output)
	if err != nil {
		return n, netip.AddrPort{}, err
	}
	addrPort, _ := udpAddrPort(addr)
	return n, addrPort, nil
}

func (c *SalamanderConn) readFromUDP(output []byte) (int, *net.UDPAddr, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for {
		n, addr, err := c.UDPConn.ReadFromUDP(c.readBuf)
		if err != nil {
			return 0, addr, err
		}
		n, key, ok := c.decode(c.readBuf[:n], output)
		if !ok {
			continue
		}
		c.remember(addr, key)
		return n, addr, nil
	}
}

func (c *SalamanderConn) WriteTo(payload []byte, addr net.Addr) (int, error) {
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0, fmt.Errorf("Salamander requires UDP destination, got %T", addr)
	}
	return c.WriteToUDP(payload, udpAddr)
}

func (c *SalamanderConn) WriteToUDP(payload []byte, addr *net.UDPAddr) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	key, err := c.keyForAddress(addr)
	if err != nil {
		return 0, err
	}
	n, err := encode(payload, c.writeBuf, key)
	if err != nil {
		return 0, err
	}
	written, err := c.UDPConn.WriteToUDP(c.writeBuf[:n], addr)
	if err != nil {
		return 0, err
	}
	if written != n {
		return 0, io.ErrShortWrite
	}
	return len(payload), nil
}

func (c *SalamanderConn) WriteToUDPAddrPort(payload []byte, addr netip.AddrPort) (int, error) {
	return c.WriteToUDP(payload, net.UDPAddrFromAddrPort(addr))
}

func (c *SalamanderConn) ReadMsgUDP(payload, oob []byte) (n, oobn, flags int, addr *net.UDPAddr, err error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for {
		n, oobn, flags, addr, err = c.UDPConn.ReadMsgUDP(c.readBuf, oob)
		if err != nil {
			return 0, oobn, flags, addr, err
		}
		var key Key
		var ok bool
		n, key, ok = c.decode(c.readBuf[:n], payload)
		if !ok {
			continue
		}
		c.remember(addr, key)
		return n, oobn, flags, addr, nil
	}
}

func (c *SalamanderConn) WriteMsgUDP(payload, oob []byte, addr *net.UDPAddr) (n, oobn int, err error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	key, err := c.keyForAddress(addr)
	if err != nil {
		return 0, 0, err
	}
	encoded, err := encode(payload, c.writeBuf, key)
	if err != nil {
		return 0, 0, err
	}
	written, oobn, err := c.UDPConn.WriteMsgUDP(c.writeBuf[:encoded], oob, addr)
	if err != nil {
		return 0, oobn, err
	}
	if written != encoded {
		return 0, oobn, io.ErrShortWrite
	}
	return len(payload), oobn, nil
}

func (c *SalamanderConn) keyForAddress(addr *net.UDPAddr) (Key, error) {
	var zero Key
	addrPort, ok := udpAddrPort(addr)
	if !ok {
		return zero, errors.New("Salamander destination is not a valid IP endpoint")
	}
	c.cacheMu.RLock()
	key, found := c.outbound[addrPort]
	if !found {
		key, found = c.learned[addrPort]
	}
	c.cacheMu.RUnlock()
	if found {
		return key, nil
	}
	if len(c.keys) == 1 {
		return c.keys[0], nil
	}
	return zero, fmt.Errorf("no Salamander key for destination %s", addrPort)
}

// AssociateEndpoint installs the key selected for a configured WireGuard
// endpoint. Bind calls it after resolving the exact endpoint string passed
// through WireGuard's UAPI.
func (c *SalamanderConn) AssociateEndpoint(addr netip.AddrPort, key Key) {
	if !addr.IsValid() {
		return
	}
	c.cacheMu.Lock()
	c.outbound[canonicalAddrPort(addr)] = key
	c.cacheMu.Unlock()
}

// DisassociateEndpoint removes a dynamic outbound association only when it
// still names the expected key. The comparison prevents one peer releasing an
// old lease from deleting a newer association installed for the same address.
func (c *SalamanderConn) DisassociateEndpoint(addr netip.AddrPort, expected Key) {
	if !addr.IsValid() {
		return
	}
	addr = canonicalAddrPort(addr)
	c.cacheMu.Lock()
	if key, ok := c.outbound[addr]; ok && key == expected {
		delete(c.outbound, addr)
	}
	c.cacheMu.Unlock()
}

func (c *SalamanderConn) remember(addr *net.UDPAddr, key Key) {
	addrPort, ok := udpAddrPort(addr)
	if !ok {
		return
	}
	c.cacheMu.Lock()
	if len(c.learned) >= maxLearnedEndpoints {
		clear(c.learned)
	}
	c.learned[addrPort] = key
	c.cacheMu.Unlock()
}

func (c *SalamanderConn) decode(packet, output []byte) (int, Key, bool) {
	var zero Key
	if len(packet) <= SalamanderHeaderSize || len(output) < len(packet)-SalamanderHeaderSize {
		return 0, zero, false
	}
	salt := packet[:SalamanderSaltSize]
	hint := packet[SalamanderSaltSize:SalamanderHeaderSize]
	for _, key := range c.keys {
		expected := packetHint(key, salt)
		if subtle.ConstantTimeCompare(hint, expected[:]) != 1 {
			continue
		}
		stream := packetStream(key, salt)
		payload := packet[SalamanderHeaderSize:]
		for i, value := range payload {
			output[i] = value ^ stream[i%len(stream)]
		}
		return len(payload), key, true
	}
	return 0, zero, false
}

func encode(payload, output []byte, key Key) (int, error) {
	if len(payload) == 0 || len(payload)+SalamanderHeaderSize > maxUDPPayload {
		return 0, errors.New("invalid Salamander payload length")
	}
	if len(output) < len(payload)+SalamanderHeaderSize {
		return 0, errors.New("Salamander output buffer is too short")
	}
	salt := output[:SalamanderSaltSize]
	if _, err := rand.Read(salt); err != nil {
		return 0, err
	}
	hint := packetHint(key, salt)
	copy(output[SalamanderSaltSize:SalamanderHeaderSize], hint[:])
	stream := packetStream(key, salt)
	for i, value := range payload {
		output[SalamanderHeaderSize+i] = value ^ stream[i%len(stream)]
	}
	return len(payload) + SalamanderHeaderSize, nil
}

func packetHint(key Key, salt []byte) [SalamanderHintSize]byte {
	sum := keyedHash(hintDomain, key, salt)
	var hint [SalamanderHintSize]byte
	copy(hint[:], sum[:SalamanderHintSize])
	return hint
}

func packetStream(key Key, salt []byte) [blake2b.Size256]byte {
	return keyedHash(streamDomain, key, salt)
}

func keyedHash(domain []byte, key Key, salt []byte) [blake2b.Size256]byte {
	hash, err := blake2b.New256(key[:])
	if err != nil {
		panic(err)
	}
	_, _ = hash.Write(domain)
	_, _ = hash.Write(salt)
	var result [blake2b.Size256]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func udpAddrPort(addr *net.UDPAddr) (netip.AddrPort, bool) {
	if addr == nil || addr.Port < 0 || addr.Port > 65535 {
		return netip.AddrPort{}, false
	}
	ip, ok := netip.AddrFromSlice(addr.IP)
	if !ok {
		return netip.AddrPort{}, false
	}
	if addr.Zone != "" {
		ip = ip.WithZone(addr.Zone)
	}
	return canonicalAddrPort(netip.AddrPortFrom(ip, uint16(addr.Port))), true
}

func canonicalAddrPort(addr netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port())
}

func EncodedLength(plainLength int) int {
	return plainLength + SalamanderHeaderSize
}

func Salt(packet []byte) uint64 {
	if len(packet) < SalamanderSaltSize {
		return 0
	}
	return binary.BigEndian.Uint64(packet[:SalamanderSaltSize])
}
