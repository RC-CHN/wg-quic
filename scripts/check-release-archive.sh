#!/bin/sh
set -eu

archive=${1:-}
target_os=${2:-}
target_arch=${3:-}
version=${4:-}
if [ -z "$archive" ] || [ -z "$target_os" ] || [ -z "$target_arch" ] || [ -z "$version" ]; then
	echo "usage: $0 ARCHIVE OS ARCH VERSION" >&2
	exit 2
fi
version=${version#v}
root="wg-quic-v${version}-${target_os}-${target_arch}"

temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
case "$target_os" in
windows)
	members=$(unzip -Z1 "$archive")
	printf '%s\n' "$members" | grep -Fx "$root/wg-quic.exe" >/dev/null
	printf '%s\n' "$members" | grep -Fx "$root/wg-quic-quick.exe" >/dev/null
	printf '%s\n' "$members" | grep -Fx "$root/wintun.dll" >/dev/null
	printf '%s\n' "$members" | grep -Fx "$root/SHA256SUMS" >/dev/null
	unzip -q "$archive" -d "$temporary"
	(cd "$temporary/$root" && sha256sum -c SHA256SUMS)
	;;
linux|freebsd)
	members=$(tar -tzf "$archive")
	printf '%s\n' "$members" | grep -Fx "$root/wg-quic" >/dev/null
	printf '%s\n' "$members" | grep -Fx "$root/wg-quic-quick" >/dev/null
	backslash=$(printf '\134')
	if printf '%s\n' "$members" | grep -F "$backslash" >/dev/null; then
		echo "archive contains a non-Unix path separator" >&2
		exit 1
	fi
	tar -xzf "$archive" -C "$temporary"
	if [ "$target_os" = linux ]; then
		test -f "$temporary/$root/wg-quic@.service"
	else
		test -x "$temporary/$root/wg_quic"
	fi
	;;
*)
	echo "unsupported target OS: $target_os" >&2
	exit 2
	;;
esac

test "$(sed -n '1p' "$temporary/$root/VERSION")" = "$version"
binary_suffix=
if [ "$target_os" = windows ]; then
	binary_suffix=.exe
fi
binary_description=$(file \
	"$temporary/$root/wg-quic$binary_suffix" \
	"$temporary/$root/wg-quic-quick$binary_suffix")
printf '%s\n' "$binary_description"
case "$target_os:$target_arch:$binary_description" in
windows:amd64:*PE32+*x86-64*) ;;
windows:arm64:*PE32+*ARM64*|windows:arm64:*PE32+*Aarch64*) ;;
linux:amd64:*ELF*x86-64*) ;;
linux:arm64:*ELF*ARM*aarch64*) ;;
freebsd:amd64:*ELF*x86-64*) ;;
freebsd:arm64:*ELF*ARM*aarch64*) ;;
*)
	echo "archive binaries do not match $target_os/$target_arch" >&2
	exit 1
	;;
esac
if [ "$target_os" = linux ] && [ "$target_arch" = amd64 ]; then
	test "$("$temporary/$root/wg-quic" version)" = "wg-quic $version"
	test "$("$temporary/$root/wg-quic-quick" version)" = "wg-quic-quick $version"
fi
echo "validated $archive"
