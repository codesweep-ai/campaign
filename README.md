# campaign

> **Give a team of AI coding agents one mission, run them in Firecracker microVMs, and get back
> evidence of what they did.**

[![CI](https://github.com/codesweep-ai/campaign/actions/workflows/ci.yml/badge.svg)](https://github.com/codesweep-ai/campaign/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
![Agents](https://img.shields.io/badge/agents-Claude%20Code%20%C2%B7%20Codex%20%C2%B7%20OpenCode-informational)
![Platforms](https://img.shields.io/badge/platform-Linux-lightgrey)

A **campaign** is one engagement: a repository, a mission, a team of coding agents, and the evidence
of what they did.

You write the mission and you pick the team. `cs-campaign` gives every member of that team its own
microVM and its own clone of the repository, then hands the mission to one member, the
**orchestrator**. The orchestrator works out how to get the mission done, splits it across the
others, and reviews what each one sends back. It decides when the mission is answered, or that it
cannot be. A campaign runs for hours or days while you watch, and you can take useful work out of it
at any point rather than only when it succeeds.

## What a campaign is made of

**The mission** is the goal, and the only thing the whole team is measured against. You write it in
`mission.md`, as an outcome rather than a task list. Give it criteria someone else could check,
because the orchestrator has to answer against them:

```markdown
Add a `--dry-run` flag to the report generator, and prove it writes nothing.

## Acceptance

1. `report --dry-run` exits 0 and creates no file.
2. A test fails without the flag's implementation and passes with it.
3. The flag is documented where the other flags are.
```

**The team** is one orchestrator and the agents working under it, named in `profile.yaml`. You
compose the team before the campaign starts and it is fixed for the campaign's life. The
orchestrator decides who does what, and cannot add a member, drop one, or change what one is for.

**A member** is one seat on that team: a microVM running one coding agent, with its own
clone of the repository and its own git branch that nobody else commits to. Claude Code, Codex and
OpenCode can each take either role.

**A role brief** says what one member owns, and there is one per member in `roles/`, the
orchestrator included. An agent's brief names what it owns, what it must not touch, and the proof
its work has to carry. The orchestrator's brief is how to run this team. It sets the rework rounds
to allow, what to do with a teammate that keeps failing, and what to verify before answering.

`cs-campaign init` scaffolds the profile, the mission and every brief as blanks to fill in.
[`testdata/example-campaign/`](testdata/example-campaign/) is a small complete one. This project's
own checks validate it on every change, so it stays true. Your campaign's files live in a directory
of your own, outside this repository.

## While the campaign runs

Work travels as **dispatches**. A dispatch is one unit of work, named by an ID the tool mints, and
it stays open until the member it went to writes a reply file. One rule covers the whole protocol:

> A dispatch is open until its reply appears. Anything sent while it is open continues it. The first
> thing sent after it closes opens a new one.

Nobody has to classify a message under that rule. Rework is a new dispatch, because the previous one
was closed by its reply. A nudge to a member that went quiet is a continuation, because its dispatch
is still open.

The mission is a dispatch too. The host opens it on the orchestrator as `m1` and holds it open for
the whole run. A campaign is running for exactly as long as `m1` is open, so there is no separate
lifecycle to keep in step with anything.

```
  host / operator                campaign group (one isolated network, one key pair)
 ┌────────────────┐   ┌───────────────────────────────────────────────────────────┐
 │ cs-campaign    │   │  ┌──────────────┐  d001 ──►  ┌───────────┐                 │
 │                │   │  │ orchestrator │  ◄── reply │ backend   │  own VM, own    │
 │  create ──── m1 ──►│  │              │            └───────────┘  branch, no SSH │
 │  observe ◄─────────┼─ │  plans       │  d001 ──►  ┌───────────┐  to anyone      │
 │  archive       │   │  │  judges      │  ◄── reply │ qa        │                 │
 │  destroy       │   │  │  reports     │            └───────────┘                 │
 └────────────────┘   │  └──────────────┘                                          │
        │             └───────────────────────────────────────────────────────────┘
        │                                    ▲
        └── one probe per node, on demand ───┘   nothing is stored: each look asks the machines
```

Nothing about a dispatch is stored on the host. What a member is doing is computed when you ask,
from that member's own machine, so nothing has to be caught as it happens:

```console
$ cs-campaign observe acme
DERIVED — computed now, from each node's own machine
  node           role           state            dispatch detail
  orchestrator   orchestrator   node-working     m1       open 4210s · 1 cont, 0 restarts
  backend        agent          node-replied     d004     reply present, not accepted
  qa             agent          node-working     d003     open 611s · 0 cont, 0 restarts

CLAIMED — the orchestrator's own record (a claim, shown beside the facts, never merged)
  09:14:02  plan        backend takes the parser; qa takes the fixtures.
  09:41:55  accepted    backend d003

gateway: port 8214 — one entrance for this campaign's services
```

The left half is computed from the machines. The right half is what the orchestrator says it did.
Keeping them apart is what lets you read "orchestrator says qa is working" beside "qa is
unreachable".

## How a campaign ends

The orchestrator's reply to `m1` is the campaign's verdict. It carries exactly one outcome, and all
four answer the same question: why are you stopping?

| Outcome | Stopping because | What you do next |
|---|---|---|
| `campaign-met` | every criterion the mission states is satisfied | nothing |
| `campaign-converged` | criteria are unmet, and more effort from this team would not close them | decide whether the gaps are acceptable |
| `campaign-exhausted` | budget or wall clock ran out while progress was still being made | resume, or extend |
| `campaign-blocked` | effort was never the problem, and no amount of iteration moves the obstacle | remove the obstacle |

The test is mechanical. If the orchestrator can name something unmet, the outcome is not
`campaign-met`. The other three have to list what is unmet, and that list is the real answer.

Unmet means unmet against the mission as written, never against some absolute standard. That is what
makes the acceptance criteria worth the time you spend on them. A mission with no criteria can never
be met, because there is no bar to clear.

The same shape holds one level down. An agent's reply says its own dispatch is concluded, and the
orchestrator then judges it against the branch rather than against the report. Anything it does not
accept goes back out as rework.

## The evidence

`archive` copies out what every member was asked and what it produced. Run it before `destroy`,
because a sandbox is the only copy of that member's work until it is harvested.

```text
archive/
├── campaign.json              the record: profile digest, resolved policy, every member
├── campaign-profile.yaml      the profile this campaign ran on
├── readback.json              each member's restatement of its own job
├── fleet-verdict.json         the audit's findings
├── member-harness.json        each member's tools, measured again at archive time
├── upstream-fingerprint.json  the tool versions the campaign ended on
├── orchestrator/              the same four channels as every agent
└── agents/<name>/
    ├── input/                 every dispatch this member was sent
    ├── output/                its replies, and anything durable it wrote
    ├── transcript/            its coding agent's own session record
    └── source-metadata/       branch, commits, diff against the base
```

Each kind of evidence is collected on its own, and they are never folded together:

- **The branch** is where every member commits its work. `fetch` pulls one into the host repository
  and reports the tree against the base, never a commit count. A branch that changes nothing reads
  as no change, however many commits it carries.
- **The reply** is the member's own account of what it did. The tool stamps a measurement next to
  that account: whether the branch really differs from its base, and whether anything was left
  uncommitted.
- **The transcript** is the raw session record its coding agent kept.
- **The orchestrator's log** records what it planned, what it accepted and what it assessed. That is
  a claim, so it sits beside the facts rather than inside them.

`audit` checks the team against that evidence rather than against what the profile declared. It
asks whether each member really ran the coding agent it was given, and whether any other family's
session or credential state is lying around. `archive` runs it first, while the sandboxes are still
up, because afterwards there is nothing left to ask.

Anything that fails to collect leaves an `INCOMPLETE-` marker where it should have been. Partial
evidence is worth more than none, and silently partial evidence is worth less than none. You can
re-run every check yourself from a fresh clone.

Afterwards, turn one archive into a single self-contained page:

```bash
cs-dispatch-viewer ./runs/acme-1 -o acme-1.html
```

## Quickstart

[INSTALL.md](INSTALL.md) covers the one-time host setup: the binaries, a group-aware
[`cs-sandbox`](https://github.com/codesweep-ai/sandbox), its agent tools, and the pin. After that a
campaign is five commands:

```bash
# 1. Scaffold the campaign's own files: profile, mission, one brief per member.
cs-campaign init acme --orchestrator codex --agent backend=claude --agent qa=codex
$EDITOR acme/mission.md acme/roles/*.md acme/profile.yaml

# 2. Check it. This allocates nothing and costs nothing.
cs-campaign validate acme/profile.yaml

# 3. Boot the team, brief it, make every member restate its job, and open the mission.
cs-campaign create acme --profile acme/profile.yaml

# 4. Watch. Ask as often as you like; nothing has to be caught as it happens.
cs-campaign observe acme

# 5. Take the evidence out before the machines go.
cs-campaign archive acme --output ./runs/acme-1
cs-campaign destroy acme
```

## What the harness does

**One campaign, one sandbox group.** Every member lives in a `cs-sandbox` group with its own
network, its own SSH keys and its own gateway. A second campaign gets its own group, and the two
cannot see or reach each other. Inside a group, agents can call each other's application ports but
cannot log in to each other: they do not have each other's keys.

**Every member has to say back what it was asked to do.** Sending a briefing and having it read are
not the same thing, and from outside the machine they look identical. So `create` asks each member
to describe its own job in its own words, shows you the answers, and refuses to start the campaign
if any member cannot answer.

**A stalled agent gets nudged, then restarted.** When an agent stops without answering, nothing can
tell from outside whether it merely halted or its session is wedged. So nothing tries to diagnose
it. It gets a nudge first, which is cheap and keeps its context, then a restart, which drops the
session and points it back at the work still open. You set how many of each are allowed. The
orchestrator does this for its agents; you do it for the orchestrator, which cannot see its own
failure.

## What it costs

Every member is a microVM running a frontier model for the length of a mission, so a
campaign spends real money for as long as it runs. Confirm the profile before `create`. The
`deadline` you declare tells the orchestrator when to stop, and nothing enforces a spending limit.

## Docs

- [INSTALL.md](INSTALL.md) · getting it onto a machine
- [MANUAL.md](MANUAL.md) · every command, flag, file and diagnostic
- [PROTOCOL.md](PROTOCOL.md) · the dispatch protocol every campaign runs on
- [SPEC.md](SPEC.md) · what it guarantees, and what is left open
- [CONTRIBUTING.md](CONTRIBUTING.md) · the gates and the conventions
- [AGENTS.md](AGENTS.md) · where an agent looks first
- [ledger/ledger.html](ledger/ledger.html) · what is broken, with the records under `ledger/issues/`

`cs-campaign --help` is generated from the code and is always current. Prefer it over any command
line quoted in a document.

## Contributing

Bug reports and pull requests are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) first: it names
the one command to run before you push, and what a change must not break.

## License

[Apache-2.0](LICENSE).
