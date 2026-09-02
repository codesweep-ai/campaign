#!/usr/bin/env bash
# Prove the committed cassettes can still be replayed by the pinned cs-vcr.
#
# A cassette is keyed by a normalization ruleset, and cs-vcr bumps that ruleset
# when the meaning of a key changes. Replaying a cassette across such a bump
# does not miss a few entries: every key means something else now, so every
# model call misses at once. What that looks like from the outside is not an
# error but a hang -- the members wait out the protocol's whole ladder for turns
# that can never be served, and `make test-smoke` sits silent for fifteen
# minutes before the readback bound finally names something unrelated.
#
# test-smoke asserts this per scenario before it provisions anything, which is
# the check that matters most. But that tier needs /dev/kvm, podman and a
# group-aware cs-sandbox, so on most machines -- and in most CI jobs -- it never
# runs at all, and the fixtures rot unobserved. This asks the same question with
# one process and no machine, which is what lets `make check` carry it.
#
# Exit 0 when every cassette replays under the current ruleset, or when there is
# nothing to check: no cassettes at all is a repository that has not recorded
# any. cs-vcr itself is pinned in go.mod and run with `go tool`, so it is always
# there and this no longer skips for a missing tool.
set -euo pipefail

root=${1:-test/cassettes}

shopt -s nullglob
scenarios=("$root"/*/)
if [ ${#scenarios[@]} -eq 0 ]; then
  echo "fixtures: no cassettes under $root; nothing to check" >&2
  exit 0
fi

# cs-vcr names cassettes relative to a configured root, so the root goes in a
# config and the members are named beneath it. One config for the whole run:
# every scenario resolves against the same root.
config=$(mktemp)
trap 'rm -f "$config"' EXIT
printf 'cassettes: %s\n' "$(cd "$root" && pwd)" > "$config"

# Per scenario rather than all at once, so a failure can name the one command
# that re-records what broke.
status=0
for scenario in "${scenarios[@]}"; do
  name=$(basename "$scenario")
  members=()
  for index in "$scenario"*/index.jsonl; do
    members+=("$name/$(basename "$(dirname "$index")")")
  done
  [ ${#members[@]} -eq 0 ] && continue

  if ! output=$(go tool cs-vcr cassette verify --config "$config" "${members[@]}" 2>&1); then
    echo "$output" >&2
    echo "  re-record with: make record-fixtures FIXTURE_TESTS='TestLiveRecordsACassette/$name'" >&2
    status=1
  fi

  # A cassette can be perfectly well-formed and still be the wreck of a run that
  # never finished: cs-vcr writes entries as it serves them, so a recording that
  # dies partway leaves valid keys, a clean `cassette verify`, and no verdict.
  # The recording writes recorded.json before it starts and settles it only once
  # the campaign is shown to have met its mission, so an unsettled claim says
  # exactly that and nothing else.
  #
  # A missing claim is NOT a failure. Every cassette recorded before this
  # existed has none, and refusing those would fail the gate on fixtures that
  # replay perfectly well. It is worth one line, because a fixture nobody can
  # vouch for is worth knowing about.
  claim="$scenario""recorded.json"
  if [ ! -f "$claim" ]; then
    echo "note: $name has no recorded.json — recorded before completeness was tracked; the next re-record adds one" >&2
    continue
  fi
  outcome=$(sed -n 's/.*"outcome"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$claim" | head -1)
  case "$outcome" in
    campaign-met) ;;
    in-progress)
      # The claim was never settled: the run started and did not finish.
      echo "$name: the recording that produced this never finished" >&2
      echo "  its entries are valid and its campaign never reached a verdict" >&2
      echo "  re-record with: make record-fixtures FIXTURE_TESTS='TestLiveRecordsACassette/$name'" >&2
      status=1
      ;;
    *)
      # Settled, but on a verdict the smoke tier asserts against. Recording one
      # of these commits a fixture whose replay is required to fail.
      echo "$name: recorded a campaign that was not met (outcome: ${outcome:-unreadable})" >&2
      echo "  the smoke tier asserts campaign-met, so this cassette can only replay red" >&2
      echo "  re-record with: make record-fixtures FIXTURE_TESTS='TestLiveRecordsACassette/$name'" >&2
      status=1
      ;;
  esac
done

if [ $status -eq 0 ]; then
  echo "ok  every committed cassette replays under this cs-vcr's ruleset"
fi
exit $status
