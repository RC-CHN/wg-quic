#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
assets_dir=${1:-"$script_dir/.qemu/images/25.12.5"}
version=25.12.5
image_name="openwrt-$version-armsr-armv8-generic-ext4-combined-efi.img"
disk=${OPENWRT_QEMU_DISK:-"$assets_dir/openwrt-$version-wg-quic.qcow2"}
ssh_port=${OPENWRT_QEMU_SSH_PORT:-2222}
quic_port=${OPENWRT_QEMU_QUIC_PORT:-25180}

command -v qemu-system-aarch64 >/dev/null 2>&1 || {
	echo 'missing required command: qemu-system-aarch64' >&2
	exit 69
}

for path in "$assets_dir/u-boot.bin" "$assets_dir/$image_name" "$disk"; do
	[ -f "$path" ] || {
		echo "missing QEMU asset: $path" >&2
		echo "run $script_dir/prepare-qemu.sh first" >&2
		exit 66
	}
done

exec qemu-system-aarch64 \
	-machine virt,gic-version=3 \
	-cpu cortex-a72 \
	-accel tcg,thread=multi \
	-smp 4 \
	-m 1024 \
	-bios "$assets_dir/u-boot.bin" \
	-drive "if=none,id=rootdisk,file=$disk,format=qcow2" \
	-device virtio-blk-device,drive=rootdisk \
	-device virtio-net-device,netdev=wan \
	-netdev "user,id=wan,hostfwd=tcp:127.0.0.1:$ssh_port-:22,hostfwd=udp:127.0.0.1:$quic_port-:51820" \
	-nographic
