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
    python3 -m unittest discover \
        -s "${project_dir}/scripts/qemu" \
        -p '*_test.py'

(
    cd "${monorepo_dir}"
    GOCACHE=/tmp/wg-quic-opnsense-go-test-cache \
        go test ./wg-quic-opnsense/scripts/qemu/linux-client
)

"${project_dir}/scripts/qemu/run_host_interop_test.sh"

find "${plugin_dir}" -name '*.js' -type f -exec sh -c '
    for source_file do
        node --check --input-type=module < "${source_file}"
    done
' sh {} +

find "${plugin_dir}" -name '*.volt' -type f -exec sh -c '
    for source_file do
        sed -n "/<script>/,/<\\/script>/p" "${source_file}" |
            sed "1d;\$d;s/{{[^}]*}}/TRANSLATED/g" |
            node --check
    done
' sh {} +

shellcheck "${project_dir}/scripts/build-wg-quic.sh"
shellcheck "${project_dir}/scripts/build-package-freebsd.sh"
shellcheck "${project_dir}/scripts/check-static.sh"
shellcheck "${project_dir}/scripts/collect-artifacts.sh"
shellcheck "${project_dir}/scripts/verify-artifacts.sh"
shellcheck "${project_dir}/scripts/verify-package.sh"
shellcheck "${project_dir}/scripts/qemu/guest-validate.sh"
shellcheck "${project_dir}/scripts/qemu/prepare-host-interop.sh"
shellcheck "${project_dir}/scripts/qemu/prepare-shared.sh"
shellcheck "${project_dir}/scripts/qemu/run-host-interop.sh"
shellcheck "${project_dir}/scripts/qemu/run_host_interop_test.sh"

test ! -e "${project_dir}/cmd"
test -f "${project_dir}/../go.mod"
test -d "${project_dir}/../cmd/wg-quic"
test -d "${project_dir}/../cmd/wg-quic-quick"
