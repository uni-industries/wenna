#!/bin/sh
# Install the wenna binary from a release:
#   curl -fsSL https://raw.githubusercontent.com/uni-industries/wenna/main/scripts/install.sh | bash
#
# WENNA_VERSION       release tag to install; default: the latest release
# WENNA_INSTALL_DIR   where wenna goes; default: $HOME/.local/bin (no sudo)
# WENNA_RELEASES_URL  where releases are downloaded from; default: GitHub
#
# Downloads only; sends nothing. Never reads stdin: it runs piped into a shell.
set -eu

releases=${WENNA_RELEASES_URL:-https://github.com/uni-industries/wenna/releases}
install_dir=${WENNA_INSTALL_DIR:-$HOME/.local/bin}
version=${WENNA_VERSION:-}

fail() {
	echo "wenna install: $*" >&2
	exit 1
}

command -v curl >/dev/null || fail "curl is required."
command -v tar >/dev/null || fail "tar is required."

os=$(uname -s)
arch=$(uname -m)
case $os in
Darwin) os=darwin ;;
Linux) os=linux ;;
esac
case $arch in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
esac
# An Intel shell under Rosetta on Apple silicon: install the native build.
# The sysctl key exists only on Apple silicon; elsewhere the test is false.
if [ "$os/$arch" = darwin/amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null)" = 1 ]; then
	arch=arm64
fi
case $os/$arch in
darwin/amd64 | darwin/arm64 | linux/amd64 | linux/arm64) ;;
*) fail "no wenna build for $(uname -s) $(uname -m); build it from source instead: https://github.com/uni-industries/wenna#install" ;;
esac

if command -v sha256sum >/dev/null; then
	sha256() { sha256sum "$1"; }
elif command -v shasum >/dev/null; then
	sha256() { shasum -a 256 "$1"; }
else
	fail "sha256sum or shasum is required to verify the download."
fi

asset=wenna_${os}_${arch}.tar.gz
if [ -n "$version" ]; then
	base=$releases/download/$version
else
	base=$releases/latest/download
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' HUP INT TERM

fetch() {
	curl -fsL --proto-redir '=https' -o "$tmp/$1" "$base/$1" ||
		fail "could not download $base/$1"
}

echo "Downloading wenna ${version:-(latest release)} for $os/$arch"
fetch "$asset"
fetch checksums.txt

want=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "checksums.txt has no entry for $asset."
got=$(sha256 "$tmp/$asset" | awk '{ print $1 }')
[ "$got" = "$want" ] || fail "checksum mismatch for $asset; refusing to install it."

tar -xzf "$tmp/$asset" -C "$tmp"
[ -f "$tmp/wenna" ] || fail "$asset does not contain wenna."

mkdir -p "$install_dir"
# Copy beside the target, then rename: replacing a running wenna is atomic.
cp "$tmp/wenna" "$install_dir/.wenna.new"
chmod 755 "$install_dir/.wenna.new"
mv -f "$install_dir/.wenna.new" "$install_dir/wenna"

installed=$("$install_dir/wenna" version) || fail "installed $install_dir/wenna, but it does not run on this machine."
echo "Installed wenna $installed to $install_dir/wenna"

case :$PATH: in
*:"$install_dir":*) ;;
*) echo "Warning: $install_dir is not on your PATH; add it, or AI apps cannot start wenna." >&2 ;;
esac

echo "Next, connect your personal agents: https://github.com/uni-industries/wenna#connect-your-personal-agents"
echo "Apps on this computer: https://github.com/uni-industries/wenna#apps-on-this-computer"
