"""Pure helpers for deriving peer activity and liveness in the Web UI."""

import time


def derive_activity(peer):
    """Return (timestamp, direction) while tolerating older core schemas."""
    last_rx = int(peer.get("last_rx", 0) or 0)
    last_tx = int(peer.get("last_tx", 0) or 0)
    last_activity = int(peer.get("last_activity", 0) or 0)
    direction = peer.get("last_activity_direction", "")
    if direction not in ("received", "sent"):
        direction = ""

    if last_activity:
        if not direction:
            direction = "received" if last_rx >= last_tx else "sent"
        return last_activity, direction
    if last_rx >= last_tx and last_rx:
        return last_rx, "received"
    if last_tx:
        return last_tx, "sent"

    latest_handshake = int(peer.get("latest_handshake", 0) or 0)
    if latest_handshake:
        return latest_handshake, "received"
    return 0, ""


def classify_peer_status(session, last_rx, latest_handshake, now=None):
    """Classify WireGuard liveness without treating QUIC as authentication."""
    current_time = int(time.time()) if now is None else int(now)
    last_seen = int(last_rx or latest_handshake or 0)
    if last_seen:
        age = max(0, current_time - last_seen)
        return "online" if age <= 300 else "stale"
    if session in ("dialing", "established"):
        return "stale"
    return "offline"
