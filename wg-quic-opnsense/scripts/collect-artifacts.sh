#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(CDPATH='' cd -- "${script_dir}/.." && pwd)
source_dir=${1:-"${project_dir}/.qemu/shared"}
dist_dir="${project_dir}/dist"
plugin_version=$(sed -n 's/^PLUGIN_VERSION=[[:space:]]*//p' \
    "${project_dir}/net/wg-quic/Makefile")

mkdir -p "${dist_dir}"

for target in 26.1 26.7; do
    package="os-wg-quic-${plugin_version}-opnsense-${target}-amd64.pkg"
    test -f "${source_dir}/${package}"
    tar -tf "${source_dir}/${package}" >/dev/null
    cp "${source_dir}/${package}" "${dist_dir}/${package}"
done

(
    cd "${dist_dir}"
    sha256sum \
        "os-wg-quic-${plugin_version}-opnsense-26.1-amd64.pkg" \
        "os-wg-quic-${plugin_version}-opnsense-26.7-amd64.pkg" \
        > SHA256SUMS
)

cat "${dist_dir}/SHA256SUMS"
