import ipaddress
import importlib.util
import re
import unittest
from pathlib import Path


PROJECT_DIR = Path(__file__).resolve().parents[2]
STATUS_HELPER_PATH = (
    PROJECT_DIR
    / "net/wg-quic/src/opnsense/scripts/wg-quic/wg_quic_status.py"
)
STATUS_HELPER_SPEC = importlib.util.spec_from_file_location(
    "wg_quic_status",
    STATUS_HELPER_PATH,
)
STATUS_HELPER = importlib.util.module_from_spec(STATUS_HELPER_SPEC)
STATUS_HELPER_SPEC.loader.exec_module(STATUS_HELPER)


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
        self.assertIn("hold:                       *hold", client)

    def test_outer_rebind_fixture_is_idle_and_checks_autonomous_recovery(self):
        prepare = self.read("scripts/qemu/prepare-host-interop.sh")
        runner = self.read("scripts/qemu/run-outer-rebind.sh")
        guest = self.read("scripts/qemu/guest-outer-rebind.sh")
        client = self.read("scripts/qemu/linux-client/main.go")

        self.assertNotIn("PersistentKeepalive", prepare)
        self.assertIn("source 198.18.0.3", runner)
        self.assertIn("drop on", runner)
        self.assertIn("source 198.18.0.4", runner)
        self.assertIn("-require-autonomous-reconnect", runner)
        self.assertIn("reconnect_attempts=[1-9]", runner)
        self.assertIn('.peers[0].endpoint == $endpoint', guest)
        self.assertIn(".records[]", guest)
        self.assertIn('flag.Bool(\n\t\t"require-autonomous-reconnect"', client)

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

    def test_status_exposes_authenticated_peer_activity(self):
        show = self.read("net/wg-quic/src/opnsense/scripts/wg-quic/show.py")
        controller = self.read(
            "net/wg-quic/src/opnsense/mvc/app/controllers/OPNsense/"
            "WireguardQuic/Api/ServiceController.php"
        )
        view = self.read(
            "net/wg-quic/src/opnsense/mvc/app/views/OPNsense/"
            "WireguardQuic/status.volt"
        )
        widget = self.read(
            "net/wg-quic/src/opnsense/www/js/widgets/WireguardQuic.js"
        )

        self.assertIn("classify_peer_status(", show)
        self.assertIn("derive_activity(peer)", show)
        self.assertIn("'last-activity'", controller)
        self.assertIn('data-column-id="last-activity-epoch"', view)
        self.assertIn('data-column-id="latest-handshake-epoch"', view)
        self.assertIn("row.endpoint", widget)
        self.assertIn("row['last-activity-epoch']", widget)

    def test_quic_session_alone_does_not_mark_peer_online(self):
        classify = STATUS_HELPER.classify_peer_status

        self.assertEqual(classify("established", 0, 0, now=1000), "stale")
        self.assertEqual(classify("dialing", 0, 0, now=1000), "stale")
        self.assertEqual(classify("idle", 0, 0, now=1000), "offline")
        self.assertEqual(classify("idle", 900, 0, now=1000), "online")
        self.assertEqual(classify("idle", 600, 0, now=1000), "stale")
        self.assertEqual(classify("idle", 0, 900, now=1000), "online")

    def test_status_exposes_automatic_reconnect_progress(self):
        show = self.read("net/wg-quic/src/opnsense/scripts/wg-quic/show.py")
        controller = self.read(
            "net/wg-quic/src/opnsense/mvc/app/controllers/OPNsense/"
            "WireguardQuic/Api/ServiceController.php"
        )
        view = self.read(
            "net/wg-quic/src/opnsense/mvc/app/views/OPNsense/"
            "WireguardQuic/status.volt"
        )

        self.assertIn('"reconnect_attempts",', show)
        self.assertIn('"reconnect_failures",', show)
        self.assertIn('peer.get("next_reconnect", 0)', show)
        self.assertIn("'next-reconnect'", controller)
        self.assertIn('data-column-id="reconnect-attempts"', view)
        self.assertIn('data-column-id="reconnect-failures"', view)
        self.assertIn("row['next-reconnect-epoch']", view)

    def test_activity_preserves_direction_and_old_core_fallback(self):
        derive = STATUS_HELPER.derive_activity

        self.assertEqual(
            derive({"last_rx": 20, "last_tx": 10}),
            (20, "received"),
        )
        self.assertEqual(
            derive({"last_rx": 10, "last_tx": 20}),
            (20, "sent"),
        )
        self.assertEqual(
            derive({"latest_handshake": 30}),
            (30, "received"),
        )


if __name__ == "__main__":
    unittest.main()
