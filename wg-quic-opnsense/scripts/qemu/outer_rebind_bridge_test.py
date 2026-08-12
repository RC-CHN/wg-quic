import importlib.util
import ipaddress
import struct
import sys
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("outer_rebind_bridge.py")
SPEC = importlib.util.spec_from_file_location("outer_rebind_bridge", MODULE_PATH)
bridge = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
sys.modules[SPEC.name] = bridge
SPEC.loader.exec_module(bridge)


class OuterRebindBridgeTest(unittest.TestCase):
    def test_ipv4_udp_frame_has_valid_header_and_payload(self):
        source_mac = bytes.fromhex("525400510002")
        destination_mac = bytes.fromhex("525400510001")
        payload = b"wg-quic outer packet"
        frame = bridge.ipv4_udp_frame(
            source_mac,
            destination_mac,
            ipaddress.IPv4Address("198.18.0.2"),
            ipaddress.IPv4Address("198.18.0.1"),
            52821,
            52820,
            payload,
            7,
        )

        self.assertEqual(frame[:6], destination_mac)
        self.assertEqual(frame[6:12], source_mac)
        self.assertEqual(struct.unpack("!H", frame[12:14])[0], bridge.ETHERTYPE_IPV4)
        header = frame[14:34]
        self.assertEqual(bridge.internet_checksum(header), 0)
        self.assertEqual(ipaddress.IPv4Address(header[12:16]), ipaddress.IPv4Address("198.18.0.2"))
        self.assertEqual(ipaddress.IPv4Address(header[16:20]), ipaddress.IPv4Address("198.18.0.1"))
        source_port, destination_port, length, checksum = struct.unpack("!HHHH", frame[34:42])
        self.assertEqual((source_port, destination_port), (52821, 52820))
        self.assertEqual(length, 8 + len(payload))
        self.assertEqual(checksum, 0)
        self.assertEqual(frame[42:], payload)

    def test_arp_reply_claims_selected_source(self):
        source_mac = bytes.fromhex("525400510002")
        destination_mac = bytes.fromhex("525400510001")
        frame = bridge.arp_reply(
            source_mac,
            ipaddress.IPv4Address("198.18.0.3"),
            destination_mac,
            ipaddress.IPv4Address("198.18.0.1"),
        )

        self.assertEqual(frame[:6], destination_mac)
        self.assertEqual(struct.unpack("!H", frame[12:14])[0], bridge.ETHERTYPE_ARP)
        operation = struct.unpack("!H", frame[20:22])[0]
        self.assertEqual(operation, bridge.ARP_REPLY)
        self.assertEqual(frame[22:28], source_mac)
        self.assertEqual(ipaddress.IPv4Address(frame[28:32]), ipaddress.IPv4Address("198.18.0.3"))

    def test_parsers_reject_ambiguous_values(self):
        self.assertEqual(bridge.parse_address("127.0.0.1:53900"), ("127.0.0.1", 53900))
        self.assertEqual(bridge.parse_mac("52:54:00:51:00:01"), bytes.fromhex("525400510001"))
        with self.assertRaises(Exception):
            bridge.parse_address("53900")
        with self.assertRaises(Exception):
            bridge.parse_mac("52:54:00")


if __name__ == "__main__":
    unittest.main()
