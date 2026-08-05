#!/bin/sh

set -eu

package=${1:?usage: verify-package.sh package 26.1|26.7}
target=${2:?usage: verify-package.sh package 26.1|26.7}
case "${target}" in
    26.1) freebsd_abi=14 ;;
    26.7) freebsd_abi=15 ;;
    *) echo "unsupported OPNsense target: ${target}" >&2; exit 64 ;;
esac

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(CDPATH='' cd -- "${script_dir}/.." && pwd)
plugin_dir="${project_dir}/net/wg-quic"
plugin_version=$(sed -n 's/^PLUGIN_VERSION=[[:space:]]*//p' \
    "${plugin_dir}/Makefile")
manifest_file=$(mktemp /tmp/wg-quic-manifest.XXXXXX)
trap 'rm -f -- "${manifest_file}"' EXIT HUP INT TERM

test -f "${package}"
tar -xOf "${package}" +MANIFEST > "${manifest_file}"

test "$(jq -r '.name' "${manifest_file}")" = "os-wg-quic"
test "$(jq -r '.version' "${manifest_file}")" = "${plugin_version}"
test "$(jq -r '.annotations.product_abi' "${manifest_file}")" = "${target}"
test "$(jq -r '.annotations.product_arch' "${manifest_file}")" = "amd64"
test "$(jq -r '.arch' "${manifest_file}")" = "freebsd:${freebsd_abi}:x86:64"

source_count=$(find "${plugin_dir}/src" -type f | wc -l | tr -d ' ')
expected_file_count=$((source_count + 1))
test "$(jq -r '.files | length' "${manifest_file}")" = "${expected_file_count}"

find "${plugin_dir}/src" -type f | sort |
    while IFS= read -r source_file; do
        relative_path=${source_file#"${plugin_dir}/src/"}
        package_path="/usr/local/${relative_path}"
        source_hash=$(sha256sum "${source_file}" | cut -d' ' -f1)
        package_hash=$(jq -r --arg path "${package_path}" \
            '.files[$path] // empty' "${manifest_file}")
        if [ "${package_hash}" != "1\$${source_hash}" ]; then
            echo "${package}: stale or missing ${package_path}" >&2
            exit 1
        fi
    done

for hook in PRE_DEINSTALL POST_INSTALL POST_DEINSTALL; do
    case "${hook}" in
        PRE_DEINSTALL) manifest_hook=pre-deinstall; suffix= ;;
        POST_INSTALL) manifest_hook=post-install; suffix=.post ;;
        POST_DEINSTALL) manifest_hook=post-deinstall; suffix=.post ;;
    esac
    hook_fragment=$(cat "${plugin_dir}/+${hook}${suffix}")
    hook_script=$(jq -r --arg hook "${manifest_hook}" \
        '.scripts[$hook] // empty' "${manifest_file}")
    case "${hook_script}" in
        *"${hook_fragment}"*) ;;
        *)
            echo "${package}: missing +${hook}${suffix} package hook" >&2
            exit 1
            ;;
    esac
done

version_abi=$(tar -xOf "${package}" \
    /usr/local/opnsense/version/wg-quic |
    jq -r '.product_abi')
test "${version_abi}" = "${target}"
echo "$(basename "${package}"): manifest, hooks, and source content OK"
