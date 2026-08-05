#!/bin/sh

set -eu

target=${1:?usage: build-package-freebsd.sh 26.1|26.7 [output-directory]}
case "${target}" in
    26.1) freebsd_release=14.3 ;;
    26.7) freebsd_release=15.1 ;;
    *) echo "unsupported OPNsense target: ${target}" >&2; exit 64 ;;
esac

if [ "$(uname -s)" != FreeBSD ]; then
    echo "OPNsense packages must be built inside FreeBSD" >&2
    exit 69
fi

running_release=$(freebsd-version -u | cut -d- -f1)
case "${running_release}" in
    "${freebsd_release}"*) ;;
    *)
        echo "OPNsense ${target} requires FreeBSD ${freebsd_release}, got ${running_release}" >&2
        exit 69
        ;;
esac

for command_name in git go make pkg; do
    if ! command -v "${command_name}" >/dev/null 2>&1; then
        echo "missing build command: ${command_name}" >&2
        exit 69
    fi
done

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(CDPATH='' cd -- "${script_dir}/.." && pwd)
monorepo_dir=$(CDPATH='' cd -- "${project_dir}/.." && pwd)
output_dir=${2:-"${project_dir}/dist/${target}"}
mkdir -p "${output_dir}"
output_dir=$(CDPATH='' cd -- "${output_dir}" && pwd)

build_root=$(mktemp -d /tmp/wg-quic-opnsense-package.XXXXXX)
cleanup()
{
    rm -rf -- "${build_root}"
}
trap cleanup EXIT HUP INT TERM

echo "== official OPNsense ${target} sources =="
git clone --depth 1 --branch "stable/${target}" \
    https://github.com/opnsense/plugins.git "${build_root}/plugins"
git clone --depth 1 --branch "stable/${target}" \
    https://github.com/opnsense/core.git "${build_root}/core"
git -C "${build_root}/plugins" rev-parse HEAD
git -C "${build_root}/core" rev-parse HEAD

binary_version=${WG_QUIC_VERSION:-"$(sed -n '1p' "${monorepo_dir}/VERSION")-opnsense"}
WG_QUIC_VERSION="${binary_version}" "${script_dir}/build-wg-quic.sh"

mkdir -p "${build_root}/plugins/net/wg-quic"
tar -C "${project_dir}/net/wg-quic" -cf - . |
    tar -C "${build_root}/plugins/net/wg-quic" -xf -

source_revision=${GITHUB_SHA:-unknown}
if [ "${source_revision}" = unknown ]; then
    source_revision=$(git -C "${monorepo_dir}" rev-parse HEAD 2>/dev/null || echo unknown)
fi

echo "== official plugin lint =="
cd "${build_root}/plugins/net/wg-quic"
make \
    PLUGIN_ABI="${target}" \
    PLUGIN_ARCH=amd64 \
    PLUGIN_HASH="${source_revision}" \
    lint

echo "== official plugin package =="
make \
    PLUGIN_ABI="${target}" \
    PLUGIN_ARCH=amd64 \
    PLUGIN_HASH="${source_revision}" \
    package

plugin_version=$(sed -n 's/^PLUGIN_VERSION=[[:space:]]*//p' Makefile)
package_file=$(find work/pkg -name 'os-wg-quic-*.pkg' -type f | head -n 1)
test -n "${package_file}"
destination="${output_dir}/os-wg-quic-${plugin_version}-opnsense-${target}-amd64.pkg"
cp "${package_file}" "${destination}"

pkg info -F "${destination}"
sha256 "${destination}"
echo "package: ${destination}"
