#!/usr/bin/env bash
#
# Builds the named VM artifacts for one architecture into $OUT_DIR:
#
#   build/vm-artifacts.sh amd64 guest-kernel cloud-hypervisor virtiofsd passt
#
# Versions come from $VM_VERSIONS, which internal/vmruntime generates, so nothing here is a second copy
# of a version or a file name. virtiofsd and passt are compiled, so those two need a host of the
# architecture they are for; build/vm-artifacts.Dockerfile is how you get one.
set -euo pipefail

versions=${VM_VERSIONS:-internal/vmruntime/versions.json}
out_dir=${OUT_DIR:-dist/release}

arch=${1:-}
shift || true
if [ -z "$arch" ] || [ "$#" -eq 0 ]; then
	echo "usage: vm-artifacts.sh <amd64|arm64> <artifact>..." >&2
	exit 2
fi
case "$arch" in
	amd64) host_arch=x86_64; ch_asset=cloud-hypervisor-static ;;
	arm64) host_arch=aarch64; ch_asset=cloud-hypervisor-static-aarch64 ;;
	*) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac
if [ ! -f "$versions" ]; then
	echo "no versions file at $versions: run make generate, or set VM_VERSIONS" >&2
	exit 1
fi

read_version_for() { jq -er --arg name "${1//-/_}" '.[$name].version' "$versions"; }
read_asset_for() { jq -er --arg name "${1//-/_}" --arg arch "$arch" '.[$name].assets[$arch]' "$versions"; }
read_kata_release() { jq -er '.guest_kernel.kata_release' "$versions"; }

require_native_host() {
	if [ "$(uname -s)" != "Linux" ] || [ "$(uname -m)" != "$host_arch" ]; then
		echo "$1 is compiled, so it needs a linux/$arch host, not $(uname -s)/$(uname -m)" >&2
		exit 1
	fi
}

make_package() {
	local dir=$1 asset=$2
	shift 2
	tar -czf "$out_dir/$asset" -C "$dir" "$@"
	echo "built $out_dir/$asset"
}

# write_notice puts what an artifact is, where its exact source lives and its license texts next to the
# binary. deepcove unpacks only the binary, so the notice is for whoever opens the archive.
write_notice() {
	local dir=$1 name=$2 version=$3 license=$4 source=$5
	shift 5
	{
		echo "$name $version"
		echo "License: $license"
		echo "Source: $source"
		for text in "$@"; do
			echo
			echo "--------------------------------------------------------------------------------"
			echo
			cat "$text"
		done
	} > "$dir/NOTICE"
}

download_guest_kernel() {
	local kata version kernel out url candidate decompress found count
	kata=$(read_kata_release)
	version=$(read_version_for guest-kernel)
	# Kata names its kernels <kernel version>-<its config revision>.
	kernel=${version%-*}
	out="$work/guest-kernel"
	mkdir -p "$out"

	# .tar.zst is current; older releases used .tar.xz.
	url=""
	for ext in tar.zst tar.xz; do
		candidate="https://github.com/kata-containers/kata-containers/releases/download/$kata/kata-static-$kata-$arch.$ext"
		if curl -sfIL "$candidate" >/dev/null; then url="$candidate"; break; fi
	done
	if [ -z "$url" ]; then
		echo "no kata-static asset for $arch in release $kata" >&2
		exit 1
	fi
	echo "guest-kernel: $url"

	case "$url" in
		*.zst) decompress="zstd -dc" ;;
		*.xz) decompress="xz -dc" ;;
		*) echo "unknown archive type: $url" >&2; exit 1 ;;
	esac
	# The tarball is around a gigabyte, so it is streamed rather than saved, and only the kernel is kept.
	# The leading wildcard absorbs a possible "./" on the entries.
	curl -sL "$url" | $decompress \
		| tar -x --wildcards -C "$out" '*opt/kata/share/kata-containers/vmlinux-*'

	# Kata ships variants next to the plain kernel, so match vmlinux-<version>-<revision> and nothing
	# after it. -type f skips the vmlinux.container symlink.
	found=$(find "$out" -type f -name 'vmlinux-*' | grep -E '/vmlinux-[0-9]+\.[0-9]+\.[0-9]+-[0-9]+$' | sort)
	count=$(echo "$found" | grep -c . || true)
	if [ "$count" -ne 1 ]; then
		echo "expected one kernel for $arch, found $count:" >&2
		echo "$found" >&2
		exit 1
	fi
	# Only this archive fails to name its version, so check it here.
	if [ "$(basename "$found")" != "vmlinux-$version" ]; then
		echo "kata $kata carries $(basename "$found"), but this build wants vmlinux-$version" >&2
		exit 1
	fi

	mv "$found" "$out/vmlinux"
	curl -sfL -o "$work/GPL-2.0" \
		"https://git.kernel.org/pub/scm/linux/kernel/git/stable/linux.git/plain/LICENSES/preferred/GPL-2.0?h=v$kernel"
	curl -sfL -o "$work/Linux-syscall-note" \
		"https://git.kernel.org/pub/scm/linux/kernel/git/stable/linux.git/plain/LICENSES/exceptions/Linux-syscall-note?h=v$kernel"
	write_notice "$out" "Linux kernel, as built by Kata Containers $kata" "$version" \
		"GPL-2.0 WITH Linux-syscall-note" \
		"https://cdn.kernel.org/pub/linux/kernel/v${kernel%%.*}.x/linux-$kernel.tar.xz with the config and patches at https://github.com/kata-containers/kata-containers/tree/$kata/tools/packaging/kernel" \
		"$work/GPL-2.0" "$work/Linux-syscall-note"
	make_package "$out" "$(read_asset_for guest-kernel)" vmlinux NOTICE
}

download_cloud_hypervisor() {
	local version out license
	version=$(read_version_for cloud-hypervisor)
	out="$work/cloud-hypervisor"
	mkdir -p "$out"
	curl -sfL -o "$out/cloud-hypervisor" \
		"https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/$version/$ch_asset"
	chmod +x "$out/cloud-hypervisor"
	for license in Apache-2.0 BSD-3-Clause; do
		curl -sfL -o "$work/$license.txt" \
			"https://raw.githubusercontent.com/cloud-hypervisor/cloud-hypervisor/$version/LICENSES/$license.txt"
	done
	write_notice "$out" cloud-hypervisor "$version" "Apache-2.0, BSD-3-Clause" \
		"https://github.com/cloud-hypervisor/cloud-hypervisor/tree/$version" \
		"$work/Apache-2.0.txt" "$work/BSD-3-Clause.txt"
	make_package "$out" "$(read_asset_for cloud-hypervisor)" cloud-hypervisor NOTICE
}

# Upstream releases carry an x86_64 binary and nothing else. Statically linked so one file runs on any
# host: crt-static covers the Rust side, and the .a files from libseccomp-dev and libcap-ng-dev the rest.
build_virtiofsd() {
	local version src out
	require_native_host virtiofsd
	version=$(read_version_for virtiofsd)
	src="$work/virtiofsd-$version"
	out="$work/out-virtiofsd"
	mkdir -p "$out"

	curl -sfL "https://gitlab.com/virtio-fs/virtiofsd/-/archive/$version/virtiofsd-$version.tar.gz" \
		| tar -xz -C "$work"
	(
		cd "$src"
		RUSTFLAGS="-C target-feature=+crt-static" \
			LIBSECCOMP_LINK_TYPE=static LIBSECCOMP_LIB_PATH="/usr/lib/$host_arch-linux-gnu" \
			LIBCAPNG_LINK_TYPE=static LIBCAPNG_LIB_PATH="/usr/lib/$host_arch-linux-gnu" \
			cargo build --release --target "$host_arch-unknown-linux-gnu"
	)
	cp "$src/target/$host_arch-unknown-linux-gnu/release/virtiofsd" "$out/virtiofsd"
	write_notice "$out" virtiofsd "$version" "Apache-2.0, BSD-3-Clause" \
		"https://gitlab.com/virtio-fs/virtiofsd/-/tree/$version" \
		"$src/LICENSE-APACHE" "$src/LICENSE-BSD-3-Clause"
	make_package "$out" "$(read_asset_for virtiofsd)" virtiofsd NOTICE
}

# Upstream publishes binaries only under a path that moves, so build the commit versions.json names.
# `static` builds pasta and the manuals too; a VM only ever runs passt.
build_passt() {
	local commit src out
	require_native_host passt
	commit=$(read_version_for passt)
	src="$work/passt-$commit"
	out="$work/out-passt"
	mkdir -p "$out"

	curl -sfL -o "$out/passt-$commit.tar.gz" "https://passt.top/passt/snapshot/passt-$commit.tar.gz"
	tar -xzf "$out/passt-$commit.tar.gz" -C "$work"
	make -C "$src" static
	cp "$src/passt" "$out/passt"
	# Built here rather than passed on, so GPL-2.0 wants the exact source to travel with the binary. It is
	# about 400 KB.
	write_notice "$out" passt "$commit" "GPL-2.0-or-later, BSD-3-Clause" \
		"passt-$commit.tar.gz in this archive, from https://passt.top/passt/snapshot/passt-$commit.tar.gz" \
		"$src/LICENSES/GPL-2.0-or-later.txt" "$src/LICENSES/BSD-3-Clause.txt"
	make_package "$out" "$(read_asset_for passt)" passt NOTICE "passt-$commit.tar.gz"
}

mkdir -p "$out_dir"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for artifact in "$@"; do
	case "$artifact" in
		guest-kernel) download_guest_kernel ;;
		cloud-hypervisor) download_cloud_hypervisor ;;
		virtiofsd) build_virtiofsd ;;
		passt) build_passt ;;
		*) echo "unknown artifact: $artifact" >&2; exit 1 ;;
	esac
done
