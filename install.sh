#!/bin/sh
# Installs sniffarr as a systemd service: from the sarak.sh Debian registry on
# Debian and Ubuntu, from the release archive elsewhere on Linux. Run it as
# root or with sudo at hand. SNIFFARR_VERSION, "0.38.0", pins the archive's
# version; the registry gives its newest.
#
#   curl -fsSL https://sarak.sh/mikluko/sniffarr/raw/branch/main/install.sh | sh
#
# The whole script is a function called on its last line, so a download cut
# short runs nothing.
set -eu

FORGE=https://sarak.sh
REPO=mikluko/sniffarr
OWNER=mikluko
DISTRIBUTION=trixie
COMPONENT=main

main() {
	[ "$(uname -s)" = Linux ] || fail "sniffarr's installer is for Linux; see $FORGE/$REPO#install"
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) fail "no build for $(uname -m); amd64 and arm64 are built" ;;
	esac
	if [ "$(id -u)" -eq 0 ]; then
		sudo=
	elif command -v sudo >/dev/null; then
		sudo=sudo
		echo "sudo is asked for the steps that write under /etc, /usr and /lib."
	else
		fail "run as root, or install sudo"
	fi
	if debian_like; then
		install_apt
	else
		install_archive "$arch"
	fi
	echo
	echo "sniffarr runs; its settings are in /etc/default/sniffarr, its database under /var/lib/sniffarr."
	echo "The web UI is on :8081 and authenticates nobody: see $FORGE/$REPO/src/branch/main/docs/web-ui.md."
}

# debian_like reports whether the host takes .deb packages from apt.
debian_like() {
	[ -r /etc/os-release ] || return 1
	# shellcheck disable=SC1091
	. /etc/os-release
	case " ${ID:-} ${ID_LIKE:-} " in
	*" debian "* | *" ubuntu "*) command -v apt-get >/dev/null ;;
	*) return 1 ;;
	esac
}

install_apt() {
	echo "Installing from the $FORGE Debian registry."
	$sudo apt-get install -y -q ca-certificates curl
	$sudo install -d -m 0755 /etc/apt/keyrings
	curl -fsSL "$FORGE/api/packages/$OWNER/debian/repository.key" | $sudo tee /etc/apt/keyrings/sniffarr.asc >/dev/null
	echo "deb [signed-by=/etc/apt/keyrings/sniffarr.asc] $FORGE/api/packages/$OWNER/debian $DISTRIBUTION $COMPONENT" |
		$sudo tee /etc/apt/sources.list.d/sniffarr.list >/dev/null
	$sudo apt-get update -q
	$sudo apt-get install -y -q sniffarr
}

install_archive() {
	arch=$1
	version=${SNIFFARR_VERSION:-$(latest)}
	echo "Installing $version for $arch from the release archive."
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	base=$FORGE/$REPO/releases/download/v$version
	archive=sniffarr_${version}_linux_$arch.tar.gz
	curl -fsSL -o "$tmp/$archive" "$base/$archive"
	curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"
	(cd "$tmp" && sha256sum -c --ignore-missing --quiet checksums.txt)
	tar -xzf "$tmp/$archive" -C "$tmp"
	$sudo install -m 0755 "$tmp/sniffarr" /usr/local/bin/sniffarr
	$sudo install -m 0644 "$tmp/sniffarr.service" /etc/systemd/system/sniffarr.service
	if [ ! -e /etc/default/sniffarr ]; then
		$sudo install -m 0644 "$tmp/sniffarr.default" /etc/default/sniffarr
	fi
	$sudo systemctl daemon-reload
	$sudo systemctl enable --now sniffarr.service
}

# latest is the version of the newest release: the tag the releases page
# redirects to, without its v.
latest() {
	tag=$(curl -fsSI -o /dev/null -w '%{redirect_url}' "$FORGE/$REPO/releases/latest")
	tag=${tag##*/}
	[ -n "$tag" ] || fail "cannot tell the newest release; set SNIFFARR_VERSION"
	echo "${tag#v}"
}

fail() {
	echo "install.sh: $*" >&2
	exit 1
}

main "$@"
