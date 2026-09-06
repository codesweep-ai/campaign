#!/usr/bin/env bash
# Re-record every cassette the smoke tier replays, from this machine's own
# credentials.
#
# Six scenarios, each a real campaign: two microVMs, a fabric, a dispatch ladder
# and real model turns. This checks what they need before any of that starts, and
# `make record-fixtures-strict` fails on a scenario it cannot sign in for rather than
# skipping it. Recording five of six and reporting green is the outcome worth
# refusing.
#
#   ./scripts/record-fixtures.sh          record all six
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
[[ ${1:-} == --check ]] && { check_only=1; shift; }

# Named scenarios re-record just those, which is what a run that lost one or two
# wants: the rest are already committed, and re-recording a cassette that is
# fine spends money to replace it with a different recording of the same thing.
#
#   ./scripts/record-fixtures.sh codex-api-key opencode-fireworks
#
# It goes through this script rather than through `make record-fixtures` so the
# credential tree below is staged either way. Without it the lent scenarios find
# no key to borrow.
only=("$@")
fixture_tests=TestLiveRecordsACassette
if (( ${#only[@]} )); then
  fixture_tests="TestLiveRecordsACassette/($(IFS='|'; echo "${only[*]}"))"
fi

fail() { printf '\n%s\n' "$*" >&2; exit 1; }
ok()   { printf '  ok    %s\n' "$*"; }
bad()  { printf '  MISS  %s\n' "$*"; }

# The three API keys, from the .env this repository already keeps them in.
[[ -f .env ]] || fail ".env not found in $repo — it holds the three API keys."
set -a
# shellcheck disable=SC1091
. ./.env
set +a

# Where this host keeps its own credentials. cs-sandbox reads a login from
# <tree>/.cs-<agent> and a provider key from <tree>/.cs-keys/<provider>, and
# these are the profiles the wrappers keep rather than the agents' own
# directories.
source_home=${CS_SANDBOX_AGENT_HOME:-$HOME}

# The recording does not read that tree directly. CS_SANDBOX_AGENT_HOME moves
# the whole lookup, logins and keys together, so the run gets a scratch tree
# instead: the logins are symlinked, so no credential is copied anywhere, and
# the three keys are written from .env for the lender to read. Nothing lands in
# the developer's home, which now needs no ~/.cs-keys at all.
#
# Removed on exit. A SIGKILL is the one case that leaves the keys behind, and
# they are mode 600 inside a mode 700 directory when it does.
creds_tree=
cleanup_creds() { [[ -n $creds_tree ]] && rm -rf -- "$creds_tree"; }
trap cleanup_creds EXIT

lend_tree() {
  creds_tree=$(mktemp -d "${TMPDIR:-/tmp}/cs-campaign-record-creds.XXXXXX")
  chmod 700 "$creds_tree"
  for agent in claude codex; do
    [[ -e $source_home/.cs-$agent ]] && ln -s "$source_home/.cs-$agent" "$creds_tree/.cs-$agent"
  done
  mkdir -m 700 "$creds_tree/.cs-keys"
  write_key() { printf %s "${!2}" > "$creds_tree/.cs-keys/$1"; chmod 600 "$creds_tree/.cs-keys/$1"; }
  write_key anthropic ANTHROPIC_API_KEY
  write_key openai    OPENAI_API_KEY
  write_key fireworks FIREWORKS_API_KEY
  export CS_SANDBOX_AGENT_HOME="$creds_tree"
}

# The image these six campaigns will boot. The slim one, because that is what
# `make test-smoke` and CI replay on, and a cassette is bound to the image that
# recorded it: replay serves the model's tool calls from the cassette and then
# runs them for real, so a turn that used a binary the other variant drops
# replays as a member doing quietly less than it did. Reported rather than
# assumed — the Makefile picks it, and this is where an operator sees which.
image=${CS_SANDBOX_IMAGE:-$(cs-sandbox version --images 2>/dev/null | awk '$1=="image-slim"{print $2}')}

echo "Recording from:"
echo "  repo             $repo"
echo "  branch           $(git rev-parse --abbrev-ref HEAD)"
echo "  credentials in   $source_home (read through a scratch tree, never copied)"
echo "  cs-sandbox       $(cs-sandbox version 2>/dev/null | awk 'NR==1{print $2}' || echo MISSING)"
echo "  built against    $(awk '/codesweep-ai\/sandbox / {print $2; exit}' go.mod 2>/dev/null || echo '?')"
echo "  image            ${image:-MISSING} (slim — what test-smoke replays on)"
echo

missing=0

# Still .env, as they always were. What changed is where they are read: the
# three key scenarios lend now, and a lender reads a file rather than the
# caller's environment, so lend_tree writes each one into the scratch tree
# below. The variables are wanted for the preflight too, which runs each agent
# on this host against its real provider before it clears a cassette.
echo "API keys (.env):"
for v in ANTHROPIC_API_KEY OPENAI_API_KEY FIREWORKS_API_KEY; do
  if [[ -n ${!v:-} ]]; then ok "$v is set"; else bad "$v is not set"; missing=1; fi
done

echo
echo "Subscription logins:"
claude_cred="$source_home/.cs-claude/.credentials.json"
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

codex_cred="$source_home/.cs-codex/auth.json"
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
# is how a recording run died on all six scenarios, each after taking a group, a
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
  echo "--check: everything the six scenarios need is present. Nothing recorded,"
  echo "and no credential written: the scratch tree is built by a real run."
  echo "would run: go test -run '$fixture_tests'"
  exit 0
fi

if (( ${#only[@]} )); then
  printf '\nThis runs %d real campaign(s) against real providers, and spends real money:\n' "${#only[@]}"
  printf '  %s\n' "${only[@]}"
else
  printf '\nThis runs all six real campaigns against real providers, and spends real money.\n'
fi
cat <<'WARN'
Re-recording REPLACES each cassette; it does not append.

WARN
read -r -p "Type 'record' to continue: " answer
[[ $answer == record ]] || fail "Nothing recorded."

echo
lend_tree
echo "Credentials for this run: $CS_SANDBOX_AGENT_HOME"
echo "  .cs-claude, .cs-codex   symlinks to $source_home"
echo "  .cs-keys/*              written from .env, removed when this exits"
echo
make record-fixtures-strict FIXTURE_TESTS="$fixture_tests"

cat <<'AFTER'

Recorded. Before committing, read what changed:

  git status --short
  git diff --stat test/cassettes/

Then replay them the way CI will:

  make test-smoke
AFTER
