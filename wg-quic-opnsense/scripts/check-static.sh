#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(CDPATH='' cd -- "${script_dir}/.." && pwd)
monorepo_dir=$(CDPATH='' cd -- "${project_dir}/.." && pwd)
plugin_dir="${project_dir}/net/wg-quic"

find "${plugin_dir}" -name '*.xml' -type f -print0 |
    xargs -0 -n1 xmllint --noout

find "${plugin_dir}" -name '*.py' -type f -print0 |
    xargs -0 -n1 env PYTHONPYCACHEPREFIX=/tmp/wg-quic-pycache python3 -m py_compile

env PYTHONPYCACHEPREFIX=/tmp/wg-quic-pycache \
    python3 -m py_compile "${project_dir}/scripts/qemu/browser-connect.py"
env PYTHONPYCACHEPREFIX=/tmp/wg-quic-pycache \
    python3 -m py_compile "${project_dir}/scripts/qemu/outer_rebind_bridge.py"
env PYTHONPYCACHEPREFIX=/tmp/wg-quic-pycache \
    python3 -m unittest discover \
        -s "${project_dir}/scripts/qemu" \
        -p '*_test.py'

(
    cd "${monorepo_dir}"
    GOCACHE=/tmp/wg-quic-opnsense-go-test-cache \
        go test ./wg-quic-opnsense/scripts/qemu/linux-client
)

"${project_dir}/scripts/qemu/run_host_interop_test.sh"

# Fail the check if any template's rendered JavaScript is invalid. Unlike
# find -exec, subprocess.run(check=True) propagates a child syntax failure.
python3 - "${plugin_dir}" <<'PYCODE'
from pathlib import Path
import re
import subprocess
import sys
for source in Path(sys.argv[1]).rglob('*.js'):
    subprocess.run(['node', '--check', '--input-type=module'], input=source.read_text(), text=True, check=True)
for source in Path(sys.argv[1]).rglob('*.volt'):
    for script in re.findall(r'<script>(.*?)</script>', source.read_text(), re.S):
        rendered = re.sub(r'{{.*?}}', 'TRANSLATED', script, flags=re.S)
        subprocess.run(['node', '--check'], input=rendered, text=True, check=True)
PYCODE

shellcheck "${project_dir}/scripts/build-wg-quic.sh"
shellcheck "${project_dir}/scripts/build-package-freebsd.sh"
shellcheck "${project_dir}/scripts/check-static.sh"
shellcheck "${project_dir}/scripts/collect-artifacts.sh"
shellcheck "${project_dir}/scripts/verify-artifacts.sh"
shellcheck "${project_dir}/scripts/verify-package.sh"
shellcheck "${project_dir}/scripts/qemu/guest-validate.sh"
shellcheck "${project_dir}/scripts/qemu/guest-outer-rebind.sh"
shellcheck "${project_dir}/scripts/qemu/prepare-host-interop.sh"
shellcheck "${project_dir}/scripts/qemu/prepare-shared.sh"
shellcheck "${project_dir}/scripts/qemu/run-host-interop.sh"
shellcheck "${project_dir}/scripts/qemu/run-outer-rebind.sh"
shellcheck "${project_dir}/scripts/qemu/run_host_interop_test.sh"

test ! -e "${project_dir}/cmd"
test -f "${project_dir}/../go.mod"
test -d "${project_dir}/../cmd/wg-quic"
test -d "${project_dir}/../cmd/wg-quic-quick"
