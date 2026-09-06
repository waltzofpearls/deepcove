#!/usr/bin/env bash
#
#   build/install-vm-artifacts.sh <data-dir> <amd64|arm64> <artifact>...
set -euo pipefail

data_dir=${1:-}
arch=${2:-}
if [ -z "$data_dir" ] || [ -z "$arch" ] || [ "$#" -lt 3 ]; then
	echo "usage: install-vm-artifacts.sh <data-dir> <amd64|arm64> <artifact>..." >&2
	exit 2
fi
shift 2

versions=${VM_VERSIONS:-internal/vmruntime/versions.json}
out_dir=${OUT_DIR:-dist/release}

# The kernel is stored by version, not by architecture, so a foreign build would overwrite this one.
case "$(uname -m)" in
	x86_64) host_arch=amd64 ;;
	arm64 | aarch64) host_arch=arm64 ;;
	*) host_arch=unknown ;;
esac
if [ "$arch" != "$host_arch" ]; then
	echo "refusing to install $arch artifacts on a $host_arch machine" >&2
	exit 1
fi

for artifact in "$@"; do
	case "$artifact" in
		guest-kernel)
			version=$(jq -er '.guest_kernel.version' "$versions")
			asset=$(jq -er --arg arch "$arch" '.guest_kernel.assets[$arch]' "$versions")
			dest="$data_dir/vm-kernel/$version"
			mkdir -p "$dest"
			tar -xzf "$out_dir/$asset" -C "$dest"
			echo "installed $dest/vmlinux"
			;;
		*) echo "$artifact stays in $out_dir, nothing looks for it yet" ;;
	esac
done
