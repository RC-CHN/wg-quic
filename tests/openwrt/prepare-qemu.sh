#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
state_dir=${1:-"$script_dir/.qemu/images/25.12.5"}

version=25.12.5
base_url="https://downloads.openwrt.org/releases/$version/targets/armsr/armv8"
image_name="openwrt-$version-armsr-armv8-generic-ext4-combined-efi.img"
image_sha256=d7dcf013547e8be28006d83ce2c2232cd065755b803f4a5ee6b2e22391cfbc76
uboot_sha256=b9220be21f413450e0f2bff0d6d25d936c9a19a82f6c10d27660c4a359e4ae94

for command_name in curl gzip qemu-img sha256sum; do
	command -v "$command_name" >/dev/null 2>&1 || {
		echo "missing required command: $command_name" >&2
		exit 69
	}
done

mkdir -p "$state_dir"
state_dir=$(CDPATH='' cd -- "$state_dir" && pwd)

download()
{
	download_url="$1"
	download_destination="$2"

	[ -f "$download_destination" ] && return 0
	curl --fail --location --retry 3 --retry-delay 2 \
		--output "$download_destination.part" "$download_url"
	mv "$download_destination.part" "$download_destination"
}

download "$base_url/$image_name.gz" "$state_dir/$image_name.gz"
download "$base_url/u-boot-qemu_armv8/u-boot.bin" "$state_dir/u-boot.bin"

(
	cd "$state_dir"
	printf '%s  %s\n' "$image_sha256" "$image_name.gz" |
		sha256sum -c -
	printf '%s  %s\n' "$uboot_sha256" u-boot.bin |
		sha256sum -c -
)

if [ ! -f "$state_dir/$image_name" ]; then
	gzip -dc "$state_dir/$image_name.gz" > "$state_dir/$image_name.part"
	mv "$state_dir/$image_name.part" "$state_dir/$image_name"
fi

overlay="$state_dir/openwrt-$version-wg-quic.qcow2"
if [ ! -f "$overlay" ]; then
	qemu-img create -f qcow2 -F raw \
		-b "$state_dir/$image_name" "$overlay"
fi

printf 'OPENWRT_QEMU_ASSETS=%s\n' "$state_dir"
printf 'OPENWRT_QEMU_DISK=%s\n' "$overlay"
