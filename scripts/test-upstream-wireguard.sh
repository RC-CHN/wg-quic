#!/bin/sh
set -eu

module=golang.zx2c4.com/wireguard
expected_version=v0.0.0-20260522210424-ecfc5a8d5446
actual_version=$(go list -m -f '{{.Version}}' "$module")
target_os=$(go env GOOS)

if [ "$actual_version" != "$expected_version" ]; then
	echo "wireguard-go version mismatch: got $actual_version, want $expected_version" >&2
	exit 1
fi

echo "Running every $target_os-applicable upstream test from $module@$actual_version"
go test -count=1 "$module/..."
