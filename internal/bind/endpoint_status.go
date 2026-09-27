package armorbind

import "net/netip"

type EndpointObservation struct {
	Session   EndpointSessionState
	Reconnect EndpointReconnectStatus
}

type EndpointObservations map[netip.AddrPort]EndpointObservation

func (o EndpointObservations) For(endpoint netip.AddrPort) EndpointObservation {
	endpoint = netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
	result := o[endpoint]
	if result.Session == "" {
		result.Session = EndpointSessionIdle
	}
	return result
}

// EndpointStatusSnapshot indexes each live session once, avoiding a scan of
// every session for every peer in a status request. Returned values are owned
// by the caller and preserve current-path migration semantics.
func (b *Bind) EndpointStatusSnapshot() EndpointObservations {
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return nil
	}
	state.mu.Lock()
	endpoints := make([]*Endpoint, 0, len(state.endpoints))
	for _, ep := range state.endpoints {
		endpoints = append(endpoints, ep)
	}
	sessions := make([]*session, 0, len(state.sessions))
	for _, sess := range state.sessions {
		sessions = append(sessions, sess)
	}
	state.mu.Unlock()
	result := make(EndpointObservations, max(len(endpoints), len(sessions)))
	for _, ep := range endpoints {
		ep.mu.Lock()
		observation := EndpointObservation{Session: EndpointSessionIdle, Reconnect: EndpointReconnectStatus{Attempts: ep.reconnectAttempts, Failures: ep.reconnectFailures, ConsecutiveFailures: ep.consecutiveFailures}}
		if ep.reconnectScheduled {
			observation.Session = EndpointSessionReconnecting
		}
		if !ep.nextReconnect.IsZero() {
			observation.Reconnect.NextReconnect = ep.nextReconnect.Unix()
		}
		result[ep.addr] = observation
		ep.mu.Unlock()
	}
	for _, sess := range sessions {
		if sess.closed.Load() {
			continue
		}
		remote := sess.currentRemoteAddr()
		remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
		if !remote.IsValid() {
			continue
		}
		observation := result.For(remote)
		sess.mu.Lock()
		established := sess.conn != nil
		sess.mu.Unlock()
		if established {
			observation.Session = EndpointSessionEstablished
		} else if observation.Session != EndpointSessionEstablished {
			observation.Session = EndpointSessionDialing
		}
		result[remote] = observation
	}
	return result
}

func (b *Bind) EndpointSessionState(endpoint netip.AddrPort) EndpointSessionState {
	endpoint = netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return EndpointSessionIdle
	}
	state.mu.Lock()
	configuredEndpoint := state.endpoints[endpoint]
	sessions := make([]*session, 0, 1)
	for _, candidate := range state.sessions {
		remote := candidate.currentRemoteAddr()
		remote = netip.AddrPortFrom(remote.Addr().Unmap(), remote.Port())
		if remote == endpoint {
			sessions = append(sessions, candidate)
		}
	}
	state.mu.Unlock()
	result := EndpointSessionIdle
	for _, session := range sessions {
		if session.closed.Load() {
			continue
		}
		session.mu.Lock()
		established := session.conn != nil
		session.mu.Unlock()
		if established {
			return EndpointSessionEstablished
		}
		result = EndpointSessionDialing
	}
	if result == EndpointSessionIdle && configuredEndpoint != nil {
		configuredEndpoint.mu.Lock()
		reconnecting := configuredEndpoint.reconnectScheduled
		configuredEndpoint.mu.Unlock()
		if reconnecting {
			return EndpointSessionReconnecting
		}
	}
	return result
}

func (b *Bind) EndpointReconnectStatus(endpoint netip.AddrPort) EndpointReconnectStatus {
	endpoint = netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port())
	b.mu.Lock()
	state := b.state
	b.mu.Unlock()
	if state == nil {
		return EndpointReconnectStatus{}
	}
	state.mu.Lock()
	ep := state.endpoints[endpoint]
	state.mu.Unlock()
	if ep == nil {
		return EndpointReconnectStatus{}
	}
	ep.mu.Lock()
	defer ep.mu.Unlock()
	result := EndpointReconnectStatus{
		Attempts:            ep.reconnectAttempts,
		Failures:            ep.reconnectFailures,
		ConsecutiveFailures: ep.consecutiveFailures,
	}
	if !ep.nextReconnect.IsZero() {
		result.NextReconnect = ep.nextReconnect.Unix()
	}
	return result
}
