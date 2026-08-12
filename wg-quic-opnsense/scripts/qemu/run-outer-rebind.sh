#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(CDPATH='' cd -- "${script_dir}/../.." && pwd)
state_dir="${project_dir}/.qemu"
client="${state_dir}/tools/wg-quic-netstack-client"
bridge="${script_dir}/outer_rebind_bridge.py"
config="${state_dir}/host-interop.conf"
control_socket=${WG_QUIC_REBIND_CONTROL_SOCKET:-"${state_dir}/outer-rebind.sock"}
phase=${WG_QUIC_REBIND_PHASE:-live}
hold_seconds=${WG_QUIC_REBIND_HOLD_SECONDS:-60}
blackout_seconds=${WG_QUIC_REBIND_BLACKOUT_SECONDS:-20}
log="${state_dir}/outer-rebind-${phase}.log"
pid=

case "${phase}" in
    live|reconnect) ;;
    *) echo "WG_QUIC_REBIND_PHASE must be live or reconnect" >&2; exit 2 ;;
esac
for value in "${hold_seconds}" "${blackout_seconds}"; do
    case "${value}" in
        ''|*[!0-9]*) echo "hold and blackout values must be non-negative integers" >&2; exit 2 ;;
    esac
done

cleanup()
{
    if [ -n "${pid}" ]; then
        kill -TERM "${pid}" >/dev/null 2>&1 || true
        wait "${pid}" 2>/dev/null || true
    fi
    "${bridge}" control --control-socket "${control_socket}" drop off >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

bridge_control()
{
    "${bridge}" control --control-socket "${control_socket}" "$@"
}

wait_for_line()
{
    description=$1
    pattern=$2
    attempts=${3:-300}
    attempt=0
    while ! grep -q "${pattern}" "${log}" 2>/dev/null; do
        if ! kill -0 "${pid}" >/dev/null 2>&1; then
            wait "${pid}" || true
            echo "client exited before ${description}" >&2
            sed -n '1,240p' "${log}" >&2
            exit 1
        fi
        attempt=$((attempt + 1))
        if [ "${attempt}" -ge "${attempts}" ]; then
            echo "timed out waiting for ${description}" >&2
            sed -n '1,240p' "${log}" >&2
            exit 1
        fi
        sleep 0.1
    done
}

test -x "${client}"
test -x "${bridge}"
test -S "${control_socket}"
test -s "${config}"
if grep -q '^PersistentKeepalive' "${config}"; then
    echo "outer-rebind client profile must not enable PersistentKeepalive" >&2
    exit 1
fi

bridge_control drop off
bridge_control source 198.18.0.2

case "${phase}" in
    live)
        "${client}" -config "${config}" -recheck-after 8s \
            -hold "${hold_seconds}s" >"${log}" 2>&1 &
        pid=$!
        wait_for_line "initial tunnel traffic" 'HOST INTEROP PASSED:'
        bridge_control source 198.18.0.3
        wait_for_line "traffic after live source-address migration" \
            'HOST INTEROP RECHECK PASSED:' 450
        echo "OUTER REBIND HOST PASSED: live path moved to 198.18.0.3"
        ;;
    reconnect)
        "${client}" -config "${config}" -recheck-after 22s \
            -require-autonomous-reconnect -recovery-timeout 45s \
            -hold "${hold_seconds}s" >"${log}" 2>&1 &
        pid=$!
        wait_for_line "initial tunnel traffic" 'HOST INTEROP PASSED:'
        bridge_control drop on
        sleep "${blackout_seconds}"
        bridge_control source 198.18.0.4
        bridge_control drop off
        wait_for_line "idle autonomous reconnect from the new address" \
            'HOST INTEROP RECHECK PASSED:' 900
        grep -Eq 'reconnect_attempts=[1-9][0-9]* restored_sessions=[1-9][0-9]*' "${log}"
        echo "OUTER REBIND HOST PASSED: idle session redialed from 198.18.0.4"
        ;;
esac

cat "${log}"
wait "${pid}"
pid=
