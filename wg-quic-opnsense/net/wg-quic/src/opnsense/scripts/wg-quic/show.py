#!/usr/local/bin/python3

"""Return wg-quic interface and peer state without exposing secret keys."""

import json
import subprocess
import xml.etree.ElementTree as ET
from pathlib import Path

from wg_quic_status import classify_peer_status, derive_activity


WG_QUIC = "/usr/local/sbin/wg-quic"
RUN_DIR = Path("/var/run/wg-quic")
CONFIG_XML = "/conf/config.xml"


def text(node, name, default=""):
    value = node.findtext(name, default=default)
    return value.strip() if value else default


def endpoint(host, port):
    if not host:
        return ""
    if ":" in host and not host.startswith("["):
        host = f"[{host}]"
    return f"{host}:{port}"


def configured_records():
    tree = ET.parse(CONFIG_XML)
    clients = {}
    for client in tree.findall(
        "./OPNsense/wireguardquic/client/clients/client"
    ):
        uuid = client.get("uuid", "")
        if uuid:
            clients[uuid] = client

    records = {}
    for server in tree.findall(
        "./OPNsense/wireguardquic/server/servers/server"
    ):
        if text(server, "enabled", "0") != "1":
            continue
        instance = text(server, "instance")
        if not instance.isdigit():
            continue
        interface = f"quic{instance}"
        peers = []
        for uuid in filter(None, text(server, "peers").split(",")):
            client = clients.get(uuid)
            if client is None or text(client, "enabled", "0") != "1":
                continue
            host = text(client, "serveraddress")
            port = text(client, "serverport", "51820")
            peers.append({
                "if": interface,
                "type": "peer",
                "public-key": text(client, "pubkey"),
                "endpoint": endpoint(host, port),
                "allowed-ips": text(client, "tunneladdress"),
                "latest-handshake": 0,
                "last-rx": 0,
                "last-tx": 0,
                "last-activity": 0,
                "last-activity-direction": "",
                "reconnect-attempts": 0,
                "reconnect-failures": 0,
                "next-reconnect": 0,
                "transfer-rx": 0,
                "transfer-tx": 0,
                "persistent-keepalive": text(client, "keepalive"),
                "session": "idle",
                "peer-status": "offline",
            })
        records[interface] = {
            "interface": {
                "if": interface,
                "type": "interface",
                "public-key": text(server, "pubkey"),
                "listen-port": text(server, "port"),
                "endpoint": text(server, "port"),
                "status": "down",
                "transfer-rx": 0,
                "transfer-tx": 0,
            },
            "peers": peers,
        }
    return records


def interface_states():
    result = {}
    ifconfig = subprocess.run(
        ["/sbin/ifconfig"],
        capture_output=True,
        text=True,
    )
    for line in ifconfig.stdout.splitlines():
        if not line.startswith("\t") and "<" in line:
            name = line.split(":", 1)[0]
            flags = line.split("<", 1)[1].split(">", 1)[0].split(",")
            result[name] = "up" if "UP" in flags else "down"
    return result


result = {"status": "ok", "records": []}
try:
    configured = configured_records()
except (OSError, ET.ParseError):
    configured = {}

states = interface_states()
errors = []
for interface, records in sorted(configured.items()):
    interface_record = records["interface"]
    interface_record["status"] = states.get(interface, "down")
    socket = RUN_DIR / f"{interface}.sock"
    status = None
    if socket.exists():
        try:
            command = subprocess.run(
                [WG_QUIC, "show", interface, "--json"],
                capture_output=True,
                text=True,
                timeout=10,
            )
            if command.returncode != 0:
                errors.append(command.stderr.strip())
            else:
                status = json.loads(command.stdout)
        except (
            OSError,
            subprocess.SubprocessError,
            json.JSONDecodeError,
        ) as error:
            errors.append(str(error))

    runtime_peers = {}
    if status:
        interface_record.update({
            "listen-port": status.get(
                "listen_port",
                interface_record["listen-port"],
            ),
            "endpoint": status.get(
                "listen_port",
                interface_record["endpoint"],
            ),
            "carrier": status.get("carrier", ""),
            "fec-mode": status.get("fec_mode", ""),
            "obfs-mode": status.get("obfs_mode", ""),
        })
        if status.get("state") != "up":
            interface_record["status"] = "down"
        runtime_peers = {
            peer.get("public_key", ""): peer
            for peer in status.get("peers", [])
        }
        stats = status.get("stats", {})
        interface_record["transfer-tx"] = stats.get("wg_tx_bytes", 0)
        interface_record["transfer-rx"] = stats.get("wg_rx_bytes", 0)

    single_peer = len(records["peers"]) == 1
    for peer_record in records["peers"]:
        peer = runtime_peers.get(peer_record["public-key"], {})
        session = peer.get("session", "idle")
        latest_handshake = peer.get("latest_handshake", 0)
        last_rx = peer.get("last_rx", 0)
        last_tx = peer.get("last_tx", 0)
        last_activity, last_direction = derive_activity(peer)
        peer_record["session"] = session
        peer_record["latest-handshake"] = latest_handshake
        peer_record["last-rx"] = last_rx
        peer_record["last-tx"] = last_tx
        peer_record["last-activity"] = last_activity
        peer_record["last-activity-direction"] = last_direction
        peer_record["reconnect-attempts"] = peer.get(
            "reconnect_attempts",
            0,
        )
        peer_record["reconnect-failures"] = peer.get(
            "reconnect_failures",
            0,
        )
        peer_record["next-reconnect"] = peer.get("next_reconnect", 0)
        peer_record["transfer-tx"] = peer.get("transfer_tx", 0)
        peer_record["transfer-rx"] = peer.get("transfer_rx", 0)
        peer_record["peer-status"] = classify_peer_status(
            session,
            last_rx,
            latest_handshake,
        )
        if peer.get("endpoint"):
            peer_record["endpoint"] = peer["endpoint"]
        if (
            single_peer
            and status
            and not peer_record["transfer-tx"]
            and not peer_record["transfer-rx"]
        ):
            stats = status.get("stats", {})
            peer_record["transfer-tx"] = stats.get("wg_tx_bytes", 0)
            peer_record["transfer-rx"] = stats.get("wg_rx_bytes", 0)

    result["records"].append(interface_record)
    result["records"].extend(records["peers"])

if errors:
    result["status"] = "failed"
    result["error"] = "; ".join(filter(None, errors))

print(json.dumps(result))
