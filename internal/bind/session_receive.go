package armorbind

import (
	"errors"
	"net/netip"
	"time"

	"github.com/RC-CHN/wg-quic/internal/telemetry"
	"github.com/RC-CHN/wg-quic/internal/transport/fec"
	quiccarrier "github.com/RC-CHN/wg-quic/internal/transport/quic"
)

func (s *session) receiveLoop() {
	defer s.state.reassembly.discardSession(s.id)
	select {
	case <-s.ready:
	case <-s.ctx.Done():
		return
	}
	s.mu.Lock()
	qconn := s.conn
	s.mu.Unlock()
	type receiveResult struct {
		datagram quiccarrier.ReceivedDatagram
		err      error
	}
	// Buffer the handoff so a processing burst never stalls the goroutine
	// blocked in ReceiveDatagramOwned; 64 covers a full QUIC batch with
	// headroom without queuing unbounded datagrams.
	incoming := make(chan receiveResult, 64)
	go func() {
		for {
			datagram, err := qconn.ReceiveDatagramOwned(s.ctx)
			select {
			case incoming <- receiveResult{datagram: datagram, err: err}:
			case <-s.ctx.Done():
				datagram.Release()
				return
			}
			if err != nil {
				return
			}
		}
	}()

	expiry := time.NewTicker(fecExpiryPoll)
	defer expiry.Stop()
	for {
		select {
		case received := <-incoming:
			if received.err != nil {
				received.datagram.Release()
				if s.ctx.Err() == nil {
					reason, class, message := quiccarrier.ClassifyConnectionError(received.err)
					s.setCloseCause(reason, class, errors.New(message))
					s.endpoint.owner.debugf(
						"QUIC receive stopped: session=%d remote=%s error=%v",
						s.id, s.endpoint.addr, received.err,
					)
				}
				return
			}
			s.handleReceivedDatagram(qconn, received.datagram)
		case now := <-expiry.C:
			s.state.reassembly.expire(now)
			s.sendFECFeedback(s.fecDecoder.Expire(now))
			continue
		case <-s.ctx.Done():
			return
		}
	}
}

// endpointForAddrPort snapshots the live QUIC path for the WireGuard packet
// being delivered. quic-go can migrate an established connection after a NAT
// rebinding, while the endpoint created by Accept retains the address of the
// initial path. WireGuard must receive the new immutable address so an
// authenticated packet updates its roaming state and status output without
// changing the session's configured/reconnect identity.
func (s *session) endpointForAddrPort(remote netip.AddrPort) *receiveEndpoint {
	return s.endpointForIngress(remote, s.endpoint.owner.receiveSequence.Add(1))
}

// endpointForIngress materializes an immutable snapshot using the sequence
// reserved before FEC decoding. Datagrams emitting no frame need no snapshot.
func (s *session) endpointForIngress(remote netip.AddrPort, sequence uint64) *receiveEndpoint {
	remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
	return &receiveEndpoint{
		owner: s.endpoint.owner, addr: remote,
		receiveSequence: sequence,
		session:         s,
		route:           s.receiveRoute,
	}
}

func (s *session) sendFECFeedback(feedbacks []fec.Feedback) {
	for _, feedback := range feedbacks {
		s.endpoint.owner.stats.fecRawLost.Add(uint64(feedback.Missing))
		s.endpoint.owner.stats.fecRecovered.Add(uint64(feedback.Recovered))
		s.stats.fecRawLost.Add(uint64(feedback.Missing))
		s.stats.fecRecovered.Add(uint64(feedback.Recovered))
		if feedback.Missing > feedback.Recovered {
			s.endpoint.owner.stats.fecUnrecovered.Add(uint64(feedback.Missing - feedback.Recovered))
			s.stats.fecUnrecovered.Add(uint64(feedback.Missing - feedback.Recovered))
		}
		select {
		case s.control <- fec.MarshalFeedback(feedback):
		default:
		}
	}
}

// deliverFrame parses and reassembles one WGQ1 frame. owned reports whether
// frame stays valid after this call returns (FEC decoder output) as opposed
// to aliasing the pooled QUIC receive datagram.
func (s *session) deliverFrame(frame []byte, owned bool, endpoint *receiveEndpoint, now time.Time) {
	frag, err := parseFragment(frame)
	if err != nil {
		return
	}
	packet, err := s.state.reassembly.add(now, s.id, frag, owned)
	if err != nil || packet == nil {
		return
	}
	s.endpoint.owner.stats.wgRxPackets.Add(1)
	s.endpoint.owner.stats.wgRxBytes.Add(uint64(len(packet)))
	s.stats.wgRxPackets.Add(1)
	s.stats.wgRxBytes.Add(uint64(len(packet)))
	rp := receivedPacket{data: packet, ep: endpoint}
	if !(owned && frag.count == 1) {
		// Everything except an owned single-fragment passthrough is a buffer
		// this package allocated; return it to the pool once the receive
		// worker has copied it out. Foreign capacities no-op inside release.
		rp.release = releaseReassemblyBuffer
	}
	if s.role == "inbound" && s.authentication.Load() == 0 {
		if !s.reserveUnauthenticatedReceive() {
			s.endpoint.owner.stats.queueDrops.Add(1)
			s.stats.queueDrops.Add(1)
			rp.dispose()
			return
		}
		rp.reservation = s
	}
	select {
	case s.state.recv <- rp:
	case <-s.ctx.Done():
		rp.dispose()
	default:
		s.endpoint.owner.stats.queueDrops.Add(1)
		s.stats.queueDrops.Add(1)
		if s.claimQueueDropEvent(time.Now()) {
			observation := s.telemetry(time.Now())
			s.endpoint.owner.recordSessionEventAt(
				s.id, s.generation, telemetry.SessionEventQueueDrop,
				"receive_queue_full", observation.SampledAt, nil,
				sessionEventMetricsFromStats(
					observation.Stats, s.currentFECPolicy(), observation.CurrentEndpoint,
				),
			)
		}
		rp.dispose()
	}
}

// handleReceivedDatagram consumes one owned QUIC datagram.
func (s *session) handleReceivedDatagram(qconn *quiccarrier.Connection, datagram quiccarrier.ReceivedDatagram) {
	defer datagram.Release()
	now := time.Now()
	wirePacket := datagram.Data
	previousRemote := s.currentRemoteAddr()
	if datagram.RemoteAddr != previousRemote {
		nextRemote := datagram.RemoteAddr
		s.remoteAddr.Store(&nextRemote)
		if previousRemote.IsValid() {
			s.endpoint.owner.recordSessionEventAt(
				s.id, s.generation, telemetry.SessionEventEndpointMoved,
				"authenticated_path_observed", now,
				&telemetry.SessionEventMetrics{Endpoint: previousRemote.String()},
				&telemetry.SessionEventMetrics{Endpoint: datagram.RemoteAddr.String()},
			)
		}
	}
	// Reserve ingress identity before decoding: a concurrent configuration
	// readiness barrier must still exclude this datagram. Feedback, duplicate,
	// parity-only and invalid FEC packets need no heap-allocated endpoint.
	sequence := s.endpoint.owner.receiveSequence.Add(1)
	s.endpoint.owner.stats.wireRxPackets.Add(1)
	s.endpoint.owner.stats.wireRxBytes.Add(uint64(len(wirePacket)))
	s.stats.wireRxPackets.Add(1)
	s.stats.wireRxBytes.Add(uint64(len(wirePacket)))
	result, err := s.fecDecoder.Handle(now, wirePacket)
	if err != nil {
		return
	}
	if result.Handled {
		if result.ObservedFeedback != nil && s.fecEncoder != nil {
			s.fecEncoder.Observe(*result.ObservedFeedback)
			qconn.ObserveFECFeedback(
				uint64(result.ObservedFeedback.Total),
				uint64(result.ObservedFeedback.Missing),
				uint64(result.ObservedFeedback.Recovered),
			)
		}
		s.sendFECFeedback(result.SendFeedback)
		if len(result.Frames) == 0 {
			return
		}
		receiveEndpoint := s.endpointForIngress(datagram.RemoteAddr, sequence)
		for _, frame := range result.Frames {
			s.deliverFrame(frame, true, receiveEndpoint, now)
		}
		return
	}
	s.deliverFrame(wirePacket, false, s.endpointForIngress(datagram.RemoteAddr, sequence), now)
}
