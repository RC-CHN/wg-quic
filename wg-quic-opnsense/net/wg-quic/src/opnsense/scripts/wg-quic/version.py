#!/usr/local/bin/python3

import json
import subprocess


try:
    result = subprocess.run(
        ["/usr/local/sbin/wg-quic", "--version"],
        capture_output=True,
        check=True,
        text=True,
    )
    version = result.stdout.splitlines()[0] if result.stdout else ""
    print(json.dumps({"status": "ok", "version": version}))
except (OSError, subprocess.SubprocessError) as error:
    print(json.dumps({"status": "failed", "error": str(error)}))
