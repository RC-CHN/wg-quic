package armorbind

import (
	quiccarrier "github.com/RC-CHN/wg-quic/internal/transport/quic"
)

// Control traffic has its own bounded producer and QUIC queue reservation, so
// it can progress even while the bulk producer is waiting for pacing credit.
// Plain WGQ1 frames are already supported alongside FEC frames by every peer.
// Keepalives retain their two independent copies; handshakes are sent once.
func (s *session) prioritySendLoop() {
	select {
	case <-s.ready:
	case <-s.ctx.Done():
		return
	}
	s.mu.Lock()
	qconn := s.conn
	s.mu.Unlock()
	for {
		var frame []byte
		select {
		case frame = <-s.control:
		case packet := <-s.priority:
			var err error
			frame, err = framePreparedPacket(packet.preparedFrame, packet.id)
			if err != nil {
				quiccarrier.ReleaseDatagramSendBuffer(packet.preparedFrame)
				return
			}
		case <-s.ctx.Done():
			return
		}
		size := len(frame)
		if err := qconn.SendPriorityDatagramOwned(s.ctx, frame); err != nil {
			quiccarrier.ReleaseDatagramSendBuffer(frame)
			if s.ctx.Err() == nil {
				reason, class, _ := quiccarrier.ClassifyConnectionError(err)
				s.setCloseCause(reason, class, err)
			}
			return
		}
		s.endpoint.owner.stats.wireTxPackets.Add(1)
		s.endpoint.owner.stats.wireTxBytes.Add(uint64(size))
		s.stats.wireTxPackets.Add(1)
		s.stats.wireTxBytes.Add(uint64(size))
	}
}
