package armorbind

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	frameHeaderSize = 21
	maxFragmentData = 4075
	maxFragments    = 128
	maxDatagramSize = 65535
	reassemblyTTL   = 3 * time.Second
)

var frameMagic = [4]byte{'W', 'G', 'Q', '1'}

type fragment struct {
	packetID uint64
	index    uint16
	count    uint16
	total    uint32
	data     []byte
}

func fragmentPacket(packet []byte, packetID uint64) ([][]byte, error) {
	return fragmentPacketSized(packet, packetID, maxFragmentData)
}

func fragmentPacketSized(packet []byte, packetID uint64, fragmentData int) ([][]byte, error) {
	if len(packet) == 0 || len(packet) > maxDatagramSize {
		return nil, fmt.Errorf("invalid WireGuard datagram size %d", len(packet))
	}
	if fragmentData <= 0 || fragmentData > maxFragmentData {
		return nil, fmt.Errorf("invalid fragment payload size %d", fragmentData)
	}
	count := (len(packet) + fragmentData - 1) / fragmentData
	if count > maxFragments {
		return nil, fmt.Errorf("datagram needs %d fragments, maximum is %d", count, maxFragments)
	}
	frames := make([][]byte, 0, count)
	for i, off := 0, 0; off < len(packet); i, off = i+1, off+fragmentData {
		end := min(off+fragmentData, len(packet))
		frame := make([]byte, frameHeaderSize+end-off)
		writeFragmentHeader(frame, packetID, uint16(i), uint16(count), uint32(len(packet)))
		copy(frame[frameHeaderSize:], packet[off:end])
		frames = append(frames, frame)
	}
	return frames, nil
}

// framePreparedPacket fills the reserved header in a buffer containing one
// complete WireGuard datagram. The caller allocates frameHeaderSize bytes of
// headroom before copying the WireGuard payload into the buffer, combining the
// queue-lifetime copy and the common one-fragment framing copy.
func framePreparedPacket(frame []byte, packetID uint64) ([]byte, error) {
	payloadLen := len(frame) - frameHeaderSize
	if payloadLen <= 0 || payloadLen > maxDatagramSize {
		return nil, fmt.Errorf("invalid prepared WireGuard datagram size %d", payloadLen)
	}
	writeFragmentHeader(frame, packetID, 0, 1, uint32(payloadLen))
	return frame, nil
}

func writeFragmentHeader(frame []byte, packetID uint64, index, count uint16, total uint32) {
	copy(frame[:4], frameMagic[:])
	frame[4] = 1
	binary.BigEndian.PutUint64(frame[5:13], packetID)
	binary.BigEndian.PutUint16(frame[13:15], index)
	binary.BigEndian.PutUint16(frame[15:17], count)
	binary.BigEndian.PutUint32(frame[17:21], total)
}

func priorityWireGuardDatagram(packet []byte) bool {
	if len(packet) < 4 {
		return false
	}
	switch binary.LittleEndian.Uint32(packet[:4]) {
	case 1, 2, 3:
		return true
	case 4:
		// A type-4 packet with no encrypted payload is a WireGuard keepalive:
		// 16-byte transport header plus the 16-byte AEAD tag.
		return len(packet) == 32
	default:
		return false
	}
}

func parseFragment(frame []byte) (fragment, error) {
	if len(frame) <= frameHeaderSize {
		return fragment{}, errors.New("frame is too short")
	}
	if string(frame[:4]) != string(frameMagic[:]) || frame[4] != 1 {
		return fragment{}, errors.New("invalid frame magic or version")
	}
	f := fragment{
		packetID: binary.BigEndian.Uint64(frame[5:13]),
		index:    binary.BigEndian.Uint16(frame[13:15]),
		count:    binary.BigEndian.Uint16(frame[15:17]),
		total:    binary.BigEndian.Uint32(frame[17:21]),
		data:     frame[frameHeaderSize:],
	}
	if f.count == 0 || f.count > maxFragments || f.index >= f.count {
		return fragment{}, errors.New("invalid fragment index or count")
	}
	if f.total == 0 || f.total > maxDatagramSize {
		return fragment{}, errors.New("invalid total datagram size")
	}
	if len(f.data) > maxFragmentData {
		return fragment{}, errors.New("fragment payload is too large")
	}
	return f, nil
}

type reassemblyKey struct {
	sessionID uint64
	packetID  uint64
}

type reassembly struct {
	created time.Time
	total   uint32
	count   uint16
	seen    int
	shards  [][]byte
}

type reassembler struct {
	groups map[reassemblyKey]*reassembly
}

func newReassembler() *reassembler {
	return &reassembler{groups: make(map[reassemblyKey]*reassembly)}
}

// reassemblyBufferSize covers one full-size fragment (plus header slack), the
// dominant receive-path allocation. Larger reassembled packets fall back to
// fresh allocations and are simply not recycled.
const reassemblyBufferSize = maxFragmentData + frameHeaderSize

var reassemblyBufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, reassemblyBufferSize)
		return &b
	},
}

// acquireReassemblyBuffer returns a buffer of n bytes, pooled when possible.
// The caller must slice it from offset zero for later recycling to apply.
func acquireReassemblyBuffer(n int) []byte {
	if n > reassemblyBufferSize {
		return make([]byte, n)
	}
	p := reassemblyBufferPool.Get().(*[]byte)
	return (*p)[:n]
}

// releaseReassemblyBuffer returns a buffer from acquireReassemblyBuffer to
// the pool. Buffers with a foreign capacity are left to the GC.
func releaseReassemblyBuffer(data []byte) {
	if cap(data) != reassemblyBufferSize {
		return
	}
	full := data[:reassemblyBufferSize]
	reassemblyBufferPool.Put(&full)
}

// add feeds one fragment into reassembly. owned reports whether the caller
// retains f.data for the packet's whole downstream lifetime (true for FEC
// decoder output); borrowed frames are still copied out because pooled QUIC
// datagrams are recycled after the receive callback returns.
func (r *reassembler) add(now time.Time, sessionID uint64, f fragment, owned bool) ([]byte, error) {
	if f.count == 1 {
		if len(f.data) != int(f.total) {
			return nil, errors.New("single-fragment length mismatch")
		}
		if owned {
			return f.data, nil
		}
		buf := acquireReassemblyBuffer(len(f.data))
		copy(buf, f.data)
		return buf, nil
	}
	for key, group := range r.groups {
		if now.Sub(group.created) > reassemblyTTL {
			delete(r.groups, key)
		}
	}
	if len(r.groups) >= 2048 {
		return nil, errors.New("too many incomplete datagrams")
	}
	key := reassemblyKey{sessionID: sessionID, packetID: f.packetID}
	group := r.groups[key]
	if group == nil {
		group = &reassembly{created: now, total: f.total, count: f.count, shards: make([][]byte, f.count)}
		r.groups[key] = group
	}
	if group.total != f.total || group.count != f.count {
		delete(r.groups, key)
		return nil, errors.New("inconsistent fragment metadata")
	}
	if group.shards[f.index] == nil {
		group.shards[f.index] = append([]byte(nil), f.data...)
		group.seen++
	}
	if group.seen != int(group.count) {
		return nil, nil
	}
	packet := acquireReassemblyBuffer(int(group.total))[:0]
	for _, shard := range group.shards {
		packet = append(packet, shard...)
	}
	delete(r.groups, key)
	if len(packet) != int(group.total) {
		return nil, errors.New("reassembled datagram length mismatch")
	}
	return packet, nil
}
