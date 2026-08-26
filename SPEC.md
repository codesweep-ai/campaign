# The cs-campaign specification

`cs-campaign` runs a team of coding agents in Firecracker microVMs, under one dispatch protocol,
and preserves the evidence of what they did. One member of the team is an orchestrator that plans
the work and judges it; the rest are agents that do it. An operator states the mission, watches,
and harvests the result.

This document is the contract. A reimplementation is free to make different structural choices and
still be correct, so nothing here cites a source line or describes the current code as such.
Requirements are numbered `R1` upward and can be cited from a pull request. Where a rule exists
because getting it wrong fails *silently*, the reason follows the requirement in italics.

Read [`PROTOCOL.md`](PROTOCOL.md) first when you want the dispatch machine itself. It is the
authority on dispatches, replies and node states, and this document constrains implementations of
it rather than restating it. For using the tool see [`MANUAL.md`](MANUAL.md); for working on it,
[`CONTRIBUTING.md`](CONTRIBUTING.md).

The key words **MUST**, **MUST NOT**, **SHOULD**, **SHOULD NOT** and **MAY** are used as RFC 2119
defines them.

---

## 1. Purpose

A team of coding agents is easy to start and hard to trust. Each agent is a model in a loop, so
each can stop early, wander, or report success it cannot support. Running several at once
multiplies that, and adds a second problem. The machines they run on can reach each other, and an
agent that can reach a sibling can borrow its credentials, edit its work, or answer on its behalf.

`cs-campaign` addresses both. Isolation comes from `cs-sandbox`: one campaign is one sandbox group
with its own network, its own SSH trust material and its own gateway. Trust comes from the dispatch
protocol. Work is named and closes only when its own reply artifact exists. Every node's state is
computed from that node's own machine rather than remembered.

### 1.1 Goals

1. Keep orchestrator, agents, application processes and nested containers off the host.
2. Prevent one campaign from reaching another campaign's sandboxes.
3. Prevent agents from initiating SSH to the orchestrator, to siblings, or to the host.
4. Let the orchestrator drive every agent in its own campaign, and nothing else.
5. Support Claude, Codex and OpenCode in either role, where their adapters permit it.
6. Survive individual model, process, virtual-machine and host-command failures for the days a
   campaign may run.
7. Expose progress without requiring intervention, and let useful work be harvested at any point
   rather than only on success.
8. Preserve transcripts, structured results and git work before teardown.
9. Let a human inspect, intervene, and reach member application services through one gateway.

### 1.2 Non-goals

1. **Grading a model's output.** The product checks that a reply exists and has the required
   shape. Whether the work is good is the orchestrator's judgement, and then the operator's.
2. **Tamper-proof provenance.** The orchestrator administers its agents and can open a shell in
   any of them, so an archive records what was observed rather than proving nobody edited it.
3. **Scheduling across campaigns.** One campaign is one group. Nothing here allocates hosts,
   queues campaigns, or shares a team between missions.
4. **Being the sandbox.** Virtualization, images and the network belong to `cs-sandbox`.
5. **A stable machine-readable output for every command.** Two commands emit JSON on request and
   `plan` always does; the rest print for a human.

---

## 2. Vocabulary

| Term | Meaning |
|---|---|
| **campaign** | One orchestrated unit of work, and the host-side record of it. |
| **member** | One sandbox in a campaign, running one coding agent. |
| **orchestrator** | The member that plans the campaign, drives the agents and judges their work. |
| **agent** | A member that performs delegated work. Exactly one role below the orchestrator. |
| **campaign group** | The `cs-sandbox` group shared only by one campaign's members. It owns the campaign network, the SSH trust material and the gateway. |
| **member reference** | `<sandbox>.<group>`, a member's address on the host plane. |
| **driver** | Whoever invokes a dispatcher's side of the protocol. The host drives the orchestrator; the orchestrator drives each agent. |
| **adapter** | The per-CLI knowledge that starts a turn, delivers a prompt, finds the transcript and restarts a session. One adapter per supported CLI. |
| **turn** | One invocation of a model: it runs, produces output, and stops. |
| **dispatch** | A unit of work a dispatcher asked for, named by an ID the tool mints. |
| **reply** | The durable artifact that states a dispatch is concluded. Its presence closes the dispatch. |
| **mission** | The host's one dispatch to the orchestrator, reserved ID `m1`, open for the campaign's whole life. |
| **node** | One participant in the protocol, as the state computation sees it. Every member is a node. |
| **node state** | What a node is doing about its current dispatch, computed on demand and never stored. |
| **readback** | A member's restatement of its own job, produced before a campaign is usable. |
| **channel** | One of the four directional surfaces every member has: input, output, transcript, source. |
| **orientation** | The product-authored standing context placed in every member's input channel. |
| **manifest** | The orchestrator's machine-readable roster of its teammates. |
| **settling window** | The grace period after any message, during which a node with no live turn driver still reads as working. |
| **ladder** | The mechanical recovery sequence a dispatcher runs: templated continues, then a restart re-anchor. |
| **pin** | The recorded sha256 of every upstream tool a campaign runs on, with a note saying why that surface is trusted. |
| **family guard** | The refusal that fires when a per-CLI remote tool is aimed at a member of a different CLI family. |
| **solo** | A `cs-sandbox` property that withholds the SSH login credential from a member. Agents are solo; the orchestrator is not. |
| **covmap** | The behaviour map: an authored rubric of behaviours, filled only by records that tests emit as they prove them. |

---

## 3. Interfaces

Four surfaces touch the outside world.

### 3.1 The host command surface

`cs-campaign` is the operator's and the host driver's command. Its verbs group into planning,
lifecycle, the protocol, member access, evidence and health:

```sh
cs-campaign init|validate|plan                        # author and check; allocate nothing
cs-campaign create <name> --profile <file>            # provision, brief, verify, dispatch
cs-campaign observe|send|restart <campaign>           # the protocol surface
cs-campaign ssh|fetch|transcript <campaign>[/member]  # member access
cs-campaign archive|audit <campaign>                  # evidence
cs-campaign ls                                        # every campaign on this host
cs-campaign destroy <campaign>                        # teardown, and reclaim the group
cs-campaign doctor|pin|manual|version                 # health and metadata
```

[`MANUAL.md`](MANUAL.md) documents every command, flag and diagnostic.

### 3.2 The member helper surface

`cs-campaign-member` is installed into every member and is the only sanctioned way a member
participates. Every member has `inbox`, `check-inputs` and `reply`. The orchestrator additionally
has `list`, `observe`, `send`, `read`, `restart`, `accept`, `note`, `wait`, `fetch` and `push`.

Invoked under a `cs-<cli>-remote` name, the same binary is the family guard.

### 3.3 The viewer

`cs-dispatch-viewer` renders one archive as a single self-contained HTML page: every node's
dispatch and reply record on a timeline, the orchestrator's log beside it, and a findings list.
It reads an archive and writes one file.

### 3.4 What a campaign runs on

A campaign needs a group-aware `cs-sandbox` and, for each supported CLI, that CLI's session and
remote tools. `cs-campaign` shells out to them and never links them. The exact set is recorded in
the pin.

---

## 4. The functional specification

### 4.1 Topology and trust

**R1.** A campaign **MUST** be exactly one `cs-sandbox` group, and a second campaign **MUST**
receive a different group.

**R2.** The group's isolated network, its own SSH trust material and its gateway **MUST** come up
with the first member.

**R3.** An implementation **MUST NOT** adopt a pre-existing network that does not inspect as its
own and isolated. *A shared key would turn a reachability bug into an immediate breach; per-group
keys keep it a reachability bug.*

**R4.** The orchestrator **MUST** be an agent-type sandbox, never a user-type one. *It sits atop
the autonomous tier and must not be able to authenticate to the host or to a user-tier review
sandbox.*

**R5.** A member **MUST NOT** receive the host container socket. Agents **MAY** run nested containers
inside their own sandbox.

**R6.** The host **MUST** remain able to reach every member.

**R7.** Teardown **MUST** reclaim the group once its members are gone: network, keys, gateway
container, published gateway port, tap prefix and fabric directory. *Gateway ports come from a
range of a hundred, so a leak per campaign exhausts the host.*

**R8.** A member has two addresses and they **MUST NOT** be interchangeable. Host-plane commands
**MUST** use the qualified `<sandbox>.<group>`; an in-group reference from the orchestrator to an
agent **MUST** be the bare `<sandbox>`.

| Plane | Address | Used by |
|---|---|---|
| Host | `<sandbox>.<group>` | every sandbox command, host-driven remote tools |
| In-group | bare `<sandbox>` | the orchestrator reaching agents |

**R9.** The manifest the orchestrator reads **MUST** carry bare names, and campaign state **MUST**
record both forms. *Sandbox names are unique per group rather than per host, so a bare name on the
host plane means the default group there. Inside the group the opposite holds: a guest's SSH client
config matches `Host * !*.*`, so a dotted reference is never offered the tier key and cannot
authenticate at all.*

**R10.** Member sandbox names **MUST** stay campaign-unique rather than bare member names. *A
member's returned branch is derived from its name and fetched into the shared host source
repository, which sits outside every group. Two campaigns with a `backend` member would collide on
one ref.*

**R11.** Planning **MUST** compute the composed socket path for every member and refuse ahead of
time when it would overflow, naming both the overflow and the remedy. *A member's Unix sockets live
at `<instances>/<group>/<member>/`, and `AF_UNIX` bounds the whole path at 108 bytes. Names that
pass every per-label check can still overflow it. The path is then truncated, a socket binds under
the shortened name, and the only symptom is a microVM that never becomes ready.*

**R12.** The campaign discriminator in a group name **SHOULD** be short rather than the full
campaign ID, for the same budget.

### 4.2 Grants

Grants cover three independent concerns. Conflating them is how ambient authority leaks.

**R13.** At creation the driver **MUST** specify, per principal, the repositories, writable clones,
frozen snapshots, reference artifacts, task inputs and result channels each member can see.

**R14.** The orchestrator **MAY** hand an agent an additional file through the control channel
without granting general access to its own filesystem.

**R15.** Reachability **MUST** follow this table:

| Source | Destination | Application ports | Sandbox-managed SSH login |
|---|---|:---:|:---:|
| Orchestrator | Campaign agent | yes | yes |
| Solo agent | Campaign orchestrator | yes | **no** |
| Solo agent | Campaign solo agent | yes | **no** |
| Any member | Another campaign | **no** | **no** |

**R16.** `solo` **MUST** be a credential boundary rather than a firewall rule. *A solo agent can
open a TCP connection to port 22 and cannot authenticate. Same-campaign traffic is deliberately
allowed, so one agent can call or test another's service without gaining shell control over it.*

**R17.** A member **MUST NOT** receive source-hosting credentials, host SSH keys,
credential-helper state, cloud credentials, or other ambient host identity.

**R18.** A model login or API key **MUST** be a separate explicit grant. It permits model usage and
spending, and **MUST NOT** transitively grant source-hosting or host access.

**R19.** Grants **MUST** be revocable, and **MUST** be excluded from state, transcripts and
archives.

### 4.3 Channels

**R20.** Every member, the orchestrator included, **MUST** have the same four channels:

| Channel | Author | Recipient access | Purpose |
|---|---|---|---|
| input | the member's driver | member reads only | prompts, task files, doctrine |
| output | the member | driver reads only | results, status, logs |
| transcript | the member's CLI | driver reads only | model activity, audit trail |
| source | the member | driver reads a snapshot or view | diff and commit review |

**R21.** An agent **MUST NOT** have access to the orchestrator's channels or to another agent's.

**R22.** Host campaign state **MUST NOT** record dispatch state. Every dispatch **MUST** exist only
as files in the worker's input channel, and **MUST** enter host records only at archive time.

**R23.** A live audit of any work **MUST** read the members' channels rather than a host record.
*An implementation that reports host state as though it covered the work is claiming knowledge it
does not have.*

**R24.** The one permitted host-side copy is a mirror of the orchestrator's log. It **MUST** be
treated as a backup of a claim and **MUST NOT** be an input to any computation.

**R25.** For a long or multiline instruction the sender **SHOULD** write a named file and then send
a short single-line trigger to read it. *This avoids multiline paste failures in interactive
terminal interfaces, and keeps asks visibly separate from products.*

**R26.** The control operations **MUST** stay narrow: put an input, trigger a turn, read state,
output or transcript, and fetch source. *Provenance is advisory rather than tamper-proof, and a narrow
operation set leaves room for a hardened mode that does not change the user-facing commands.*

### 4.4 The member contract

Sections 4.1 to 4.3 say what a member is. This says what a member is for.

**R27.** A campaign **MUST** have exactly two inputs: the team and the mission.

**R28.** The operator **MUST** compose the team at creation, and that composition **MUST** be
immutable for the campaign's life. The orchestrator **MUST NOT** create a member, retire one, or
re-scope one.

Decomposition therefore happens twice, and only the second is the orchestrator's:

| | Who | When | What |
|---|---|---|---|
| Structural | operator | before create | which roles exist and what each owns |
| Dynamic | orchestrator | during the run | which work goes where, and whether it is acceptable |

**R29.** Every declared member **MUST** have a written purpose, and the campaign **MUST** have a
mission. Both **MUST** resolve before anything is allocated, and creation **MUST** be refused when
either is absent. *The check is worth nothing after the team is booted and the bill has started.*

**R30.** A briefing that could be silently substituted for a generic one **MUST NOT** exist. *A
member handed boilerplate restates that boilerplate faithfully, so the check meant to catch an
unbriefed member cannot see through it.*

**R31.** A member's knowledge **MUST** be exactly three things: its trees, its seeded input, and
its dispatches. *The enumeration is what makes "what could this member possibly know?" answerable.*

**R32.** An implementation **MUST NOT** broadcast the mission to every member, and **MUST NOT**
assume any member has seen it.

**R33.** A dispatch **MUST** carry everything its recipient needs. *Visibility is per principal, so
the sender cannot know what the recipient can read.*

**R34.** The product **MUST** supply each member with standing context it can derive from nothing
else. That context is its identity, its branch, the location of its channels, the files it was
given, and its obligations.

**R35.** That standing context **MUST** state only what is true of every campaign. Anything
campaign-specific belongs in the operator's briefing, and the two **MUST NOT** restate each
other.

**R36.** Everything a member must read **MUST** be reachable from one machine-readable list,
product-authored context included. *Two delivery paths for one job is how a member ends up with an
unverified half: whichever path lacks the existence check is the one whose failure nobody sees.*

**R37.** Context **MUST** be re-established whenever a member loses it, before it is asked to work
again. *Clearing a session is a recovery action. Without this rule the remedy for a stuck member
strips it of the obligations it is judged against, and it keeps going.*

**R38.** Every member **MUST** commit its work to its own branch. *Harvesting reads a branch, so
work that is never committed cannot be recovered and is destroyed with the machine.*

**R39.** Every member **MUST** write durable results to its output channel.

**R40.** A member **MUST NOT** initiate contact with peers or with the host. This is enforced by
the credential boundary in R15.

**R41.** The orchestrator **MUST** drive teammates only through the scoped helper. This is enforced
by the family guard in R58.

**R42.** The orchestrator **MUST** verify returned work rather than accept a member's report. *It
is accountable for what the campaign delivers, so a member's self-report is a claim and its branch
is the evidence.*

**R43.** The orchestrator **MUST** deliver against the mission on its own branch, and **MUST**
report an outcome.

**R44.** Every campaign **MUST** end in exactly one exit state, and all but the first **MUST** name
what remains unmet:

| Exit state | Meaning |
|---|---|
| `campaign-met` | The work satisfies the mission. |
| `campaign-converged` | The mission is satisfiable, and this team stopped getting closer to it. |
| `campaign-exhausted` | Stopped on budget or wall clock while progress was still being made. |
| `campaign-blocked` | Effort was never the problem: an obstacle no amount of iteration resolves, including a mission that cannot be satisfied as written. |

**R45.** An established impossibility **MUST** be reported as `campaign-blocked` explicitly. *The
three states that end with gaps are separated by one question: was effort ever the problem? Each
implies a different move by the operator: resume it, change it, or unblock it. `campaign-converged`
reported for an impossible mission is the costliest error, because it asserts the goal was reachable
and blames the team for not reaching it.*

**R46.** Before a campaign is usable, every member **MUST** be asked to restate its own job, and
the campaign **MUST** be refused if any member cannot.

**R47.** The restatement **MUST** be a restatement rather than a confirmation. *A member that read
nothing answers a yes-or-no question correctly, and cannot invent the scope its brief describes.*

**R48.** The restatement **MUST** cover both halves of what a member was given: what the operator
asked of it, and what the product requires of it. *Checking only the operator's half leaves the
product's own doctrine delivered on trust, including the obligation in R38 whose failure is
irreversible.*

**R49.** The member **MUST** check its own environment against the machine-readable manifest it was
given, before it restates anything. *A briefing that never arrived is then reported by the member
rather than inferred later from a vague answer.*

**R50.** Structure **MUST** be verified and content **MUST NOT** be graded. The restatements
**MUST** be surfaced for a human to read. *Whether a restatement is correct depends on what the
operator meant, which the product cannot know, and a product that graded prose would fail healthy
teams on phrasing.*

**R51.** Skipping the readback **MUST** be recorded on the campaign. *A team nobody verified must
not be indistinguishable afterwards from one that passed.*

### 4.5 Dispatch and completion

The machine is specified in [`PROTOCOL.md`](PROTOCOL.md). What follows is what an implementation of
it must preserve.

**R52.** Infrastructure health, model activity and task completion **MUST** remain separately
observable. *Reporting any one as another is the defining bug of this domain.*

**R53.** Every message **MUST** carry a dispatch ID minted by the tool, from a per-node sequence
whose lexical order is its chronological order.

**R54.** A dispatch **MUST** be open until its reply artifact exists, at one predictable path per
ID. *Completion is then an existence check with no parsing and no per-CLI knowledge.*

**R55.** Node state **MUST** be computed on demand from the node's own machine and **MUST NOT** be
stored. No file, field, column or in-memory record may outlive the look.

**R56.** The one off-node input **MUST** be the orchestrator's acceptance record, and acceptances
**MUST** be node-qualified. *Dispatch IDs are per-node sequences, so a bare ID silently frees a
different node's unjudged reply.*

**R57.** There **MUST** be exactly one send operation. It opens a dispatch if the current one is
closed and continues it if open, computed from the worker's channel and never classified by a
model.

**R58.** Recovery **MUST** be mechanical: the templated continue, then the restart re-anchor. The
dispatcher's `wait` runs it under operator-set policy numbers, bounded by attempt counts and an
elapsed clock, whichever trips first.

**R59.** A restart **MUST** leave a countable message. *Ladder position is then derived from a
listing rather than remembered.*

**R60.** A freshly sent message **MUST NOT** read as stopped while its turn is still booting, and
the settling window **MUST** re-arm on every send, a restart's own re-anchor included. *Otherwise
the restart rung is dead on arrival: the re-anchor increments the restart count, and the next poll
reads the node as stuck while the restarted session is still starting.*

**R61.** A message delivery **MUST** refuse an existing message name, and a sender that loses the
mint race **MUST** reclassify against a fresh look rather than overwrite.

**R62.** A turn's ending **MUST NOT** be consumed as a dispatch's outcome. *Exit codes, output
footers, transcript quiet and watcher verdicts are evidence about a turn or its watcher. Wall
clock, stall detection and dropped connections all end a watcher for reasons that say nothing about
the work.*

**R63.** A node that stopped without replying **MUST** get the ladder rather than a failure
verdict.

**R64.** Liveness **MUST** be measured from the node's own process table, as the presence of its
family's turn driver. *That is the right measure for silent work: the process is there whether or
not anything is being emitted.*

**R65.** A failed probe **MUST** be treated as a fact about the observation rather than about the
node. Only a run of consecutive failures past the operator's threshold **MAY** become the
conclusion that the machine is gone. A probe **MUST** be bounded. *An unbounded one defeats this
rule entirely: the observation blocks on the single node it exists to notice, and every state
computed from it stops being reached, this conclusion included.*

A probe that missed that bound **MUST NOT** count towards the conclusion. *An error comes back from a
machine that answered, and a run of them is evidence it has died. A missed bound is silence, which a
machine under load produces as readily as a machine that is gone. A node starved of CPU misses the
bound while working steadily, and calling it gone is the same mistake as calling a slow node
stalled.*

**R125.** The dispatcher's blocking `wait` **MUST** return only on a judgment. A judgment is a
**world event** that needs a decision no code can make: `node-replied` and `node-stuck`, those two
and no others. `node-free` **MUST NOT** end a block. *Its only arrows in are `accept` and campaign
start, a dispatcher action and an initial condition, both the dispatcher's own. Returning for it
wakes the model to report what the model itself just did. It also cannot be waited out. A phased
fleet parks seats deliberately, nothing on a node ever clears the state, and no instrument records
"leave this one free". So the wait returns on its first snapshot, before sleeping once, for the rest
of the campaign. Observed live: a model met that, reasoned correctly that looping on it spends a
turn per poll, then backgrounded a poller and ended its turn. Nothing can wake that, and the host
reads it as a stopped orchestrator.* A free node **MUST** still be named when the chunk elapses, so
deferring the signal costs no visibility.

**R126.** The chunk a `wait` blocks for **MUST** be bounded, and overridable outside the profile on
the same terms as `pollSeconds` (§6.1). *R125 makes that bound load-bearing. Before it, a free node
short-circuited nearly every chunk, so the number was rarely reached. A replay tier that cannot
shorten it pays the full chunk on every recorded `wait`.*

### 4.6 Team conformance

**R66.** Each member **MUST** be provisioned for exactly one declared CLI, and the declaration
**MUST** be enforced rather than trusted, in three layers.

**R67.** The orchestrator **MUST** drive peers only through a scoped helper that routes by declared
CLI.

**R68.** A raw per-family remote tool inside the orchestrator **MUST** refuse a wrong-family
invocation against a member, with the true diagnosis. *Without this the wrong family reports "not
logged in", which has previously been answered by copying credentials, manufacturing an undeclared
second member of the wrong family.*

**R69.** The team **MUST** be audited against evidence rather than declaration. Each member's
declared-CLI transcript stream must be the one that did the work. No member may carry another
family's session or credential state.

**R70.** That audit **MUST** run while the sandboxes still exist, before destroy. *A divergence is
then provable from the archive rather than dependent on a member's self-report.*

**R71.** The audit **MUST** check magnitude rather than presence. *A present-but-trivial evidence
file is the shape a silently dead member leaves.*

**R72.** Each adapter **MUST** declare its capabilities and the CLI versions its prompt delivery,
state markers, transcript paths and recovery behaviour were tested against.

**R73.** Campaign state and archives **MUST** record the installed version rather than the tested
one.

**R74.** An untested version **MUST** produce a visible warning. Creation **MUST** fail only when a
required capability is absent or the adapter cannot safely operate, and **MUST NOT** fail merely
because a patch version changed. *A version floor that refuses a working build is worse than a
warning nobody reads, because knowing better cannot override it.*

**R75.** A toolchain whose tools are present but not the pinned ones **MUST** be reported as a
deviation rather than accepted. Accepting one deliberately **MUST** be recorded on the campaign it
built.

### 4.7 The command-line contract

**R76.** The host executable **MUST** be `cs-campaign`, with noun-like subcommand names, stable
tabular output, environment defaults and host-owned JSON state.

**R77.** Normal operation **MUST** be create, then observe. The host dispatches to the orchestrator
alone, with the create-time readback as the single host-to-agent exception.

**R78.** Every repair inside a running campaign **MUST** belong to the orchestrator. `send` and
`restart` against the orchestrator are operator instruments and **MUST NOT** become a loop.

**R79.** `observe` **MUST** render its two panes separately and **MUST NOT** merge them: node
states computed now from each node's own machine, beside what the orchestrator recorded. *One is
derived fact and the other is a claim. A single merged status column destroys exactly the line an
operator most needs: "orchestrator says working" beside "unreachable".*

**R80.** Member names **MUST** be unique DNS labels, **MUST NOT** use a reserved role name, and
**MUST** be stable campaign-local identities used in addresses, state, logs, branches, archives and
dispatches.

**R81.** Homogeneous shorthand **MUST** produce deterministic names.

**R82.** Explicit and shorthand team definition **MUST** be mutually exclusive. *Membership must
not depend on ambiguous flag merging.*

**R83.** Listing output **MUST** print the group and every member's qualified reference on request.
*The abstraction then stays debuggable with the underlying sandbox tool.*

**R84.** Verbs that answer different questions **MUST NOT** be named as though they answer the same
one. A verb that is safe to call repeatedly **MUST NOT** take a write lock.

**R85.** Where several commands report state, they **MUST** either agree or say plainly which
question each answers.

### 4.8 Profiles

**R86.** A profile **MUST** be desired configuration rather than live state. The campaign record
captures one resolved execution.

**R87.** Profile parsing and ordinary create flags **MUST** produce one internal spec. YAML
**MUST NOT** be translated into a shell command and executed.

**R88.** `validate` **MUST** check schema, names, adapter capabilities, path existence, grant
conflicts and cross-field invariants without allocating or creating anything, and **MUST** accept
exactly what `create` accepts. *A profile that creates cleanly must not fail validation.*

**R89.** `plan` **MUST** resolve defaults, paths, refs, generated names and required actions, then
print a deterministic, secret-redacted plan without changing state.

**R90.** `create` **MUST** execute that same typed plan.

**R91.** An unknown override path or a type change **MUST** fail rather than be silently accepted.

**R92.** Defaults **MUST** be expanded into each member before any sandbox is created, and a member
that declares a key **MUST** replace the default rather than merge with it. *A member can then opt
out of one.*

**R93.** `model` and `effort` **MUST** be handed to that member's CLI verbatim. *They are not a
shared vocabulary: an effort level means a different thing per adapter.*

**R94.** The resolved `model` and `effort` **MUST** be recorded on the member. *Omitting them leaves
the member on a moving default, and the record should say what a seat actually ran on rather than
nothing at all.*

**R95.** Where a declaration can be read back, it **MUST** be verified at create. Where an
adapter's store cannot name the answering model, that **MUST** be said rather than papered over
with the line a verified member gets.

**R96.** Profiles **MUST** contain secret references or login-inheritance requests, never secret
values.

**R97.** A credential value **MUST NOT** appear in a plan, a subprocess argument, campaign state,
or an archive.

**R98.** Authentication **MUST** resolve from explicitly declared grants only. Host credential
discovery **MUST NOT** grant an undeclared credential.

### 4.9 Campaign state

**R99.** Host-side state **MUST** be authoritative for provisioning. It **MUST** record at least
the following.

- Campaign identity.
- The source profile path, its digest, the overrides and the resolved policy.
- The create checkpoint and the timestamps.
- The group and the derived network.
- Each member's sandbox name, qualified reference, adapter and branch.
- The source repository identity and base revision.
- The sandbox version and capabilities used to create it.

**R100.** Host-side state **MUST NOT** record a campaign lifecycle. *A campaign runs for exactly as
long as the mission dispatch is open, which is computed.*

**R101.** State writes **MUST** be atomic, and a save performed under concurrency **MUST NOT** lose
a concurrently written member's record. *A whole-campaign copy saved without the lock silently
reverts other members.*

**R102.** Create and destroy **MUST** be resumable. Every completed resource is recorded as it is
made, and teardown tolerates an already-absent resource, including a group already reclaimed by an
interrupted destroy.

**R103.** Identity **MUST** be `(group, name)`. Membership **MUST NOT** be inferred from a name
prefix, and a lookup **MUST NOT** be keyed on a bare sandbox name. *A listing indexed by name
collapses same-named members of different campaigns onto one entry, so a member can read another
campaign's status.*

**R104.** Corrupt or unreadable state **MUST** be surfaced rather than skipped. *A listing that
quietly omits a record it could not parse reports a smaller, healthier team than exists.*

**R105.** The state schema **MUST** be versioned, and **MUST NOT** be migrated across a boundary
that changes addressing. *A record written before campaigns were groups addresses members in a way
a group-aware tool cannot see, so such a record is refused with an actionable error rather than
loaded into a campaign whose every command would miss.*

**R106.** Secrets and inherited logins **MUST NOT** be copied into state or archives.

### 4.10 Archives

**R107.** An archive **MUST** capture the same evidence classes for the orchestrator and for every
agent: what each was asked, and what it produced.

**R108.** `transcript/` **MUST** contain only paths declared by that member's adapter.

**R109.** `source-metadata/` **MUST** record base and head commits, branch, status and a diff
reference, and **MAY** omit a repository harvested elsewhere.

**R110.** Collection **MUST** be by allowlist. Credentials, auth files, injected secrets, caches and
unrelated home state **MUST** be outside the allowlist rather than removed after collection. *A
filter that runs second is a filter a new file can outrun.*

**R111.** Archiving **MUST** be a host-driver responsibility, and a campaign member **MUST NOT** be
able to rewrite the authoritative archive.

**R112.** Archiving **MUST** run before destroy. *The microVMs are the only copy of a
member's work until it is harvested.*

**R113.** An archive step that cannot complete **MUST** mark itself incomplete in the artifact
rather than fail the whole collection. *Partial evidence is worth more than none, and silently
partial evidence is worth less than none.*

### 4.11 Repository and result model

**R114.** Host bind-mounted worktrees sharing the host repository's git metadata **MUST NOT** be
used. *They are not usable across the boundary and would weaken it.*

**R115.** The host repository and an immutable base commit **MUST** be resolved and recorded.

**R116.** The orchestrator and every agent **MUST** be seeded with separate clones at that base.

**R117.** The mapping from member name to sandbox and branch **MUST** be recorded rather than
derived later. *A derivation duplicated on both sides of a boundary drifts, and the first symptom is
a fetch that lands nowhere.*

**R118.** Every member **MUST** commit only in its own clone.

**R119.** The orchestrator **MAY** fetch agent branches over campaign-local SSH to review, test,
cherry-pick or merge into its integration branch. The host **MAY** fetch any branch at any time.

**R120.** The tool **MUST NOT** push to a remote unless the driver explicitly asks.

**R121.** Campaign IDs **MUST** be deterministic, so destroying a campaign and recreating it
identically produces the same group, member names and branches.

**R122.** A fast-forward fetch that correctly refuses a rerun's sibling history **MUST** be
explained rather than removed, reporting what occupies the branch and both ways out.

**R123.** For a new application the tool **MUST** create or adopt a host-owned repository with an
initial commit and use it as the common base. *The application must not exist only inside a
disposable sandbox.*

**R124.** The agent owning an application **MUST** own its runtime: build scripts, compose
definitions, databases and child containers run under that agent's nested containers and are part
of the deliverable.

---

## 5. Data model

### 5.1 The campaign profile

The profile is YAML, with `apiVersion: codesweep.ai/v1alpha1` and `kind: CampaignProfile`.

```yaml
apiVersion: codesweep.ai/v1alpha1
kind: CampaignProfile
defaults:
  engine: firecracker
  deadline: 6h
  resources: {cpus: 2, memoryMiB: 2048}
  policy: {continueAttempts: 2, restarts: 1, pollSeconds: 30}
orchestrator:
  cli: codex
  model: gpt-5.1-codex
  repos: [{path: /srv/product}]
  auth: {apiKeyFromEnv: [OPENAI_API_KEY]}
agents:
  backend:
    cli: claude
    repos: [{path: /srv/product}]
    auth: {inheritAgentLogin: [claude]}
    policy: {stallSeconds: 240}
```

| Field | Where | Meaning |
|---|---|---|
| `engine` | `defaults` | The sandbox engine every member runs on: `firecracker` for a microVM, or `podman` for a container. |
| `deadline` | `defaults` | The campaign wall clock as a duration from create. |
| `resources.cpus`, `resources.memoryMiB` | `defaults`, member | The microVM's size. |
| `policy` | `defaults`, member | The dispatch machine's numbers. See §6. |
| `env` | `defaults`, member | `KEY=VALUE` injected into the sandbox, or a bare `KEY` to inherit the host's value. |
| `cli` | member | One of `claude`, `codex`, `opencode`. |
| `model`, `effort` | member | Passed to that member's CLI verbatim. `opencode` requires `model` whenever `effort` is set. |
| `repos[].path`, `.ref`, `.name` | member | A host repository cloned into the member. |
| `snapshots[].path`, `.name` | member | A frozen tree the member can read. |
| `auth.apiKeyFromEnv` | member | Host environment variable names whose values are granted. |
| `auth.inheritAgentLogin` | member | CLI families whose host login this member inherits. |

### 5.2 Campaign state

One JSON document per campaign, `version: 2`. It records:

- campaign identity, the group, the network and the gateway port;
- the engine, the create checkpoint and the timestamps;
- the profile path and digest, the overrides and the resolved policy;
- the resolved deadline and the pin verdict at create;
- one record per member.

A member record carries its identity: role, CLI, bare sandbox name, qualified reference, address,
branch, and whether it is `solo`. It carries the resolved model, effort and stall threshold, and
the per-member harness verdict. It also carries the sha256 of every seeded input, the readback, the
session name and the member's own profile.

### 5.3 The member document

`~/.config/cs-campaign/member.json` sits inside each member. It carries the campaign, the member
name, the role, the network and the branch. Beside those it carries the repositories with their
base commits, the list of inputs, the input and output channel paths, the orientation path, the
resolved policy and the campaign deadline.

`~/.config/cs-campaign/manifest.json` exists only in the orchestrator. It carries the campaign
name, the network, and the policy its `wait` loop runs on. One roster row per agent follows, with
that agent's CLI, bare sandbox name, session name, repositories with branches, base commits and
snapshots.

### 5.4 Dispatch messages

Files in a node's input channel, under `~/.local/share/cs-campaign/input/`:

| Name | Meaning |
|---|---|
| `d007.md` | the opening message of dispatch `d007` |
| `d007.001.md` | its first continuation |
| `d007.002.restart.md` | a restart re-anchor, counted as a restart rather than a continue |
| `m1.md`, `m1.001.md` | the mission and its continuations |

IDs run `d001` to `d999` per node, so lexical order is chronological order in every listing. The
mint refuses past `d999` rather than widening and breaking sort order.

### 5.5 The reply

`~/.local/share/cs-campaign/output/replies/<id>.json`. The model supplies `note`, and `outcome` and
`unmet` on the mission reply; the tool stamps the rest.

```json
{
  "dispatch": "d007",
  "phase": "done",
  "note": "Rewrote the parser and added the round-trip test.",
  "at": "2026-08-19T17:02:11Z",
  "repos": [
    {"repo": "product", "head": "9f2c1ab", "treeDiffersFromBase": true}
  ]
}
```

| Field | Meaning |
|---|---|
| `dispatch` | The ID this reply closes. |
| `phase` | `done`, or `needs-input` when the team cannot act without outside help. |
| `note` | The member's own account of the work. |
| `outcome` | Mission reply only: one of the four exit states in R44. |
| `unmet` | Mission reply only: what remains unmet. Required unless the outcome is `campaign-met`. |
| `at` | When the reply was written. |
| `repos[].treeDiffersFromBase` | The durable-work test: tree against base, never commit count. |
| `repos[].dirty` | Uncommitted changes were present at reply time. |

The file is written to a temporary name and renamed, so it appears whole. Presence is the signal,
so a torn write must never be observable.

### 5.6 The orchestrator's log

`~/.local/share/cs-campaign/output/log.jsonl`, append-only, one JSON object per line:

```json
{"at":"2026-08-19T17:02:11Z","kind":"plan","text":"backend takes the parser; qa takes the fixtures."}
```

`kind` is `plan`, `accepted` or `assessment`, and nothing else. A later entry of a kind supersedes
an earlier one. There is no rewrite path.

### 5.7 The archive

```text
archive/
├── campaign.json                 the record, resolved policy included
├── campaign-profile.yaml         what was declared
├── readback.json                 every member's restatement of its job
├── fleet-verdict.json            the audit's findings
├── member-harness.json           each member's tool surface, measured again here
├── upstream-fingerprint.json     the surface the campaign ended on
├── orchestrator/
│   ├── config/                   member.json, manifest.json, the delivered stall threshold
│   ├── input/                    m1 and its continuations
│   ├── output/                   replies/m1.json, and log.jsonl
│   ├── transcript/               its CLI's own session evidence
│   └── source-metadata/          branch, base, commit, status, diff
└── agents/<name>/
    ├── config/                   member.json, the delivered stall threshold
    ├── input/                    d001 onward
    ├── output/replies/           its replies, evidence stamped
    ├── transcript/               its CLI's own session evidence
    └── source-metadata/          branch, base, commit, status, diff
```

Both layouts hold the same evidence classes (R107); only the orchestrator's output channel carries a
log. An audit that found something also leaves `FLEET-ANOMALY.txt` at the root.

Anything that failed to collect leaves an `INCOMPLETE-*` marker beside where it should have been.
Each collection command is bounded, so a member that stops answering leaves a marker naming the
deadline rather than holding the archive open.

A collection that prepares readable evidence before copying the raw kind gives the preparation its
own smaller bound, and copies whatever it produced. The opencode session exports beside that
adapter's SQLite database are the case. A slow member can therefore hand back fewer exports than it
has sessions, and never fewer databases. `audit` reads an empty evidence tarball as another CLI
having done the work, so the raw stream is the one that must always be collected.

### 5.8 The pin

`~/.config/cs-campaign/pin.json`: the time it was recorded, the `cs-sandbox` version, the sha256 of
every pinned tool, and a note saying what was run and what it proved.

Where the host also has the replay surface, `cs-vcr` today, its hash and its normalization ruleset
are recorded beside them. They are recorded separately: a campaign host that cannot record or replay
a cassette does not have it, and must not read as deviating for that.

---

## 6. Configuration

### 6.1 The policy numbers

Every number the dispatch machine runs on comes from `defaults.policy` and falls back to a
compiled-in default. One resolved set governs the campaign: it is recorded on the campaign, in every
member document and in the orchestrator's manifest, and every node is computed against it.

| Number | Default | Meaning |
|---|---|---|
| `continueAttempts` | 2 | Continues before escalating to a restart. |
| `restarts` | 1 | Restarts before a node is unrecoverable. |
| `elapsedSeconds` | the campaign deadline, else 86400 | Bound on recovering one dispatch, from its opening. |
| `blindProbes` | 10 | Consecutive failed probes before a machine is called gone. |
| `pollSeconds` | 30 | How often the orchestrator's `wait` looks. |
| `settlingSeconds` | 300 | Grace after any send before a driverless node counts as stopped. |
| `stallSeconds` | 180 for agents, 1800 for the orchestrator | The turn driver's own idle threshold, delivered at create through the sandbox environment. |

`stallSeconds` is the one number resolved per seat, because it is the one number that is not the
machine's. It belongs to a member's own turn driver, and long quiet means something different for a
supervisor than for a worker. A member's `policy:` block sets it for that member, `defaults.policy`
for the campaign, and otherwise the role's compiled-in default applies. The resolved value is
recorded on that member's record.

`CS_CAMPAIGN_POLL_SECONDS` overrides `pollSeconds` where the wait sleeps, and nowhere else. It is deliberately
outside the policy an implementation records. A replay tier needs a shorter interval than a real
campaign. That number in the profile, or in a member's recorded policy, would change the bytes a
recorded session is matched on.

The chunk one `wait` blocks for is the same class of number, and is kept out of the policy set for
the same reason. It defaults to 240 seconds, short enough for every agent CLI's cap on a single tool
call (PROTOCOL.md §8). The caller may ask for another with `--for`. The environment can override both,
`--for` included, through `CS_CAMPAIGN_WAIT_SECONDS`. What a replay tier replays is a recorded model that asked for the
campaign's number, so an override the argument could beat would bound nothing (R126).

`defaults.deadline` is the campaign wall clock as a duration from create, such as `90m` or `6h`. It
stops nothing by itself: the orchestrator's judgement enforces the deadline, and the machine uses it
only as the `elapsedSeconds` default.

### 6.2 Resolution order

Each member field, and `stallSeconds`, resolves in this order:

1. Take the member's own declaration in the profile.
2. Otherwise take the profile's `defaults` block.
3. Otherwise take the compiled-in default.

A member that declares a key replaces the default rather than merging with it (R92).

The rest of the policy numbers have no step 1: they resolve once, from `defaults.policy` over the
compiled-in defaults, and govern every node (§6.1). A member that declares one is refused by
`validate`, rather than provisioned to run on the campaign's value with its own profile saying
otherwise.

### 6.3 Environment

| Variable | Read by | Effect |
|---|---|---|
| `CS_CAMPAIGN_STATE_DIR` | `cs-campaign` | Where campaign state lives. Default `$XDG_CONFIG_HOME/cs-campaign/campaigns`. |
| `CS_CAMPAIGN_PIN` | `cs-campaign` | Where the pin lives. Default `~/.config/cs-campaign/pin.json`. |
| `CS_CAMPAIGN_GUEST_BIN` | `cs-campaign` | A guest binary to install instead of the embedded one. |
| `CS_SANDBOX_BIN` | `cs-campaign` | The `cs-sandbox` executable to invoke. Default `cs-sandbox`. |
| `CS_SANDBOX_INSTANCES_DIR`, `CS_SANDBOX_HOME`, `XDG_DATA_HOME` | `cs-campaign` | Where `cs-sandbox` keeps instances, for the socket-path budget check in R11. |
| `CS_CAMPAIGN_POLL_SECONDS` | `cs-campaign`, `cs-campaign-member` | Overrides `pollSeconds` where the wait sleeps (§6.1). |
| `CS_CAMPAIGN_WAIT_SECONDS` | `cs-campaign-member`, forwarded by `cs-campaign` | Overrides how long one `wait` blocks, `--for` included (§6.1, R126). |

Files a campaign reads or writes are listed in [`MANUAL.md`](MANUAL.md).

---

## 7. Implementation

### 7.1 Packages

| Package | Role |
|---|---|
| `internal/protocol` | The dispatch protocol: channel paths, dispatch identity, the reply and log shapes, the probe, and the node-state computation. |
| `internal/model` | The profile, campaign and member types, and the adapter list every surface derives from. |
| `internal/store` | Campaign state on disk: atomic saves, the per-campaign lock, the schema version. |
| `internal/cli` | Every host command, the sandbox shell-out, briefs, orientation, archive, audit, doctor and pin. |
| `internal/covmap` | The behaviour map: the rubric, the run records, and the rendered page. |
| `cmd/cs-campaign` | The host binary. |
| `cmd/cs-campaign-member` | The guest binary: the member verbs, the dispatcher verbs, and the family guard. |
| `cmd/covmap` | Folds the run buffer into the map and re-renders it. |
| `dispatch-viewer` | The archive viewer, its frame model and its findings registry. |

Dependencies run one way: `cli` and the guest binary both depend on `protocol` and `model`;
`protocol` depends on neither. *Both ends of every channel build on the same package, so the two
agree by construction rather than by convention.*

### 7.2 Key types

`protocol.Msg` is one observed message file. `protocol.Dispatch` is the reconstruction of one
dispatch from its messages. `protocol.Facts` is everything one probe returns: the messages, which
replies exist, and how many turn drivers are alive. `protocol.Observation` is the computed state
plus the mechanical next move. `model.Campaign` is the host record; `protocol.Member` is what a
member is told about itself.

### 7.3 The properties the design exists to guarantee

**No stored dispatch state.** There is no field to go stale, because there is no field. The cost is
a probe per look; the benefit is that a host that was switched off learns nothing wrong when it
comes back.

**One send.** The classification lives in one function over one directory listing. A model never
decides whether its own message opens or continues.

**Order in the state computation.** Reachability precedes everything, and the reply check precedes
the liveness check. A node that replied and then exited is `node-replied`, not `node-stopped`.

**The settling window before the ladder counts.** A restart re-anchor is a send with the same cold
start as any other, so the window re-arms and the next poll does not read a booting session as
stuck.

### 7.4 Lifecycle and concurrency

Create and destroy hold an exclusive `flock` on the campaign for the whole command. Every save
writes the whole record to a temporary file and renames it, so a reader sees one version or the
other and never a torn one. `doctor <campaign>` probes without the lock and takes it only to save,
where it re-reads the record and writes back the one field it owns. Message delivery mints an ID
and refuses to clobber an existing name; a
sender that loses the race looks again. Create checkpoints each completed resource, so a re-run
continues rather than starting over. Destroy tolerates an absent resource at every step.

### 7.5 The behaviour map

`covmap/behaviors.json` is an authored rubric, and every row cites the section of this document
whose promise it restates. Cells fill only from records that tests emit at their proving assertion,
so a cell is filled because a test ran and proved it. `covmap/covmap.html` is a pure function of
rubric plus records, and a unit test fails when it is stale. `make covmap` folds a run's buffer
into the records, prunes what no longer has a test, and re-renders the page.

### 7.6 Testing strategy
#### The four tiers

Each tier answers a question the one below it cannot, and each costs more than the one below it.

| Tier | Command | What runs | Cost |
|---|---|---|---|
| unit | `make test` | Everything in the default build, against faked sandboxes and a scripted world. | Free, and what `make check` runs. |
| smoke | `make test-smoke` | The whole protocol on real machines, with every model call served from the committed cassette. | Minutes and no money. What CI runs on every push. |
| integration | `make test-integration` | The same campaign against real providers, once per backend. | Real money, and about half an hour. |
| fixtures | `make fixtures` | One recorded campaign, which is what the smoke tier replays. | Real money, run when a recording goes stale. |

A build tag adds files to a package rather than hiding the rest, so every tier above unit also
passes `-run` to select its own tests. Without it `make test-smoke` would re-run the unit tier
under a timeout sized for booting microVMs.

#### The smoke tier

`make test-smoke` boots two Firecracker machines and runs a campaign end to end: the readback, the
mission, the orchestrator driving an agent, a verdict, the archive and the audit. It holds no
credential and reaches no provider, because a cs-vcr joined to the campaign's own fabric network
serves every model call from `test/cassettes/`.

The proxy is one container per group, taking the network alias `vcr` on `cs-sandbox-<group>`. An
alias is network-scoped, so every campaign profile names the same URL; the container name is
host-global, so it carries the group. One campaign therefore cannot read or write another's
cassettes, and the traffic never leaves the podman bridge.

It skips, saying which, when the host has no `/dev/kvm`, no podman, no `cs-vcr`, or no cassette. It
does not skip for want of a login. Every member is given a fabricated credential. For a key
scenario that is the variable the profile names. For a subscription it is the credential file the
agent reads, written into a profile tree `CS_SANDBOX_AGENT_HOME` points cs-sandbox at.

That works because no member reaches a provider that could refuse it. A base URL aims the model
calls at cs-vcr, and these agents also contact hosts of their own: Claude Code checks its OAuth
session and Codex fetches its account's connectors. What those answer changes the prompt, so every
member points `HTTP_PROXY` at the same cs-vcr, which refuses that handful and tunnels the rest.

The proxy is set while recording as well as while replaying. Refused in both halves, the two runs
ask the same question, which is what lets a session recorded under a real subscription replay under
a fabricated one.

**A replay reproduces the model's decisions, not the world's facts.** The orchestrator's judgement
is replayed rather than re-derived, so this tier proves the harness carried a campaign to a verdict.
It does not verify the work that campaign delivered.

`make fixtures` records what this host can sign in for and skips the rest, saying which credential
was missing. `make fixtures-strict` fails on that skip instead, which is what a host holding all
five wants. `scripts/record-fixtures.sh` checks the environment first and runs the strict form.

Recording asks each scenario's agent for a single word, on this host and against the real provider,
before it clears a cassette. A campaign is two microVMs, a fabric and a dispatch ladder.
Discovering there that a key was revoked costs all of it, and leaves a half-written cassette where a
committed one used to be.

#### The backend matrix

`make test-integration` runs the smallest complete campaign once per backend, with the team
homogeneous so a failure names the backend that broke:

| Scenario | Adapter | Signed in with |
|---|---|---|
| `claude-subscription` | claude | a Claude Pro or Max subscription on this host |
| `claude-api-key` | claude | `ANTHROPIC_API_KEY` |
| `codex-subscription` | codex | a ChatGPT subscription on this host |
| `codex-api-key` | codex | `OPENAI_API_KEY` |
| `opencode-fireworks` | opencode | `FIREWORKS_API_KEY` |

A scenario this host cannot sign in for skips with the credential it wants, which is how a run
reports what one more login would cover. One further test drives a mixed team, because a helper
that routes by declared CLI is only exercised when the two members differ.

The replay tier runs every scenario, and so does CI. `opencode-fireworks` was once left out: on a
two-core runner its member never got its turn started. The driver behind that has since been fixed.
A relapse is legible rather than silent, because a run that keeps evidence collects each member's
agent-driver log, and CI uploads it when the job fails.

**Every scenario is recordable.** Aiming an adapter at cs-vcr means setting the base URL it reads,
and the profile's `env:` block sets it:

| Adapter | Variable |
|---|---|
| claude | `ANTHROPIC_BASE_URL` |
| opencode | `OPENCODE_BASE_URL` |
| codex | `OPENAI_BASE_URL` |

Claude reads its own directly. The other two do not, and each wrapper ships from
[the sandbox repository](https://github.com/codesweep-ai/sandbox), whose manual documents the
behaviour.

An opencode base URL belongs to the provider rather than to the client. opencode reads
`OPENAI_BASE_URL` and `ANTHROPIC_BASE_URL`, and nothing else. A model on any other backend ignores
both. Setting `OPENAI_BASE_URL` for a fireworks model therefore records nothing: the campaign runs
against the real endpoint while the proxy sits idle. What works for every backend is a `baseURL` in
opencode's own configuration. `cs-opencode` derives it from the pinned model and writes that
configuration inline from `OPENCODE_BASE_URL`.

Codex reads no such variable at all, taking a provider declaration instead. `cs-codex` builds one
from `OPENAI_BASE_URL` and passes it as `-c` overrides, with nothing written to a configuration
file.

Codex's two auth modes take different URLs. With an API key the provider names the variable holding
it, and the base URL ends in `/v1`. On a ChatGPT subscription codex authenticates as itself and
takes the bare URL. The scenario table carries that difference, and a subscription member must be
granted no `OPENAI_API_KEY`, and a key present wins.

#### Recording a cassette

```bash
make fixtures      # records test/cassettes/ through a cs-vcr in record mode
make test-smoke    # replays it
```

The cassette is bound to the profile that recorded it. A campaign's ID is the sha256 of its
resolved profile, so the group, both sandbox names, the branches and the sessions all derive from
it. Those names reach the wire inside tool-call arguments, which cs-vcr rightly matches exactly.
Editing the profile the driver renders invalidates the recording, so re-record rather than
hand-editing a cassette.

Recording forgets the host's session records on the way out. `startTurn` asks `hostSessionFresh`
which branch of `protocol.Trigger` to send, and the two emit different prompt text, so a replay
against a warm session sends a request the cassette never recorded.

A cassette is also keyed by cs-vcr's normalization ruleset, which is bumped whenever the meaning of
a key changes. Cassettes do not survive that bump, and the failure it produces is a silent hang
rather than an error. `make fixtures-check` answers whether the committed ones still replay under
the cs-vcr you have, without booting anything; `make check` runs it, and `make test-smoke` asserts
it per scenario before provisioning. `test/cassettes/README.md` has the detail.

#### What an interrupted live run leaves behind

A live run killed part-way leaves machines, a network, a key pair and a gateway port behind, with
model turns still being charged. The driver catches SIGINT and SIGTERM and reclaims the campaign's
group before it exits, and `t.Cleanup` covers a test that merely fails.

The first interrupt reclaims; a second gives up, names the group and leaves it standing. That
second reader is not a convenience. Registering a signal handler displaces Go's own
die-on-interrupt, so without one a teardown that wedged would swallow every further Ctrl+C, and the
run could only be ended from another terminal.

For the same reason the interrupt path reclaims the group directly rather than running `destroy`.
`destroy` takes the campaign lock, and an interrupt arrives while `create` is still holding it. The
lock is per open file description, so asking for it again would block the process against its own
hold, with no signal left to break it.

Neither path covers SIGKILL, and recovery from that one is:

```bash
cs-sandbox group ls          # find the group the run left
cs-sandbox group rm <group> -f
```

The live tier also refuses to start unless the run was asked for, and the make targets above are
what ask. Running `go test -tags integration` by hand skips instead, and the skip message names the
variable to set. That guard exists because absent credentials are not a safety mechanism: the host
most likely to run this tier is the one that holds every login.

Pipe a live run to a file rather than to `tail`. `make test-integration 2>&1 | tail -40` shows
nothing until the pipe closes, so a monitor reporting zero bytes while machines are live looks
exactly like a suite that never started.

#### What the tiers prove
The unit tier fakes `cs-sandbox` rather than the protocol: the same `internal/protocol` code that
runs in production computes state in the tests.

The smoke tier is what makes the whole protocol testable without a provider. A record-and-replay
proxy joins the campaign's own network under a fixed alias, and each member's adapter is aimed at
it through the profile's `env` block. A recording is bound to the profile that produced it, because
the campaign ID is that profile's digest and the names derived from it reach the wire inside
tool-call arguments.

**An adapter is replayable when something the operator controls can point it at a proxy.** A
variable the adapter reads is the simplest form of that. An adapter that takes a provider
declaration instead accepts the same thing as a per-invocation override, which the layer launching
it builds from the same variable. Every supported adapter is reachable one way or the other.


#### The example campaign

`testdata/example-campaign/` is the smallest complete campaign this repository carries: a profile,
a mission and one brief per member. It allocates nothing, and `make check` validates it, so an
example a reader copies stays true.

```console
$ cs-campaign validate --profile testdata/example-campaign/profile.yaml
valid CampaignProfile 2544b277c598
mission e6802c6e662f, 2 role briefs
```

#### Doc claims are tests
`internal/cli/docclaims_test.go` encodes promises the documents make as assertions. Corrupt state
is surfaced rather than skipped, so `ls` names a record it could not parse instead of reporting a
smaller, healthier team. Each test names the document and the sentence it restates.

This is the cheapest defence against documentation drift there is. An agent reviewing docs quarterly
finds rot after the fact; a claim test fails on the commit that causes it.

#### Coverage
Every test target writes coverage into its own tier under `.coverage/`, so running several
aggregates rather than overwrites. `make coverage` merges what is there and prints the report.

`make coverage-check` runs inside `make check` and in CI. It fails when a package
`.coverage-baseline` lists stops being reached: presence, never a percentage. What it catches is a
suite that stopped running while the tests still report green. When a package is meant to lose its
coverage, re-run `make coverage-baseline` and commit the result.

The baseline records the unit tier alone today. CI runs the smoke tier on every scenario, but what
that tier reaches still depends on the host: it skips entirely without `/dev/kvm`, podman, `cs-vcr`
or a cassette. A baseline recorded from a host that ran it would fail every host that cannot, which
is the opposite of what this gate is for.

---

## 8. Conformance

An implementation conforms when it satisfies R1 to R124 and can demonstrate each of the following
by test.

**Isolation.** Two concurrent campaigns cannot resolve or connect to one another by name or raw
address, and neither one's SSH trust material authenticates to the other's members. A solo agent
cannot authenticate over SSH to the orchestrator, to siblings or to the host, and has no network
path to another campaign. A solo agent can reach a deliberately exposed application port within its
own campaign without gaining shell control. No member accesses the host container socket, and no
member receives ambient source-hosting, host SSH, cloud or credential-helper identity. Destroying
one campaign leaves another fully operational.

**Lifecycle.** Destroying a campaign reclaims its group, so no network, key pair, gateway, gateway
port, tap prefix or fabric directory outlives the campaign that created it. A failed host command
can resume creation or destruction without losing ownership information. A campaign can run for
days and recover after a stopped or restarted member.

**Addressing.** A campaign command never resolves a member through a bare sandbox name on the host
plane, and the orchestrator never receives a qualified reference for an in-group peer. An
orchestrator can drive only the agents listed in its own campaign.

**Dispatch.** Dispatch completion is satisfied by exactly one thing: that dispatch's own reply
artifact. Node state is computed on demand and never stored. A message delivery refuses to
overwrite an existing message name. Infrastructure health, model activity and task completion
remain separately observable. A working member is never reported as failed on a watcher's verdict
alone, and a freshly sent message never reads as stopped inside its settling window.

**Configuration.** Profile and flag inputs resolve to the same typed plan. `validate` and `plan`
perform no mutations, unknown overrides fail, and an archived resolved profile contains no secret
values. Adapter selection honours declared role capabilities, a heterogeneous team is expressible,
and homogeneous shorthand produces deterministic names.

**The member contract.** Every declared member has a written purpose and the campaign has a
mission, both resolved before anything is allocated. No briefing is silently substituted. A
member's knowable universe is exactly its trees, its seeded input and its dispatches. Every member
restates its own job before the campaign is usable, the restatement is checked for structure and
never graded for content, and skipping the check is recorded. A member reports its own missing
inputs. A member whose session is reset is returned to its standing context before it is asked to
work again. Every campaign ends in exactly one exit state, and all but `campaign-met` name what
remains unmet.

**Evidence.** Every member has the same four-channel model. Visibility grants differ between
orchestrator and agents, and an explicitly handed-off artifact does not expose the orchestrator's
other files. Archives preserve inputs, outputs, non-secret transcripts and source metadata for
every member in one layout, and include no credentials. Agent branches can be fetched into the
orchestrator's integration clone, and any branch to the host, without a remote push. The campaign
gateway is the single entrance to member services, and a tunnel through it exposes only the
requested service.

---

## 9. Quality attributes

```
[QA-01] Blast radius: cross-campaign reachability = 0
  Measured by: two concurrent campaigns; every name and address probed both ways
  Classification: BEHAVIORAL

[QA-02] Resource reclamation: host artifacts outliving a destroyed campaign = 0
  Measured by: group listing, network listing, gateway port range and tap prefix after destroy
  Classification: BEHAVIORAL

[QA-03] Dispatch state durability: state lost to a host restart = 0
  Measured by: computing every node's state after the host record is deleted and restored
  Classification: BEHAVIORAL

[QA-04] Harvest completeness: committed member work unreachable after archive = 0
  Measured by: fetching every member branch from the archive and diffing against base
  Classification: BEHAVIORAL

[QA-05] Readback coverage: members entering a usable campaign without a restatement = 0
  Measured by: create against a team with one member's brief removed; create must refuse
  Classification: BEHAVIORAL

[QA-06] Credential containment: secret values in state, plans or archives = 0
  Measured by: scanning a completed campaign's record and archive for every granted value
  Classification: BEHAVIORAL

[QA-07] Doc-claim drift: documented promises with no asserting test = 0
  Measured by: the doc-claim tier, which names the document and sentence each test restates
  Classification: MAINTAINABILITY

[QA-08] Observation cost: one look at a campaign = one probe per node
  Measured by: counting transport invocations for a single `observe`
  Classification: PERFORMANCE
```

---

## 10. The hard limits

**Provenance is advisory.** The orchestrator is the authorized administrator of its agents and can
open a shell in any of them. An archive preserves what was observed and cannot prove the
orchestrator never edited an agent-owned file. Making it tamper-proof needs a forced-command
channel the orchestrator cannot step outside, which is a different trust model rather than a
tighter version of this one.

**A model's judgement is not checkable here.** The product verifies that a reply exists, that it
carries an outcome, and that a tree differs from its base. Whether the work is right is a question
no amount of protocol answers.

**The team is fixed at create.** An orchestrator that discovers it needs a fourth agent cannot
have one. Allowing it would put team composition and cost inside the loop the product exists to
bound.

**Recovery cannot distinguish a wedged model from a slow one.** The ladder trades a bounded number
of wasted continues against never rescuing a stopped node. The numbers are the operator's, and no
setting removes the trade.

**A campaign costs real money for as long as it runs.** Nothing here caps spend. `deadline` bounds
the orchestrator's judgement rather than the bill.

---

## 11. Open questions

1. **The elapsed bound is per dispatch, not per campaign.** A campaign that opens many short
   dispatches can outlive any `elapsedSeconds` setting. Whether a campaign-level bound belongs in
   the machine or stays the operator's job is undecided.
2. **`--json` covers two commands.** `ls` and `observe` emit machine-readable output on request,
   and `plan` always prints the campaign record as JSON. `audit` and `doctor` print for a human
   only, so an automated caller reading either is reading text that may change.
3. **The acceptance record has no schema check.** `accepted` entries are ordinary log lines, and a
   malformed one is ignored rather than reported.
4. **Readback structure is checked, not its coverage.** A member can satisfy the structural check
   with a restatement that omits most of its brief, and only a human reading it would notice.
5. **The pin covers the host's tools, and each member's harness is checked separately.** Nothing
   yet reconciles the two into one verdict an operator can read at a glance.
6. **Adapter capability declarations are not enforced against the installed CLI.** An adapter that
   claims a capability its CLI has lost fails at the first turn rather than at create.
7. **Nothing bounds an archive's size.** A long campaign's transcripts can be large, and collection
   has no budget or sampling policy.
8. **The behaviour map has no floor.** Unproven cells render as gaps and nothing fails, so the map
   records the backlog rather than gating it.
9. **R71 is stated and not met.** The team audit asks whether a member's declared-CLI evidence
   stream is empty, which is presence rather than magnitude. A member that failed every turn still
   leaves the files its CLI writes on start-up, and passes.
