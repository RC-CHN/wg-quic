#!/usr/bin/env python3

"""
Exercise the complete WebUI peer-generator workflow against a disposable
OPNsense VM. The provision phase stores the generated peer and writes only the
client-side values needed by the local wg-quic harness. The verify phase
checks the resulting online state in Status and the Dashboard widget.
"""

import argparse
import base64
import json
import os
import time
import urllib.request
from pathlib import Path


class Browser:
    def __init__(self, driver_url, base_url):
        self.driver_url = driver_url.rstrip("/")
        self.base_url = base_url.rstrip("/")
        self.session_id = None
        session = self.request(
            "POST",
            "/session",
            {
                "capabilities": {
                    "alwaysMatch": {
                        "browserName": "firefox",
                        "acceptInsecureCerts": True,
                        "moz:firefoxOptions": {"args": ["-headless"]},
                    }
                }
            },
        )
        self.session_id = session["sessionId"]
        self.request(
            "POST",
            f"/session/{self.session_id}/window/rect",
            {"width": 1440, "height": 1100},
        )

    @property
    def prefix(self):
        return f"/session/{self.session_id}"

    def request(self, method, path, payload=None):
        data = None if payload is None else json.dumps(payload).encode()
        request = urllib.request.Request(
            self.driver_url + path,
            data=data,
            method=method,
            headers={"Content-Type": "application/json"},
        )
        with urllib.request.urlopen(request, timeout=30) as response:
            result = json.load(response)
        value = result.get("value")
        if isinstance(value, dict) and value.get("error"):
            raise RuntimeError(value.get("message", value["error"]))
        return value

    def execute(self, script, arguments=None):
        return self.request(
            "POST",
            self.prefix + "/execute/sync",
            {"script": script, "args": arguments or []},
        )

    def navigate(self, path):
        self.request("POST", self.prefix + "/url", {"url": self.base_url + path})
        self.wait(
            "return document.readyState === 'complete'",
            f"page did not load: {path}",
        )
        time.sleep(1)

    def wait(self, script, message, timeout=30, arguments=None):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                if self.execute(script, arguments):
                    return
            except Exception:
                pass
            time.sleep(0.25)
        raise RuntimeError(message)

    def click(self, selector):
        clicked = self.execute(
            """
            const element = document.querySelector(arguments[0]);
            if (!element) return false;
            element.click();
            return true;
            """,
            [selector],
        )
        if not clicked:
            raise RuntimeError(f"element not found: {selector}")

    def screenshot(self, path):
        encoded = self.request("GET", self.prefix + "/screenshot")
        Path(path).write_bytes(base64.b64decode(encoded))

    def login(self, username, password):
        self.navigate("/")
        self.execute(
            """
            document.querySelector('[name="usernamefld"]').value = arguments[0];
            document.querySelector('[name="passwordfld"]').value = arguments[1];
            document.querySelector('[name="login"]').click();
            """,
            [username, password],
        )
        self.wait(
            "return document.body && document.body.innerText.includes('Dashboard')",
            "WebUI login failed",
        )

    def close(self):
        if self.session_id is not None:
            try:
                self.request("DELETE", self.prefix)
            finally:
                self.session_id = None


def provision(browser, arguments):
    browser.navigate("/ui/wireguardquic/general#configbuilder")
    browser.wait(
        """
        const privateKey = document.getElementById('configbuilder.privkey');
        const address = document.getElementById('configbuilder.address');
        const endpoint = $('#configbuilder\\\\.endpoint');
        return privateKey && privateKey.value.length === 44 &&
            address && address.value.length > 0 &&
            endpoint.data('pubkey');
        """,
        "Peer generator did not initialise",
    )
    browser.execute(
        """
        const setValue = (id, value) => {
            const element = document.getElementById(id);
            $(element).val(value).trigger('change');
        };
        setValue('configbuilder.name', arguments[0]);
        setValue('configbuilder.endpoint', arguments[1]);
        setValue('configbuilder.keepalive', '1');
        """,
        [arguments.peer_name, arguments.endpoint],
    )
    browser.wait(
        """
        const output = document.getElementById('configbuilder.output').value;
        return output.includes('Endpoint = ' + arguments[0]) &&
            output.includes('PrivateKey = ');
        """,
        "generated configuration did not update",
        arguments=[arguments.endpoint],
    )
    values = browser.execute(
        """
        const value = id => document.getElementById(id).value;
        return {
            guestPublicKey: $('#configbuilder\\\\.endpoint').data('pubkey'),
            clientPrivateKey: value('configbuilder.privkey'),
            clientPublicKey: value('configbuilder.pubkey'),
            clientAddress: value('configbuilder.address').split(',')[0].split('/')[0],
            config: value('configbuilder.output')
        };
        """
    )
    if not all(values.get(key) for key in (
        "guestPublicKey",
        "clientPrivateKey",
        "clientPublicKey",
        "clientAddress",
    )):
        raise RuntimeError("Peer generator returned an incomplete client configuration")

    browser.screenshot(arguments.output_dir / "webui-peer-generator.png")
    original_public_key = values["clientPublicKey"]
    browser.click("#btn_configbuilder_save")
    browser.wait(
        """
        const publicKey = document.getElementById('configbuilder.pubkey');
        return publicKey && publicKey.value.length === 44 &&
            publicKey.value !== arguments[0];
        """,
        "Store and generate next did not complete",
        timeout=45,
        arguments=[original_public_key],
    )

    browser.navigate("/ui/wireguardquic/general#peers")
    browser.wait(
        "return document.body.innerText.includes(arguments[0])",
        "stored peer did not appear in the Peers grid",
        arguments=[arguments.peer_name],
    )
    browser.screenshot(arguments.output_dir / "webui-generated-peer.png")

    browser.click("#reconfigureAct")
    browser.wait(
        """
        const button = document.querySelector('#reconfigureAct');
        return button && !button.disabled &&
            !document.querySelector('.alert-danger');
        """,
        "Apply action did not complete cleanly",
        timeout=45,
    )
    time.sleep(3)

    payload = {
        "guestPrivateKey": "",
        "guestPublicKey": values["guestPublicKey"],
        "clientPrivateKey": values["clientPrivateKey"],
        "clientPublicKey": values["clientPublicKey"],
        "guestAddress": arguments.guest_address,
        "clientAddress": values["clientAddress"],
        "endpoint": arguments.endpoint,
    }
    arguments.config.parent.mkdir(parents=True, exist_ok=True)
    arguments.config.write_text(
        json.dumps(payload, indent=2) + "\n",
        encoding="utf-8",
    )
    os.chmod(arguments.config, 0o600)
    print(f"WEBUI PROVISION PASSED: {arguments.peer_name} -> {arguments.config}")


def ensure_wg_quic_widget(browser):
    if browser.execute(
        """
        return Array.from(document.querySelectorAll('.widgetdiv, [id^="widget-"]'))
            .some(element => element.innerText.includes(arguments[0]));
        """,
        ["wg-quic"],
    ):
        return
    browser.click("#add_widget")
    browser.wait(
        "return document.querySelector('#widget-selection') !== null",
        "Dashboard widget picker did not open",
    )
    browser.execute(
        """
        const select = document.querySelector('#widget-selection');
        for (const option of select.options) {
            option.selected = option.value === 'WireguardQuic';
        }
        $(select).selectpicker('refresh').trigger('change');
        """
    )
    browser.click(".modal.in .modal-footer button:first-child")


def verify(browser, arguments):
    browser.navigate("/ui/wireguardquic/status")
    browser.wait(
        "return document.body.innerText.includes(arguments[0])",
        "generated peer did not appear in Status",
        timeout=45,
        arguments=[arguments.peer_name],
    )
    status = browser.execute(
        """
        const grid = document.getElementById('grid-wireguardquic-status');
        const row = grid
            ? Array.from(grid.querySelectorAll('.tabulator-row, tr'))
                .find(item => item.innerText.includes(arguments[0]))
            : null;
        return row ? {text: row.innerText, html: row.innerHTML} : null;
        """,
        [arguments.peer_name],
    )
    if status is None or "text-success" not in status["html"]:
        diagnostics = browser.execute(
            """
            const describe = element => ({
                tag: element.tagName,
                id: element.id,
                className: element.className,
                text: element.innerText
            });
            return {
                url: window.location.href,
                gridIds: Array.from(document.querySelectorAll('[id]'))
                    .map(element => element.id)
                    .filter(id => id.includes('wireguard') || id.includes('status')),
                matches: Array.from(document.querySelectorAll('tr, td, div, span'))
                    .filter(element => element.innerText &&
                        element.innerText.includes(arguments[0]))
                    .slice(-12)
                    .map(describe)
            };
            """,
            [arguments.peer_name],
        )
        browser.screenshot(arguments.output_dir / "webui-client-status-failure.png")
        raise RuntimeError(
            f"generated peer is not online in Status: {status}; {diagnostics}"
        )
    if "B" not in status["text"]:
        raise RuntimeError(f"generated peer has no visible transfer counters: {status}")
    browser.screenshot(arguments.output_dir / "webui-client-status.png")

    browser.navigate("/ui/core/dashboard")
    ensure_wg_quic_widget(browser)
    browser.wait(
        "return document.body.innerText.includes(arguments[0])",
        "generated peer did not appear in the Dashboard widget",
        timeout=45,
        arguments=[arguments.peer_name],
    )
    widget = browser.execute(
        """
        const link = Array.from(document.querySelectorAll('a'))
            .find(item => item.innerText.includes(arguments[0]));
        const root = link ? link.closest('.widgetdiv') || link.parentElement.parentElement : null;
        return root ? {text: root.innerText, html: root.innerHTML} : null;
        """,
        [arguments.peer_name],
    )
    if widget is None or "text-success" not in widget["html"]:
        raise RuntimeError(f"generated peer is not online in Dashboard: {widget}")
    browser.screenshot(arguments.output_dir / "webui-client-dashboard.png")
    print(f"WEBUI VERIFY PASSED: {arguments.peer_name} is online with traffic")


def parse_arguments():
    parser = argparse.ArgumentParser()
    parser.add_argument("phase", choices=("provision", "verify"))
    parser.add_argument("--driver-url", default="http://127.0.0.1:4444")
    parser.add_argument("--base-url", default="https://127.0.0.1:8447")
    parser.add_argument("--username", default="root")
    parser.add_argument("--password", default="opnsense")
    parser.add_argument("--peer-name", default="webui-local-client")
    parser.add_argument("--endpoint", default="127.0.0.1:51820")
    parser.add_argument("--guest-address", default="10.66.0.1")
    parser.add_argument(
        "--config",
        type=Path,
        default=Path(".qemu/webui-client.json"),
    )
    parser.add_argument(
        "--output-dir",
        type=Path,
        default=Path(".qemu/ui-shots"),
    )
    arguments = parser.parse_args()
    arguments.output_dir.mkdir(parents=True, exist_ok=True)
    return arguments


def main():
    arguments = parse_arguments()
    browser = Browser(arguments.driver_url, arguments.base_url)
    try:
        browser.login(arguments.username, arguments.password)
        if arguments.phase == "provision":
            provision(browser, arguments)
        else:
            verify(browser, arguments)
    finally:
        browser.close()


if __name__ == "__main__":
    main()
