#!/usr/bin/env bash
# Re-record every cassette the smoke tier replays, from this machine's own
# credentials.
#
# Five scenarios, each a real campaign: two microVMs, a fabric, a dispatch ladder
# and real model turns. This checks what they need before any of that starts, and
# `make record-fixtures-strict` fails on a scenario it cannot sign in for rather than
# skipping it. Recording four of five and reporting green is the outcome worth
# refusing.
#
#   ./scripts/record-fixtures.sh          record all five
#   ./scripts/record-fixtures.sh --check  say what would run, record nothing
#
# The tests do the rest. Each one asks its agent for a single word against the
# real provider before it clears a cassette, so a revoked key costs a few hundred
# tokens instead of a campaign, and the committed cassette stays where it is.
set -euo pipefail

cd "$(dirname -- "${BASH_SOURCE[0]}")/.."
repo=$PWD

# The tools this recording runs on: cs-sandbox and cs-vcr at the go.mod pins,
# built into this checkout by `make tools` and put ahead of everything else.
#
# Built here rather than assumed. The preflight below reports what it found and
# refuses a cs-sandbox that is not the pinned one, and both questions are only
# worth asking of the binaries the run will actually get — `make fixtures-strict`
# puts this same directory on PATH for the tests themselves, so without this the
# script would be vouching for one surface and recording on another.
make tools >/dev/null
export PATH="$repo/bin/tools:$PATH"

check_only=0
[[ ${1:-} == --check ]] && check_only=1

fail() { printf '\n%s\n' "$*" >&2; exit 1; }
ok()   { printf '  ok    %s\n' "$*"; }
bad()  { printf '  MISS  %s\n' "$*"; }

# The three API keys, from the .env this repository already keeps them in.
[[ -f .env ]] || fail ".env not found in $repo — it holds the three API keys."
set -a
# shellcheck disable=SC1091
. ./.env
set +a

# The subscription logins. cs-sandbox inherits each from ~/.cs-<agent>, which is
# the profile the wrappers keep and not the agent's own directory.
login_home=${CS_SANDBOX_AGENT_HOME:-$HOME}

# The image these five campaigns will boot. The slim one, because that is what
# `make test-smoke` and CI replay on, and a cassette is bound to the image that
# recorded it: replay serves the model's tool calls from the cassette and then
# runs them for real, so a turn that used a binary the other variant drops
# replays as a member doing quietly less than it did. Reported rather than
# assumed — the Makefile picks it, and this is where an operator sees which.
image=${CS_SANDBOX_IMAGE:-$(cs-sandbox version --images 2>/dev/null | awk '$1=="image-slim"{print $2}')}

echo "Recording from:"
echo "  repo             $repo"
echo "  branch           $(git rev-parse --abbrev-ref HEAD)"
echo "  logins under     $login_home"
echo "  cs-sandbox       $(cs-sandbox version 2>/dev/null | awk 'NR==1{print $2}' || echo MISSING)"
echo "  built against    $(awk '/codesweep-ai\/sandbox / {print $2; exit}' go.mod 2>/dev/null || echo '?')"
echo "  image            ${image:-MISSING} (slim — what test-smoke replays on)"
echo

missing=0

echo "API keys (.env):"
for v in ANTHROPIC_API_KEY OPENAI_API_KEY FIREWORKS_API_KEY; do
  if [[ -n ${!v:-} ]]; then ok "$v is set"; else bad "$v is not set"; missing=1; fi
done

echo
echo "Subscription logins:"
claude_cred="$login_home/.cs-claude/.credentials.json"
if [[ -f $claude_cred ]]; then
  # The same five-minute margin the agent applies, checked here where the fix is
  # one `cs-claude` away and no campaign has started.
  if python3 - "$claude_cred" <<'PY'
import json, sys, time
exp = json.load(open(sys.argv[1])).get("claudeAiOauth", {}).get("expiresAt", 0) / 1000
sys.exit(0 if exp > time.time() + 300 else 1)
PY
  then ok "Claude login at $claude_cred"
  else bad "Claude login at $claude_cred is expired — run 'cs-claude' to refresh"; missing=1
  fi
else
  bad "no Claude login at $claude_cred"; missing=1
fi

codex_cred="$login_home/.cs-codex/auth.json"
if [[ -f $codex_cred ]]; then
  ok "Codex login at $codex_cred"
else
  bad "no Codex login at $codex_cred — run 'cs-codex login'"; missing=1
fi

# The agents run inside the members, but the preflight in the recording test runs
# them here, against the real provider, before it clears a cassette.
echo
echo "Agents on PATH, for the preflight:"
for a in claude codex opencode; do
  if command -v "$a" >/dev/null; then ok "$a"; else bad "$a is not on PATH"; missing=1; fi
done

echo
echo "Host prerequisites:"
for c in cs-sandbox cs-vcr; do
  if command -v "$c" >/dev/null; then ok "$c"; else bad "$c is not on PATH"; missing=1; fi
done
if [[ -w /dev/kvm ]]; then ok "/dev/kvm is writable"; else bad "/dev/kvm is not writable — the members are microVMs"; missing=1; fi
# The doctor rather than a `podman image exists`, because the image is not the
# only artifact these campaigns need. Every member is copied from a base rootfs
# built FROM that image and kept beside it, one per image variant, and a host can
# hold the image and be unable to boot a single member. That is not a guess: it
# is how a recording run died on all five scenarios, each after taking a group, a
# network and a gateway port.
#
# The image is named rather than asked for with `--slim`, and the two are the
# same request: $image above already resolved the slim reference the recording
# will boot. Naming it also works against the cs-sandbox this checkout PINS,
# which `--slim` does not — that flag is newer than the pin, and a preflight
# built on it reports "cannot boot" on every run, for the flag rather than for
# the host, and swallows the findings it exists to show.
#
# Not a failure. `make record-fixtures` runs setup-smoke, which builds whatever
# is missing before the first campaign starts, so this reports rather than
# refuses — and reports before the confirmation prompt, so a long build is
# expected rather than puzzling.
if [[ -z $image ]]; then
  bad "cannot resolve the slim image name — is cs-sandbox on PATH?"; missing=1
elif CS_SANDBOX_IMAGE="$image" cs-sandbox doctor --engine firecracker >/dev/null 2>&1; then
  ok "this host is ready to boot $image"
else
  ok "this host cannot boot $image yet — setup-smoke will build what is missing:"
  CS_SANDBOX_IMAGE="$image" cs-sandbox doctor --engine firecracker 2>&1 |
    sed -e 's/\x1b\[[0-9;]*m//g' -n -e 's/^  NO  /        /p'
fi

# A deviating surface stops `create` on the first member, after the proxy is up
# and the group exists. Asking here costs nothing and names it before the wait.
#
# go.mod is the reference, exactly as it is for `cs-campaign doctor`: a built
# cs-campaign carries that manifest and refuses any other cs-sandbox. Read from
# the checkout rather than from the binary because this script runs in one.
echo
echo "Upstream:"
pinned=$(awk '/codesweep-ai\/sandbox / {print $2; exit}' go.mod 2>/dev/null || echo "")
# NR==1 because `cs-sandbox version` names the image it would use on a second
# line. Reading $2 from every line makes $live multi-line, and the comparison
# below then reports a mismatch on a host that is perfectly in step.
live=$(cs-sandbox version 2>/dev/null | awk 'NR==1{print $2}' || echo "")
if [[ -n $pinned && $pinned == "$live" ]]; then
  ok "go.mod names cs-sandbox $pinned, which is what is installed"
else
  bad "go.mod names cs-sandbox ${pinned:-?} and this host has ${live:-?}"
  bad "  go install github.com/codesweep-ai/sandbox/cmd/cs-sandbox@${pinned:-<version>}, or create will refuse"
  missing=1
fi

echo
if (( missing )); then
  fail "Something above is missing. record-fixtures-strict fails on it rather than
skipping, so fix it before a campaign starts."
fi

if (( check_only )); then
  echo "--check: everything the five scenarios need is present. Nothing recorded."
  exit 0
fi

cat <<'WARN'

This runs five real campaigns against real providers, and spends real money.
Re-recording REPLACES each cassette; it does not append.

WARN
read -r -p "Type 'record' to continue: " answer
[[ $answer == record ]] || fail "Nothing recorded."

echo
make record-fixtures-strict

cat <<'AFTER'

Recorded. Before committing, read what changed:

  git status --short
  git diff --stat test/cassettes/

Then replay them the way CI will:

  make test-smoke
AFTER
