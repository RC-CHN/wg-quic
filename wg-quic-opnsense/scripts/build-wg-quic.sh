#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(CDPATH='' cd -- "${script_dir}/.." && pwd)
monorepo_dir=$(CDPATH='' cd -- "${project_dir}/.." && pwd)
source_dir="${WG_QUIC_SOURCE_DIR:-${monorepo_dir}}"
output_dir="${project_dir}/net/wg-quic/src/sbin"
core_output_file="${output_dir}/wg-quic"
quick_output_file="${output_dir}/wg-quic-quick"
version="${WG_QUIC_VERSION:-0.3.0-opnsense}"

: "${GOCACHE:=/tmp/wg-quic-go-build-cache}"
: "${GOMODCACHE:=$(go env GOMODCACHE)}"

mkdir -p "${output_dir}" "${GOCACHE}" "${GOMODCACHE}"

(
    cd "${source_dir}"
    GOOS=freebsd \
    GOARCH=amd64 \
    CGO_ENABLED=0 \
    GOCACHE="${GOCACHE}" \
    GOMODCACHE="${GOMODCACHE}" \
    go build \
        -buildvcs=false \
        -mod=readonly \
        -trimpath \
        -ldflags="-s -w -X main.version=${version}" \
        -o "${core_output_file}" \
        ./cmd/wg-quic
)

(
    cd "${source_dir}"
    GOOS=freebsd \
    GOARCH=amd64 \
    CGO_ENABLED=0 \
    GOCACHE="${GOCACHE}" \
    GOMODCACHE="${GOMODCACHE}" \
    go build \
        -buildvcs=false \
        -mod=readonly \
        -trimpath \
        -ldflags="-s -w -X main.version=${version}" \
        -o "${quick_output_file}" \
        ./cmd/wg-quic-quick
)

chmod 0755 "${core_output_file}" "${quick_output_file}"
file "${core_output_file}" "${quick_output_file}"
sha256sum "${core_output_file}" "${quick_output_file}"
