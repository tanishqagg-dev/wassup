#!/bin/sh
# Installs the latest wassup release into ~/.local/bin (override with WASSUP_INSTALL_DIR).
set -eu
REPO="tanishqagg-dev/wassup"
DIR="${WASSUP_INSTALL_DIR:-$HOME/.local/bin}"
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "unsupported CPU: $arch" >&2; exit 1 ;; esac
case "$os" in darwin|linux) ;; *) echo "unsupported OS: $os (download the Windows zip from the releases page)" >&2; exit 1 ;; esac
tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
[ -n "$tag" ] || { echo "couldn't find the latest release" >&2; exit 1; }
ver=${tag#v}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "https://github.com/$REPO/releases/download/$tag/wassup_${ver}_${os}_${arch}.tar.gz" -o "$tmp/wassup.tgz"
curl -fsSL "https://github.com/$REPO/releases/download/$tag/checksums.txt" -o "$tmp/sums"
want=$(grep "wassup_${ver}_${os}_${arch}.tar.gz" "$tmp/sums" | cut -d' ' -f1)
have=$( (command -v sha256sum >/dev/null && sha256sum "$tmp/wassup.tgz" || shasum -a 256 "$tmp/wassup.tgz") | cut -d' ' -f1)
[ "$want" = "$have" ] || { echo "checksum mismatch" >&2; exit 1; }
tar -xzf "$tmp/wassup.tgz" -C "$tmp"
mkdir -p "$DIR"
install -m 0755 "$tmp/wassup" "$DIR/wassup"
echo "installed wassup $tag to $DIR/wassup"
case ":$PATH:" in *":$DIR:"*) ;; *) echo "add $DIR to your PATH" ;; esac
echo "next: wassup login"
