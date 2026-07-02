#!/usr/bin/env bash
# keydris installer — downloads a prebuilt `keydris` binary. No Go, no checkout.
#
#   curl -fsSL https://dev.get.keydris.com/keydris-cli/install.sh | bash                     # stable
#   curl -fsSL https://dev.get.keydris.com/keydris-cli/install.sh | KEYDRIS_CHANNEL=dev bash # dev (zero-config)
#
# Env:
#   PREFIX           install prefix (default /usr/local)  -> $PREFIX/bin/keydris
#   KEYDRIS_CHANNEL  stable (default) | dev
#   KEYDRIS_VERSION  version to install (default: latest)
#   KEYDRIS_BASE_URL base download URL (default: https://get.keydris.dev/keydris-cli)
set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
CHANNEL="${KEYDRIS_CHANNEL:-stable}"
VERSION="${KEYDRIS_VERSION:-latest}"
BASE="${KEYDRIS_BASE_URL:-https://dev.get.keydris.com/keydris-cli}"

need() { command -v "$1" >/dev/null 2>&1 || { echo "error: missing dependency: $1" >&2; exit 1; }; }
need curl
need uname

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in darwin|linux) ;; *) echo "unsupported OS: $os" >&2; exit 1 ;; esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64)  arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported arch: $arch" >&2; exit 1 ;;
esac

name="keydris-$os-$arch"
verdir="$BASE/$CHANNEL/$VERSION"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

echo "==> downloading $name ($CHANNEL/$VERSION)"
curl -fSL --proto '=https' "$verdir/$name"       -o "$tmp/$name"
curl -fSL --proto '=https' "$verdir/SHA256SUMS"  -o "$tmp/SHA256SUMS"

echo "==> verifying checksum"
expected=$(grep " $name\$" "$tmp/SHA256SUMS" | awk '{print $1}')
if command -v sha256sum >/dev/null 2>&1; then actual=$(sha256sum "$tmp/$name" | awk '{print $1}');
else actual=$(shasum -a 256 "$tmp/$name" | awk '{print $1}'); fi
if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
  echo "error: checksum mismatch for $name (expected '$expected', got '$actual')" >&2
  exit 1
fi

BINDIR="$PREFIX/bin"
SUDO=""
if [ ! -w "$BINDIR" ] && [ "$(id -u)" -ne 0 ]; then SUDO="sudo"; fi
echo "==> installing to $BINDIR/keydris"
$SUDO install -d "$BINDIR"
$SUDO install -m 0755 "$tmp/$name" "$BINDIR/keydris"

# Dev channel: drop a zero-config ~/.keydris.toml (points the CLI at the dev
# control plane + IdP) so it works without setting any env vars. Never clobber an
# existing file.
if [ "$CHANNEL" = dev ]; then
  dst="$HOME/.keydris.toml"
  if [ -e "$dst" ]; then
    echo "==> $dst already exists; leaving it (delete it to pick up dev defaults)"
  elif curl -fSL --proto '=https' "$BASE/dev/$VERSION/keydris.toml" -o "$dst" 2>/dev/null; then
    echo "==> wrote dev config to $dst"
  fi
fi

echo "==> done: $("$BINDIR/keydris" version 2>/dev/null || echo 'keydris installed')"
