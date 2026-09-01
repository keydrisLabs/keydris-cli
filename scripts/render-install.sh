#!/usr/bin/env bash
# Render install.sh for one release channel to stdout.
#
#   scripts/render-install.sh stable > dist/install-stable.sh
#
# A piped script cannot see the URL it came from, so the channel and its host are
# baked in at publish time; CloudFront routes each host to its own copy.
set -euo pipefail

channel="${1:-}"
case "$channel" in
  stable) base="https://get.keydris.com/keydris-cli" ;;
  dev)    base="https://dev.get.keydris.com/keydris-cli" ;;
  *) echo "usage: ${0##*/} stable|dev" >&2; exit 2 ;;
esac

src="$(cd "$(dirname "$0")/.." && pwd)/install.sh"
[ -r "$src" ] || { echo "error: cannot read $src" >&2; exit 1; }

rendered=$(sed \
  -e "s|^CHANNEL_DEFAULT=.*|CHANNEL_DEFAULT=\"$channel\"|" \
  -e "s|^BASE_DEFAULT=.*|BASE_DEFAULT=\"$base\"|" \
  "$src")

# Never publish an installer bound to the wrong channel, e.g. if the binding
# lines were renamed or reshaped.
for expected in "CHANNEL_DEFAULT=\"$channel\"" "BASE_DEFAULT=\"$base\""; do
  if ! printf '%s\n' "$rendered" | grep -qxF "$expected"; then
    echo "error: install.sh has no '${expected%%=*}' line to substitute" >&2
    exit 1
  fi
done

printf '%s\n' "$rendered"
