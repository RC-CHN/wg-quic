#!/usr/bin/env python3

"""Unprivileged Ethernet/UDP bridge for the OPNsense outer-rebind fixture.

QEMU's dgram backend exchanges complete Ethernet frames over UDP.  This
bridge presents a small IPv4 host on that link and forwards one UDP socket to
the wg-quic listener in the guest.  Changing the selected source address
therefore looks like a real NAT/public-address change to OPNsense without
requiring a TAP device or host privileges.
"""

from __future__ import annotations

import argparse
import ipaddress
import json
import os
import selectors
import signal
import socket
import struct
from dataclasses import asdict, dataclass
from pathlib import Path


ETHERTYPE_IPV4 = 0x0800
ETHERTYPE_ARP = 0x0806
ARP_REQUEST = 1
ARP_REPLY = 2
IPPROTO_UDP = 17


def parse_address(value: str) -> tuple[str, int]:
    host, separator, port = value.rpartition(":")
    if not separator or not host:
        raise argparse.ArgumentTypeError("address must be HOST:PORT")
    try:
        parsed_port = int(port)
    except ValueError as error:
        raise argparse.ArgumentTypeError("port must be an integer") from error
    if not 1 <= parsed_port <= 65535:
        raise argparse.ArgumentTypeError("port must be between 1 and 65535")
    return host, parsed_port


def parse_mac(value: str) -> bytes:
    pieces = value.split(":")
    if len(pieces) != 6:
        raise argparse.ArgumentTypeError("MAC address must contain six octets")
    try:
        result = bytes(int(piece, 16) for piece in pieces)
    except ValueError as error:
        raise argparse.ArgumentTypeError("invalid MAC address") from error
    if any(len(piece) != 2 for piece in pieces):
        raise argparse.ArgumentTypeError("MAC octets must use two hex digits")
    return result


def internet_checksum(payload: bytes) -> int:
    if len(payload) % 2:
        payload += b"\x00"
    words = struct.unpack(f"!{len(payload) // 2}H", payload)
    total = sum(words)
    while total >> 16:
        total = (total & 0xFFFF) + (total >> 16)
    return (~total) & 0xFFFF


def ipv4_udp_frame(
    source_mac: bytes,
    destination_mac: bytes,
    source_ip: ipaddress.IPv4Address,
    destination_ip: ipaddress.IPv4Address,
    source_port: int,
    destination_port: int,
    payload: bytes,
    identification: int,
) -> bytes:
    udp_length = 8 + len(payload)
    udp = struct.pack(
        "!HHHH", source_port, destination_port, udp_length, 0
    ) + payload
    total_length = 20 + len(udp)
    header_without_checksum = struct.pack(
        "!BBHHHBBH4s4s",
        0x45,
        0,
        total_length,
        identification & 0xFFFF,
        0x4000,
        64,
        IPPROTO_UDP,
        0,
        source_ip.packed,
        destination_ip.packed,
    )
    header = header_without_checksum[:10] + struct.pack(
        "!H", internet_checksum(header_without_checksum)
    ) + header_without_checksum[12:]
    ethernet = destination_mac + source_mac + struct.pack("!H", ETHERTYPE_IPV4)
    return ethernet + header + udp


def arp_reply(
    source_mac: bytes,
    source_ip: ipaddress.IPv4Address,
    destination_mac: bytes,
    destination_ip: ipaddress.IPv4Address,
) -> bytes:
    ethernet = destination_mac + source_mac + struct.pack("!H", ETHERTYPE_ARP)
    arp = struct.pack(
        "!HHBBH6s4s6s4s",
        1,
        ETHERTYPE_IPV4,
        6,
        4,
        ARP_REPLY,
        source_mac,
        source_ip.packed,
        destination_mac,
        destination_ip.packed,
    )
    return ethernet + arp


@dataclass
class BridgeStatus:
    source: str
    dropping: bool = False
    client_packets: int = 0
    guest_packets: int = 0
    dropped_packets: int = 0
    arp_replies: int = 0


class Bridge:
    def __init__(self, arguments: argparse.Namespace):
        self.guest_ip = ipaddress.IPv4Address(arguments.guest_ip)
        self.source_ips = {
            ipaddress.IPv4Address(value) for value in arguments.source_ip
        }
        initial_source = ipaddress.IPv4Address(arguments.source_ip[0])
        self.status = BridgeStatus(source=str(initial_source))
        self.current_source = initial_source
        self.host_mac = arguments.host_mac
        self.guest_mac = arguments.guest_mac
        self.guest_port = arguments.guest_port
        self.identification = 0
        self.client_address: tuple[str, int] | None = None
        self.control_path = Path(arguments.control_socket)
        self.selector = selectors.DefaultSelector()
        self.running = True

        self.ethernet = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.ethernet.bind(arguments.ethernet_listen)
        self.ethernet.connect(arguments.qemu_address)
        self.ethernet.setblocking(False)
        self.selector.register(self.ethernet, selectors.EVENT_READ, self.read_ethernet)

        self.client = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.client.bind(arguments.udp_listen)
        self.client.setblocking(False)
        self.selector.register(self.client, selectors.EVENT_READ, self.read_client)

        self.control_path.parent.mkdir(parents=True, exist_ok=True)
        self.control_path.unlink(missing_ok=True)
        self.control = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.control.bind(str(self.control_path))
        os.chmod(self.control_path, 0o600)
        self.control.listen(4)
        self.control.setblocking(False)
        self.selector.register(self.control, selectors.EVENT_READ, self.accept_control)

    def close(self) -> None:
        for connection in (self.control, self.client, self.ethernet):
            try:
                self.selector.unregister(connection)
            except (KeyError, ValueError):
                pass
            connection.close()
        self.selector.close()
        self.control_path.unlink(missing_ok=True)

    def read_client(self, connection: socket.socket) -> None:
        payload, address = connection.recvfrom(65535)
        self.client_address = address
        self.status.client_packets += 1
        if self.status.dropping:
            self.status.dropped_packets += 1
            return
        self.identification += 1
        frame = ipv4_udp_frame(
            self.host_mac,
            self.guest_mac,
            self.current_source,
            self.guest_ip,
            address[1],
            self.guest_port,
            payload,
            self.identification,
        )
        self.ethernet.send(frame)

    def read_ethernet(self, connection: socket.socket) -> None:
        frame = connection.recv(65535)
        if len(frame) < 14:
            return
        destination_mac, source_mac, ethertype = struct.unpack("!6s6sH", frame[:14])
        if ethertype == ETHERTYPE_ARP:
            self.handle_arp(frame, source_mac)
            return
        if ethertype != ETHERTYPE_IPV4 or destination_mac != self.host_mac:
            return
        if self.status.dropping:
            self.status.dropped_packets += 1
            return
        if len(frame) < 34 or frame[14] >> 4 != 4:
            return
        header_length = (frame[14] & 0x0F) * 4
        if header_length < 20 or len(frame) < 14 + header_length + 8:
            return
        if frame[23] != IPPROTO_UDP:
            return
        destination_ip = ipaddress.IPv4Address(frame[30:34])
        if destination_ip != self.current_source:
            return
        udp_offset = 14 + header_length
        source_port, destination_port, udp_length, _ = struct.unpack(
            "!HHHH", frame[udp_offset : udp_offset + 8]
        )
        if (
            source_port != self.guest_port
            or self.client_address is None
            or destination_port != self.client_address[1]
            or udp_length < 8
            or udp_offset + udp_length > len(frame)
        ):
            return
        payload = frame[udp_offset + 8 : udp_offset + udp_length]
        self.client.sendto(payload, self.client_address)
        self.status.guest_packets += 1

    def handle_arp(self, frame: bytes, source_mac: bytes) -> None:
        if len(frame) < 42:
            return
        hardware, protocol, hardware_size, protocol_size, operation = struct.unpack(
            "!HHBBH", frame[14:22]
        )
        if (
            hardware != 1
            or protocol != ETHERTYPE_IPV4
            or hardware_size != 6
            or protocol_size != 4
            or operation != ARP_REQUEST
        ):
            return
        sender_ip = ipaddress.IPv4Address(frame[28:32])
        target_ip = ipaddress.IPv4Address(frame[38:42])
        if target_ip != self.current_source:
            return
        self.ethernet.send(
            arp_reply(self.host_mac, target_ip, source_mac, sender_ip)
        )
        self.status.arp_replies += 1

    def accept_control(self, connection: socket.socket) -> None:
        client, _ = connection.accept()
        client.settimeout(2)
        try:
            command = client.recv(4096).decode("utf-8").strip()
            response = self.handle_control(command)
        except Exception as error:  # Return diagnostics to the local fixture caller.
            response = {"ok": False, "error": str(error)}
        client.sendall(json.dumps(response, sort_keys=True).encode("utf-8") + b"\n")
        client.close()

    def handle_control(self, command: str) -> dict[str, object]:
        pieces = command.split()
        if pieces == ["status"]:
            return {"ok": True, **asdict(self.status)}
        if pieces == ["quit"]:
            self.running = False
            return {"ok": True, **asdict(self.status)}
        if len(pieces) == 2 and pieces[0] == "drop" and pieces[1] in {"on", "off"}:
            self.status.dropping = pieces[1] == "on"
            return {"ok": True, **asdict(self.status)}
        if len(pieces) == 2 and pieces[0] == "source":
            candidate = ipaddress.IPv4Address(pieces[1])
            if candidate not in self.source_ips:
                raise ValueError(f"source address {candidate} was not configured")
            self.current_source = candidate
            self.status.source = str(candidate)
            return {"ok": True, **asdict(self.status)}
        raise ValueError("command must be status, quit, drop on|off, or source IP")

    def run(self) -> None:
        try:
            while self.running:
                for key, _ in self.selector.select(timeout=1):
                    key.data(key.fileobj)
        finally:
            self.close()


def control(path: str, command: str) -> None:
    connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    connection.settimeout(3)
    connection.connect(path)
    connection.sendall(command.encode("utf-8") + b"\n")
    response = connection.recv(65535)
    connection.close()
    print(response.decode("utf-8").strip())
    parsed = json.loads(response)
    if not parsed.get("ok"):
        raise SystemExit(1)


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser()
    subparsers = result.add_subparsers(dest="action", required=True)

    run_parser = subparsers.add_parser("run")
    run_parser.add_argument("--ethernet-listen", type=parse_address, required=True)
    run_parser.add_argument("--qemu-address", type=parse_address, required=True)
    run_parser.add_argument("--udp-listen", type=parse_address, required=True)
    run_parser.add_argument("--control-socket", required=True)
    run_parser.add_argument("--guest-ip", default="198.18.0.1")
    run_parser.add_argument("--guest-port", type=int, default=52820)
    run_parser.add_argument(
        "--source-ip",
        action="append",
        required=True,
        help="allowed virtual source IP; the first is active initially",
    )
    run_parser.add_argument("--host-mac", type=parse_mac, default=parse_mac("52:54:00:51:00:02"))
    run_parser.add_argument("--guest-mac", type=parse_mac, default=parse_mac("52:54:00:51:00:01"))

    control_parser = subparsers.add_parser("control")
    control_parser.add_argument("--control-socket", required=True)
    control_parser.add_argument("command", nargs="+")
    return result


def main() -> None:
    arguments = parser().parse_args()
    if arguments.action == "control":
        control(arguments.control_socket, " ".join(arguments.command))
        return
    bridge = Bridge(arguments)
    signal.signal(signal.SIGTERM, lambda _signum, _frame: setattr(bridge, "running", False))
    signal.signal(signal.SIGINT, lambda _signum, _frame: setattr(bridge, "running", False))
    bridge.run()


if __name__ == "__main__":
    main()
