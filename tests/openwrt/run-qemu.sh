#!/bin/sh

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)

architecture=${OPENWRT_QEMU_ARCH:-arm64}
case "${1:-}" in
arm64|x86_64)
	architecture=$1
	shift
	;;
esac
version=25.12.5
case "$architecture" in
arm64)
	assets_dir=${1:-"$script_dir/.qemu/images/$version"}
	image_name="openwrt-$version-armsr-armv8-generic-ext4-combined-efi.img"
	disk=${OPENWRT_QEMU_DISK:-"$assets_dir/openwrt-$version-wg-quic.qcow2"}
	qemu='qemu-system-aarch64'
	;;
x86_64)
	assets_dir=${1:-"$script_dir/.qemu/images/$version-x86_64"}
	image_name="openwrt-$version-x86-64-generic-ext4-combined.img"
	disk=${OPENWRT_QEMU_DISK:-"$assets_dir/openwrt-$version-x86_64-wg-quic.qcow2"}
	qemu='qemu-system-x86_64'
	;;
*)
	echo "unsupported OpenWrt QEMU architecture: $architecture" >&2
	exit 64
	;;
esac
ssh_port=${OPENWRT_QEMU_SSH_PORT:-2222}
quic_port=${OPENWRT_QEMU_QUIC_PORT:-25180}

command -v "$qemu" >/dev/null 2>&1 || {
	echo "missing required command: $qemu" >&2
	exit 69
}

required_paths="$assets_dir/$image_name $disk"
if [ "$architecture" = arm64 ]; then
	required_paths="$assets_dir/u-boot.bin $required_paths"
fi
for path in $required_paths; do
	[ -f "$path" ] || {
		echo "missing QEMU asset: $path" >&2
		echo "run $script_dir/prepare-qemu.sh first" >&2
		exit 66
	}
done

if [ "$architecture" = arm64 ]; then
	exec "$qemu" \
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
fi

exec "$qemu" \
	-machine q35 \
	-cpu max \
	-accel tcg,thread=multi \
	-smp 4 \
	-m 1024 \
	-drive "if=virtio,file=$disk,format=qcow2" \
	-device virtio-net-pci,netdev=wan \
	-netdev "user,id=wan,hostfwd=tcp:127.0.0.1:$ssh_port-:22,hostfwd=udp:127.0.0.1:$quic_port-:51820" \
	-nographic
