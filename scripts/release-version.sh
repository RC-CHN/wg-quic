#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH='' cd -- "${script_dir}/.." && pwd)
requested=${1:-}
version=$(sed -n '1p' "${repo_dir}/VERSION")

case "${version}" in
''|*[!0-9A-Za-z.+-]*)
	echo "invalid repository VERSION: ${version}" >&2
	exit 65
	;;
esac

if [ -n "${requested}" ]; then
	requested=${requested#v}
	if [ "${requested}" != "${version}" ]; then
		echo "requested release version ${requested} does not match VERSION ${version}" >&2
		exit 65
	fi
fi

printf '%s\n' "${version}"
