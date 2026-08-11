import argparse
import base64
import contextlib
import importlib.util
import io
import stat
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock


MODULE_PATH = Path(__file__).with_name("browser-connect.py")
SPEC = importlib.util.spec_from_file_location("browser_connect", MODULE_PATH)
browser_connect = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(browser_connect)


class FakeBrowser:
    def __init__(self, values):
        self.values = values
        self.executions = []

    def navigate(self, path):
        pass

    def wait(self, script, message, timeout=30, arguments=None):
        pass

    def execute(self, script, arguments=None):
        self.executions.append((script, arguments))
        if "clientPrivateKey:" in script:
            return self.values
        return None

    def screenshot(self, path):
        pass

    def click(self, selector):
        pass


class BrowserConnectTest(unittest.TestCase):
    def test_safe_qemu_defaults(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            output_dir = Path(temporary_directory) / "shots"
            arguments = browser_connect.parse_arguments(
                ["provision", "--output-dir", str(output_dir)],
                environ={browser_connect.PASSWORD_ENV: "from-environment"},
            )

            self.assertEqual(arguments.base_url, "https://127.0.0.1:10443")
            self.assertEqual(arguments.endpoint, "127.0.0.1:52820")
            self.assertEqual(arguments.guest_address, "10.77.0.1")
            self.assertEqual(arguments.config, Path(".qemu/webui-client.conf"))
            self.assertEqual(arguments.password, "from-environment")
            self.assertEqual(stat.S_IMODE(output_dir.stat().st_mode), 0o700)

    def test_password_sources(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            password_file = Path(temporary_directory) / "password"
            password_file.write_text("from-file\n", encoding="utf-8")

            from_file = types.SimpleNamespace(
                password_file=password_file,
                password=None,
            )
            from_cli = types.SimpleNamespace(
                password_file=None,
                password="legacy-cli",
            )
            from_environment = types.SimpleNamespace(
                password_file=None,
                password=None,
            )

            self.assertEqual(
                browser_connect.resolve_password(
                    from_file,
                    {browser_connect.PASSWORD_ENV: "from-environment"},
                ),
                "from-file",
            )
            self.assertEqual(
                browser_connect.resolve_password(from_cli, {}),
                "legacy-cli",
            )
            self.assertEqual(
                browser_connect.resolve_password(
                    from_cli,
                    {browser_connect.PASSWORD_ENV: "from-environment"},
                ),
                "from-environment",
            )
            self.assertEqual(
                browser_connect.resolve_password(
                    from_environment,
                    {browser_connect.PASSWORD_ENV: "from-environment"},
                ),
                "from-environment",
            )
            with self.assertRaisesRegex(RuntimeError, browser_connect.PASSWORD_ENV):
                browser_connect.resolve_password(from_environment, {})

            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                browser_connect.parse_arguments(
                    [
                        "verify",
                        "--output-dir",
                        str(Path(temporary_directory) / "missing-password"),
                    ],
                    environ={},
                )

    def test_generated_config_is_ini_and_private(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            config_path = Path(temporary_directory) / "client.conf"
            config_path.write_text("old", encoding="utf-8")
            config_path.chmod(0o644)
            content = (
                "[Interface]\n"
                "PrivateKey = client-key\n\n"
                "[Peer]\n"
                "PublicKey = server-key\n"
                "AllowedIPs = 10.77.0.1/32\n"
            )

            browser_connect.write_generated_config(config_path, content)

            self.assertEqual(config_path.read_text(encoding="utf-8"), content)
            self.assertEqual(stat.S_IMODE(config_path.stat().st_mode), 0o600)

    def test_generated_config_rejects_non_ini_payload(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            with self.assertRaisesRegex(RuntimeError, "wg-quick"):
                browser_connect.write_generated_config(
                    Path(temporary_directory) / "client.conf",
                    '{"clientPrivateKey":"secret"}',
                )

    def test_screenshot_directory_and_file_are_private(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            output_dir = Path(temporary_directory) / "shots"
            output_dir.mkdir(mode=0o755)
            screenshot_path = output_dir / "peer-generator.png"
            screenshot_path.write_bytes(b"old image")
            screenshot_path.chmod(0o644)
            browser = browser_connect.Browser.__new__(browser_connect.Browser)
            browser.session_id = "test-session"
            browser.request = lambda method, path: base64.b64encode(b"image").decode()

            browser.screenshot(screenshot_path)

            self.assertEqual(screenshot_path.read_bytes(), b"image")
            self.assertEqual(stat.S_IMODE(output_dir.stat().st_mode), 0o700)
            self.assertEqual(stat.S_IMODE(screenshot_path.stat().st_mode), 0o600)

    def test_provision_limits_allowed_ips_and_writes_generated_profile(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            config_path = Path(temporary_directory) / "webui-client.conf"
            generated = (
                "[Interface]\n"
                "PrivateKey = client-private\n"
                "Address = 10.77.0.3/32\n\n"
                "[Peer]\n"
                "PublicKey = guest-public\n"
                "Endpoint = 127.0.0.1:52820\n"
                "AllowedIPs = 10.77.0.1/32\n"
            )
            browser = FakeBrowser({
                "guestPublicKey": "guest-public",
                "clientPrivateKey": "client-private",
                "clientPublicKey": "client-public",
                "clientAddress": "10.77.0.3",
                "config": generated,
            })
            arguments = types.SimpleNamespace(
                peer_name="webui-local-client",
                endpoint="127.0.0.1:52820",
                guest_address="10.77.0.1",
                output_dir=Path(temporary_directory) / "shots",
                config=config_path,
            )

            with mock.patch.object(browser_connect.time, "sleep"), \
                    contextlib.redirect_stdout(io.StringIO()):
                browser_connect.provision(browser, arguments)

            setup_script, setup_arguments = browser.executions[0]
            self.assertIn("configbuilder.tunneladdress", setup_script)
            self.assertEqual(
                setup_arguments,
                ["webui-local-client", "127.0.0.1:52820", "10.77.0.1"],
            )
            self.assertEqual(config_path.read_text(encoding="utf-8"), generated)
            self.assertEqual(stat.S_IMODE(config_path.stat().st_mode), 0o600)

    def test_guest_address_requires_ipv4(self):
        self.assertEqual(browser_connect.ipv4_address("192.168.1.1"), "192.168.1.1")
        with self.assertRaises(argparse.ArgumentTypeError):
            browser_connect.ipv4_address("2001:db8::1")


if __name__ == "__main__":
    unittest.main()
