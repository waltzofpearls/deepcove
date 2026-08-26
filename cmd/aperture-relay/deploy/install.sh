#!/bin/sh
# Installs the Aperture relay as a system service. Run it again to upgrade: the binary and the unit are
# replaced, your settings are not.
#
#   sudo ./install.sh --upstream aperture.example.ts.net:80 --allow 100.75.35.244/32
#
# --listen defaults to this machine's tailnet address, so the relay is not reachable off-tailnet.
set -eu

BIN_DIR=/usr/local/bin
CONF=/etc/deepcove/aperture-relay.env
UNIT=/etc/systemd/system/aperture-relay.service

listen="" upstream="" allow=""
while [ $# -gt 0 ]; do
	case "$1" in
		--listen) listen="$2"; shift 2 ;;
		--upstream) upstream="$2"; shift 2 ;;
		--allow) allow="$2"; shift 2 ;;
		*) echo "unknown option: $1" >&2; exit 1 ;;
	esac
done

# Values land in an env file systemd reads line by line, so anything with whitespace or a newline would
# either break its line or add one of its own.
check_value() {
	case "$2" in
		'' ) echo "$1 cannot be empty" >&2; exit 1 ;;
		*[[:space:]]* ) echo "$1 must be a single value with no spaces" >&2; exit 1 ;;
	esac
}

[ "$(id -u)" -eq 0 ] || { echo "run this as root" >&2; exit 1; }
command -v systemctl >/dev/null || { echo "this installer needs systemd" >&2; exit 1; }

case "$(uname -m)" in
	x86_64|amd64) arch=amd64 ;;
	aarch64|arm64) arch=arm64 ;;
	*) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
src="$(dirname "$0")/aperture-relay-linux-$arch"
[ -f "$src" ] || { echo "missing $src" >&2; exit 1; }

# Settings are only asked for on a first install, so an upgrade needs no arguments.
if [ ! -f "$CONF" ]; then
	[ -n "$upstream" ] || { echo "--upstream is required on a first install" >&2; exit 1; }
	[ -n "$allow" ] || { echo "--allow is required on a first install; pass the addresses that may use the relay" >&2; exit 1; }
	if [ -z "$listen" ]; then
		listen="$(tailscale ip -4 2>/dev/null || true):9001"
		[ "$listen" != ":9001" ] || { echo "could not find this machine's tailnet address; pass --listen" >&2; exit 1; }
	fi
	check_value --upstream "$upstream"
	check_value --allow "$allow"
	check_value --listen "$listen"
	mkdir -p "$(dirname "$CONF")"
	cat >"$CONF" <<EOF
# Edit, then: systemctl restart aperture-relay
RELAY_LISTEN=$listen
RELAY_UPSTREAM=$upstream
RELAY_ALLOW=$allow
EOF
	chmod 0644 "$CONF"
	echo "wrote $CONF"
else
	echo "keeping the existing $CONF"
fi

mkdir -p "$BIN_DIR" "$(dirname "$UNIT")"
install -m 0755 "$src" "$BIN_DIR/aperture-relay"
install -m 0644 "$(dirname "$0")/aperture-relay.service" "$UNIT"
systemctl daemon-reload
systemctl enable --now aperture-relay
systemctl restart aperture-relay
echo
systemctl --no-pager --lines=5 status aperture-relay || true
