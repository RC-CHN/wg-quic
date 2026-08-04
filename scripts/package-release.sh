#!/bin/sh
set -eu

repo_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
target_os=${1:-}
target_arch=${2:-}
version=${3:-}
dist_dir=${4:-"$repo_dir/dist"}

case "$target_os" in
linux|freebsd|windows)
	;;
*)
	echo "usage: $0 linux|freebsd|windows amd64|arm64 VERSION [dist-directory]" >&2
	exit 2
	;;
esac
case "$target_arch" in
amd64|arm64)
	;;
*)
	echo "usage: $0 linux|freebsd|windows amd64|arm64 VERSION [dist-directory]" >&2
	exit 2
	;;
esac
if [ -z "$version" ]; then
	echo "release version is required" >&2
	exit 2
fi
version=${version#v}
case "$version" in
*[!0-9A-Za-z.+-]*|"")
	echo "invalid release version: $version" >&2
	exit 2
	;;
esac

case "$dist_dir" in
/*)
	;;
*)
	dist_dir="$repo_dir/$dist_dir"
	;;
esac
mkdir -p "$dist_dir"

package_name="wg-quic-v${version}-${target_os}-${target_arch}"
staging_root=$(mktemp -d)
trap 'rm -rf "$staging_root"' EXIT HUP INT TERM
package_dir="$staging_root/$package_name"
mkdir -p "$package_dir"

if [ "$target_os" = windows ]; then
	WG_QUIC_VERSION="$version" "$repo_dir/scripts/package-windows.sh" "$target_arch" "$package_dir"
	(
		cd "$staging_root"
		zip -q -r "$package_name.zip" "$package_name"
	)
	install -m 0644 "$staging_root/$package_name.zip" "$dist_dir/$package_name.zip"
	echo "$dist_dir/$package_name.zip"
	exit 0
fi

ldflags="-s -w -X main.version=$version"
env CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
	go build -trimpath -ldflags "$ldflags" -o "$package_dir/wg-quic" "$repo_dir/cmd/wg-quic"
env CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
	go build -trimpath -ldflags "$ldflags" -o "$package_dir/wg-quic-quick" "$repo_dir/cmd/wg-quic-quick"
install -m 0644 "$repo_dir/LICENSE" "$package_dir/LICENSE"
install -m 0644 "$repo_dir/README.md" "$package_dir/README.md"
printf '%s\n' "$version" >"$package_dir/VERSION"

case "$target_os" in
linux)
	install -m 0644 "$repo_dir/packaging/linux/wg-quic@.service" "$package_dir/wg-quic@.service"
	;;
freebsd)
	install -m 0755 "$repo_dir/packaging/freebsd/wg_quic" "$package_dir/wg_quic"
	;;
esac

(
	cd "$staging_root"
	tar -czf "$dist_dir/$package_name.tar.gz" "$package_name"
)
echo "$dist_dir/$package_name.tar.gz"
