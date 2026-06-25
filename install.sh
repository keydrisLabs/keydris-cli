#!/usr/bin/env bash
# keydris-cli installer: builds and installs the `keydris` CLI from source.
#
#   curl -fsSL https://raw.githubusercontent.com/keydrisLabs/keydris-cli/main/install.sh | bash
#   # or, from a checkout:
#   ./install.sh
#
# Env:
#   PREFIX   install prefix (default: /usr/local) -> binary at $PREFIX/bin/keydris
#
# This installs only the client/agent-side `keydris` CLI. It talks to a
# separately-run keydris control plane (issuer + broker); point it at one with
# KEYDRIS_CONTROL_ADDR / KEYDRIS_CONTROL_MTLS_ADDR (see .env.example).
set -euo pipefail

PREFIX="${PREFIX:-/usr/local}"
BINDIR="$PREFIX/bin"
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

need() { command -v "$1" >/dev/null 2>&1 || { echo "error: missing dependency: $1" >&2; exit 1; }; }
need go

echo "==> building keydris"
mkdir -p "$REPO_DIR/bin"
( cd "$REPO_DIR" && go build -o bin/keydris ./cmd/keydris )

SUDO=""
if [ ! -w "$BINDIR" ] && [ "$(id -u)" -ne 0 ]; then SUDO="sudo"; fi

echo "==> installing keydris to $BINDIR"
$SUDO install -d "$BINDIR"
$SUDO install -m 0755 "$REPO_DIR/bin/keydris" "$BINDIR/keydris"

# Optional: install the node systemd unit on a systemd Linux host (for the
# transparent data plane, which runs `keydris proxy up` as a long-lived service).
if [ "$(uname -s)" = "Linux" ] && [ -d /run/systemd/system ]; then
  echo "==> installing systemd unit (keydris.service)"
  $SUDO install -d /etc/keydris
  $SUDO install -m 0644 "$REPO_DIR/deploy/systemd/keydris.service" /etc/systemd/system/keydris.service
  $SUDO systemctl daemon-reload
  echo "    enable with: sudo systemctl enable --now keydris"
fi

echo "==> done. Try: keydris status"
