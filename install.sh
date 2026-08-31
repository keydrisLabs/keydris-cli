#!/usr/bin/env bash
# keydris installer — downloads a prebuilt `keydris` binary. No Go, no checkout.
#
# The URL selects the channel; each host serves only its own (zero-config).
#
#   curl -fsSL https://get.keydris.com/keydris-cli/install.sh | bash      # stable
#   curl -fsSL https://dev.get.keydris.com/keydris-cli/install.sh | bash  # dev
#
# Env:
#   PREFIX           install prefix (default /usr/local)  -> $PREFIX/bin/keydris
#   KEYDRIS_VERSION  version to install (default: latest)
#   KEYDRIS_BASE_URL base download URL (default: this installer's channel host)
#   KEYDRIS_CHANNEL  must match this installer's channel unless KEYDRIS_BASE_URL is set
#   KEYDRIS_NO_CONFIG=1  keep an existing ~/.keydris.toml instead of overwriting it
set -euo pipefail

# --- channel binding: rewritten at publish time by scripts/render-install.sh ---
CHANNEL_DEFAULT="stable"
BASE_DEFAULT="https://get.keydris.com/keydris-cli"
# --- end channel binding ---

PREFIX="${PREFIX:-/usr/local}"
VERSION="${KEYDRIS_VERSION:-latest}"
CHANNEL="${KEYDRIS_CHANNEL:-$CHANNEL_DEFAULT}"
case "$CHANNEL" in
  stable|dev) ;;
  *) echo "error: KEYDRIS_CHANNEL must be stable or dev (got '$CHANNEL')" >&2; exit 1 ;;
esac
# A mismatch is a mistake, not a request — unless the base was redirected too.
if [ "$CHANNEL" != "$CHANNEL_DEFAULT" ] && [ -z "${KEYDRIS_BASE_URL:-}" ]; then
  echo "error: this installer serves the '$CHANNEL_DEFAULT' channel, not '$CHANNEL'" >&2
  case "$CHANNEL" in
    stable) echo "       install stable from https://get.keydris.com/keydris-cli/install.sh" >&2 ;;
    dev)    echo "       install dev from https://dev.get.keydris.com/keydris-cli/install.sh" >&2 ;;
  esac
  exit 1
fi
BASE="${KEYDRIS_BASE_URL:-$BASE_DEFAULT}"

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

# Install channel config, replacing the existing one. Backup is saved as ~/.keydris.toml.bak.
# Set KEYDRIS_NO_CONFIG=1 to keep the existing config.
dst="$HOME/.keydris.toml"
if [ "${KEYDRIS_NO_CONFIG:-0}" = "1" ]; then
  echo "==> KEYDRIS_NO_CONFIG=1; leaving $dst unchanged"
elif curl -fSL --proto '=https' "$verdir/keydris.toml" -o "$tmp/keydris.toml" 2>/dev/null; then
  if [ -e "$dst" ] && cmp -s "$tmp/keydris.toml" "$dst"; then
    echo "==> $CHANNEL config already up to date at $dst"
  else
    if [ -e "$dst" ]; then
      cp -p "$dst" "$dst.bak"
      echo "==> backed up existing config to $dst.bak"
    fi
    install -m 0644 "$tmp/keydris.toml" "$dst"
    echo "==> wrote $CHANNEL config to $dst"
  fi
fi

# stderr stays attached: the binary's first run prints its one-time telemetry
# disclosure (opt out with `keydris telemetry off` or DO_NOT_TRACK=1).
echo "==> done: $("$BINDIR/keydris" version || echo 'keydris installed')"
