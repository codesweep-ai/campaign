# campaign

> **Give a team of AI coding agents one mission, run them in microVM sandboxes, and get back
> evidence of what they did.**

[![CI](https://github.com/codesweep-ai/campaign/actions/workflows/ci.yml/badge.svg)](https://github.com/codesweep-ai/campaign/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
![Agents](https://img.shields.io/badge/agents-Claude%20Code%20%C2%B7%20Codex%20%C2%B7%20OpenCode-informational)
![Platforms](https://img.shields.io/badge/platform-Linux-lightgrey)

A **campaign** is one engagement: a repository, a mission, a team of agents, and the evidence of
what they did. The mission says what the team is there to accomplish.

`cs-campaign` boots one **orchestrator** and any number of named **agents**, each in its own microVM
sandbox, and hands them the repository and the mission. The orchestrator works out how to get the
mission done, splits the work across the team, and reviews what each member sends back. It decides
when the mission is finished, or that it cannot be. A campaign runs for hours or days, and you can
harvest useful work from it at any point, not only when it succeeds.

Once it is running, the team works the mission on its own and you watch.

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

## What you write

A campaign is three kinds of file, and `cs-campaign init` scaffolds all of them for you to edit.

**The profile** names the team: who the orchestrator is, what agents there are, which coding agent
each one runs, and which repositories they get.

**The mission** says what to do and how you will know it is done. Keep the acceptance criteria
checkable, because the orchestrator answers against them:

```markdown
Add a `--dry-run` flag to the report generator, and prove it writes nothing.

## Acceptance

1. `report --dry-run` exits 0 and creates no file.
2. A test fails without the flag's implementation and passes with it.
3. The flag is documented where the other flags are.
```

**A role brief per member** says what that member owns. The orchestrator's says how to split the
work and when to answer the mission. An agent's says what it is responsible for, and to commit its
work to its own branch.

[`testdata/example-campaign/`](testdata/example-campaign/) is a complete one: a profile, a mission
and two role briefs, for one orchestrator and one agent. This project's own checks validate it on
every change, so it stays true. Copy it and edit, or start from `cs-campaign init`.

Your campaign's files live in a directory of your own, outside this repository.

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

`observe` is the whole observation surface, and it prints two panes that are never merged:

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

The left half is computed from the machines; the right half is what the orchestrator recorded.
Keeping them apart is what lets you see "orchestrator says qa is working" beside "qa is
unreachable".

Afterwards, turn the archive into one self-contained page:

```bash
cs-dispatch-viewer ./runs/acme-1 -o acme-1.html
```

## What the harness does

**One campaign, one sandbox group.** Every member lives in a `cs-sandbox` group with its own
network, its own SSH keys and its own gateway. A second campaign gets its own group, and the two
cannot see or reach each other. Inside a group, agents can call each other's application ports but
cannot log in to each other: they do not have each other's keys.

**You decide the team; the orchestrator decides the work.** You choose the members and write the
mission before the campaign starts, and the team is fixed for its whole life. The orchestrator hands
out the work, but cannot add a member, remove one, or change what one is for.

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

**The evidence is the point.** `archive` copies out every member's inputs, outputs and transcript.
It also runs `audit`, which checks that each member really ran the agent it was given. That has to
happen while the machines are still up. Each member works on its own git branch, so `fetch` tells
you whether that branch actually changed anything. An empty commit shows up as no change, and you
can re-run all the checks yourself from a fresh clone.

## What it costs

Every member is a microVM sandbox running a frontier model for the length of a mission, so a
campaign spends real money for as long as it runs. Confirm the profile before `create`. The
`deadline` you declare bounds the orchestrator's judgement, not the bill.

## Docs

| You want to | Read |
|---|---|
| get it onto a machine | [INSTALL.md](INSTALL.md) |
| use the tools | [MANUAL.md](MANUAL.md): every command, flag, file and diagnostic |
| understand the dispatch model | [PROTOCOL.md](PROTOCOL.md): the protocol every campaign runs on |
| know what it guarantees | [SPEC.md](SPEC.md): the contract, and what is left open |
| change the harness | [CONTRIBUTING.md](CONTRIBUTING.md): the gates and the conventions |
| work here as an agent | [AGENTS.md](AGENTS.md) |
| know what is broken | [`ledger/ledger.html`](ledger/ledger.html), or the records under `ledger/issues/` |

`cs-campaign --help` is generated from the code and is always current. Prefer it over any command
line quoted in a document.

## Contributing

Bug reports and pull requests are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) first: it names
the one command to run before you push, and what a change must not break.

## License

[Apache-2.0](LICENSE).
