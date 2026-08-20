#!/bin/sh

set -eu

target=${1:?usage: build-release-target.sh arm64|x86_64 [WG-QUIC-VERSION [OUTPUT-DIR]]}

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH='' cd -- "$script_dir/../.." && pwd)
wg_quic_version=${2:-$("$repo_dir/scripts/release-version.sh")}
openwrt_version=${OPENWRT_VERSION:-25.12.5}
output_dir=${3:-"$repo_dir/dist/openwrt/$target"}
cache_dir=${OPENWRT_SDK_CACHE:-/tmp/wg-quic-openwrt-sdk-cache}

if [ "$openwrt_version" != 25.12.5 ]; then
	echo "unsupported pinned OpenWrt version: $openwrt_version" >&2
	exit 64
fi

case "$target" in
arm64)
	openwrt_target=armsr/armv8
	sdk_target=armsr-armv8
	goarch=arm64
	package_arch=aarch64_generic
	sdk_sha256=1b0316604a3e820b2b008a1baff3f9dac6716af942bef800930e58c7de98c98b
	artifact_target=armsr-armv8
	;;
x86_64|amd64)
	openwrt_target=x86/64
	sdk_target=x86-64
	goarch=amd64
	package_arch=x86_64
	sdk_sha256=0c8df0151a1e88feb7c03d694d61f6a18d51872815b7c811d76e2b77504d5e9c
	artifact_target=x86-64
	;;
*)
	echo "unsupported OpenWrt release target: $target" >&2
	exit 64
	;;
esac

for command_name in curl find go make sha256sum tar; do
	command -v "$command_name" >/dev/null 2>&1 || {
		echo "missing required command: $command_name" >&2
		exit 69
	}
done

sdk_name="openwrt-sdk-$openwrt_version-${sdk_target}_gcc-14.3.0_musl.Linux-x86_64.tar.zst"
sdk_url="https://downloads.openwrt.org/releases/$openwrt_version/targets/$openwrt_target/$sdk_name"

mkdir -p "$cache_dir" "$output_dir"
cache_dir=$(CDPATH='' cd -- "$cache_dir" && pwd)
output_dir=$(CDPATH='' cd -- "$output_dir" && pwd)
sdk_archive="$cache_dir/$sdk_name"

if [ ! -f "$sdk_archive" ]; then
	curl --continue-at - --fail --location --retry 5 --retry-all-errors \
		--retry-delay 2 \
		--output "$sdk_archive.part" "$sdk_url"
	mv "$sdk_archive.part" "$sdk_archive"
fi
printf '%s  %s\n' "$sdk_sha256" "$sdk_archive" | sha256sum -c -

build_root=$(mktemp -d /tmp/wg-quic-openwrt-release.XXXXXX)
cleanup()
{
	trap - EXIT HUP INT TERM
	rm -rf -- "$build_root"
}
trap cleanup EXIT HUP INT TERM

tar --zstd -xf "$sdk_archive" -C "$build_root"
sdk_dir=$(find "$build_root" -mindepth 1 -maxdepth 1 \
	-type d -name 'openwrt-sdk-*' -print -quit)
if [ -z "$sdk_dir" ]; then
	echo "OpenWrt SDK directory was not present in $sdk_archive" >&2
	exit 65
fi

feed_attempt=1
while ! (
	cd "$sdk_dir"
	./scripts/feeds update -f base
); do
	if [ "$feed_attempt" -ge 3 ]; then
		echo 'failed to update the pinned OpenWrt base feed after 3 attempts' >&2
		exit 69
	fi
	feed_attempt=$((feed_attempt + 1))
	sleep 2
done
(
	cd "$sdk_dir"
	make defconfig
	./scripts/feeds install ip-full
)

package_output="$build_root/package-output"
mkdir -p "$package_output"
"$script_dir/build-package.sh" \
	"$sdk_dir" "$wg_quic_version" "$goarch" "$package_output"

package_file=$(find "$package_output" -maxdepth 1 -type f \
	\( -name 'wg-quic-*.apk' -o -name 'wg-quic_*.ipk' \) -print -quit)
if [ -z "$package_file" ]; then
	echo 'OpenWrt package build did not produce an apk or ipk' >&2
	exit 1
fi

if [ "${package_file##*.}" = apk ]; then
	metadata=$(
		"$sdk_dir/staging_dir/host/bin/apk" adbdump "$package_file"
	)
	printf '%s\n' "$metadata" | grep -q "  arch: $package_arch"
	printf '%s\n' "$metadata" | grep -q '    - ip-full'
	printf '%s\n' "$metadata" | grep -q '    - kmod-tun'
fi

extension=${package_file##*.}
artifact_name="wg-quic-${wg_quic_version}-r1-openwrt-${openwrt_version}-${artifact_target}.${extension}"
install -m 0644 "$package_file" "$output_dir/$artifact_name"
sha256sum "$output_dir/$artifact_name"
