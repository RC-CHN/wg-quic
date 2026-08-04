#!/bin/sh
set -eu

repo_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
expected_module=github.com/RC-CHN/wg-quic
actual_module=$(go list -m -f '{{.Path}}')

if [ "$actual_module" != "$expected_module" ]; then
	echo "module path mismatch: got $actual_module, want $expected_module" >&2
	exit 1
fi

if ! dependency_dirs=$(go list -deps -f '{{.Dir}}' ./...); then
	echo "failed to resolve the project dependency graph" >&2
	exit 1
fi
for dir in $dependency_dirs; do
	case "$dir" in
		"$repo_dir/references"|"$repo_dir/references"/*)
			echo "formal build depends on local references path: $dir" >&2
			exit 1
			;;
	esac
done

active_gomod=$(go env GOMOD)
case "$active_gomod" in
*/references/*)
	echo "active Go module is under references/" >&2
	exit 1
	;;
esac

if ! module_list=$(go list -m all); then
	echo "failed to resolve the project module graph" >&2
	exit 1
fi
if printf '%s\n' "$module_list" | grep -q '^golang\.zx2c4\.com/wireguard '; then
	echo "formal build still depends on the upstream wireguard-go module" >&2
	exit 1
fi

fork_device_dir=$(go list -f '{{.Dir}}' "$expected_module/third_party/wireguard-go/device")
if [ "$fork_device_dir" != "$repo_dir/third_party/wireguard-go/device" ]; then
	echo "WireGuard device resolved outside the in-repository fork: $fork_device_dir" >&2
	exit 1
fi

quick_dependencies=$(go list -deps ./internal/quick)
if printf '%s\n' "$quick_dependencies" | grep -q "^$expected_module/internal/core\$"; then
	echo "wg-quic-quick embeds the wg-quic core instead of supervising the separate executable" >&2
	exit 1
fi

bind_imports=$(go list -f '{{join .Imports "\n"}}' ./internal/bind)
if printf '%s\n' "$bind_imports" | grep -q '^github\.com/quic-go/quic-go$'; then
	echo "ArmorBind imports quic-go directly instead of using internal/transport/quic" >&2
	exit 1
fi

echo "$actual_module uses the in-repository WireGuard fork and has no dependency on references/"
