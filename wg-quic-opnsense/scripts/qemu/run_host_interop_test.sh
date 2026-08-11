#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
temporary_directory=$(mktemp -d)
trap 'rm -rf -- "${temporary_directory}"' EXIT HUP INT TERM

project_dir="${temporary_directory}/project"
runner_dir="${project_dir}/scripts/qemu"
tools_dir="${project_dir}/.qemu/tools"
fake_path="${temporary_directory}/bin"
arguments_file="${temporary_directory}/arguments"
expected_file="${temporary_directory}/expected"

mkdir -p "${runner_dir}" "${tools_dir}" "${fake_path}"
cp "${script_dir}/run-host-interop.sh" "${runner_dir}/run-host-interop.sh"
printf '%s\n' '[Interface]' > "${project_dir}/.qemu/host-interop.conf"

for tool in wg-quic wg-quic-quick; do
    printf '%s\n' '#!/bin/sh' 'exit 0' > "${tools_dir}/${tool}"
    chmod +x "${tools_dir}/${tool}"
done
# The generated fake expands this variable when the runner invokes it.
# shellcheck disable=SC2016
printf '%s\n' \
    '#!/bin/sh' \
    'printf '\''%s\n'\'' "$@" > "${WG_QUIC_TEST_ARGUMENTS}"' \
    > "${tools_dir}/wg-quic-netstack-client"
chmod +x "${tools_dir}/wg-quic-netstack-client"

printf '%s\n' '#!/bin/sh' 'exit 1' > "${fake_path}/sudo"
chmod +x "${fake_path}/sudo"

env \
    PATH="${fake_path}:${PATH}" \
    WG_QUIC_TEST_ARGUMENTS="${arguments_file}" \
    WG_QUIC_HOST_INTEROP_HOLD_SECONDS=37 \
    "${runner_dir}/run-host-interop.sh"

printf '%s\n' \
    '-config' \
    "${project_dir}/.qemu/host-interop.conf" \
    '-hold' \
    '37s' \
    > "${expected_file}"
cmp "${expected_file}" "${arguments_file}"

for invalid in abc -1; do
    invalid_arguments="${temporary_directory}/arguments-${invalid}"
    if env \
        PATH="${fake_path}:${PATH}" \
        WG_QUIC_TEST_ARGUMENTS="${invalid_arguments}" \
        WG_QUIC_HOST_INTEROP_HOLD_SECONDS="${invalid}" \
        "${runner_dir}/run-host-interop.sh" >/dev/null 2>&1; then
        echo "invalid hold value unexpectedly succeeded: ${invalid}" >&2
        exit 1
    fi
    test ! -e "${invalid_arguments}"
done

echo "host interoperability runner contract passed"
