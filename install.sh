#!/bin/sh
# ngmux installer — downloads the latest prebuilt binary from GitHub Releases,
# drops it in an install dir, and makes sure that dir is on your PATH.
#
#   curl -fsSL https://raw.githubusercontent.com/NexuraGrid/ng_mux/main/install.sh | sh
#
# Environment overrides:
#   NGMUX_INSTALL_DIR   where to put the binary   (default: ~/.local/bin)
#   NGMUX_VERSION       tag to install            (default: latest release)
#   NGMUX_NO_MODIFY_PATH set to 1 to skip editing your shell rc file
set -eu

REPO="NexuraGrid/ng_mux"
BIN="ngmux"
INSTALL_DIR="${NGMUX_INSTALL_DIR:-$HOME/.local/bin}"

say()  { printf '%s\n' "$*"; }
err()  { printf 'install: %s\n' "$*" >&2; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

# fetch <url> <dest>   ("-" as dest streams to stdout)
fetch() {
	if have curl; then
		if [ "$2" = "-" ]; then curl -fsSL "$1"; else curl -fsSL "$1" -o "$2"; fi
	elif have wget; then
		if [ "$2" = "-" ]; then wget -qO- "$1"; else wget -qO "$2" "$1"; fi
	else
		err "need curl or wget"
	fi
}

# --- detect platform ---------------------------------------------------------
os=$(uname -s)
case "$os" in
	Linux)  os=linux ;;
	Darwin) os=darwin ;;
	*)      err "unsupported OS: $os (use the Windows install.ps1)" ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64|amd64)  arch=amd64 ;;
	aarch64|arm64) arch=arm64 ;;
	*)             err "unsupported architecture: $arch" ;;
esac

# --- resolve version -------------------------------------------------------- -
tag="${NGMUX_VERSION:-}"
if [ -z "$tag" ]; then
	tag=$(fetch "https://api.github.com/repos/$REPO/releases/latest" - \
		| grep '"tag_name"' | head -1 | cut -d'"' -f4)
	[ -n "$tag" ] || err "could not determine the latest release (is one published yet?)"
fi

asset="${BIN}_${os}_${arch}"
url="https://github.com/$REPO/releases/download/$tag/$asset"

say "ngmux $tag  ($os/$arch)"
say "  -> $INSTALL_DIR/$BIN"

# --- download + verify ----------------------------------------------------- -
tmp=$(mktemp)
trap 'rm -f "$tmp" "$tmp.sums"' EXIT
fetch "$url" "$tmp" || err "download failed: $url"

if fetch "https://github.com/$REPO/releases/download/$tag/SHA256SUMS" "$tmp.sums" 2>/dev/null; then
	want=$(awk -v a="$asset" '{sub(/^\*/,"",$2)} $2==a {print $1}' "$tmp.sums")
	if [ -n "$want" ]; then
		if have sha256sum; then got=$(sha256sum "$tmp" | awk '{print $1}')
		elif have shasum;    then got=$(shasum -a 256 "$tmp" | awk '{print $1}')
		else got=""; fi
		[ -z "$got" ] || [ "$got" = "$want" ] || err "checksum mismatch for $asset"
	fi
fi

install -d "$INSTALL_DIR"
chmod 0755 "$tmp"
mv "$tmp" "$INSTALL_DIR/$BIN"

# --- PATH ---------------------------------------------------------------- ---
case ":$PATH:" in
	*":$INSTALL_DIR:"*)
		say ""
		say "Installed. Run: $BIN"
		;;
	*)
		line="export PATH=\"$INSTALL_DIR:\$PATH\""
		rc=""
		case "$(basename "${SHELL:-}")" in
			zsh)  rc="$HOME/.zshrc" ;;
			bash) rc="$HOME/.bashrc" ;;
			fish) rc="$HOME/.config/fish/config.fish"
			      line="fish_add_path $INSTALL_DIR" ;;
		esac
		if [ -n "$rc" ] && [ "${NGMUX_NO_MODIFY_PATH:-0}" != "1" ]; then
			mkdir -p "$(dirname "$rc")"
			if ! grep -qF "$INSTALL_DIR" "$rc" 2>/dev/null; then
				printf '\n# added by the ngmux installer\n%s\n' "$line" >> "$rc"
				say ""
				say "Added $INSTALL_DIR to your PATH in $rc"
				say "Open a new terminal (or: source $rc) then run: $BIN"
			fi
		else
			say ""
			say "Add $INSTALL_DIR to your PATH, e.g.:"
			say "  $line"
		fi
		;;
esac
