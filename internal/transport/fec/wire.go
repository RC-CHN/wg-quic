package fec

import (
	"encoding/binary"
	"errors"

	quiccarrier "github.com/RC-CHN/wg-quic/internal/transport/quic"
)

const headerSize = 24

// DataPacketOverhead is the FEC header plus the encoded source-length prefix.
const DataPacketOverhead = headerSize + 2

var magic = [4]byte{'W', 'G', 'Q', 'F'}

type Kind uint8

const (
	KindData Kind = iota
	KindParity
	KindClose
	KindFeedback
)

type packet struct {
	kind    Kind
	epoch   uint16
	groupID uint64
	index   uint16
	k       uint16
	r       uint16
	payload []byte
}

func PacketKind(data []byte) (Kind, bool) {
	p, handled, err := parsePacket(data)
	if err != nil || !handled {
		return 0, false
	}
	return p.kind, true
}

func marshalPacket(p packet) []byte {
	// Draw from the carrier's send pool: the buffer is handed to
	// SendDatagramOwned downstream and quic-go recycles it once the frame is
	// serialized. Oversized packets fall back to a fresh allocation inside.
	out := quiccarrier.AcquireDatagramSendBuffer(headerSize + len(p.payload))
	copy(out[:4], magic[:])
	out[4] = 1
	out[5] = byte(p.kind)
	binary.BigEndian.PutUint16(out[6:8], p.epoch)
	binary.BigEndian.PutUint64(out[8:16], p.groupID)
	binary.BigEndian.PutUint16(out[16:18], p.index)
	binary.BigEndian.PutUint16(out[18:20], p.k)
	binary.BigEndian.PutUint16(out[20:22], p.r)
	binary.BigEndian.PutUint16(out[22:24], uint16(len(p.payload)))
	copy(out[headerSize:], p.payload)
	return out
}

func parsePacket(data []byte) (packet, bool, error) {
	if len(data) < 4 || string(data[:4]) != string(magic[:]) {
		return packet{}, false, nil
	}
	if len(data) < headerSize {
		return packet{}, true, errors.New("FEC packet is shorter than its header")
	}
	if data[4] != 1 {
		return packet{}, true, errors.New("unsupported FEC wire version")
	}
	kind := Kind(data[5])
	if kind > KindFeedback {
		return packet{}, true, errors.New("unknown FEC packet kind")
	}
	payloadLen := int(binary.BigEndian.Uint16(data[22:24]))
	if payloadLen != len(data)-headerSize {
		return packet{}, true, errors.New("FEC payload length mismatch")
	}
	if payloadLen > MaxFrameSize+2 {
		return packet{}, true, errors.New("FEC payload exceeds the receiver limit")
	}
	return packet{
		kind:    kind,
		epoch:   binary.BigEndian.Uint16(data[6:8]),
		groupID: binary.BigEndian.Uint64(data[8:16]),
		index:   binary.BigEndian.Uint16(data[16:18]),
		k:       binary.BigEndian.Uint16(data[18:20]),
		r:       binary.BigEndian.Uint16(data[20:22]),
		payload: data[headerSize:],
	}, true, nil
}
