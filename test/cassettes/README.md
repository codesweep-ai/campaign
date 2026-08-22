# Cassettes

`make test-smoke` replays what is here: one directory per scenario, each holding
one cassette per campaign member, recorded by `make fixtures` through a cs-vcr on
the campaign's own fabric.

One directory per scenario in `scenarios()`, two cassettes in each — ten in all,
because every campaign has an orchestrator and a dev:

```text
test/cassettes/
  claude-subscription/   csrclaude-orchestrator/  csrclaude-dev/
  claude-api-key/        csrclkey-orchestrator/   csrclkey-dev/
  codex-subscription/    csrcxsub-orchestrator/   csrcxsub-dev/
  codex-api-key/         csrcxkey-orchestrator/   csrcxkey-dev/
  opencode-fireworks/    csrocfw-orchestrator/    csrocfw-dev/
```

Both Claude rows are there on purpose, and neither covers the other. Claude Code
takes an API key in a header where a subscription takes OAuth, so the two send
different requests, and only recording both covers the pair. The same holds for
the two codex rows.

A scenario with no directory here is skipped by the smoke tier rather than
failed: a recording that was never made cannot have a broken replay. CI skips
the whole job when none exists.

A cassette is bound to the profile that recorded it. The campaign ID is the
sha256 of the resolved profile, so the group, both sandbox names, the branches
and the sessions all derive from it. Those names reach the wire inside tool-call
arguments, which cs-vcr matches exactly. Editing the profile the driver renders
invalidates the recording, so re-record rather than hand-editing a cassette.

Re-record one scenario at a time — each costs a real campaign:

```bash
make fixtures FIXTURE_TESTS='TestLiveRecordsACassette/codex-api-key'
```

## Ruleset compatibility

A cassette is keyed by cs-vcr's normalization ruleset, and its `cassette.yaml`
records which one under `normalize_version`. cs-vcr bumps that number when the
meaning of a key changes, and a cassette does not survive the bump: the keys
mean something else afterwards, so every request misses at once.

From the outside that is not an error but a hang. The members wait out the
protocol's whole ladder for turns that can never be served, and `make
test-smoke` sits silent for fifteen minutes before the readback bound finally
names something unrelated. So the question is asked before anything is
provisioned:

```bash
make fixtures-check
```

One process, no machine, no provider, so it runs on hosts that cannot run the
smoke tier at all — which is the point, since `make check` runs it and most
machines never boot a member. `make test-smoke` asserts the same thing per
scenario before it provisions. Either way the answer names the scenario and the
command that re-records it.
