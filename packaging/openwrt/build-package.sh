#!/bin/sh

set -eu

sdk_dir=${1:?usage: build-package.sh OPENWRT-SDK [VERSION [GOARCH [OUTPUT-DIR]]]}
version=${2:-}
goarch=${3:-arm64}
output_dir=${4:-}

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH='' cd -- "$script_dir/../.." && pwd)

if [ -z "$version" ]; then
	version=$("$repo_dir/scripts/release-version.sh")
fi

case "$sdk_dir" in
/*) ;;
*) sdk_dir=$(CDPATH='' cd -- "$sdk_dir" && pwd) ;;
esac
test -f "$sdk_dir/rules.mk"

case "$version" in
''|*[!0-9A-Za-z.+~-]*)
	echo "invalid package version: $version" >&2
	exit 64
	;;
esac
case "$goarch" in
amd64|arm64) ;;
*)
	echo "unsupported Go architecture: $goarch" >&2
	exit 64
	;;
esac

if [ -z "$output_dir" ]; then
	output_dir="$repo_dir/dist/openwrt"
fi
case "$output_dir" in
/*) ;;
*) output_dir="$repo_dir/$output_dir" ;;
esac
mkdir -p "$output_dir"

build_root=$(mktemp -d /tmp/wg-quic-openwrt-package.XXXXXX)
package_dir="$sdk_dir/package/wg-quic-local"
sdk_config="$sdk_dir/.config"
saved_config="$build_root/sdk.config"
had_sdk_config=0
cleanup()
{
	trap - EXIT HUP INT TERM
	if [ "$had_sdk_config" -eq 1 ]; then
		[ ! -f "$saved_config" ] || cp "$saved_config" "$sdk_config"
	else
		rm -f -- "$sdk_config"
	fi
	rm -rf -- "$build_root"
	rm -rf -- "$package_dir"
}
trap cleanup EXIT HUP INT TERM

if [ -e "$package_dir" ]; then
	echo "temporary SDK package path already exists: $package_dir" >&2
	exit 73
fi

# Release SDKs contain the kernel package metadata, but may omit source
# definitions for user-space packages such as ip-full. Link that definition
# from an already-indexed base feed. Fetching feeds remains an explicit caller
# step so this build helper never performs an unexpected network operation.
iproute2_makefile=$(find -L "$sdk_dir/package" \
	-type f -path '*/iproute2/Makefile' -print -quit)
if [ -z "$iproute2_makefile" ] && [ -f "$sdk_dir/feeds/base.index" ]; then
	(
		cd "$sdk_dir"
		./scripts/feeds install ip-full
	)
	iproute2_makefile=$(find -L "$sdk_dir/package" \
		-type f -path '*/iproute2/Makefile' -print -quit)
fi
if [ -z "$iproute2_makefile" ]; then
	echo 'ip-full package definition is missing; run:' >&2
	echo '  ./scripts/feeds update base' >&2
	echo '  ./scripts/feeds install ip-full' >&2
	exit 69
fi

cp -R "$script_dir" "$package_dir"
cp "$repo_dir/LICENSE" "$package_dir/LICENSE"
cp "$repo_dir/VERSION" "$package_dir/VERSION"
if [ -f "$sdk_config" ]; then
	cp "$sdk_config" "$saved_config"
	had_sdk_config=1
fi

binary_version="${version}-openwrt"
for command_name in wg-quic wg-quic-quick; do
	CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" \
		go build -buildvcs=false -trimpath \
		-ldflags="-s -w -X main.version=$binary_version" \
		-o "$build_root/$command_name" \
		"$repo_dir/cmd/$command_name"
done

(
	cd "$sdk_dir"
	# SDK release configs commonly enable ALL_KMODS and ALL_NONSHARED. Leaving
	# those defaults in place turns a one-package build into a rebuild of the
	# entire target package set. Keep the package's runtime dependency metadata,
	# but select only wg-quic and dependencies needed by its build graph.
	sed -i \
		-e '/^CONFIG_ALL_KMODS=/d' \
		-e '/^# CONFIG_ALL_KMODS is not set$/d' \
		-e '/^CONFIG_ALL_NONSHARED=/d' \
		-e '/^# CONFIG_ALL_NONSHARED is not set$/d' \
		-e '/^CONFIG_PACKAGE_wg-quic=/d' \
		-e '/^# CONFIG_PACKAGE_wg-quic is not set$/d' \
		.config
	printf '%s\n' \
		'# CONFIG_ALL_KMODS is not set' \
		'# CONFIG_ALL_NONSHARED is not set' \
		'CONFIG_PACKAGE_wg-quic=m' >> .config
	make defconfig
	# The '+' runtime dependencies above are intentionally present in the APK
	# metadata, but their official binaries are already in OpenWrt repositories.
	# Do not rebuild their complete transitive dependency graph in the SDK.
	make package/wg-quic-local/compile \
		NO_DEPS=1 \
		WG_QUIC_BIN_DIR="$build_root" \
		WG_QUIC_VERSION="$version"
)

package_list="$build_root/package-list"
find "$sdk_dir/bin" -type f \
	\( -name "wg-quic-${version}-r*.apk" -o \
	   -name "wg-quic_${version}-r*.ipk" \) -print > "$package_list"
package_count=$(wc -l < "$package_list" | tr -d ' ')
if [ "$package_count" -ne 1 ]; then
	echo "expected one wg-quic package, found $package_count" >&2
	find "$sdk_dir/bin" -type f -name 'wg-quic*.*pk' -print >&2
	exit 1
fi
package_file=$(sed -n '1p' "$package_list")

install -m 0644 "$package_file" "$output_dir/$(basename "$package_file")"
echo "$output_dir/$(basename "$package_file")"
