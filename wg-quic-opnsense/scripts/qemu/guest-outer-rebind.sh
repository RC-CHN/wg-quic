#!/bin/sh

set -eu

mount_dir=/mnt/wg-quic-share
interface=${WG_QUIC_REBIND_INTERFACE:-vtnet1}
guest_outer_ip=${WG_QUIC_REBIND_GUEST_IP:-198.18.0.1}
client_outer_port=${WG_QUIC_REBIND_CLIENT_PORT:-52821}

usage()
{
    echo "usage: guest-outer-rebind.sh setup|snapshot|assert CLIENT_IP [MIN_HANDSHAKE]" >&2
    exit 64
}

snapshot()
{
    /usr/local/sbin/wg-quic-quick show quic0 --json
}

case "${1:-}" in
    setup)
        test -f "${mount_dir}/host-interop.json"
        ifconfig "${interface}" inet "${guest_outer_ip}/24" up
        ifconfig "${interface}" -rxcsum -txcsum -tso4 -lro 2>/dev/null || true
        /usr/local/bin/php \
            "${mount_dir}/configure-host-client.php" \
            "${mount_dir}/host-interop.json"
        configctl template reload OPNsense/WireguardQuic
        configctl wireguardquic configure
        if grep -q '^PersistentKeepalive' /usr/local/etc/wg-quic/quic0.conf; then
            echo "outer-rebind server profile unexpectedly enables PersistentKeepalive" >&2
            exit 1
        fi
        # A disposable fixture has no permanent assignment/rule for vtnet1.
        # The production plugin behavior is tested with PF enabled elsewhere;
        # this path isolates transport roaming from firewall policy.
        pfctl -d >/dev/null
        attempt=0
        while [ ! -S /var/run/wg-quic/quic0.sock ] && [ "${attempt}" -lt 150 ]; do
            sleep 0.1
            attempt=$((attempt + 1))
        done
        test -S /var/run/wg-quic/quic0.sock
        echo "OUTER REBIND GUEST READY: ${interface}=${guest_outer_ip}/24"
        ;;
    snapshot)
        snapshot
        ;;
    assert)
        test "${#}" -eq 2 -o "${#}" -eq 3 || usage
        expected_endpoint="${2}:${client_outer_port}"
        minimum_handshake=${3:-0}
        status=$(snapshot)
        printf '%s\n' "${status}"
        printf '%s\n' "${status}" | jq -e \
            --arg endpoint "${expected_endpoint}" \
            --argjson minimum "${minimum_handshake}" \
            '.state == "up"
             and .peers[0].session == "established"
             and .peers[0].endpoint == $endpoint
             and .peers[0].latest_handshake > $minimum
             and .peers[0].last_activity > 0' >/dev/null
        configctl wireguardquic show | jq -e \
            --arg endpoint "${expected_endpoint}" \
            '.records[]
             | select(.type == "peer")
             | .endpoint == $endpoint
               and ."peer-status" == "online"' >/dev/null
        echo "OUTER REBIND GUEST PASSED: endpoint=${expected_endpoint} handshake_gt=${minimum_handshake}"
        ;;
    *)
        usage
        ;;
esac
