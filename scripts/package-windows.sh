#!/bin/sh
set -eu

repo_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
architecture=${1:-amd64}
version=${WG_QUIC_VERSION:-}
if [ -z "$version" ]; then
	version=$("$repo_dir/scripts/release-version.sh")
fi

case "$architecture" in
amd64|arm64)
	;;
*)
	echo "usage: $0 [amd64|arm64] [output-directory]" >&2
	exit 2
	;;
esac

output_dir=${2:-"$repo_dir/build/wg-quic-windows-$architecture"}
case "$output_dir" in
/*)
	;;
*)
	output_dir="$repo_dir/$output_dir"
	;;
esac

(cd "$repo_dir/third_party/wintun" && sha256sum -c SHA256SUMS)
mkdir -p "$output_dir"
ldflags="-s -w -X main.version=$version"

env CGO_ENABLED=0 GOOS=windows GOARCH="$architecture" \
	go build -trimpath -ldflags "$ldflags" -o "$output_dir/wg-quic.exe" "$repo_dir/cmd/wg-quic"
env CGO_ENABLED=0 GOOS=windows GOARCH="$architecture" \
	go build -trimpath -ldflags "$ldflags" -o "$output_dir/wg-quic-quick.exe" "$repo_dir/cmd/wg-quic-quick"

install -m 0644 "$repo_dir/third_party/wintun/$architecture/wintun.dll" "$output_dir/wintun.dll"
install -m 0644 "$repo_dir/third_party/wintun/LICENSE.txt" "$output_dir/LICENSE-WINTUN.txt"
install -m 0644 "$repo_dir/third_party/wintun/ORIGIN.md" "$output_dir/ORIGIN-WINTUN.md"
install -m 0644 "$repo_dir/LICENSE" "$output_dir/LICENSE-wg-quic.txt"
install -m 0644 "$repo_dir/packaging/windows/README.md" "$output_dir/README.md"
install -m 0644 "$repo_dir/README_CN.md" "$output_dir/README_CN.md"
printf '%s\n' "$version" >"$output_dir/VERSION"

(cd "$output_dir" && sha256sum \
	wg-quic.exe wg-quic-quick.exe wintun.dll \
	LICENSE-WINTUN.txt ORIGIN-WINTUN.md LICENSE-wg-quic.txt README.md README_CN.md VERSION > SHA256SUMS)

echo "created self-contained Windows $architecture bundle for $version at $output_dir"
