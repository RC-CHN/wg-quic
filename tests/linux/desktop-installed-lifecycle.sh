#!/usr/bin/env bash

set -euo pipefail

core=/usr/lib/wg-quic/bin/wg-quic
quick=/usr/lib/wg-quic/bin/wg-quic-quick
tunnel_name="wgqd$$"
config_directory=/etc/wg-quic
config_path="$config_directory/$tunnel_name.conf"
primary_socket="/run/wg-quic/$tunnel_name.sock"
status_socket="$primary_socket.status"
listen_port=$((54000 + $$ % 1000))
octet=$((20 + $$ % 200))
local_address="198.19.$octet.1"
peer_address="198.19.$octet.2"
fixture_directory=$(mktemp -d -p /tmp wg-quic-desktop-linux.XXXXXX)
source_config="$fixture_directory/$tunnel_name.conf"

# Invoked by the EXIT trap.
# shellcheck disable=SC2317,SC2329
cleanup() {
    sudo "$quick" down "$tunnel_name" >/dev/null 2>&1 || true
    sudo rm -f -- "$config_path"
    rm -rf -- "$fixture_directory"
}
trap cleanup EXIT

for executable in "$core" "$quick"; do
    if [[ ! -x $executable ]]; then
        echo "installed desktop package is missing $executable" >&2
        exit 1
    fi
done
for license in \
    /usr/lib/wg-quic/licenses/wg-quic-AGPL-3.0.txt \
    /usr/lib/wg-quic/licenses/quic-go/LICENSE \
    /usr/lib/wg-quic/licenses/wireguard-go/LICENSE; do
    if [[ ! -f $license ]]; then
        echo "installed desktop package is missing $license" >&2
        exit 1
    fi
done
if [[ ! -f /lib/systemd/system/wg-quic@.service ]]; then
    echo "installed desktop package is missing its systemd service" >&2
    exit 1
fi
for command in ip pkexec systemctl; do
    if ! command -v "$command" >/dev/null; then
        echo "installed desktop package dependency is missing $command" >&2
        exit 1
    fi
done
if ! systemctl cat wg-quic@.service | grep -Fq \
    'ExecStart=/usr/lib/wg-quic/bin/wg-quic-quick run %i'; then
    echo "installed systemd service does not use the bundled runtime" >&2
    exit 1
fi
if ! systemctl cat wg-quic@.service | grep -Fq \
    'KillMode=control-group'; then
    echo "installed systemd service does not own the complete process group" >&2
    exit 1
fi

private_key=$($core genkey)
peer_private_key=$($core genkey)
peer_public_key=$(printf '%s\n' "$peer_private_key" | "$core" pubkey)
cat >"$source_config" <<EOF
[Interface]
PrivateKey = $private_key
Address = $local_address/32
ListenPort = $listen_port
MTU = 1380

[Peer]
PublicKey = $peer_public_key
AllowedIPs = $peer_address/32
Endpoint = 192.0.2.$octet:62000
PersistentKeepalive = 1
EOF

sudo install -d -m 0755 -- "$config_directory"
sudo install -m 0600 -- "$source_config" "$config_path"
sudo "$quick" check "$config_path"
if ! sudo "$quick" up "$tunnel_name"; then
    sudo systemctl status --no-pager "wg-quic@$tunnel_name.service" || true
    sudo journalctl --no-pager -u "wg-quic@$tunnel_name.service" -n 120 || true
    echo "installed Linux desktop service failed to start" >&2
    exit 1
fi

ready=false
status=
for _ in {1..150}; do
    candidate_status=$($core show "$tunnel_name" --json 2>/dev/null || true)
    if systemctl is-active --quiet "wg-quic@$tunnel_name.service" &&
        ip link show dev "$tunnel_name" >/dev/null 2>&1 &&
        [[ -S $primary_socket && -S $status_socket ]] &&
        grep -Eq '"interface"[[:space:]]*:[[:space:]]*"'"$tunnel_name"'"' \
            <<<"$candidate_status" &&
        grep -Eq '"state"[[:space:]]*:[[:space:]]*"up"' \
            <<<"$candidate_status"; then
        status=$candidate_status
        ready=true
        break
    fi
    sleep 0.2
done
if [[ $ready != true ]]; then
    sudo systemctl status --no-pager "wg-quic@$tunnel_name.service" || true
    sudo journalctl --no-pager -u "wg-quic@$tunnel_name.service" -n 120 || true
    echo "installed Linux desktop tunnel did not become ready" >&2
    exit 1
fi

grep -Eq '"interface"[[:space:]]*:[[:space:]]*"'"$tunnel_name"'"' \
    <<<"$status"
grep -Eq '"state"[[:space:]]*:[[:space:]]*"up"' <<<"$status"
[[ $(stat -c '%a' "$primary_socket") == 600 ]]
[[ $(stat -c '%a' "$status_socket") == 666 ]]
[[ $(stat -c '%a' "$(dirname -- "$primary_socket")") == 755 ]]
echo "unprivileged installed status endpoint passed"

sudo "$quick" down "$tunnel_name"
for _ in {1..150}; do
    if ! systemctl is-active --quiet "wg-quic@$tunnel_name.service" &&
        ! ip link show dev "$tunnel_name" >/dev/null 2>&1 &&
        [[ ! -e $primary_socket && ! -e $status_socket ]]; then
        echo "installed Linux desktop service lifecycle passed"
        exit 0
    fi
    sleep 0.2
done

sudo systemctl status --no-pager "wg-quic@$tunnel_name.service" || true
echo "installed Linux desktop tunnel did not clean up" >&2
exit 1
