# The cs-campaign playbook

The manual says what every command does. This says how to decide what to run, what to write, and
when to leave the team alone.

A **campaign** is one engagement: a fixed team of coding agents, a mission, and the evidence of
what they did. Read [MANUAL.md](MANUAL.md) for the surface and [SPEC.md](SPEC.md) for the contract.
Read this before you author one, and again while one is running.

Nothing here is enforced. Every rule the product does enforce is in the manual, and the two are
written to agree.

## What goes wrong

Almost every expensive failure is an input defect. A number nobody checked, two lines that cannot
both hold, a mandate whose consequences were never mapped, an instruction the member cannot carry
out. The team is rarely the problem.

That is why the work before `create` is the cheapest work in a campaign. A first round that needs
several rounds of rework is telling you to fix the inputs, not to spend the deadline on reruns.

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

### Sizing against the deadline

The critical path is the longest serial chain of work inside one member, not the total. Take that
member, list its work in order, and ask of each piece whether it is a morning, a day or a week. A
new parser with a conformance harness is not a morning.

If the chain does not fit the deadline, there are three levers:

- **Cut scope.** Decide now which parts come out, not at hour thirty.
- **More cut points**, so a member stops waiting on work unrelated to it.
- **More, smaller dispatches**, each judged before the next opens.

Write down what gets dropped and when, before you start. A decision you have not pre-authorised is
a decision nobody will make while the clock runs.

### The orchestrator is a seat too

It plans, delegates, reviews returned work and reports the outcome. It also needs the tools to do
that. Give it every repository it must judge, because `fetch` pulls a teammate's branch into the
orchestrator's own clone. An orchestrator without the repository can read a reply and cannot inspect
the work behind it.

Give it a model you would trust to review, and headroom to think. Its stall threshold defaults to
1800 seconds against an agent's 180, because reviewing is quieter work than writing.

### Repositories and snapshots

**Every member needs a repository, and a campaign that declares none has nowhere to put its work.**
A member commits to a clone of a repository the profile gave it, harvesting reads that branch, and
anything a member never committed is destroyed with its machine. Decide the repositories before
anything else. `validate` refuses a member without one.

Two kinds of directory reach a member, and they mean different things:

- **A repository** arrives as a writable clone at the campaign's base commit, on that member's own
  branch. It is the only place a member's work survives.
- **A snapshot** arrives as a frozen copy the member can read and not change. Use one for a
  reference implementation, a corpus, a design document set, or last quarter's code.

Both land at `$HOME/<name>` inside the member, named by the profile or by the last segment of the
host path. Declare them per member:

```yaml
orchestrator:
  cli: codex
  repos: [{path: /srv/product}]
agents:
  backend:
    cli: claude
    repos: [{path: /srv/product}]
    snapshots: [{path: /srv/reference, name: reference}]
```

`repos[].ref` picks the branch or tag a member is cloned from, on a repository that already exists.
Leave it out for one the tool is about to create, because there is no branch there to name yet.

Give the orchestrator every repository it must judge. Without the clone it can read a reply and
cannot inspect the work behind it.

The `--repo` and `--snapshot` flags are the profile-less shorthand. Each takes one path and hands it
to every member, which is enough to try something and not enough to describe a real team.

### Starting an application that does not exist yet

Name the path the repository should live at, and let the tool make it. Do not create it first:

```yaml
orchestrator:
  cli: codex
  repos: [{path: ~/work/task-tracker}]   # nothing is there yet
```

`plan` pins a first commit and creates nothing, so you can read the whole campaign before anything
exists. `create` makes the repository at that path and clones every member from it. Do not give it a `ref`:
the repository is born on `main` with one commit, and any other branch is one nothing will create.

Three cases, and the difference matters:

| The path you name | What happens |
|---|---|
| does not exist | created at `create`, with a first commit as the base |
| holds a git repository with no commits | adopted, on `main` |
| holds files and no git repository | refused, so nothing is adopted by accident |

The application has to live in a repository on the host rather than only inside machines you are
going to destroy. The base commit is what makes the work fetchable, mergeable and comparable
afterwards, and for something new there is no natural base, so the tool makes one.

Then decide who owns running it. The member that owns an application owns its runtime: build
scripts, compose definitions, databases and the containers they run in. That is part of what it
delivers rather than something the orchestrator arranges afterwards, so it is a seam to settle
before you write the briefs.

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
- Pointing an agent at the mission. Only the orchestrator is given it.
- Naming a member verb that member's role refuses.
- Inventing a fifth outcome value.

### What the tool writes, and what you write

`cs-campaign init` writes one file, the profile, and names the rest:

```text
acme/
├── profile.yaml        scaffolded: who runs, on what, with which credentials
├── mission.md          yours to write
└── roles/<member>.md   yours to write, one per member, orchestrator included
```

Every seat is a **member**. One member is the **orchestrator** and the rest are **agents**, and the
two roles take different briefs.

The headings suggested below are only suggestions. Nothing parses these files and a member reads
its brief as prose, so use whatever shape suits the campaign. What has to survive is the content.

Nothing scaffolds the mission or the briefs, and that is deliberate. Those two are the documents a
member is seeded with, and the check that refuses an unbriefed campaign asks whether the file
exists. A scaffolded blank would pass it and brief a member with nothing.

So an empty workspace is refused by name, and what follows is what to put in each file.

### The mission

Two sections:

- `## Definition of done`
- `## Out of scope`

**`## Definition of done`** is rows that each say what must hold and how anyone can tell. Write it
as an outcome rather than a task list, because the orchestrator decides how to get there. Be
specific enough that someone else would reach the same verdict independently.

Make every row quotable on its own. The orchestrator ends the campaign with one of the four
outcomes above, and all but success have to name what is still unmet, item by item.

**`## Out of scope`** is what the campaign must not do. Name the nearby work you would consider a
distraction, the neighbouring system you do not want touched, and the rewrite you are not asking
for. A campaign expands to fill the space you leave, and the orchestrator cannot guess where your
interest stops.

### An agent's brief

Before a campaign is usable, every member is asked three questions and has to answer them in its
own words. Those three are the contract, and a brief's only job is to make them answerable:

| It is asked | And can only answer it from |
|---|---|
| `goal` — what this campaign is asking you to accomplish | the brief, because an agent never sees the mission |
| `scope` — what you own and what you must not touch | the brief |
| `obligations` — what this campaign requires, and what happens if you skip it | the brief and the orientation |

**What this member owns** is files, directories, subsystems. Be concrete enough that another agent
reading its own brief would not claim the same ground.

**What belongs to someone else** is the other half of `scope`, and it is the half authors drop.
Campaigns go wrong where two agents both think a file is theirs. A campaign with one agent has no
seam to guard, and then leaving it out is right.

**What its work must prove** starts with the obligation that is irreversible: work an agent does
not commit to its own branch dies with the machine. Then add what this campaign needs, and ask for
what you could check rather than what you could count. "Every endpoint has a test that fails
without it" holds up. "80% coverage" invites an agent to manufacture coverage, which the section on
gates below is about.

Write all of it for a reader with no context, because that is what an agent is. It cannot see the
mission, the other briefs, or anything you decided in your head.

Never hand two agents the same brief with a name changed. A generic briefing is restated faithfully
by an agent that read nothing, so the readback cannot see through it.

### The orchestrator's brief

The mission states what must be achieved and each agent's brief states what that agent owns, so
this one covers how the campaign is run. Two decisions:

**How the team is run** carries what the orchestrator would otherwise invent. How many rework
rounds before it stops sending a piece back. What it does when an agent fails that many times.
Whether it writes code itself or delegates everything.

**What it must verify before reporting** is the work it does in its own clone. It is accountable
for what the campaign delivers, so it verifies returned work rather than accepting an agent's
report. Where an agent reports a number, say that the orchestrator confirms it: a report is a claim
and the branch is the evidence.

Do not restate the outcome vocabulary. The four values arrive with the mission dispatch, which is
later and more authoritative than anything you seeded. A brief that names its own is a brief the
orchestrator will try to close the campaign with. `validate` warns when a seeded document names
an outcome that does not exist.

### Give it the exit before it needs one

Put this near the top of every brief: if two lines of this brief cannot both hold, stop and reply
saying so, naming both lines.

Then make stopping the cheap path in writing. Say that an honest stop costs nothing and that a
workaround costs a rework and a finding. An agent does not argue with its brief, it satisfies it, so
the incentive has to be written down rather than assumed.

### Map the blast radius before you mandate a change

Before writing "delete X", "rename X" or "replace X", grep the base commit for the symbol and for
the path. Then either widen what the member may touch or narrow the mandate. The radius is reliably
wider than it looks:

- Tests that reference it.
- Spec rules that still require it, so the spec edit is in scope too.
- Documentation, generated pages and version stamps that a rebuild moves.
- Files that merely mention the path, such as an install script or a vendored copy.
- Another consumer reading a different meaning into the same shared symbol.

An unmapped mandate produces a run that stalls on one feature repeatedly, each unblocking uncovering
the next contradiction.

### Anything crossing two members needs an owner and a route

For every artifact more than one member touches, write one sentence of this shape. One seat is
the owner. The artifact travels by a named route. Whoever receives it re-runs exactly the command
the owner named.

Failure modes need owners too. What happens when a member is stuck, when rework runs out, when a
shared component turns out to be wrong. Answer each in writing, or the orchestrator invents the
policy with nobody to ask.

Give every fallback a trigger it can actually observe. A clock trigger needs `defaults.deadline`
set. A trigger on another member's progress needs the orchestrator to hold that repository. A
fallback whose condition is unobservable is a branch that never runs.

## What each member is given to read

Everything you seed spends attention at the party least able to tell you it was wasted.

The test is simple. Name the member that reads this document, and the sentence it needs from it. If
you cannot, the document is yours rather than theirs.

**Never give a member the answers to the checks it is about to be judged on.** A traceability table,
an acceptance matrix or a filled-in evidence record all fail that test. They are your instruments
for reading the result.

Bulk is not insurance. Anything buried in forty pages is effectively absent, and you spent the
reader's attention to bury it. If you are attaching material because you fear an unanswerable
question, sharpen the requirement instead.

Moving bulk is not reducing it. A long reference belongs in a snapshot, with the brief as the map
and the mount path in it. The member still has to read it either way.

Print the delivery map rather than trusting your memory of it. Two commands show the two halves, and
they disagree with your intention more often than you would like:

```sh
cs-campaign orientation acme --profile acme/profile.yaml --member backend   # what that member holds
cs-campaign plan acme --profile acme/profile.yaml                           # what the host resolved
```

## Gates, numbers, and the letter of the law

The product never grades output. Whether the work is good is the orchestrator's judgement and then
yours, so every gate you write is a gate somebody can satisfy without doing the work.

**Verify every number you assert, and gate on none of them.**

Check each number against the exact base commit, with a command, and keep the command beside the
number. If you cannot produce the command, you do not have the number. `plan` prints each
repository's resolved base commit, so there is no excuse for checking the wrong tree.

Then keep the number out of the pass condition. Ask the member to measure and report, let the
orchestrator confirm it, and let the confirmed number bind the check. State counts as properties
instead: "every tagged block renders highlighted" survives new content, and "at least 28 highlighted
blocks" invites a member to manufacture blocks.

An unsatisfiable gate is not survivable. A capable agent facing one finds a way to appear to pass
it, which is not misbehaviour. It is doing what you asked.

These are the shapes that teach a member to work around you:

- **A grep over names.** Evaded by a rename, a runtime-built identifier or a selector assembled from
  a variable. Ask for the outcome and let the grep corroborate it.
- **A check satisfiable by spelling.** A grep for an API name passes against a wrapper that
  implements nothing.
- **A rule that punishes the honest path.** "Only the text inside this assertion may change" fails a
  test that legitimately reached the value another way. State the invariant, not the syntax.
- **A proof list the judge may not use.** If acceptance is an independent check, a twelve-line
  self-report is pure cost. Ask for the commit, a clean tree, the last line of each gate, and what
  is not done.
- **A parity check that mandates a change on one side.** Anchor parity on something both sides
  already share.

Keep one clause asking whether anything else is wrong, with a stated consequence. A broken rule
means rework even when every named check passed.

## Have someone else read it

You cannot review your own briefs. Over-specify and a capable agent satisfies the letter.
Under-specify and a reasonable agent does something reasonable that is not the mission. Contradict
yourself and it picks a side in silence. The author sees none of this, because they wrote both sides
of every seam.

Run two passes, and give the reader nothing but the files:

- **The fleet pass** takes the mission, every brief, and each member's rendered orientation,
  labelled. It finds contradictions between briefs and identifier drift.
- **The member pass, one per role,** takes that member's brief and its orientation and nothing else.
  It is the only pass that sees what the member sees.

Include the orientation in both. It decides the command vocabulary and the reply rules, so leaving
it out means the one document defining the contract is the one nothing checks against.

Expect a round to find what the previous round's edits introduced. Stop when a round turns up only
cosmetic drift. If the scope widens, go back to the fleet pass, because widening re-opens every
seam.

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

Do not commit to a source repository between `plan` and `create`. Both resolve the base commit,
the resolved profile is what the campaign ID is hashed from, and a commit in between moves the ID,
the group and every sandbox name. It also invalidates every number you verified against that tree.

Then `create` runs the readback. Every member is asked three questions as dispatch `d001`, before
any work is assigned, and a member that cannot confirm its briefing fails creation by name:

| | What it is asked |
|---|---|
| `goal` | What this campaign is asking you to accomplish. |
| `scope` | What you own and what you must not touch. |
| `obligations` | What this campaign requires of you, and what happens to your work if you skip it. |

Use those three as your own first test of every brief, in isolation. If you cannot answer all three
from that one file, its member will not be able to either.

Note which member is asked what. `goal` is asked of an agent that never receives the mission, so a
brief has to leave its member able to state a campaign-level purpose from the brief alone. That is
not licence to explain the campaign. It does mean a brief owes its member the point of its own work.

**Read the restatements as prose.** The check is structural, so it holds identity, branch, missing
inputs and obligations, and it does not grade content. A member passes with three vacuous sentences.
One that passes with an empty `goal` has just told you its brief is thin, and that is the cheapest
signal you will get all run.

## While it runs

`create` returns before the orchestrator's mission turn has done anything. Green readbacks and a
green doctor mean the team is correctly instantiated, not that it has started. Watch until you see
the first dispatch land, because this is the one window where a human is load-bearing.

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

Most unnecessary intervention comes from two misreadings. **Slow is not stalled**: a member can commit
steadily through a two-hour turn and look idle from outside, so check its branch rather than your
patience. **Free is not stuck**: leaving a seat idle while others work is a legitimate plan, and the
orchestrator's own `wait` blocks through it.

### The rules that cost the most to break

**Never type into a live session.** Keystrokes land in the member's terminal interface, and the work
that follows is no longer the team's. Observe with read-only probes instead: `observe`, transcript
growth, and `git log` on the member's branch.

**Never answer a stalled orchestrator.** A stuck orchestrator is a finding worth more than the fix.
Unsticking it destroys the finding and any claim that the team worked autonomously. Record what it
was doing, then decide whether this campaign is still answering your question.

The judgement that is yours is *which* failure you are looking at, and the two look identical from
outside. Infrastructure died, meaning a lost connection, an adapter crash or a machine gone. Recover
that and record what happened, because nothing is learned by leaving the team idle. Or the
orchestrator genuinely stalled, meaning it had everything it needed and stopped. That is a finding
about your inputs. Record it, and resist nudging it into looking successful.

You can talk to a running orchestrator, and you should design so that you do not have to. Every
mid-run correction is an input defect you are paying for at the most expensive possible moment.

Know when to stop granting authority mid-run. Granting the mechanical consequence of a mandate is
fair, such as deleting a feature's tests along with the feature. Rewriting rules one at a time to
unblock a requirement is redesigning the brief live. Carry the requirement unmet, record the defect,
and fix it next time.

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

**A skipped check is a missing input, never a pass.** Say that in the briefs. A missing tool makes a
gate report SKIP at exit zero, and "all gates green" then hides the gates that never ran.

Render the archive and read its findings. They are a graded checklist somebody already wrote, and
the manual's findings reference says what each code means. Decide before you start which of them you
would accept and which would make you re-run, so the post-mortem is not a negotiation with yourself.

Then do the part no command does for you. Re-run the acceptance gates yourself, from a fresh clone
of the integration branch. That independent check is the point of the exercise, and it has caught
failures that every test inside the campaign passed.

Judge the mission reply against the mission you wrote. An outcome is the orchestrator's claim, and
the branches, the transcripts and the archive are the evidence for or against it. A member that got
stuck with committed work is validated from its branch rather than written off.

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
