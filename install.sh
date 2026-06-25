#!/usr/bin/env bash
# keydris-cli installer: installs the `keydris` CLI (built from source).
#
#   # from a checkout (works for a private repo via your git auth):
#   git clone https://github.com/keydrisLabs/keydris-cli.git && cd keydris-cli && ./install.sh
#
#   # or piped (the repo must be public, or your git must be authed for the clone):
#   curl -fsSL https://raw.githubusercontent.com/keydrisLabs/keydris-cli/main/install.sh | bash
#
# Env:
#   PREFIX        install prefix (default: /usr/local) -> $PREFIX/bin/keydris
#   KEYDRIS_REPO  GitHub owner/repo to clone when not run from a checkout
#                 (default: keydrisLabs/keydris-cli)
#   KEYDRIS_REF   branch or tag to clone (default: main)
#
# Installs only the client/agent-side `keydris` CLI. It talks to a separately-run
# keydris control plane (issuer + broker); point it at one with
# KEYDRIS_CONTROL_ADDR / KEYDRIS_CONTROL_MTLS_ADDR (see .env.example).
set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
BINDIR="$PREFIX/bin"
REPO="${KEYDRIS_REPO:-keydrisLabs/keydris-cli}"
REF="${KEYDRIS_REF:-main}"

need() { command -v "$1" >/dev/null 2>&1 || { echo "error: missing dependency: $1" >&2; exit 1; }; }
need go

# Resolve the source tree. Run from a checkout -> build it in place. Run via a
# pipe (curl | bash, no local source, no usable $BASH_SOURCE) -> clone the repo.
SELF_DIR=""
if [ "${BASH_SOURCE:+set}" = "set" ]; then
  SELF_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd || true)"
fi

CLEANUP=""
if [ -n "$SELF_DIR" ] && [ -f "$SELF_DIR/go.mod" ]; then
  SRC_DIR="$SELF_DIR"
  echo "==> building from checkout: $SRC_DIR"
else
  need git
  SRC_DIR="$(mktemp -d)"
  CLEANUP="$SRC_DIR"
  echo "==> cloning https://github.com/$REPO (ref: $REF)"
  git clone --depth 1 --branch "$REF" "https://github.com/$REPO.git" "$SRC_DIR"
fi
trap '[ -n "$CLEANUP" ] && rm -rf "$CLEANUP"' EXIT

echo "==> building keydris"
( cd "$SRC_DIR" && go build -o bin/keydris ./cmd/keydris )

SUDO=""
if [ ! -w "$BINDIR" ] && [ "$(id -u)" -ne 0 ]; then SUDO="sudo"; fi

echo "==> installing keydris to $BINDIR"
$SUDO install -d "$BINDIR"
$SUDO install -m 0755 "$SRC_DIR/bin/keydris" "$BINDIR/keydris"

# Optional: install the node systemd unit on a systemd Linux host (for the
# transparent data plane, which runs `keydris proxy up` as a long-lived service).
if [ "$(uname -s)" = "Linux" ] && [ -d /run/systemd/system ]; then
  echo "==> installing systemd unit (keydris.service)"
  $SUDO install -d /etc/keydris
  $SUDO install -m 0644 "$SRC_DIR/deploy/systemd/keydris.service" /etc/systemd/system/keydris.service
  $SUDO systemctl daemon-reload
  echo "    enable with: sudo systemctl enable --now keydris"
fi

echo "==> done. Try: keydris status"
