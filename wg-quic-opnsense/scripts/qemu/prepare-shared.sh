#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
project_dir=$(CDPATH='' cd -- "${script_dir}/../.." && pwd)
share_dir=${1:-"${project_dir}/.qemu/shared"}

test -x "${project_dir}/net/wg-quic/src/sbin/wg-quic"
test -x "${project_dir}/net/wg-quic/src/sbin/wg-quic-quick"
mkdir -p "${share_dir}"

for target in 26.1 26.7; do
    reference="/tmp/opnsense-plugins-${target}"
    core_reference="/tmp/opnsense-core-${target}"
    test -d "${reference}/Mk"
    test -d "${core_reference}/Mk"
    test -x "${core_reference}/contrib/parallel-lint/parallel-lint"
    tar --exclude='*/__pycache__' --exclude='*.pyc' \
        -czf "${share_dir}/plugin-source-${target}.tar.gz" \
        -C "${reference}" Mk Scripts Templates \
        -C "${project_dir}" net/wg-quic
    tar -czf "${share_dir}/core-lint-${target}.tar.gz" \
        -C "${core_reference}" Mk Scripts contrib/parallel-lint
done

cp "${script_dir}/configure-test.php" "${share_dir}/configure-test.php"
cp "${script_dir}/configure-host-client.php" "${share_dir}/configure-host-client.php"
cp "${script_dir}/api-credentials.php" "${share_dir}/api-credentials.php"
cp "${script_dir}/guest-validate.sh" "${share_dir}/guest-validate.sh"
cp "${script_dir}/guest-outer-rebind.sh" "${share_dir}/guest-outer-rebind.sh"
chmod 0755 "${share_dir}/guest-validate.sh" \
    "${share_dir}/guest-outer-rebind.sh"
ls -lh "${share_dir}"
