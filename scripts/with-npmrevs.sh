#!/usr/bin/env bash
# Run a command with cs-npmrevs in front of npmjs.com, so a @codesweep-ai
# version that exists only as a container image installs like any other package.
#
#   scripts/with-npmrevs.sh npm ci
#   scripts/with-npmrevs.sh npm install --save-exact @codesweep-ai/ui@VERSION
#
# Each @codesweep-ai package publishes an image of every build it makes, such as
# ghcr.io/codesweep-ai/npm/ui:<version>. cs-npmrevs
# (https://github.com/codesweep-ai/npmrevs) reads those images and answers npm
# with the versions they hold, and passes every other package through from
# npmjs.com. The images are public, so nothing here needs a credential.
#
# It also serves its own data directory, which every project's build packs its
# npm packages into (`make npm-pack` in npmrevs, lint and ledger, `npm run
# registry:pack` in ui), so a build made earlier on this machine installs
# without being pushed anywhere. So does the npm directory of the build store
# of this repository's owner, where a clean `make ci` records its packages, so
# a pin `make repin` or `npm run repin` moved to a local build installs.
#
# `npm ci` installs from the URLs the lockfile names, so an entry naming this
# address comes from an image and every other entry still comes from npmjs.com.
# The npmrc this writes lives in a temporary directory and is passed with
# NPM_CONFIG_USERCONFIG, so ~/.npmrc is untouched.
#
# A registry already listening on the port is used as it stands, which is what
# lets `npm run registry:npmrevs` in a ui checkout serve an unpushed build to a
# rebuild here. Otherwise this starts one and stops it on the way out.
#
# The same file is in tracer, campaign, ledger, dashboards and ui, so a fix made
# in one is copied to the others rather than rewritten there.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

PORT="${CS_NPMREVS_PORT:-4875}"
URL="http://127.0.0.1:$PORT"
# The directory cs-npmrevs serves when given none, resolved as it resolves it.
DATA="${CS_NPMREVS_DATA:-${XDG_DATA_HOME:-$HOME/.local/share}/cs-npmrevs/data}"
# The registry holding the images, and the scope whose packages are looked up
# there. Every other package comes from the upstream cs-npmrevs passes through to.
IMAGES="${CS_NPMREVS_IMAGES:-ghcr.io}"
SCOPE="${CS_NPMREVS_SCOPE:-@codesweep-ai}"
[ "$#" -gt 0 ] || { echo "usage: with-npmrevs.sh COMMAND [ARG]..." >&2; exit 2; }

command -v node >/dev/null || {
  echo "with-npmrevs.sh: node is not installed, and it is how the registry is asked whether it is up." >&2
  echo "It is also what the packages being installed are for." >&2
  exit 1
}

# The command that runs cs-npmrevs, unless told otherwise. A repository with a
# go.mod runs the version that pins. Every install there is part of a build its
# Makefile drives, so a Go toolchain is present; saying so beats the error `go`
# leaves. A repository without one, such as ui, runs @codesweep-ai/npmrevs at
# the version its package.json pins: the copy `npm ci` installed, or, before the
# first install, that version from npmjs.com, kept in a cache of its own.
#
# From npm it is the platform package's own binary, rather than the node wrapper
# in node_modules/.bin: a wrapper that forwards no signal, as older ones do not,
# would leave the server running once this stops it.
npm_binary() { # node_modules dir: print the cs-npmrevs binary installed there
  local b
  for b in "$1"/@codesweep-ai/npmrevs-*/bin/cs-npmrevs; do
    if [ -x "$b" ]; then
      echo "$b"
      return 0
    fi
  done
  return 1
}
if [ -z "${NPMREVS:-}" ]; then
  if [ -f "$ROOT/go.mod" ]; then
    NPMREVS="$( (cd "$ROOT" && go tool -n cs-npmrevs) 2>/dev/null || true)"
    [ -n "$NPMREVS" ] || {
      echo "with-npmrevs.sh: cs-npmrevs is the version go.mod pins, and \`go tool -n cs-npmrevs\` did not produce it." >&2
      echo "Install Go, or set NPMREVS to the command that runs cs-npmrevs." >&2
      exit 1
    }
  elif NPMREVS="$(npm_binary "$ROOT/node_modules")"; then
    :
  else
    # shellcheck disable=SC2016 # the script is node's to read, not the shell's
    pin="$(node -p '
const p = require(process.argv[1]);
({ ...p.dependencies, ...p.devDependencies })["@codesweep-ai/npmrevs"] ?? ""
' "$ROOT/package.json" 2>/dev/null || true)"
    [ -n "$pin" ] || {
      echo "with-npmrevs.sh: there is no go.mod to pin cs-npmrevs, and package.json pins no @codesweep-ai/npmrevs." >&2
      echo "Set NPMREVS to the command that runs cs-npmrevs." >&2
      exit 1
    }
    cache="${XDG_CACHE_HOME:-$HOME/.cache}/cs-npmrevs/npm/$pin"
    if ! npm_binary "$cache/node_modules" >/dev/null; then
      # From npmjs.com whatever an npmrc says: the registry this would read from
      # is the one it is about to start. So oss-repin pins @codesweep-ai/npmrevs
      # here only to a version npmjs.com holds.
      npm install --prefix "$cache" --no-save --no-audit --no-fund \
        --registry https://registry.npmjs.org/ --@codesweep-ai:registry=https://registry.npmjs.org/ \
        "@codesweep-ai/npmrevs@$pin" >/dev/null || {
        echo "with-npmrevs.sh: @codesweep-ai/npmrevs@$pin, the version package.json pins, did not install from npmjs.com." >&2
        echo "A version that exists only as an image cannot start the registry that serves it. Pin one npmjs.com holds." >&2
        exit 1
      }
    fi
    NPMREVS="$(npm_binary "$cache/node_modules")" || {
      echo "with-npmrevs.sh: @codesweep-ai/npmrevs@$pin installed no binary for this machine." >&2
      exit 1
    }
  fi
fi

# The registry is asked over HTTP with node, which a machine running this has
# anyway: it is here to install the packages a Node build needs.
http() { # URL: print what it answers, or fail
  # shellcheck disable=SC2016 # the script is node's to read, not the shell's
  node -e '
const [url] = process.argv.slice(1);
fetch(url, { signal: AbortSignal.timeout(1500) })
  .then((response) => (response.ok ? response.text() : Promise.reject(new Error(`${response.status}`))))
  .then((body) => process.stdout.write(body))
  .catch(() => process.exit(1));
' "$1" 2>/dev/null
}

# What a cs-npmrevs on the port answers with, and nothing when the port is free
# or something else holds it.
status() { http "$URL/-/npmrevs"; }

work="$(mktemp -d)"
server=""
cleanup() {
  if [ -n "$server" ]; then
    kill "$server" 2>/dev/null
    # What the registry warned of while it served, such as a local version
    # whose bytes differ from the published one it took the place of. Its log
    # goes with the temporary directory, so the warnings are said here.
    if grep -q 'level=WARN' "$work/serve.log" 2>/dev/null; then
      echo "with-npmrevs: the registry warned:" >&2
      grep 'level=WARN' "$work/serve.log" | sed 's/^/  /' >&2
    fi
  fi
  rm -rf "$work"
  return 0
}
trap cleanup EXIT

if status >/dev/null; then
  echo "with-npmrevs: installing through the registry already on $URL"
  # One serving no images answers only what it holds locally and what npmjs.com
  # has, so a version that exists only as an image would not resolve there. That
  # is a fine registry to install through, and a confusing one to debug.
  status | grep -q '"images"' ||
    echo "with-npmrevs: it serves no images, so only npmjs.com versions and its own resolve" >&2
else
  if http "$URL/" >/dev/null; then
    echo "with-npmrevs: $URL answers and is not cs-npmrevs." >&2
    echo "Stop it, or set CS_NPMREVS_PORT to a free port." >&2
    exit 1
  fi
  # A version packed into the data directory takes the place of the image of
  # the same version, so a build made here wins over the one CI pushed.
  mkdir -p "$DATA"
  # The build store of this repository's owner holds the npm packages of every
  # local build a clean gate recorded (scripts/record-build.sh), so a pin on one
  # installs. It is served beside the data directory. A version both hold is the
  # same bytes in both, since a clean commit packs to the same bytes.
  store_npm=""
  if store="$("$ROOT/scripts/record-build.sh" store 2>/dev/null)" && [ -d "$store/npm" ]; then
    store_npm="$store/npm"
  fi
  # shellcheck disable=SC2086 # NPMREVS may be a command with arguments
  $NPMREVS serve --data "$DATA" ${store_npm:+--data "$store_npm"} --images "$IMAGES" --images-scope "$SCOPE" \
    --listen "127.0.0.1:$PORT" > "$work/serve.log" 2>&1 &
  server=$!
  for _ in $(seq 1 50); do
    status >/dev/null && break
    sleep 0.1
  done
  status >/dev/null || {
    echo "with-npmrevs.sh: the registry did not come up on $URL" >&2
    cat "$work/serve.log" >&2
    exit 1
  }
fi

printf 'registry=%s/\n' "$URL" > "$work/npmrc"
NPM_CONFIG_USERCONFIG="$work/npmrc" "$@"
