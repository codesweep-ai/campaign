# The cs-campaign manual

## Name

`cs-campaign`: give a team of coding agents one mission, run them in Firecracker microVMs, and
harvest the evidence of what they did.

The repository ships three programs. `cs-campaign` is the host command an operator drives.
`cs-campaign-member` is installed inside every member and is how a member reads its work and answers
it. `cs-dispatch-viewer` turns one finished campaign's archive into a single HTML page. The two
host programs, `cs-campaign` and `cs-dispatch-viewer`, print this manual with their `manual` verb.

## Synopsis

```sh
cs-campaign init <campaign> [--dir DIR] [--orchestrator CLI] [--agent NAME=CLI]...
cs-campaign validate [PROFILE] [--profile PROFILE]
cs-campaign plan <campaign> --profile PROFILE [--set PATH=VALUE]...
cs-campaign create <campaign> --profile PROFILE [--accept-upstream-change] [--dry-run]
cs-campaign observe <campaign> [--json]
cs-campaign send <campaign> [TEXT] [--file PATH]
cs-campaign restart <campaign>
cs-campaign ssh <campaign>[/member] [ARGS...]
cs-campaign fetch <campaign>[/member]
cs-campaign transcript <campaign>[/member]
cs-campaign archive <campaign> [--output DIR]
cs-campaign audit [<campaign>] [--archive DIR]
cs-campaign destroy <campaign> [--archive] [--archive-output DIR] [--force]
cs-campaign ls [--json]
cs-campaign doctor [<campaign>]
cs-campaign manual | version

cs-campaign-member <verb> [args]
cs-dispatch-viewer <run-dir> [-o out.html]
```

## Description

A **campaign** is one engagement: a team of coding agents, a mission, and the evidence of what they
did. The mission says what the team is there to accomplish. Each member is a microVM running
one coding agent. One member is the **orchestrator**. It works out how to get the mission done,
splits the work across the others, and reviews what they send back. It decides when the mission is
finished, or that it cannot be. The rest are **agents**, which do the work delegated to them.

Everything runs on one **dispatch**, a unit of work named by an ID the tool mints. One rule
governs the whole protocol:

> A dispatch is open until its reply appears. Anything sent while it is open continues it. The first
> thing sent after it closes opens a new one.

Nothing about a dispatch is stored on the host. What a **node** is doing is computed on demand from
that node's own machine. The computation reads three things: the messages in its input channel, the
presence of the reply file, and whether its turn driver is running. Ask `observe` at any time;
nothing has to be caught as it happens. [`PROTOCOL.md`](PROTOCOL.md) is the authority on the machine, and
[`SPEC.md`](SPEC.md) is the contract this implementation meets.

Read the output, not the exit code. Commands that collect evidence leave markers where collection
failed rather than aborting, so a zero exit can still mean an incomplete archive.

`cs-campaign --help` is generated from the code and is always current. Prefer it over any command
line quoted here.

## The shape of a campaign

1. **Decide the acceptance gates first.** Everything else hangs off the definition of done.
2. **Write the product documents** in the target repository, campaign-free.
3. **Brief the team.** Write one brief per member in `roles/`, beside the profile. `create` seeds
   each member its own brief, and the orchestrator the mission and every agent's brief. Nothing is
   copied into the product repository.
4. **Create and dispatch.** One mission prompt, then hands off. All judgement lives in the
   orchestrator; the host observes, archives and verifies.
5. **Archive before destroy,** and re-run the gates yourself from a fresh clone. That independent
   check is the point of the exercise.

## Authoring a campaign

A campaign's own files live in a workspace directory outside this repository:

```text
acme/
├── profile.yaml        who runs: orchestrator and agents, CLIs, models, repositories
├── mission.md          what this campaign must achieve
└── roles/<member>.md   one brief per declared member, orchestrator included
```

All three are found by position beside `profile.yaml`.

Give the orchestrator every repository it must judge. `fetch` pulls a teammate's branch into the
orchestrator's own clone, so an orchestrator without the repository can read replies and cannot
inspect the work.

## Commands

### init

```sh
cs-campaign init <campaign> [--dir DIR] [--orchestrator CLI] [--agent NAME=CLI]... \
                            [--agent-cli CLI --agents N] [--repo PATH]
```

Scaffolds `profile.yaml`, `mission.md` and one brief per member into `DIR`, which defaults to the
campaign name. The files are deliberately incomplete: no member has credentials, and every brief is
a blank to fill in.

```console
$ cs-campaign init acme --orchestrator codex --agent backend=claude --agent qa=codex
wrote acme/mission.md
wrote acme/profile.yaml
wrote acme/roles/backend.md
wrote acme/roles/orchestrator.md
wrote acme/roles/qa.md

Fill in the blanks, then:
  cs-campaign validate acme/profile.yaml
```

`init` refuses to overwrite an existing file and names what is in the way.

### validate

```sh
cs-campaign validate [PROFILE] [--profile PROFILE]
```

Checks the profile, the mission and every brief. It allocates nothing and creates nothing, and it
accepts exactly what `create` accepts.

```console
$ cs-campaign validate acme/profile.yaml
valid CampaignProfile fef2d2336dc4
mission 31423f5a4cb7, 3 role briefs
```

The digests are the sha256 prefixes of the profile and the mission. They are what lets a later
archive prove which text a campaign ran on.

### plan

```sh
cs-campaign plan <campaign> --profile PROFILE [--set PATH=VALUE]... [fleet flags]
```

Prints the resolved creation plan as JSON and changes nothing. Every generated name, the group, the
network, the resolved policy and each member's branch are visible before a microVM exists.

```console
$ cs-campaign plan acme --profile acme/profile.yaml
{
  "version": 2,
  "id": "acme-56aa4ee0c3a892b0",
  "name": "acme",
  "group": "acme-56aa4ee0",
  "network": "cs-sandbox-acme-56aa4ee0",
  …
}
```

### create

```sh
cs-campaign create <campaign> --profile PROFILE [--accept-upstream-change] [--dry-run]
cs-campaign create <campaign> --orchestrator CLI --agent NAME=CLI... --repo PATH
```

Provisions each member's microVM, seeds the briefs and the orientation, installs the guest
binary into every member, arms the family guard in the orchestrator, and runs the campaign doctor.
Then the protocol starts:

1. **Dispatch `d001`, the readback.** Every member is asked to restate its job and reply. The
   restatement is checked mechanically for identity, branch, missing inputs and obligations, and
   printed for you to read. A member that cannot confirm its briefing fails `create` by name.
2. **Dispatch `m1`, the mission,** opened on the orchestrator and held open for the campaign's
   whole duration. From here the campaign is running, and running means exactly that `m1` is open.

```console
$ cs-campaign create acme --profile acme/profile.yaml
ok  readback orchestrator (codex) read its briefing
ok  readback backend (claude) read its briefing
ok  readback qa (codex) read its briefing
campaign acme created — mission m1 opened on the orchestrator (group acme-56aa4ee0)
```

A readback is bounded at 15 minutes per member. That is generous for a healthy team, and under
heavy host load a member's first turn can miss it. The failure names the member and is not a dead
end: `create` is checkpoint-resumable, so running the same `create` again continues the still-open
readback dispatch rather than starting over.

`--accept-upstream-change` proceeds past an upstream deviation and records it on the campaign.

### observe

```sh
cs-campaign observe <campaign> [--json]
```

The whole observation surface, in two panes that are never merged.

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

**DERIVED** is every node's state, computed now. The orchestrator is included, because it cannot
observe its own death and you can.

| State | Meaning |
|---|---|
| `node-free` | No dispatch is open, or the open one was replied to and accepted. |
| `node-working` | A dispatch is open and this node's turn driver is alive. |
| `node-stopped` | A dispatch is open, no turn driver is alive, and the ladder has a move left. |
| `node-replied` | The reply exists and the orchestrator has not accepted it. |
| `node-stuck` | The ladder is spent, the elapsed bound tripped, or the machine is gone. |
| `node-unreachable` | This look failed. An overlay on every state, not a state. |

**CLAIMED** is the orchestrator's own append-only log. A claim beside the facts: "orchestrator says
qa is working" next to "qa is unreachable" is the line this command exists for. `observe` also
mirrors the log beside the campaign record, so the claim survives a lost orchestrator machine.

When the orchestrator has replied to `m1`, `observe` prints the mission reply, which is the campaign's
verdict, one of `campaign-met`, `campaign-converged`, `campaign-exhausted` or `campaign-blocked`,
with what remains unmet.

### send

```sh
cs-campaign send <campaign> [TEXT] [--file PATH]
```

The host's one way to talk to the orchestrator. It continues `m1` if the mission is open, and opens
a new dispatch if you are past it. You never classify the message; the tool computes which it is
from the orchestrator's own channel.

```console
$ cs-campaign send acme --file nudge.md
m1 continued
```

Use `--file` for anything long or multiline. Text passed as an argument goes through a shell on the
way, so backticks and `$( )` in it are substituted before delivery.

### restart

```sh
cs-campaign restart <campaign>
```

Drops the orchestrator's wedged session and re-anchors it against the open dispatch by mechanical
replay.

```console
$ cs-campaign restart acme
restarted — session dropped, re-anchored against the open dispatch
```

`send` and `restart` target the orchestrator alone, for the one failure only you can see: it
stopped. Agent recovery is the orchestrator's ladder, run inside its own `wait`.

### ssh

```sh
cs-campaign ssh <campaign>[/member] [ARGS...]
```

This is the supported non-interactive look inside a member. The remote end re-joins the argument list with
spaces, so inner quoting does not survive. Pass a compound command as one locally quoted argument,
and ship anything quoting-sensitive as base64:

```sh
cs-campaign ssh acme/backend 'cd repo && git status -sb'
echo "$script" | base64 -w0 | xargs -I{} cs-campaign ssh acme/backend 'echo {} | base64 -d | sh'
```

### fetch

```sh
cs-campaign fetch <campaign>[/member]
```

Harvests a member's branch into the host source repository and reports tree against base, never
commit count.

```console
$ cs-campaign fetch acme/backend
product cs-sandbox/backend-56aa4ee0.acme-56aa4ee0 — tree differs from base (real changes present)
```

One line per repository that member holds, naming the repository as the member sees it and the
branch the work arrived on. A branch whose tree is identical to its base gets a warning instead: it
delivers no change, however many commits it carries.

### transcript

```sh
cs-campaign transcript <campaign>[/member]
```

Streams the raw model-session transcript. Human forensics only, and never a state input.

### archive

```sh
cs-campaign archive <campaign> [--output DIR]
```

Collects the evidence: campaign state, every member's input and output channels, member
configuration, CLI transcripts, source metadata, and the team audit verdict. The audit runs in the
same pass, while the sandboxes still exist. Without `--output` the archive lands in
`archives/<campaign>-<UTC timestamp>`, and `destroy --archive` uses `archives/<campaign>-final`.

Anything that failed to collect leaves an `INCOMPLETE-*` marker beside where it should have been.
Each collection command is bounded, so a member that stops answering leaves a marker naming the
deadline rather than holding the archive open.
Partial evidence is worth more than none; silently partial evidence is worth less than none.

### audit

```sh
cs-campaign audit [<campaign>] [--archive DIR]
```

Checks evidence rather than declaration: did each member's declared CLI do the work, and is any
foreign-family session or credential state present. It runs automatically inside `archive`, before
anything is destroyed, and can be re-run later against a preserved archive.

```console
$ cs-campaign audit acme
ok  audit: every member's declared CLI matches its evidence; no foreign-family state
```

### destroy

```sh
cs-campaign destroy <campaign> [--archive] [--archive-output DIR] [--force]
```

Tears down every member, then reclaims the group: network, keys, gateway and gateway port. Teardown
is re-runnable and tolerates a resource that is already gone.

With `--archive`, the archive runs first and an incomplete collection stops the destroy. Without
`--force`, a member that refuses to go leaves the campaign state in place rather than orphaning it.

### ls

```sh
cs-campaign ls [--json]
```

```console
$ cs-campaign ls
NAME  PROVISIONING  GROUP           MEMBERS  GATEWAY  AGE
acme                acme-56aa4ee0   3        8214     2h14m
```

A record that cannot be parsed is reported rather than skipped, because a listing that quietly omits
one reports a smaller, healthier team than exists.

### doctor

```sh
cs-campaign doctor [<campaign>]
```

With no argument, checks the host surface: the `cs-sandbox` build, each CLI's remote tool family,
the sibling `cs-` tools, and the state directory.

```console
$ cs-campaign doctor
cs-campaign doctor

cs-sandbox (required):
  ok  cs-sandbox version v0.0.0-20260827001716-910b73da3b6c
  ok  cs-sandbox supports ls --json
  ok  cs-sandbox supports sandbox groups

agent tooling (required — one family per CLI):
  ok  claude remote tool family
  ok  codex remote tool family
  ok  opencode remote tool family

upstream (checked against this build's go.mod):
  ok  cs-sandbox on PATH is the one this build names: v0.0.0-20260827001716-910b73da3b6c
  ok  cs-vcr on PATH matches this build (v0.0.0-20260826160252-bd9e6f2b8ab6)
  ok  not on PATH (fine — a campaign needs none of them): cs-lint cs-ledger cs-tracer

state:
  ok  state directory: /home/user/.config/cs-campaign/campaigns

All good — try: cs-campaign init <name>
```

Every check is measured and reported, so a host with two gaps is told about both rather than about
one at a time. The exit status is set once, from the whole report.

The upstream surface is named by the `go.mod` embedded in this binary at build time, so there is no
pin file and nothing to record. `cs-sandbox` at another version fails `doctor` and refuses `create`.
The sibling `cs-` tools are reported and never gate: a host that runs campaigns needs none of them,
and one at another version gets a line rather than a refusal.

With a campaign name, it re-verifies that campaign's instantiation. It checks each member's harness
against what `cs-sandbox agent-tools` says it ships, manifest fidelity, guest controls, the family
guard actually firing, and each member's declared CLI present in its own machine. `create` ends by
running it. **If it is not green, do not dispatch.** Fix it or destroy it.

### manual, version

`manual` prints this file, which is compiled into the binary. `version` prints the build stamp.

## Inside a member: cs-campaign-member

Every member holds the same guest binary. The roles differ in which verbs it will run.

**Every member**

| Verb | What it does |
|---|---|
| `inbox` | The current open dispatch: its ID and every message, in order. |
| `check-inputs` | Verifies every file `member.json` lists under `inputs`, and prints any that are absent. |
| `reply [--file F\|-]` | Writes the reply that closes the current dispatch. |

`reply` on the mission requires `--outcome`, one of `campaign-met`, `campaign-converged`,
`campaign-exhausted` or `campaign-blocked`, and `--unmet "<item>"` (repeatable) unless the outcome
is `campaign-met`. Any reply may carry `--needs-input` when the team cannot act without outside
help.

**Orchestrator only**

| Verb | What it does |
|---|---|
| `list` | The roster: every teammate, its CLI, its repositories. |
| `observe` | Every agent's state, computed now, in one snapshot. |
| `send <agent> --file F\|-` | Dispatch or continue. Opens if closed, continues if open. |
| `read <agent> [path]` | The agent's reply to its current dispatch, or a file from its output channel. |
| `restart <agent>` | Drops its session and re-anchors it against its open dispatch. |
| `accept <agent>` | Records its current reply as accepted, which frees the agent. |
| `note plan\|assessment --file F\|-` | Appends to the log. Re-planning is another `plan` entry. |
| `wait [--for SECS]` | Blocks. Recovery runs itself. Returns when a judgement is due — a reply to judge, or a node gone stuck — or when the chunk elapses: `--for` seconds, 240 by default. A free teammate is not a judgement; it is named on the elapsed line instead. |
| `fetch <agent> [repo]` | Fetches its branch to `refs/remotes/campaign/<agent>/<repo>`. |
| `push <agent> [repo]` | Pushes HEAD to it at `refs/campaign/orchestrator`. |

A dispatcher verb run on an agent is refused, naming the role. Invoked under a `cs-<cli>-remote`
name, the same binary is the family guard: a wrong-family call against a member is refused with the
true diagnosis and exit 78.

## The archive viewer: cs-dispatch-viewer

```sh
cs-dispatch-viewer <run-dir> [-o viewer.html]
cs-dispatch-viewer manual | version | help
```

Renders one campaign run archive as a single self-contained HTML page. It opens straight from disk:
no server, no network, no runtime dependencies. `<run-dir>` is an archive directory holding
`campaign.json`, or a run directory holding `archive/`. Output defaults to `viewer.html` in the
current directory.

It reads the archive and nothing else:

- each member's `input/` channel, whose message names and modification times are the dispatch
  record;
- each `output/replies/*.json`, whose filename decides which dispatch it closes;
- the orchestrator's `output/log.jsonl`;
- `campaign.json`, `readback.json`, `fleet-verdict.json`, `FLEET-ANOMALY.txt`, and any
  `INCOMPLETE-*` markers.

**Timeline.** Each node gets one lane, with the orchestrator first. Squares are channel artifacts: the facts any
observer can verify from the files alone. Every mark's colour is a design token from one palette
map (`dispatch-viewer/app/src/model.ts`):

| Mark | Token | Meaning |
|---|---|---|
| square | `--color-neutral` | dispatch open |
| square | `--color-warning` | continue |
| square | `--color-cat-8-mid` | restart re-anchor |
| square | `--color-success` | reply, phase `done` |
| square | `--color-error` | reply, phase `blocked` or `needs-input` |
| square, haloed | `--color-link` with a `--color-accent-bg` halo | the mission verdict; `--color-severe` with a `--color-severe-bg` halo when the campaign was not met |
| hollow circle | `--color-accent` | accept (log claim) |
| circle | `--color-cat-1` | plan (log claim) |
| circle | `--color-cat-4` | assessment (log claim) |

A connector runs from a dispatch's open square to its reply, showing the span it was open. Columns
are event-ordered so dense exchanges stay readable, and the ruler underneath shows the same events
at true wall-clock spacing.

**show orchestrator log.** Off by default, so the bare timeline is the dispatch and reply protocol
as the channels prove it. Checking the box overlays the orchestrator's claims as circles on the
`log` sub-lane, and reveals the verbatim log panel. Everything on the `log` sub-lane comes from the
orchestrator's `log.jsonl`: claims, not channel traffic. Acceptance is a log claim, so it appears
only with the log shown.

**Selection.** Click any mark to inspect it. Reply notes render as markdown, evidence blocks as
JSON, and the `raw` toggle shows the artifact byte for byte as it sits in the archive. Selecting an
accept circle outlines the reply it judged, and the reverse. Arrow keys step through events (with
the timeline focused or anywhere on the page), Home and End jump to the first and last event, and
Escape clears the selection.

**Issues.** Findings from the integrity checks, most severe first. Clicking one selects its evidence
on the timeline, and hovering shows the finding code's definition. Where the archive
under-determines what happened, the finding states the choice the renderer made and why.

An archive made before `cs-campaign` preserved channel modification times cannot honestly be drawn
as a timeline. Such a page shows the findings and says to regenerate the run.

The theme button cycles system, light and dark, and remembers the mode. `?theme=light`,
`?theme=dark` or `?theme=system` on the URL overrides for that load without being saved; `system`
follows the OS scheme.

### Findings reference

Severity reflects the kind of problem.

| Severity | What it means |
|---|---|
| error | The archive holds something the protocol tools could not have produced, or it cannot be read honestly. |
| severe | An obligation was never met. |
| warning | Worth a look, and often explainable. |
| info | A claim or ambiguity worth knowing about, with nothing broken. |

| Code | Severity | Meaning |
|---|---|---|
| `collection-incomplete` | error | an `INCOMPLETE-*` marker: part of the archive failed to collect |
| `fleet-anomaly` / `fleet-not-clean` | error | the team audit recorded findings when the archive was collected |
| `mtimes-clobbered` | error | message times are collection-time, not event-time; no timeline is drawn |
| `reply-unparseable` / `log-unparseable` | error | an artifact is not valid JSON |
| `reply-without-dispatch` | error | a reply exists for a dispatch no message ever opened |
| `reply-name-mismatch` | error | a reply's filename and its content name different dispatches; the filename wins |
| `reply-before-open` | error | a reply is timestamped before its dispatch opened |
| `accept-unknown` | error | the log accepts a dispatch that does not exist |
| `log-missing` | severe | the orchestrator's `log.jsonl` is absent |
| `no-reply` | severe | a dispatch was opened and never answered |
| `no-verdict` | severe | the mission has no outcome-bearing reply |
| `campaign-not-met` | severe | the verdict is anything other than `campaign-met` |
| `reply-not-accepted` | warning or severe | an agent reply the orchestrator never accepted; severe when it is the node's final dispatch |
| `continues-exceed-policy` / `restarts-exceed-policy` | warning | recovery spent more rungs than the policy allows |
| `accept-before-reply` | warning | an acceptance logged before the reply it judges: clock skew, or judgement of unanswered work |
| `readback-absent` | warning | a member never restated its briefing |
| `accept-of-readback` | info | the log accepts `d001`, a host-issued and host-judged dispatch |
| `accept-of-own-channel` | info | the log accepts a dispatch on the orchestrator's own channel, which the host judges |
| `accept-ambiguous` | info | a bare accept matches several nodes' dispatches; shown attached to all of them |
| `accepted-twice` | info | the same dispatch accepted twice; the later entry is shown |
| `ambiguous-order` | info | lifecycle events share one second; the rendered order is arbitrary |

## Options

| Option | Applies to | Meaning |
|---|---|---|
| `--json` | `ls`, `observe` | Print machine-readable JSON instead of a table. |
| `--profile PATH` | `create`, `plan`, `validate` | The campaign profile YAML. |
| `--orchestrator CLI` | `create`, `plan`, `init` | Quick team definition: the orchestrator's CLI. |
| `--agent NAME=CLI` | `create`, `plan`, `init` | Quick team definition, repeatable. |
| `--agent-cli CLI`, `--agents N` | `create`, `plan`, `init` | Homogeneous shorthand: N agents on one CLI. |
| `--repo PATH` | `create`, `plan`, `init` | A repository cloned into every member. |
| `--set PATH=VALUE` | `create`, `plan` | Override one supported profile path, repeatable. |
| `--dry-run` | `create`, `plan` | Resolve only; create nothing. |
| `--accept-upstream-change` | `create`, `plan` | Proceed despite an upstream deviation, recording it on the campaign. |
| `--dir DIR` | `init` | Scaffold into DIR instead of the campaign name. |
| `--file PATH` | `send` | Read the message text from a file. |
| `--output DIR` | `archive` | Where the archive is written. |
| `--archive` | `destroy` | Archive all member evidence before destruction. |
| `--archive-output DIR` | `destroy` | Archive destination. Requires `--archive`. |
| `-f`, `--force` | `destroy` | Force member destruction. |
| `--archive DIR` | `audit` | Audit a preserved archive instead of live machines. |
| `-o PATH` | `cs-dispatch-viewer` | Where the page is written. |

`--profile` is mutually exclusive with the quick fleet flags, and `--agent` is mutually exclusive
with `--agent-cli`/`--agents`. Membership must not depend on ambiguous flag merging.

## Configuration

A campaign is configured by its profile. The full schema is in [`SPEC.md`](SPEC.md) §5.1; this is
the operator's view.

```yaml
apiVersion: codesweep.ai/v1alpha1
kind: CampaignProfile
defaults:
  engine: firecracker
  deadline: 6h
  resources: {cpus: 2, memoryMiB: 2048}
  policy:
    continueAttempts: 2
    restarts: 1
orchestrator:
  cli: codex
  repos: [{path: /srv/product}]
  auth: {apiKeyFromEnv: [OPENAI_API_KEY]}
agents:
  backend:
    cli: claude
    model: claude-opus-5
    effort: high
    repos: [{path: /srv/product, ref: main}]
    snapshots: [{path: /srv/reference, name: reference}]
    auth: {inheritAgentLogin: [claude]}
    policy: {stallSeconds: 240}
```

`engine` says what a member runs on: `firecracker` for a microVM, or `podman` for a container.
`model` and `effort` are handed to that member's CLI unchanged, so use the slugs that CLI accepts.
For `opencode`, `effort` requires `model` beside it, because it attaches reasoning options to a
named model, not to the session. `repos[].ref` picks the branch or tag cloned into the member, and
`snapshots` gives it a frozen tree it can read.

Values are never written into a profile. `apiKeyFromEnv` names host environment variables, and the
first one actually set is used. `inheritAgentLogin` names CLI families whose existing host login the
member inherits; those expire in hours, so check yours immediately before `create`. A member that
gets a key from `apiKeyFromEnv` inherits no login: the key wins, and the two are not combined.

### The policy numbers

`defaults.policy` sets the dispatch machine's numbers for the whole campaign. Anything unset falls
back to a compiled-in default, and the resolved set is recorded on the campaign, in every member's
`member.json` and in the orchestrator's `manifest.json`. Every node is computed against that one
set.

| Number | Default | Meaning |
|---|---|---|
| `continueAttempts` | 2 | Continues before escalating to a restart. |
| `restarts` | 1 | Restarts before a node is unrecoverable. |
| `elapsedSeconds` | the campaign deadline, else 86400 | Bound on recovering one dispatch, from its opening. |
| `blindProbes` | 10 | Consecutive failed probes before a machine is called gone. |
| `pollSeconds` | 30 | How often the orchestrator's `wait` looks. |
| `settlingSeconds` | 300 | Grace after any send before a driverless node counts as stopped. |
| `stallSeconds` | 180 for agents, 1800 for the orchestrator | The turn driver's own idle threshold. |

`stallSeconds` is the one number resolved per seat, which is how the orchestrator gets a longer
threshold than the agents. A member's own `policy:` block sets it for that member, else
`defaults.policy.stallSeconds` for the campaign, else the compiled-in default for that role. The
resolved value is recorded on that member and delivered to its turn driver through the sandbox
environment. `validate` refuses any other number in a member's `policy:` block, naming it: those
belong under `defaults.policy`, which is where the dispatch machine reads them.

`CS_CAMPAIGN_POLL_SECONDS` overrides `pollSeconds` at the wait itself, for the host and for every
member. It is not profile configuration, and it never reaches the campaign record. The campaign ID is the
profile's digest, and a member reads its own policy from a file its model is shown. A number in
either would change what a recorded session is matched on. Replaying a cassette is what it is
for, where a turn answers in milliseconds and an interval sized for real turns is most of the run.

How long one `wait` blocks is the same class of number, and is kept out of the policy set for the
same reason. It is 240 seconds unless the caller asks otherwise with `--for`, sized to sit inside
every agent CLI's cap on a single tool call. The environment can override both, `--for`
included, through `CS_CAMPAIGN_WAIT_SECONDS`. A replay serves a recorded model that asked for the campaign's number, so an override that
argument could beat would bound nothing.

`defaults.deadline` is the campaign wall clock as a duration from create, such as `90m` or `6h`. It
stops nothing by itself. The orchestrator's judgement enforces the deadline, and the machine uses
the value only as the `elapsedSeconds` default.

## Files

| Path | Written by | Contents |
|---|---|---|
| `$XDG_CONFIG_HOME/cs-campaign/campaigns/<name>.json` | host | One campaign's record. |
| `$XDG_CONFIG_HOME/cs-campaign/campaigns/.<name>.lock` | host | The per-campaign write lock. |
| `$XDG_CONFIG_HOME/cs-campaign/campaigns/<name>.log-mirror.jsonl` | `observe` | A copy of the orchestrator's log, so the claim survives a lost machine. |
| `<archive>/campaign.json` | `archive` | The record as the campaign ended. |
| `<archive>/campaign-profile.yaml` | `archive` | The profile the campaign was declared with. |
| `<archive>/readback.json` | `archive` | Every member's restatement of its job. |
| `<archive>/fleet-verdict.json` | `archive` | The audit's findings. |
| `<archive>/upstream-fingerprint.json` | `archive` | The host surface the campaign ended on. |
| `<archive>/member-harness.json` | `archive` | Each member's tool surface, measured again at archive time. |
| `~/.config/cs-campaign/member.json` | host, in each member | What that member was given. |
| `~/.config/cs-campaign/manifest.json` | host, in the orchestrator | The roster its `wait` loop runs on. |
| `~/.local/share/cs-campaign/input/` | the member's driver | Dispatch messages. |
| `~/.local/share/cs-campaign/output/replies/<id>.json` | the member | The reply that closes a dispatch. |
| `~/.local/share/cs-campaign/output/log.jsonl` | the orchestrator | Its append-only plan, assessment and accept record. |
| `~/.local/bin/cs-campaign-member` | host, in each member | The guest binary. |

## Environment

| Variable | Read by | Effect |
|---|---|---|
| `CS_CAMPAIGN_STATE_DIR` | `cs-campaign` | Where campaign state lives. Default `$XDG_CONFIG_HOME/cs-campaign/campaigns`. |
| `CS_CAMPAIGN_GUEST_BIN` | `cs-campaign` | Install this guest binary instead of the embedded one. |
| `CS_SANDBOX_BIN` | `cs-campaign` | The `cs-sandbox` executable to invoke. Default `cs-sandbox`. |
| `CS_SANDBOX_INSTANCES_DIR` | `cs-campaign` | Where `cs-sandbox` keeps instances, for the socket-path budget check. |
| `CS_SANDBOX_HOME` | `cs-campaign` | Fallback root for the same check when the instances directory is unset. |
| `CS_CAMPAIGN_POLL_SECONDS` | `cs-campaign`, `cs-campaign-member` | Overrides `policy.pollSeconds` for the wait loop alone. |
| `CS_CAMPAIGN_WAIT_SECONDS` | `cs-campaign-member`, forwarded by `cs-campaign` | Overrides how long one `wait` blocks, `--for` included. |
| `XDG_DATA_HOME` | `cs-campaign` | Last fallback for the same check. |

Precedence for the instances directory is `CS_SANDBOX_INSTANCES_DIR`, then `CS_SANDBOX_HOME`, then
`XDG_DATA_HOME`, then the platform default under the home directory.

Anything named in a member's `auth.apiKeyFromEnv` is read from the host environment at create and
granted to that member. `env:` entries in a profile reach the member's sandbox; a bare `KEY` inherits
the host's value without the value passing through campaign state.

## Exit status

| Code | Meaning |
|---|---|
| 0 | The command did what it says. Read the output: an archive can be incomplete at exit 0. |
| 1 | Any failure. The message names what failed and what to do about it; `doctor` names them in the report above its verdict. |
| 70 | The family guard is installed but broken: the real tool it stands in for is missing or will not exec. `cs-campaign-member` only. |
| 78 | The family guard refused a wrong-family call against a member. `cs-campaign-member` only. |

## Diagnostics

**`cs-sandbox does not support sandbox groups`**

The installed `cs-sandbox` predates group addressing. Campaign isolation is a group, so there is no
fallback. Install a group-aware build.

**`required agent tool <name> not found on PATH`**

A CLI family's remote tools are missing. Run `cs-sandbox install-agent-tools`.

**`upstream surface is not the one this cs-campaign was built against`**

The `cs-sandbox` on your `PATH` is a different build from the one this binary names. The message
carries the `go install` line that fixes it. If you moved upstream on purpose, bump the `go.mod`
pin, rebuild and reinstall, or pass `--accept-upstream-change` to record the deviation and proceed.

**`readback FAILED — N member(s) could not confirm their briefing; do not dispatch`**

One or more members did not restate their job. The line names each one. Fix the brief, or re-run
`create`, which continues the still-open readback dispatch.

**`campaign <name> FAILED its doctor (N problems)`**

The team exists and is not the team you declared. Do not dispatch. Fix the instantiation or
destroy it.

**`member <name> would need a N-byte socket path, M over the 108-byte AF_UNIX limit`**

The composed group and member path is too long. Shorten the campaign name or the member name, or
point `CS_SANDBOX_HOME` at a shorter directory; the message says how many characters it is over. The
check runs before anything is allocated, because the overflow is otherwise silent: the socket binds
under a truncated name and the machine never becomes ready.

**`generated group <name> already holds a foreign sandbox`**

A sandbox that is not this campaign's occupies the group this campaign name resolves to. Choose
another campaign name, or clean up the sandbox.

**`the host dispatches to the orchestrator alone`**

`send` was aimed at an agent. Agent work comes from the orchestrator; the host has no channel to an
agent after create.

**`the host restarts the orchestrator alone`**

The same holds for `restart`. Agent recovery is the orchestrator's ladder.

**`the orchestrator has dispatch <id> open; the mission cannot be opened over it`**

Something else is already open on the orchestrator. Let it close, or read it with `observe` first.

**`cannot reach <member> — no recovery instrument reaches a machine that cannot be reached at all`**

The transport to that member is down. This is not a protocol failure, and no continue or restart
will fix it. Check the sandbox.

**`archive before destroy is incomplete`**

Part of the evidence did not collect, and the destroy stopped rather than throwing the rest away.
The markers name what is missing.

**`members still present (<names>); campaign state preserved`**

A member refused to be destroyed. The record is kept rather than orphaned. Re-run with `--force`.

**`audit FAILED (N findings) — a member's work did not match its declaration`**

The evidence says a different CLI did the work, or foreign-family state is present. Each finding
names the member.

**`the embedded guest binary is the committed placeholder`**

The host binary was built without compiling the guest binary first. Build with `make build`.

**`campaign <name> uses state version N, which predates sandbox groups`**

The record predates group addressing and cannot be migrated, because the sandboxes it points at are
already unreachable. Archive or destroy it with the `cs-campaign` that made it, then remove the
record. A record from a *newer* build reports `unsupported state version N` instead.

**`<agent> has no reply to accept`**

The orchestrator tried to accept work that has not arrived.

**`campaign-met names nothing unmet`**

A mission reply claimed `campaign-met` and also listed unmet items. If you can name something unmet,
it is not `campaign-met`.
## Notes for agents

Every `cs-campaign` command is non-interactive except `ssh` with no arguments, which attaches a
terminal. `ssh` with arguments runs the command and returns.

`ls --json` and `observe --json` are the machine-readable surfaces. `plan` always prints JSON.
Everything else prints for a human and may change; parse it at your own risk.

`observe` performs one probe per member over the network, plus one read of the orchestrator's log
and, once the mission has been answered, one read of that reply. A failing member is re-probed up to
`blindProbes` times, one second apart, so budget about `blindProbes` seconds for a member whose
machine is gone.

`validate`, `plan`, `ls`, `audit`, `transcript`, `manual` and `doctor` with no argument change
nothing. `init` writes files and refuses to overwrite. `create`, `send`, `restart`, `fetch`,
`archive` and `destroy` change state. So do two commands that read: `doctor <campaign>`
records each member's refreshed harness verdict, and `observe` mirrors the orchestrator's log beside
the campaign record.

Readiness is `cs-campaign doctor`, which exits non-zero when the host surface cannot run a campaign,
and `cs-campaign doctor <campaign>`, which exits non-zero when a live campaign is not the team that
was declared.

**Never type into a live session.** Keystrokes land in the member's terminal interface. Observe with
read-only probes: `observe`, transcript growth, `git log` on the member's branch.

**Never answer a stalled orchestrator.** If it gets stuck, that is a finding to record rather than a
problem to fix. Unsticking it destroys both the finding and any claim that the team worked
autonomously.

**Archive before destroy.** The microVMs are the only copy of a member's work until you
fetch it. Read the verdict rather than assuming it.

Re-run the gates yourself from a fresh clone of the integration branch. That independent check is
the point of the exercise, and it has caught failures that every test inside the campaign passed.

## Examples

**Scaffold, check and inspect a team, without allocating anything.**

```sh
cs-campaign init acme --orchestrator codex --agent backend=claude --agent qa=codex
$EDITOR acme/mission.md acme/roles/*.md acme/profile.yaml
cs-campaign validate acme/profile.yaml
cs-campaign plan acme --profile acme/profile.yaml | head -40
```

**Run one campaign end to end.**

```sh
cs-campaign doctor
cs-campaign create acme --profile acme/profile.yaml
cs-campaign observe acme
cs-campaign archive acme --output ./runs/acme-1
cs-campaign destroy acme
cs-dispatch-viewer ./runs/acme-1 -o acme-1.html
```

**Nudge a stopped orchestrator, then re-anchor it.**

```sh
cs-campaign observe acme                 # orchestrator node-stopped on m1
cat > nudge.md <<'EOF'
You have not replied to m1. Continue from where you stopped.
EOF
cs-campaign send acme --file nudge.md
cs-campaign observe acme                 # still stopped after the settling window
cs-campaign restart acme
```

**Harvest and verify independently.**

```sh
cs-campaign fetch acme/backend
cs-campaign fetch acme/qa
git clone --branch cs-sandbox/orchestrator-56aa4ee0.acme-56aa4ee0 . /tmp/verify
cd /tmp/verify && make check
```

## See also

- [README.md](README.md) · what this is, and how to run it.
- [INSTALL.md](INSTALL.md) · how to get the tools, and the setup they need once.
- [PROTOCOL.md](PROTOCOL.md) · the dispatch protocol, on its own terms.
- [SPEC.md](SPEC.md) · what the behaviour must be, and what is left open.
- [CONTRIBUTING.md](CONTRIBUTING.md) · how to work on the harness.
