import ipaddress
import re
import unittest
from pathlib import Path


PROJECT_DIR = Path(__file__).resolve().parents[2]


class FixtureContractsTest(unittest.TestCase):
    def read(self, relative_path):
        return (PROJECT_DIR / relative_path).read_text(encoding="utf-8")

    def test_host_fixture_exposes_an_address_pool(self):
        prepare = self.read("scripts/qemu/prepare-host-interop.sh")
        configure = self.read("scripts/qemu/configure-host-client.php")

        def argument(name):
            match = re.search(rf'--arg {name} "([^"]+)"', prepare)
            self.assertIsNotNone(match, f"missing {name}")
            return match.group(1)

        guest_address = ipaddress.ip_address(argument("guestAddress"))
        guest_tunnel = ipaddress.ip_interface(argument("guestTunnelAddress"))
        client_address = ipaddress.ip_address(argument("clientAddress"))

        self.assertIn('--arg guestTunnelAddress "10.77.0.1/24"', prepare)
        self.assertEqual(guest_tunnel.network.prefixlen, 24)
        self.assertEqual(guest_tunnel.ip, guest_address)
        self.assertIn(client_address, guest_tunnel.network)
        self.assertNotEqual(client_address, guest_address)
        self.assertIn("guestTunnelAddress: $guestTunnelAddress", prepare)
        self.assertIn("'guestTunnelAddress',", configure)
        self.assertIn(
            "$server->tunneladdress->setValue($payload['guestTunnelAddress']);",
            configure,
        )
        self.assertNotIn("$payload['guestAddress'] . '/32'", configure)

    def test_host_clients_can_remain_online_for_browser_verification(self):
        runner = self.read("scripts/qemu/run-host-interop.sh")
        client = self.read("scripts/qemu/linux-client/main.go")

        self.assertIn("WG_QUIC_HOST_INTEROP_HOLD_SECONDS", runner)
        self.assertIn('-hold "${WG_QUIC_HOST_INTEROP_HOLD_SECONDS}s"', runner)
        self.assertIn('flag.Duration("hold", 0,', client)
        self.assertIn("run(*configPath, *hold)", client)

    def test_log_page_exposes_notice_lifecycle_records(self):
        controller = self.read(
            "net/wg-quic/src/opnsense/mvc/app/controllers/OPNsense/"
            "WireguardQuic/LogController.php"
        )
        menu = self.read(
            "net/wg-quic/src/opnsense/mvc/app/models/OPNsense/"
            "WireguardQuic/Menu/Menu.xml"
        )
        browser = self.read("scripts/qemu/browser-connect.py")

        self.assertIn("default_log_severity = 'Notice'", controller)
        self.assertIn('url="/ui/wireguardquic/log"', menu)
        self.assertIn('browser.navigate("/ui/wireguardquic/log")', browser)
        self.assertIn("webui-client-log.png", browser)


if __name__ == "__main__":
    unittest.main()
