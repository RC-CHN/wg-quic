#!/bin/sh
set -eu

repo_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
expected_module=github.com/RC-CHN/wg-quic
actual_module=$(go list -m -f '{{.Path}}')

if [ "$actual_module" != "$expected_module" ]; then
	echo "module path mismatch: got $actual_module, want $expected_module" >&2
	exit 1
fi

for dir in $(go list -deps -f '{{.Dir}}' ./...); do
	case "$dir" in
		"$repo_dir/references"|"$repo_dir/references"/*)
			echo "formal build depends on local references path: $dir" >&2
			exit 1
			;;
	esac
done

if go env GOMOD | grep -q '/references/'; then
	echo "active Go module is under references/" >&2
	exit 1
fi

echo "$actual_module has no dependency on references/"
