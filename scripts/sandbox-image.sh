#!/usr/bin/env bash
# Print the sandbox image a member boots, for the cs-sandbox this repository pins.
#
#   scripts/sandbox-image.sh full    the shipped image, which the live matrix boots
#   scripts/sandbox-image.sh slim    the CI image, which every replayed tier boots
#
# cs-sandbox names each image after its own version, and only that binary knows
# its version, so the names are asked of it (`version --images`). A version CI
# published is ghcr.io/<owner>/sandbox[-slim]:<version>. One this machine built
# from a local build is localhost/<owner>/sandbox[-slim]:<version> instead, and
# CI never publishes that version (cs-sandbox SPEC R166).
#
# So the answer is the published name where this host holds that image, else
# the local name where it holds that one, else the published name again, which
# `cs-sandbox build` pulls, or builds under the local name when no registry has
# it. Asking again after that build gives the image it made. The registry is
# not asked here: that would put a round trip in front of every run, which the
# Makefile's setup-smoke is written to avoid.
#
# $SANDBOX is the cs-sandbox to ask, bin/tools/cs-sandbox by default.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SANDBOX="${SANDBOX:-$ROOT/bin/tools/cs-sandbox}"

case "${1:-}" in
  full) published=image local=image-local ;;
  slim) published=image-slim local=image-slim-local ;;
  *) echo "usage: sandbox-image.sh full|slim" >&2; exit 2 ;;
esac

refs="$("$SANDBOX" version --images)"
pub="$(awk -v k="$published" '$1 == k { print $2 }' <<<"$refs")"
loc="$(awk -v k="$local" '$1 == k { print $2 }' <<<"$refs")"
[ -n "$pub" ] || { echo "sandbox-image.sh: $SANDBOX names no $published image" >&2; exit 1; }

# A cs-sandbox from before the local names prints none, and its image is the
# published name whatever this host holds.
if [ -n "$loc" ] && ! podman image exists "$pub" 2>/dev/null && podman image exists "$loc" 2>/dev/null; then
  echo "$loc"
else
  echo "$pub"
fi
