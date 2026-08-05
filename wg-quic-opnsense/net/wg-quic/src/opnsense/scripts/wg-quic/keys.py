#!/usr/local/bin/python3

"""
Generate wg-quic keys without passing secret material through a shell.
"""

import base64
import json
import secrets
import subprocess
import sys


def run(command, input_text=None):
    result = subprocess.run(
        command,
        input=input_text,
        capture_output=True,
        check=True,
        text=True,
    )
    return result.stdout.strip()


try:
    if len(sys.argv) == 2 and sys.argv[1] == "keypair":
        private_key = run(["/usr/local/sbin/wg-quic", "genkey"])
        public_key = run(
            ["/usr/local/sbin/wg-quic", "pubkey"],
            private_key + "\n",
        )
        print(json.dumps({
            "status": "ok",
            "privateKey": private_key,
            "publicKey": public_key,
        }))
    elif len(sys.argv) == 2 and sys.argv[1] == "psk":
        print(json.dumps({
            "status": "ok",
            "presharedKey": base64.b64encode(
                secrets.token_bytes(32)
            ).decode("ascii"),
        }))
    else:
        print(json.dumps({"status": "failed", "error": "invalid operation"}))
        sys.exit(64)
except (OSError, subprocess.SubprocessError) as error:
    print(json.dumps({"status": "failed", "error": str(error)}))
    sys.exit(1)
