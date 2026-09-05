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
	"hash"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/net/ipv4"
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

// SalamanderConn provides UDP PacketConn and OOB-capable methods. GSO segment
// sizes are rewritten on send because obfuscation changes every datagram's
// length, and ReadBatch decodes every received segment before quic-go sees it.
type SalamanderConn struct {
	*net.UDPConn

	outbound map[netip.AddrPort]Key
	keyMu    sync.Mutex
	keyRefs  map[Key]int
	keyOrder []Key
	keyPairs map[Key]*digestPair
	keySet   atomic.Pointer[[]receiveKey]
	// lastKey caches the most recent receive key that decoded a packet. A
	// hint match proves key possession exactly like the scan below, so
	// decode tries it first; releaseReceiveKey clears it when its key is
	// removed from the receive set.
	lastKey atomic.Pointer[receiveKey]

	cacheMu sync.RWMutex
	learned map[netip.AddrPort]Key

	readMu        sync.Mutex
	readBuf       []byte
	batchConn     *ipv4.PacketConn
	batchMessages []ipv4.Message
	batchBuffers  [][]byte
	writeMu       sync.Mutex
	writeBuf      []byte
	writeDigests  map[Key]*digestPair // guarded by writeMu
}

type receiveKey struct {
	key  Key
	pair *digestPair
}

// WrapKeyedSalamander wraps a UDP socket without adding configuration secrets.
// At least one peer key is required to exchange packets, but an empty key list
// is accepted so an interface with no peers can still be brought up.
func WrapKeyedSalamander(connection *net.UDPConn, peers []PeerKey) (*SalamanderConn, error) {
	if connection == nil {
		return nil, errors.New("Salamander requires a UDP connection")
	}
	result := &SalamanderConn{
		UDPConn:      connection,
		outbound:     make(map[netip.AddrPort]Key),
		keyRefs:      make(map[Key]int),
		keyPairs:     make(map[Key]*digestPair),
		learned:      make(map[netip.AddrPort]Key),
		readBuf:      make([]byte, maxUDPPayload),
		batchConn:    ipv4.NewPacketConn(connection),
		writeBuf:     make([]byte, maxUDPPayload),
		writeDigests: make(map[Key]*digestPair),
	}
	seen := make(map[Key]struct{}, len(peers))
	for _, peer := range peers {
		if _, ok := seen[peer.Key]; !ok {
			seen[peer.Key] = struct{}{}
			result.addReceiveKeyLocked(peer.Key)
		}
		if peer.Endpoint.IsValid() {
			result.outbound[canonicalAddrPort(peer.Endpoint)] = peer.Key
		}
	}
	result.publishReceiveKeysLocked()
	return result, nil
}

// AcquireReceiveKey registers one key for inbound Salamander decoding. The
// immutable atomic snapshot lets blocked UDP readers observe additions and
// removals without taking a lock held across ReadFrom.
func (c *SalamanderConn) AcquireReceiveKey(key Key) func() {
	c.keyMu.Lock()
	c.addReceiveKeyLocked(key)
	c.publishReceiveKeysLocked()
	c.keyMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() { c.releaseReceiveKey(key) })
	}
}

func (c *SalamanderConn) addReceiveKeyLocked(key Key) {
	if c.keyRefs[key] == 0 {
		c.keyOrder = append(c.keyOrder, key)
		c.keyPairs[key] = newDigestPair(key)
	}
	c.keyRefs[key]++
}

func (c *SalamanderConn) releaseReceiveKey(key Key) {
	c.keyMu.Lock()
	defer c.keyMu.Unlock()
	refs := c.keyRefs[key]
	if refs <= 1 {
		delete(c.keyRefs, key)
		delete(c.keyPairs, key)
		for index, candidate := range c.keyOrder {
			if candidate == key {
				c.keyOrder = append(c.keyOrder[:index], c.keyOrder[index+1:]...)
				break
			}
		}
	} else {
		c.keyRefs[key] = refs - 1
	}
	// The cached fast-path key must never outlive its registration: without
	// this clear, a released key could keep decoding packets that the scan
	// below would reject.
	if cached := c.lastKey.Load(); cached != nil && cached.key == key {
		c.lastKey.Store(nil)
	}
	c.publishReceiveKeysLocked()
}

func (c *SalamanderConn) publishReceiveKeysLocked() {
	snapshot := make([]receiveKey, 0, len(c.keyOrder))
	for _, key := range c.keyOrder {
		if c.keyRefs[key] == 0 {
			continue
		}
		snapshot = append(snapshot, receiveKey{key: key, pair: c.keyPairs[key]})
	}
	c.keySet.Store(&snapshot)
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
	n, err := encodeWithPair(payload, c.writeBuf, digestPairFor(&c.writeDigests, key))
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
	gsoSize, err := growGSOSegmentSize(oob, SalamanderHeaderSize)
	if err != nil {
		return 0, 0, err
	}
	encoded, err := encodeSegmentsWithPair(payload, c.writeBuf, digestPairFor(&c.writeDigests, key), int(gsoSize))
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

// ReadBatch keeps quic-go's UDP batching enabled while ensuring the raw socket
// is never read around the obfuscation layer.
func (c *SalamanderConn) ReadBatch(messages []ipv4.Message, flags int) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(messages) == 0 {
		return 0, nil
	}
	c.ensureBatchBuffers(len(messages))
	for {
		raw := c.batchMessages[:len(messages)]
		for i := range raw {
			raw[i].Buffers[0] = c.batchBuffers[i]
			raw[i].OOB = messages[i].OOB
			raw[i].N, raw[i].NN, raw[i].Flags, raw[i].Addr = 0, 0, 0, nil
		}
		n, err := c.batchConn.ReadBatch(raw, flags)
		if n == 0 || err != nil {
			return n, err
		}
		decoded := 0
		for i := 0; i < n; i++ {
			if len(messages[decoded].Buffers) == 0 {
				continue
			}
			output := messages[decoded].Buffers[0]
			plain, key, ok := c.decode(raw[i].Buffers[0][:raw[i].N], output)
			if !ok {
				continue
			}
			addr, ok := raw[i].Addr.(*net.UDPAddr)
			if !ok {
				continue
			}
			c.remember(addr, key)
			messages[decoded].N = plain
			oobn := min(raw[i].NN, len(messages[decoded].OOB))
			if decoded != i {
				copy(messages[decoded].OOB[:oobn], raw[i].OOB[:oobn])
			}
			messages[decoded].NN = oobn
			messages[decoded].Flags = raw[i].Flags
			messages[decoded].Addr = raw[i].Addr
			decoded++
		}
		if decoded > 0 {
			return decoded, nil
		}
	}
}

func (c *SalamanderConn) ensureBatchBuffers(count int) {
	for len(c.batchBuffers) < count {
		c.batchBuffers = append(c.batchBuffers, make([]byte, maxUDPPayload))
		message := ipv4.Message{Buffers: make([][]byte, 1)}
		c.batchMessages = append(c.batchMessages, message)
	}
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
	keys := c.keySet.Load()
	if keys != nil && len(*keys) == 1 {
		return (*keys)[0].key, nil
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
	// The common case is a steady sender whose learned mapping already
	// holds this key; skip the exclusive lock and the map write.
	c.cacheMu.RLock()
	previous, learned := c.learned[addrPort]
	c.cacheMu.RUnlock()
	if learned && previous == key {
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
	keys := c.keySet.Load()
	if keys == nil {
		return 0, zero, false
	}
	// Fast path: try the last key that decoded a packet before scanning.
	// One active sender per socket is the common deployment, so this
	// collapses the O(keys) scan into a single derivation.
	if cached := c.lastKey.Load(); cached != nil {
		expected := cached.pair.deriveHint(salt)
		if subtle.ConstantTimeCompare(hint, expected[:]) == 1 {
			stream := cached.pair.deriveStream(salt)
			payload := packet[SalamanderHeaderSize:]
			xorWords(output, payload, &stream)
			return len(payload), cached.key, true
		}
	}
	for index := range *keys {
		decoder := &(*keys)[index]
		expected := decoder.pair.deriveHint(salt)
		if subtle.ConstantTimeCompare(hint, expected[:]) != 1 {
			continue
		}
		stream := decoder.pair.deriveStream(salt)
		payload := packet[SalamanderHeaderSize:]
		xorWords(output, payload, &stream)
		c.lastKey.Store(decoder)
		return len(payload), decoder.key, true
	}
	return 0, zero, false
}

func encode(payload, output []byte, key Key) (int, error) {
	return encodeWithPair(payload, output, newDigestPair(key))
}

// encodeWithPair is encode with the key's digest state reused across packets.
// It emits exactly the same wire bytes.
func encodeWithPair(payload, output []byte, pair *digestPair) (int, error) {
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
	hint := pair.deriveHint(salt)
	copy(output[SalamanderSaltSize:SalamanderHeaderSize], hint[:])
	stream := pair.deriveStream(salt)
	xorWords(output[SalamanderHeaderSize:], payload, &stream)
	return len(payload) + SalamanderHeaderSize, nil
}

func encodeSegments(payload, output []byte, key Key, segmentSize int) (int, error) {
	return encodeSegmentsWithPair(payload, output, newDigestPair(key), segmentSize)
}

func encodeSegmentsWithPair(payload, output []byte, pair *digestPair, segmentSize int) (int, error) {
	if segmentSize <= 0 || segmentSize >= len(payload) {
		return encodeWithPair(payload, output, pair)
	}
	written := 0
	for len(payload) > 0 {
		size := min(segmentSize, len(payload))
		n, err := encodeWithPair(payload[:size], output[written:], pair)
		if err != nil {
			return 0, err
		}
		written += n
		payload = payload[size:]
	}
	return written, nil
}

// digestPair caches the keyed BLAKE2b states of one obfuscation key. Reset
// restores the keyed initial state, so per-packet derivation only hashes the
// domain and salt instead of repeating the key schedule. A digestPair is not
// safe for concurrent use; SalamanderConn keeps separate pairs per direction
// under the corresponding mutex.
type digestPair struct {
	hint   hash.Hash
	stream hash.Hash
	// Hash.Sum takes a slice through an interface. Keep its backing store
	// here so the two per-packet sums don't escape as fresh heap objects.
	sum [blake2b.Size256]byte
}

func newDigestPair(key Key) *digestPair {
	hintDigest, err := blake2b.New256(key[:])
	if err != nil {
		panic(err)
	}
	streamDigest, err := blake2b.New256(key[:])
	if err != nil {
		panic(err)
	}
	return &digestPair{hint: hintDigest, stream: streamDigest}
}

func (p *digestPair) deriveHint(salt []byte) [SalamanderHintSize]byte {
	p.hint.Reset()
	_, _ = p.hint.Write(hintDomain)
	_, _ = p.hint.Write(salt)
	p.hint.Sum(p.sum[:0])
	var hint [SalamanderHintSize]byte
	copy(hint[:], p.sum[:SalamanderHintSize])
	return hint
}

func (p *digestPair) deriveStream(salt []byte) [blake2b.Size256]byte {
	p.stream.Reset()
	_, _ = p.stream.Write(streamDomain)
	_, _ = p.stream.Write(salt)
	p.stream.Sum(p.sum[:0])
	return p.sum
}

// digestPairFor returns the cached digest pair for key, building it on first
// use. The caller must hold the mutex guarding cache.
func digestPairFor(cache *map[Key]*digestPair, key Key) *digestPair {
	if *cache == nil {
		*cache = make(map[Key]*digestPair)
	}
	pair, ok := (*cache)[key]
	if !ok {
		pair = newDigestPair(key)
		(*cache)[key] = pair
	}
	return pair
}

// xorWords XORs src into dst with the 32-byte stream repeated. Processing
// a whole stream period per step lets the compiler eliminate the inner
// loop and its repeated bounds checks. The result is independent of host
// endianness and supports disjoint buffers or exact in-place operation.
func xorWords(dst, src []byte, stream *[blake2b.Size256]byte) {
	w0 := binary.LittleEndian.Uint64(stream[0:8])
	w1 := binary.LittleEndian.Uint64(stream[8:16])
	w2 := binary.LittleEndian.Uint64(stream[16:24])
	w3 := binary.LittleEndian.Uint64(stream[24:32])
	for len(src) >= blake2b.Size256 {
		_ = dst[31]
		binary.LittleEndian.PutUint64(dst[0:8], binary.LittleEndian.Uint64(src[0:8])^w0)
		binary.LittleEndian.PutUint64(dst[8:16], binary.LittleEndian.Uint64(src[8:16])^w1)
		binary.LittleEndian.PutUint64(dst[16:24], binary.LittleEndian.Uint64(src[16:24])^w2)
		binary.LittleEndian.PutUint64(dst[24:32], binary.LittleEndian.Uint64(src[24:32])^w3)
		dst, src = dst[32:], src[32:]
	}
	for i := range src {
		dst[i] = src[i] ^ stream[i]
	}
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
