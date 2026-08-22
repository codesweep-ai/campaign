# Contributing to cs-campaign

These rules apply to **humans and coding agents alike**. If you are an agent working in this repo,
read this file before you change anything and follow it.

This page is about changing the harness. For *using* it, see [MANUAL.md](MANUAL.md); for what it
must be, [SPEC.md](SPEC.md); for running a campaign rather than changing the harness,
[AGENTS.md](AGENTS.md).

Bug reports and pull requests are welcome. For a security issue, use GitHub's private vulnerability
reporting on this repository's Security tab, rather than opening a public issue.

## Submitting a change

File a bug or an idea as a GitHub issue on this repository. For a fix that stands on its own, a pull
request on its own is enough. For anything that changes behaviour a user can see, open an issue
first, so the design gets settled before you write it.

1. Fork the repository, and create a branch off `main`.
2. Make the change, with its test.
3. Run everything under **Before you push**.
4. Open a pull request against `main`, and say what the change does and why.

Expect comments rather than silence, and expect a small change to move quickly. A reviewer asks
whether the change keeps the design rules below, whether a test fails without it, and where a reader
would find it documented.

By opening a pull request you agree that your contribution ships under the
[Apache 2.0 licence](LICENSE) this project is released under.

## Before you push

Install the tools once. `golangci-lint` is pinned, because it gains checks between releases.

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
go install golang.org/x/tools/cmd/deadcode@latest
go install github.com/codesweep-ai/lint/cmd/cs-lint@latest
go install github.com/codesweep-ai/ledger/cmd/cs-ledger@latest
```

Then run these, in order:

```bash
make check         # gofmt, go vet, the Go linters, the unit tests and the prose checks
make ledger        # the issue records, and whether ledger.html is current
make test-smoke    # the whole protocol on real machines, with the model turns replayed
git status         # commit whatever the run regenerated under covmap/
```

`make test-smoke` takes about ten minutes and spends no money, because it replays the committed
cassettes instead of calling a provider.

**`go test` caches.** A run reporting every package `(cached)` has executed nothing. After changing
branches, rebasing or pulling, use `go test -count=1 ./...`. A cached green after moving eleven
commits is not evidence.

## Design rules

Your change has to keep these. Each one names the test or the review that holds it.

**No dispatch state is stored anywhere.** Whether a node is working, stopped or finished is computed
from that node's own machine on every look, and the computation lives in `internal/protocol`. A
status cached in a file, a field or a map that outlives the look lets the product report something
that was true an hour ago.

**A turn's ending is never a dispatch's outcome.** Exit codes, output footers, transcript quiet and
watcher verdicts are evidence about a turn or its watcher, and none of them closes a dispatch. Only
the reply artifact does. This is the single bug this whole product exists to avoid, and it is easy
to reintroduce in a helper that "just checks whether the command succeeded".

**`observe` prints two panes and never merges them.** Derived fact on one side, the orchestrator's
claim on the other. A merged status column destroys exactly the line an operator most needs.

**The host talks to the orchestrator alone.** `send` and `restart` refuse an agent by name. Agent
recovery is the orchestrator's ladder. Adding a host-to-agent repair path removes the only claim the
product makes that the team worked autonomously.

**No credential value enters a plan, a subprocess argument, campaign state or an archive.** Profiles
carry references; the values stay in the environment and in the members that were granted them.

Each of these is enforced by a test, and [SPEC.md](SPEC.md) §8 lists the conformance criteria in
full.

## Keep campaign material out of this repo

Profiles, mission prompts, rubrics, learnings and run archives belong in a workspace directory
outside this checkout. `cs-campaign` shares repositories into member microVM sandboxes, so anything
left here can arrive inside a team's clone. A campaign seeds its members from its own workspace,
which is why this repository ships the tool and none of the engagements.

`.gitignore` keeps the archive and scratch directories out of the tree, and it is not the guard.
Deciding where a campaign's files live is.

## Tests

Ship a test with your change. Where a behaviour genuinely cannot be observed in a test, say so in
the pull request.

`make test` is the tier a change usually needs. It fakes `cs-sandbox` and the transport rather than
the protocol, so the same `internal/protocol` code that runs in production computes state in the
tests.

Test the contract, not the implementation: the exit code, the message the operator reads, the file
that appeared in a channel. Test what happens when it fails, not only when it works. Say why the
case matters in a comment when it is not obvious.

**When you add a promise to a document, add the assertion that keeps it true.**
`internal/cli/docclaims_test.go` holds them, each naming the document and the sentence it restates.

Never lower a coverage baseline to make a run green. [`SPEC.md`](SPEC.md#76-testing-strategy) holds
the four tiers, what each one proves and costs, the backend matrix, how a cassette is recorded, the
behaviour map, and how coverage is measured. Read it before you run anything above the unit tier.

## Upgrading the upstream surface

`cs-campaign` runs on `cs-sandbox` plus the agent tools it ships, and `pin.json` records the sha256
of every one with a note saying why they are trusted. `create` refuses a deviating surface unless
`--accept-upstream-change` records the deviation on the campaign.

The order is **build, then validate, then pin**, never pin first:

1. Build and install the new `cs-sandbox`. Rebuild the guest image if its `image/` changed, or
   members keep the old tools.
2. Run `make test-integration`. This is the validation, and it must pass on the new surface.
3. Run `cs-campaign pin --update --note "<what you ran, and what it proved>"`.
4. Commit the repo copy of `pin.json`, so the acceptance is reviewable.

`pin --update` exists because plain `pin` refuses to overwrite a deviating pin. That refusal is the
feature. The note is the whole point of the record: a pin recorded without running step 2 is
indistinguishable from one that was earned.

**Never `make install` in the sandbox repo while a campaign is live.** It replaces the pinned binary
underneath a running team.

## Issues

This repo keeps a **ledger** of open issues in `ledger/`. Read [`ledger/AGENTS.md`](ledger/AGENTS.md)
before you start work and follow it. File records before building, close only with verified
evidence, keep `ledger/queue.json` current, and run `cs-ledger render && cs-ledger check` before
every commit that touches `ledger/`. `make ledger` runs the check half, and CI runs it too.

The rendered page is `ledger/ledger.html`: generated, never hand-edited and never hand-merged. On
conflict, re-render.

IDs (`SAC-NNN`) are stable, never renumbered and never reused. A record closes only with the
resolving commit and a statement of **how it was proved**, with the measurement rather than
"fixed".

**Closing a record from campaign work cites two hashes: the member's own commit and the integration
commit.** A squashed merge makes the member's hash unresolvable from the integration branch alone.
A closure citing only the later commit then loses the trail back to the work that earned it.

## Commits

**Keep it short.** One idea per commit, and a message a reader takes in at a glance. If a change
will not fit one idea, split it.

**Subject**, always. Under 60 characters, imperative, no trailing period, completing *"If applied,
this commit will …"*. Say what the change does, in plain English rather than in this project's
internal shorthand. Use no conventional-commit prefix: `fix(proxy):` names a category rather than a
change, and the category is already in the diff.

**Body**, rarely. Most commits need none. Add one only when the subject leaves a question a reader
would otherwise have to open the diff to answer, and then answer that question. A sentence or two
does it. Wrap it at 72 columns.

Leave out how the work was scheduled, how you tested it, and what led you to it, and stop once the
question is answered. A second paragraph usually means the message has turned into a report of the
session. A rule's reason belongs beside the rule in [`SPEC.md`](SPEC.md), and the investigation that
found it belongs in the pull request.

```
Refuse a socket path the AF_UNIX limit would truncate
```

```
Re-arm the settling window on a restart re-anchor

Otherwise the restart rung reads as stuck while the machine
is still booting, and the ladder gives up on a live member.
```

Keep the `Co-Authored-By:` trailer when an agent wrote the change. Drop any trailer linking to the
agent's session or transcript. Such a link is private to whoever ran it and dead to everyone else,
and it cannot be fixed after publication.

## Docs

Behaviour a user can see belongs in the documents next to it, updated in the same commit as the
code.

| Document | Job |
|---|---|
| [README.md](README.md) | The tour: what this is and the shortest path to seeing it work. |
| [INSTALL.md](INSTALL.md) | How to get the binaries and the setup they need once. |
| [MANUAL.md](MANUAL.md) | Every command, flag, file, variable, exit code and diagnostic. |
| [PROTOCOL.md](PROTOCOL.md) | The dispatch protocol, stated for any implementation of it. |
| [SPEC.md](SPEC.md) | What the behaviour must be, and what is left open. |
| [AGENTS.md](AGENTS.md) | The router an agent lands on. It holds no knowledge of its own. |
| [`ledger/GUIDE.md`](ledger/GUIDE.md) | How to keep the ledger. |

A change to a **MUST** in `SPEC.md` changes the contract, so say so in the pull request. `MANUAL.md`
is compiled into `cs-campaign` and printed by `cs-campaign manual`, so editing it changes a shipped
artifact.

## Writing

Six principles do most of the work. Read them before you write a document, and apply them when you
edit one:

1. **Introduce a term where you first use it**, in the same sentence, or link to the page that
   defines it. A reader should never meet a word the docs have not explained.
2. **State the point first, then qualify it.** Opening with the qualifier makes the reader decode
   the sentence backwards.
3. **Give every sentence a subject and a verb.** "Two version numbers, one verdict, one remedy"
   reads as knowing rather than clear. Say what the thing is.
4. **A walkthrough is steps that work.** Put the reasons somewhere else. A reader working through
   one wants commands that run.
5. **Describe what the software does, not how it came to do it.** Leave out what the project used
   to do, what was tried and dropped, and numbers from a run somebody did once.
6. **Do not explain a design by contrast with a worse one.** Say what it is and what you get,
   rather than asking the reader to picture a design nobody proposed.

The mechanical rules are enforced rather than restated here.
[`cs-lint`](https://github.com/codesweep-ai/lint) carries them, and `make check` runs it over this
repository. To read what a rule wants and the guidance behind it:

```bash
cs-lint docs --explain
```

That listing is the authority. Where this section and the linter disagree, the linter is right.
Turning a check off is a waiver: write it under `allow` in [`.cs-lint.yaml`](.cs-lint.yaml) with the
reason, which is printed with the finding.

## Style

Match the file you are editing: dense, comment-light code with occasional long explanatory comments
where something is genuinely non-obvious. Keep those. Each one marks a place where the obvious
implementation is wrong.

## AI-assisted contributions

An agent wrote most of this repository, and you are welcome to use one. The standard is the same
either way: you are responsible for what you submit.

Point your tool at [`AGENTS.md`](AGENTS.md), which routes it to the documents that hold the
conventions, and check three things before you open the pull request:

- You understand every line, and can answer a question about it without going back to the tool.
- You ran `make check` and it passed.
- You cut what the tool added to fill space. A model pads a commit body to the shape it was shown,
  and comments that restate the code around them. Both read as noise to a maintainer, and both are
  yours to remove.

Keep the `Co-Authored-By:` trailer, which is how the work is disclosed. An unattended agent must not
open pull requests or comment on this repository.
