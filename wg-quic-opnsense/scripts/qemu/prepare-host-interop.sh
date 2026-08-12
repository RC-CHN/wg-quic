#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(CDPATH='' cd -- "${script_dir}/../.." && pwd)
monorepo_dir=$(CDPATH='' cd -- "${project_dir}/.." && pwd)
state_dir="${project_dir}/.qemu"
tools_dir="${state_dir}/tools"
share_dir="${state_dir}/shared"
core="${tools_dir}/wg-quic"
quick="${tools_dir}/wg-quic-quick"
netstack_client="${tools_dir}/wg-quic-netstack-client"
: "${WG_QUIC_HOST_INTEROP_ENDPOINT:=127.0.0.1:52820}"

: "${GOCACHE:=/tmp/wg-quic-host-interop-build-cache}"
: "${GOMODCACHE:=$(go env GOMODCACHE)}"

mkdir -p "${tools_dir}" "${share_dir}" "${GOCACHE}" "${GOMODCACHE}"
(
    cd "${monorepo_dir}"
    CGO_ENABLED=0 GOCACHE="${GOCACHE}" GOMODCACHE="${GOMODCACHE}" \
        go build -buildvcs=false -trimpath -o "${core}" ./cmd/wg-quic
    CGO_ENABLED=0 GOCACHE="${GOCACHE}" GOMODCACHE="${GOMODCACHE}" \
        go build -buildvcs=false -trimpath -o "${quick}" ./cmd/wg-quic-quick
    CGO_ENABLED=0 GOCACHE="${GOCACHE}" GOMODCACHE="${GOMODCACHE}" \
        go build -buildvcs=false -trimpath -o "${netstack_client}" \
        ./wg-quic-opnsense/scripts/qemu/linux-client
)

guest_private=$("${core}" genkey)
guest_public=$(printf '%s\n' "${guest_private}" | "${core}" pubkey)
client_private=$("${core}" genkey)
client_public=$(printf '%s\n' "${client_private}" | "${core}" pubkey)

umask 077
jq -n \
    --arg guestPrivateKey "${guest_private}" \
    --arg guestPublicKey "${guest_public}" \
    --arg clientPublicKey "${client_public}" \
    --arg guestAddress "10.77.0.1" \
    --arg guestTunnelAddress "10.77.0.1/24" \
    --arg clientAddress "10.77.0.2" \
    '{
        guestPrivateKey: $guestPrivateKey,
        guestPublicKey: $guestPublicKey,
        clientPublicKey: $clientPublicKey,
        guestAddress: $guestAddress,
        guestTunnelAddress: $guestTunnelAddress,
        clientAddress: $clientAddress
    }' > "${share_dir}/host-interop.json"

{
    printf '%s\n' '[Interface]'
    printf 'PrivateKey = %s\n' "${client_private}"
    printf '%s\n' 'Address = 10.77.0.2/32'
    printf '%s\n' 'ListenPort = 52821'
    printf '%s\n' 'MTU = 1420'
    printf '%s\n' 'Table = off'
    printf '%s\n' '# wg-quic: congestion = auto'
    printf '%s\n' '# wg-quic: fec = auto'
    printf '%s\n' '# wg-quic: obfs = salamander'
    printf '\n%s\n' '[Peer]'
    printf '%s\n' '# wg-quic: peer.fec-latency = balanced'
    printf 'PublicKey = %s\n' "${guest_public}"
    printf 'Endpoint = %s\n' "${WG_QUIC_HOST_INTEROP_ENDPOINT}"
    printf '%s\n' 'AllowedIPs = 10.77.0.1/32'
} > "${state_dir}/host-interop.conf"

chmod 0600 "${share_dir}/host-interop.json" "${state_dir}/host-interop.conf"
"${quick}" check "${state_dir}/host-interop.conf"
echo "Prepared Linux host interoperability fixture in ${state_dir}"
