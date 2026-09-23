#!/bin/sh
# Installs deepcove into ~/.local/bin, or DEEPCOVE_INSTALL_DIR.
#
#   curl -fsSL https://deepcove.dev/install.sh | sh
#
# DEEPCOVE_VERSION pins a release, such as v0.1.1. DEEPCOVE_NO_SERVICE=1 installs it without starting it
# in the background.
set -eu

repo="waltzofpearls/deepcove"
api="https://api.github.com/repos/$repo"

# Called on the last line, so a download cut off halfway runs nothing at all.
main() {
	install_dir="${DEEPCOVE_INSTALL_DIR:-$HOME/.local/bin}"
	check_install_dir
	detect_platform
	check_requirements

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT

	read_release
	say_what_gets_replaced
	say "Downloading deepcove $tag for $os-$arch"
	download "$archive"
	download SHA256SUMS
	verify_checksum
	install_binary
	say "Installed deepcove $tag to $(tilde "$install_dir")/deepcove"

	add_to_path
	check_kvm
	start_service
	print_next_steps
}

say() { printf '%s\n' "$*"; }

fail() {
	printf 'deepcove install: %s\n' "$*" >&2
	exit 1
}

# check_install_dir refuses a folder whose name would change meaning inside the double quotes of the PATH
# line add_to_path writes, such as one with a $ or a backtick in it.
check_install_dir() {
	written=$(home_relative "$install_dir")
	case "${written#\$HOME}" in
	*[\"\$\`\\]*) fail "$install_dir has a \", \$, \` or \\ in its name, which a shell startup file cannot hold safely. Pick another folder with DEEPCOVE_INSTALL_DIR." ;;
	esac
}

detect_platform() {
	case "$(uname -s)" in
	Darwin) os=darwin ext=zip ;;
	Linux) os=linux ext=tar.gz ;;
	*) fail "$(uname -s) is not supported. On Windows, use install.ps1." ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) fail "$(uname -m) is not supported" ;;
	esac
	# A shell running under Rosetta reports x86_64 on an Apple silicon Mac.
	if [ "$os" = darwin ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null)" = 1 ]; then
		arch=arm64
	fi
}

check_requirements() {
	# deepcove is built against glibc, and on a musl system it installs fine but cannot start.
	if [ "$os" = linux ] && ldd --version 2>&1 | grep -qi musl; then
		fail "deepcove needs glibc, and this system uses musl (Alpine, for one)"
	fi
	command -v curl >/dev/null 2>&1 || fail "curl is required"
	if [ "$ext" = zip ]; then
		command -v unzip >/dev/null 2>&1 || fail "unzip is required"
	fi
}

# fetch URL FILE ACCEPT
fetch() {
	if [ -n "${GITHUB_TOKEN:-}" ]; then
		# The token goes in on stdin, since anyone on the machine can read a process's arguments. printf is
		# built into the shell, so it never shows up as a process either.
		printf 'header = "Authorization: Bearer %s"\n' "$GITHUB_TOKEN" |
			curl -fsSL -K - -H "Accept: $3" -o "$2" "$1"
	else
		curl -fsSL -H "Accept: $3" -o "$2" "$1"
	fi
}

read_release() {
	release_url="$api/releases/latest"
	if [ -n "${DEEPCOVE_VERSION:-}" ]; then
		release_url="$api/releases/tags/$DEEPCOVE_VERSION"
	fi
	fetch "$release_url" "$tmp/release.json" application/vnd.github+json || fail "no release found"
	tag=$(release_fields | sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$tag" ] || fail "could not read the release"
	archive="deepcove_${tag}_${os}-${arch}.${ext}"
}

say_what_gets_replaced() {
	[ -x "$install_dir/deepcove" ] || return 0
	current=$("$install_dir/deepcove" version 2>/dev/null | sed -n 's/^version:[[:space:]]*//p')
	if [ -n "$current" ] && [ "$current" != "$tag" ]; then
		say "Replacing deepcove $current with $tag"
	fi
}

# One JSON field per line, so plain sed and awk can read it without jq.
# shellcheck disable=SC2020 # three characters, each to a newline
release_fields() { tr ',{}' '\n\n\n' <"$tmp/release.json"; }

# download NAME, into the temp folder.
download() {
	if [ -z "${GITHUB_TOKEN:-}" ]; then
		url="https://github.com/$repo/releases/download/$tag/$1"
	else
		# A private repository only hands out an asset at its API address.
		url=$(release_fields | awk -v want="$1" '
			/"url":[[:space:]]*"https:\/\/api\.github\.com\/.*\/releases\/assets\// {
				url = $0; sub(/.*"url":[[:space:]]*"/, "", url); sub(/".*/, "", url)
			}
			/"name":/ {
				name = $0; sub(/.*"name":[[:space:]]*"/, "", name); sub(/".*/, "", name)
				if (name == want) { print url; exit }
			}')
		[ -n "$url" ] || fail "release $tag has no $1"
	fi
	fetch "$url" "$tmp/$1" application/octet-stream || fail "could not download $1"
}

verify_checksum() {
	expected=$(awk -v want="$archive" '$2 == want { print $1 }' "$tmp/SHA256SUMS")
	[ -n "$expected" ] || fail "SHA256SUMS does not list $archive"
	if command -v sha256sum >/dev/null 2>&1; then
		actual=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
	else
		actual=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
	fi
	[ "$actual" = "$expected" ] || fail "$archive does not match its checksum, so nothing was installed"
}

install_binary() {
	mkdir "$tmp/unpacked"
	if [ "$ext" = zip ]; then
		unzip -q "$tmp/$archive" -d "$tmp/unpacked"
	else
		tar -xzf "$tmp/$archive" -C "$tmp/unpacked"
	fi
	[ -f "$tmp/unpacked/deepcove" ] || fail "$archive has no deepcove inside"

	mkdir -p "$install_dir"
	# Copied beside the old one, then renamed over it, so a deepcove that is running never sees half a
	# file.
	cp "$tmp/unpacked/deepcove" "$install_dir/.deepcove.new"
	chmod 755 "$install_dir/.deepcove.new"
	mv -f "$install_dir/.deepcove.new" "$install_dir/deepcove"
}

# add_to_path offers to put the install folder on PATH in the shell's startup file. It asks through the
# terminal, since under curl | sh the script itself is on stdin. With no terminal it changes nothing.
add_to_path() {
	path_state=missing
	case ":$PATH:" in
	*":$install_dir:"*)
		path_state=ready
		found=$(command -v deepcove || true)
		if [ "$found" != "$install_dir/deepcove" ]; then
			say "Another deepcove comes first on your PATH: $found"
			say "Remove it, or put $install_dir ahead of it."
		fi
		return
		;;
	esac
	rc=$(shell_startup_file)
	[ -n "$rc" ] || return 0
	# A second run finds its own line already there, and a new terminal already has it.
	if grep -qsF "$(home_relative "$install_dir")" "$rc"; then
		path_state=written
		return
	fi
	# In a subshell: dash exits the whole script when a redirect on a builtin fails.
	(true </dev/tty) 2>/dev/null || return 0
	printf 'Add %s to your PATH in %s? [Y/n] ' "$(tilde "$install_dir")" "$(tilde "$rc")"
	read -r answer </dev/tty || answer=n
	case "$answer" in
	"" | [Yy]*) ;;
	*) return 0 ;;
	esac
	# shellcheck disable=SC2016 # $PATH is meant for the shell that reads this file
	case "$rc" in
	*.fish) line=$(printf 'fish_add_path "%s"' "$(home_relative "$install_dir")") ;;
	*) line=$(printf '# Added by the deepcove installer\nexport PATH="%s:$PATH"' "$(home_relative "$install_dir")") ;;
	esac
	# A file it cannot write to, such as one owned by root, is left alone rather than ending the install.
	if ! { mkdir -p "$(dirname "$rc")" && printf '\n%s\n' "$line" >>"$rc"; } 2>/dev/null; then
		say "Could not write to $(tilde "$rc"), so it was left as it was."
		return 0
	fi
	path_state=written
}

# tilde shows a path under HOME the way people write it.
tilde() {
	case "$1" in
	"$HOME"/*) printf '~%s' "${1#"$HOME"}" ;;
	*) printf '%s' "$1" ;;
	esac
}

# home_relative writes a path under HOME with $HOME, so a line in a startup file or one pasted into a
# terminal reads the same for everyone.
home_relative() {
	# shellcheck disable=SC2016 # $HOME is meant for the shell that runs the line
	case "$1" in
	"$HOME"/*) printf '$HOME%s' "${1#"$HOME"}" ;;
	*) printf '%s' "$1" ;;
	esac
}

shell_startup_file() {
	case "$(basename "${SHELL:-}")" in
	zsh) printf '%s' "${ZDOTDIR:-$HOME}/.zshrc" ;;
	bash)
		# Terminal on a Mac starts bash as a login shell, which reads .bash_profile rather than .bashrc.
		if [ "$os" = darwin ]; then
			printf '%s' "$HOME/.bash_profile"
		else
			printf '%s' "$HOME/.bashrc"
		fi
		;;
	fish) printf '%s' "${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/deepcove.fish" ;;
	esac
}

check_kvm() {
	[ "$os" = linux ] || return 0
	if [ ! -e /dev/kvm ]; then
		say ""
		say "This machine has no /dev/kvm, so VM sessions cannot run here. Sessions can still run on the"
		say "machine itself."
	elif [ ! -r /dev/kvm ] || [ ! -w /dev/kvm ]; then
		say ""
		say "VM sessions need /dev/kvm. Give yourself access, then log in again:"
		say "  sudo usermod -aG kvm $(id -un)"
	fi
}

# start_service keeps deepcove running in the background, and starting again on its own.
start_service() {
	running=
	[ "${DEEPCOVE_NO_SERVICE:-}" = 1 ] && return 0
	if ! has_user_session; then
		say "deepcove was not started in the background: there is no login session here. Log in as this"
		say "user (ssh works, su does not), then run: deepcove service install"
		return 0
	fi
	# install starts a new service, but leaves one that was already running on the old binary. restart
	# puts this one in its place. What install prints, service status shows too.
	if "$install_dir/deepcove" service install >/dev/null && "$install_dir/deepcove" service restart; then
		running=1
		say "deepcove is running in the background, and starts on its own from now on."
	fi
}

# A user service runs inside the user's session: systemd's user manager on Linux, the login window on a
# Mac. su, containers and CI have neither, and neither does ssh into a Mac nobody is logged in to.
has_user_session() {
	if [ "$os" = linux ]; then
		systemctl --user show-environment >/dev/null 2>&1
	else
		launchctl print "gui/$(id -u)" >/dev/null 2>&1
	fi
}

print_next_steps() {
	say ""
	if [ -z "$running" ]; then
		say "Next, start it:"
		say "  deepcove start"
	elif [ -n "${SSH_CONNECTION:-}" ]; then
		say "Next, sign in to Tailscale so your other machines can reach this one:"
		say "  deepcove network login"
	else
		say "Next, open http://127.0.0.1:9000 in your browser."
	fi
	say ""
	say "More commands:"
	if [ -z "${SSH_CONNECTION:-}" ]; then
		say "  deepcove dashboard         open it in your browser"
	fi
	say "  deepcove service status    is it running, and where its logs are"
	say "  deepcove service restart   restart it"
	say "  deepcove help              everything else"

	case "$path_state" in
	ready) ;;
	written)
		say ""
		say "These work in a new terminal. To use them in this one, run:"
		say "  export PATH=\"$(home_relative "$install_dir"):\$PATH\""
		;;
	*)
		say ""
		say "To use them, first run:"
		say "  export PATH=\"$(home_relative "$install_dir"):\$PATH\""
		;;
	esac
}

main "$@"
