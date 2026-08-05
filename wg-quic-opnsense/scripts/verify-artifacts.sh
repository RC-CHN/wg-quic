#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(CDPATH='' cd -- "${script_dir}/.." && pwd)
plugin_dir="${project_dir}/net/wg-quic"
dist_dir="${project_dir}/dist"
plugin_version=$(sed -n 's/^PLUGIN_VERSION=[[:space:]]*//p' \
    "${plugin_dir}/Makefile")

(
    cd "${dist_dir}"
    sha256sum -c SHA256SUMS
)

for target in 26.1 26.7; do
    package="${dist_dir}/os-wg-quic-${plugin_version}-opnsense-${target}-amd64.pkg"
    "${script_dir}/verify-package.sh" "${package}" "${target}"
done
