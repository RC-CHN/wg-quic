#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(CDPATH='' cd -- "${script_dir}/../.." && pwd)
state_dir="${project_dir}/.qemu"
core="${state_dir}/tools/wg-quic"
quick="${state_dir}/tools/wg-quic-quick"
netstack_client="${state_dir}/tools/wg-quic-netstack-client"
config="${state_dir}/host-interop.conf"
log="${state_dir}/host-interop.log"
pid=

cleanup()
{
    if [ -n "${pid}" ]; then
        sudo -n kill -TERM "${pid}" >/dev/null 2>&1 || true
        wait "${pid}" 2>/dev/null || true
    fi
}
trap cleanup EXIT HUP INT TERM

test -x "${core}"
test -x "${quick}"
test -x "${netstack_client}"
test -s "${config}"

if sudo -n true >/dev/null 2>&1; then
    # The unprivileged caller intentionally owns this diagnostic log.
    # shellcheck disable=SC2024
    sudo -n "${quick}" run "${config}" --name quichost > "${log}" 2>&1 &
    pid=$!

    attempt=0
    while [ ! -S /run/wg-quic/quichost.sock ] && [ "${attempt}" -lt 150 ]; do
        sleep 0.1
        attempt=$((attempt + 1))
    done
    test -S /run/wg-quic/quichost.sock

    sudo -n ping -c 3 -W 3 10.77.0.1
    status=$(sudo -n "${core}" show quichost --json)
    printf '%s\n' "${status}" | jq -e \
        '.state == "up"
         and .peers[0].session == "established"
         and .stats.wg_tx_bytes > 0
         and .stats.wg_rx_bytes > 0' >/dev/null

    echo "HOST INTEROP PASSED: Linux quichost <-> OPNsense quic0"
else
    "${netstack_client}" -config "${config}"
fi
