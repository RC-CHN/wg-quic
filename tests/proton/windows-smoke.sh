#!/usr/bin/env bash

set -euo pipefail

script_directory=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repository_directory=$(cd -- "$script_directory/../.." && pwd)
proton_path=${WG_QUIC_PROTON_PATH:-}
steam_client_path=${WG_QUIC_STEAM_CLIENT_PATH:-/home/pan/.steam/root}
msi_path=

if [[ ${1:-} == "--msi" ]]; then
    if [[ -z ${2:-} || -n ${3:-} ]]; then
        echo "usage: $0 [--msi PATH]" >&2
        exit 2
    fi
    msi_path=$(realpath -- "$2")
elif [[ $# -ne 0 ]]; then
    echo "usage: $0 [--msi PATH]" >&2
    exit 2
fi

if [[ -z $proton_path ]]; then
    for candidate in \
        "$steam_client_path/steamapps/common/Proton - Experimental/proton" \
        "$steam_client_path/steamapps/common/Proton Hotfix/proton"
    do
        if [[ -x $candidate ]]; then
            proton_path=$candidate
            break
        fi
    done
fi
if [[ ! -x $proton_path ]]; then
    echo "Proton was not found; set WG_QUIC_PROTON_PATH" >&2
    exit 1
fi
proton_directory=$(dirname -- "$proton_path")
wine_path="$proton_directory/files/bin/wine"
wineserver_path="$proton_directory/files/bin/wineserver"
if [[ ! -x $wine_path || ! -x $wineserver_path ]]; then
    echo "the selected Proton distribution does not contain Wine binaries" >&2
    exit 1
fi

fixture_directory=$(mktemp -d -p /tmp wg-quic-proton.XXXXXX)
compatibility_directory=${WG_QUIC_PROTON_COMPAT_DATA:-$fixture_directory/compatdata}
binary_directory="$fixture_directory/bin"
interop_ready_file="$fixture_directory/interop-ready"
interop_server_log="$fixture_directory/interop-server.log"
interop_server_pid=
go_overlay_directory="$fixture_directory/go-overlay"
mkdir -p -- "$compatibility_directory" "$binary_directory"

export STEAM_COMPAT_DATA_PATH=$compatibility_directory
export STEAM_COMPAT_CLIENT_INSTALL_PATH=$steam_client_path
export WINEDEBUG=-all
export PROTON_LOG=0
export WINEPREFIX="$compatibility_directory/pfx"

cleanup() {
    if [[ -n $interop_server_pid ]]; then
        kill "$interop_server_pid" >/dev/null 2>&1 || true
        wait "$interop_server_pid" >/dev/null 2>&1 || true
    fi
    "$wineserver_path" -k >/dev/null 2>&1 || true
    rm -rf -- "$fixture_directory"
}
trap cleanup EXIT

run_proton() {
    timeout "${WG_QUIC_PROTON_TIMEOUT:-60}s" "$proton_path" run "$@"
}

run_windows() {
    timeout "${WG_QUIC_PROTON_TIMEOUT:-60}s" "$wine_path" "$@"
}

version=$(tr -d '\r\n' < "$repository_directory/VERSION")
for command in wg-quic wg-quic-quick; do
    env \
        CGO_ENABLED=0 \
        GOOS=windows \
        GOARCH=amd64 \
        go build \
        -trimpath \
        -ldflags "-s -w -X main.version=$version" \
        -o "$binary_directory/$command.exe" \
        "$repository_directory/cmd/$command"
done
cp -- \
    "$repository_directory/third_party/wintun/amd64/wintun.dll" \
    "$binary_directory/wintun.dll"

echo "initializing Proton prefix"
if [[ ! -f $WINEPREFIX/system.reg ]]; then
    run_proton cmd /c ver
fi
echo "checking wg-quic.exe version"
core_version=$(run_windows "$binary_directory/wg-quic.exe" version)
echo "checking wg-quic-quick.exe version"
quick_version=$(run_windows "$binary_directory/wg-quic-quick.exe" version)
[[ $core_version == "wg-quic $version" ]]
[[ $quick_version == "wg-quic-quick $version" ]]

configuration_path=$(realpath -- "$repository_directory/tests/container/a.conf")
configuration_path=${configuration_path//\//\\}
echo "checking a Windows configuration"
check_output=$(run_windows \
    "$binary_directory/wg-quic-quick.exe" \
    check \
    "Z:$configuration_path")
grep -F "configuration is valid" <<< "$check_output"
echo "Proton Windows native command smoke passed"

env CGO_ENABLED=0 go build \
    -trimpath \
    -o "$binary_directory/interop-linux" \
    "$repository_directory/tests/proton/interop"
go_overlay=$(go run \
    "$repository_directory/tests/proton/prepare-overlay" \
    -output-dir "$go_overlay_directory")
env CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build \
    -trimpath \
    -overlay "$go_overlay" \
    -ldflags "-X main.socketCompatibility=wine-wsaioctl-fallback" \
    -o "$binary_directory/interop-windows.exe" \
    "$repository_directory/tests/proton/interop"

echo "starting native Linux WireGuard/QUIC/FEC/obfs peer"
"$binary_directory/interop-linux" \
    -role server \
    -ready-file "$interop_ready_file" \
    >"$interop_server_log" 2>&1 &
interop_server_pid=$!
for _ in {1..100}; do
    if [[ -s $interop_ready_file ]]; then
        break
    fi
    if ! kill -0 "$interop_server_pid" >/dev/null 2>&1; then
        cat "$interop_server_log" >&2
        exit 1
    fi
    sleep 0.05
done
if [[ ! -s $interop_ready_file ]]; then
    echo "Linux interop peer did not become ready" >&2
    cat "$interop_server_log" >&2
    exit 1
fi
interop_port=$(tr -d '\r\n' < "$interop_ready_file")

echo "exchanging inner IP packets with the Windows peer under Proton"
run_windows \
    "$binary_directory/interop-windows.exe" \
    -role client \
    -peer-endpoint "127.0.0.1:$interop_port"

for _ in {1..100}; do
    if ! kill -0 "$interop_server_pid" >/dev/null 2>&1; then
        break
    fi
    sleep 0.05
done
if kill -0 "$interop_server_pid" >/dev/null 2>&1; then
    echo "Linux interop peer did not finish" >&2
    cat "$interop_server_log" >&2
    exit 1
fi
if ! wait "$interop_server_pid"; then
    cat "$interop_server_log" >&2
    exit 1
fi
interop_server_pid=
cat "$interop_server_log"
echo "Proton cross-OS WireGuard/QUIC/FEC/obfs interop passed"

if [[ -z $msi_path ]]; then
    exit 0
fi

windows_msi_path=${msi_path//\//\\}
run_windows msiexec /i "Z:$windows_msi_path" /qn /norestart
desktop_executable=$(find \
    "$compatibility_directory/pfx/drive_c/Program Files" \
    -type f \
    -iname wg-quic-desktop.exe \
    -print \
    -quit)
if [[ -z $desktop_executable ]]; then
    echo "the MSI did not install wg-quic-desktop.exe in Program Files" >&2
    exit 1
fi

smoke_config_directory="$fixture_directory/config"
smoke_result_path="$fixture_directory/desktop-result.txt"
mkdir -p -- "$smoke_config_directory"
windows_config_directory=${smoke_config_directory//\//\\}
windows_result_path=${smoke_result_path//\//\\}
export WG_QUIC_CONFIG_DIR="Z:$windows_config_directory"
export WG_QUIC_DESKTOP_SMOKE=1
export WG_QUIC_DESKTOP_SMOKE_RESULT="Z:$windows_result_path"

timeout 90s "$wine_path" "$desktop_executable"
grep -Fx "wg-quic desktop renderer smoke test passed" "$smoke_result_path"
echo "Proton installed Windows desktop renderer smoke passed"
