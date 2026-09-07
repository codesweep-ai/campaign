# The cs-campaign playbook

The manual says what every command does. This says how to decide what to run, what to write, and
when to leave the team alone.

A **campaign** is one engagement: a fixed team of coding agents, a mission, and the evidence of
what they did. Read [MANUAL.md](MANUAL.md) for the surface and [SPEC.md](SPEC.md) for the contract.
Read this before you author one, and again while one is running.

Nothing here is enforced. Every rule the product does enforce is in the manual, and the two are
written to agree.

## Is this work a campaign?

A campaign suits work that splits into parts one team can own for hours or days, where the
definition of done can be written before anyone starts.

Three properties make a mission fit:

- **The work decomposes at a seam you can name.** You have to say who owns what before the team
  boots, so a mission whose parts only become visible halfway through will be delegated badly.
- **Done is checkable by someone who was not there.** The product never grades output. It checks
  that a reply exists and carries an outcome, and the rest is your judgement reading the evidence.
- **The parts can proceed without talking to each other.** An agent has no route to the
  orchestrator or to any peer. Work that needs a conversation between two agents needs the
  orchestrator to carry it, one dispatch at a time.

Work that does not fit is not a failure of the tool. A single-file change, an investigation with no
statable goal, or anything needing a human decision every few minutes is faster run by hand.

Two costs are yours and the product does not bound either. Machines bill for as long as they run,
and `deadline` bounds the orchestrator's judgement rather than the spend. A campaign nobody watches
can spend all night converging on the wrong thing.

## Designing the team

**Decomposition happens twice, and only the second is the orchestrator's.**

| | Who | When | What |
|---|---|---|---|
| Structural | you | before `create` | which roles exist and what each owns |
| Dynamic | the orchestrator | during the run | which work goes where, and whether it is acceptable |

The team is immutable for the campaign's life. An orchestrator that discovers it needs a fourth
agent cannot have one, so the structural decomposition is a real design decision made under
uncertainty. Get it wrong and your remedy is to destroy the campaign and create another.

### How many seats

Start from the seams, not from a headcount. One agent per thing that can be owned outright: a
subsystem, a package, a service and its runtime. Give a seat work that would still be coherent if
nobody else finished.

Two agents editing one file is the failure this design cannot rescue. Each commits to its own
branch, and the orchestrator merges. Overlapping ownership turns every merge into a conflict the
orchestrator has to adjudicate from two partial views.

Fewer seats than seams is usually the safer error. An orchestrator can serialize two pieces of work
onto one agent. It cannot split an agent that owns too much.

Three seats plus an orchestrator is a large campaign. Start smaller than feels right.

### The orchestrator is a seat too

It plans, delegates, reviews returned work and reports the outcome. It also needs the tools to do
that. Give it every repository it must judge, because `fetch` pulls a teammate's branch into the
orchestrator's own clone. An orchestrator without the repository can read a reply and cannot inspect
the work behind it.

Give it a model you would trust to review, and headroom to think. Its stall threshold defaults to
1800 seconds against an agent's 180, because reviewing is quieter work than writing.

### Trees a member is given

Two kinds of directory reach a member, and they mean different things:

- **A repository** arrives as a writable clone at the campaign's base commit, on that member's own
  branch. Work that is not committed there is destroyed with the machine.
- **A snapshot** arrives as a frozen copy the member can read and not change. Use one for a
  reference implementation, a corpus, a design document set, or last quarter's code.

Both land at `$HOME/<name>` inside the member, named by the profile or by the last segment of the
host path. Declare them per member in the profile:

```yaml
agents:
  backend:
    cli: claude
    repos: [{path: /srv/product, ref: main}]
    snapshots: [{path: /srv/reference, name: reference}]
```

The `--repo` and `--snapshot` flags are the profile-less shorthand. Each takes one path and hands it
to every member, which is enough to try something and not enough to describe a real team.

### Phasing work across a fixed team

A campaign has one mission and one team for its whole life, so phases are the orchestrator's policy
rather than a product feature. Write the phases into the orchestrator's brief: what must be true
before the second phase starts, and what the team does with a seat whose phase has not begun.

A seat with nothing open reads as `node-free`. That is a resting state, not a fault.

## Writing the mission

**Decide the acceptance gates first.** Everything else hangs off the definition of done, including
the outcome the orchestrator has to report against it.

State an outcome, not a task list. The orchestrator decides how to get there, and a mission written
as steps removes the judgement you are paying for.

Be specific enough that "did we get there?" has an answer someone else would reach independently.
The orchestrator reports one of four outcomes, and three of them have to name what remains unmet:

| Outcome | What it says | Your next move |
|---|---|---|
| `campaign-met` | The work satisfies the mission. | Verify it yourself, then harvest. |
| `campaign-converged` | The mission is satisfiable, and this team stopped getting closer. | Change the team or the approach. |
| `campaign-exhausted` | Budget or wall clock stopped it while progress was still being made. | Resume the work with a new campaign. |
| `campaign-blocked` | An obstacle no amount of iteration resolves. | Unblock it, or rewrite the mission. |

A vague mission produces a vague `campaign-converged`, which is the costliest report to receive. It
asserts the goal was reachable and blames the team for not reaching it.

Write the out-of-scope list. Campaigns expand to fill the space you leave, and the orchestrator has
no way to know which nearby work you consider a distraction.

## Writing the briefs

Every member gets standing context in two halves, written by two different people.

The product writes the first half. `create` seeds every member an orientation stating its identity,
its clone and branch, the files it was given, and its obligations. Read it before you write your
half:

```sh
cs-campaign orientation acme --profile acme/profile.yaml --member backend
```

You write the second half: what this member owns in this campaign. The two must not restate each
other and must not disagree. Nothing before `create` catches a contradiction, and a member holding
two instruction sources has no rule for which one wins.

Four contradictions are easy to write and all four reach the member as fact:

- Telling an agent it can ask the orchestrator a question. It has no route to one.
- Pointing a teammate at the mission. Only the orchestrator is given it.
- Naming a member verb that member's role refuses.
- Inventing a fifth outcome value.

### An agent's brief is scope

Three things, and the third is the one authors leave out:

1. **What this member owns.** Files, directories, subsystems. Be concrete enough that a second
   agent reading it would not claim the same ground.
2. **What it must not touch.** Campaigns go wrong at the seams, so name the things that are
   somebody else's.
3. **What proof its work must carry.** A test run, a benchmark, a migration applied. Say what must
   be true before this member reports done, beyond committing on its own branch.

Write for a reader with no context, because that is exactly what a member is. It cannot see the
mission, the other briefs, or anything you decided in your head.

Never hand two members the same brief with a name changed. A generic briefing is restated faithfully
by a member that read nothing, so the readback cannot see through it.

### The orchestrator's brief is policy

The mission says what must be achieved. Each agent's brief says what that agent owns. The
orchestrator's brief says how this team is run, and only you can decide it:

- How many rework rounds before it stops sending a piece back.
- What it does when a teammate fails that many times: reassign, take it on, or abandon that piece.
- What it must verify itself before reporting an outcome, in its own clone.
- Whether it writes code at all, or delegates everything.

It is accountable for what the campaign delivers, so it must verify returned work rather than accept
a member's report. Say that in terms of this campaign: which tests, run where.

## Before you spend

Four checks cost nothing and run before any machine exists:

```sh
cs-campaign doctor                                   # is this host able to run a campaign
cs-campaign validate acme/profile.yaml               # is the profile and its files coherent
cs-campaign orientation acme --profile acme/profile.yaml --member backend
cs-campaign plan acme --profile acme/profile.yaml    # the resolved campaign, as JSON
```

Read the plan. Every generated name, the group, the network, the resolved policy numbers and each
member's branch are visible there before a microVM bills a second.

Then `create` runs the readback. Every member is asked to restate its own job before the campaign
is usable, and a member that cannot confirm its briefing fails creation by name.

**Read the restatements.** The check is structural, so it holds identity, branch, missing inputs and
obligations, and it does not grade prose. A member can satisfy it with a restatement that omits most
of its brief, and only you reading the text would notice. This is the last cheap moment to find a
brief that did not land.

## While it runs

One command is the whole observation surface:

```sh
cs-campaign observe acme
```

It prints two panes and never merges them. **DERIVED** is every node's state computed now, from that
node's own machine. **CLAIMED** is the orchestrator's own log, which is a claim. The line this
command exists for is "orchestrator says qa is working" sitting next to "qa is unreachable".

Nothing has to be caught as it happens. No dispatch state is stored, so a state is computed when you
ask and is never a stale reading from an hour ago.

| State | What it means | What you do |
|---|---|---|
| `node-free` | No dispatch is open, or the open one was replied to and accepted. | Nothing. |
| `node-working` | A dispatch is open and the turn driver is alive. | Nothing. |
| `node-stopped` | A dispatch is open, no driver is alive, the ladder has a move left. | Nothing. The ladder runs. |
| `node-replied` | The reply exists and the orchestrator has not accepted it. | Nothing. Judging it is the orchestrator's job. |
| `node-stuck` | The ladder is spent, the bound tripped, or the machine is gone. | Read the transcript, and decide. |
| `node-unreachable` | This look failed. It overlays a state rather than replacing one. | Look again before concluding anything. |

The right answer is usually to do nothing. Recovery is mechanical and already running: templated
continues, then a restart re-anchor, bounded by the policy numbers you set.

### The rules that cost the most to break

**Never type into a live session.** Keystrokes land in the member's terminal interface, and the work
that follows is no longer the team's. Observe with read-only probes instead: `observe`, transcript
growth, and `git log` on the member's branch.

**Never answer a stalled orchestrator.** A stuck orchestrator is a finding worth more than the fix.
Unsticking it destroys the finding and any claim that the team worked autonomously. Record what it
was doing, then decide whether this campaign is still answering your question.

**Never repair an agent from the host.** The `send` and `restart` commands refuse an agent by name.
Agent recovery belongs to the orchestrator's ladder, and a host-to-agent repair path removes the only
claim this product makes about autonomy.

The one failure only you can see is the orchestrator stopping, because it cannot observe its own
death:

```sh
cs-campaign send acme --file nudge.md    # continues m1 if open, opens a dispatch if past it
cs-campaign restart acme                 # drop a wedged session, re-anchor on the open dispatch
```

Use `--file` for anything long. Text passed as an argument goes through a shell, so backticks and
`$( )` are substituted before delivery.

### The numbers are yours

The policy numbers govern the dispatch machine for the whole campaign, and the defaults suit a team
doing ordinary work. Raise `continueAttempts` and `restarts` when turns are long and expensive to
lose. Raise `stallSeconds` for a seat whose work is quiet, such as one reading a large tree before
it writes anything.

No setting removes the underlying trade. Recovery cannot tell a wedged model from a slow one, so a
short ladder abandons live members and a long one burns turns on dead ones.

### Harvest early

Do not wait for the end to find out whether anything is landing:

```sh
cs-campaign fetch acme/backend
```

It reports tree against base rather than commit count, so a branch with twelve commits and no change
is named as delivering nothing. That is a signal worth having on day one rather than at teardown.

## Harvesting and closing out

The microVMs are the only copy of a member's work until you harvest it. Order matters:

```sh
cs-campaign fetch acme                     # every member's branch into the host repository
cs-campaign archive acme                   # evidence, with the team audit in the same pass
cs-dispatch-viewer archives/acme-<stamp>   # one self-contained HTML page
cs-campaign destroy acme                   # teardown, and reclaim the group
```

**Archive before destroy, always.** The audit runs inside the archive while the sandboxes still
exist, so it can check that each member's declared CLI is the one that did the work. After teardown
that question is unanswerable.

**Read the output, not the exit code.** Collection is bounded per member and leaves an
`INCOMPLETE-*` marker where a step could not finish. A zero exit is compatible with a partial
archive, and partial evidence you know about is worth more than none.

Then do the part no command does for you. Re-run the acceptance gates yourself, from a fresh clone
of the integration branch. That independent check is the point of the exercise, and it has caught
failures that every test inside the campaign passed.

Judge the mission reply against the mission you wrote. An outcome is the orchestrator's claim, and
the branches, the transcripts and the archive are the evidence for or against it.

## What to keep

A campaign produces two kinds of result, and the second is easy to throw away.

The **work** is the branches and the archive. Harvest it, verify it, merge what you want.

The **findings** are what the run taught you about running campaigns. A brief that was ambiguous, a
seam that was really one seat, a phase that never started, a number set too low. Write them down
while the archive is open. They are what makes the next campaign cheaper, and nothing in
the product records them for you.

Keep campaign material out of the product repository. Profiles, missions, briefs, rubrics and
archives belong in a workspace directory of their own. A campaign shares repositories into member
microVMs, so anything left in this tree arrives inside the team's clone.
