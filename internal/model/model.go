package model

import (
	"slices"
	"time"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

const APIVersion = "codesweep.ai/v1alpha1"

// AdapterCLIs is the canonical list of supported agent CLIs, in display
// order. Profile validation, tool routing, and the coverage matrix all derive
// from this single slice, so a new adapter grows every surface at once.
var AdapterCLIs = []string{"claude", "codex", "opencode"}

// Roles are the member roles a campaign supports, in display order.
var Roles = []string{"orchestrator", "agent"}

// ValidAdapterCLI reports whether cli is a supported adapter.
func ValidAdapterCLI(cli string) bool { return slices.Contains(AdapterCLIs, cli) }

type Resources struct {
	CPUS      int `yaml:"cpus,omitempty" json:"cpus,omitempty"`
	MemoryMiB int `yaml:"memoryMiB,omitempty" json:"memoryMiB,omitempty"`
}
type Repo struct {
	Path           string `yaml:"path" json:"path"`
	Ref            string `yaml:"ref,omitempty" json:"ref,omitempty"`
	Name           string `yaml:"name,omitempty" json:"name,omitempty"`
	ResolvedCommit string `yaml:"-" json:"resolvedCommit,omitempty"`
	Initialize     bool   `yaml:"-" json:"initialize,omitempty"`
}
type Snapshot struct {
	Path string `yaml:"path" json:"path"`
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
}
type Auth struct {
	APIKeyFromEnv     []string `yaml:"apiKeyFromEnv,omitempty" json:"apiKeyFromEnv,omitempty"`
	InheritAgentLogin []string `yaml:"inheritAgentLogin,omitempty" json:"inheritAgentLogin,omitempty"`
}

// Model and Effort are the cost/capability lever, declared rather than injected
// by hand after create. Both are pass-through strings handed to the member's own
// adapter, never a shared vocabulary: "high" on one CLI is not "high" on another,
// and each CLI's effort set is open (codex alone carries a Custom variant), so a
// normalised ladder would invent a cross-CLI equivalence that does not exist.
// Empty means "whatever the CLI defaults to" — which is a moving target, and
// exactly why the resolved values are recorded on the Member.
type MemberProfile struct {
	CLI string `yaml:"cli" json:"cli"`
	// Policy carries this seat's own stall threshold — how the orchestrator
	// gets a longer one than its agents. Nothing else belongs here: every
	// other number governs the whole campaign, and validation refuses a
	// member that declares one (protocol.Policy.CampaignOnlyFields).
	Policy    protocol.Policy `yaml:"policy,omitempty" json:"policy,omitzero"`
	Model     string          `yaml:"model,omitempty" json:"model,omitempty"`
	Effort    string          `yaml:"effort,omitempty" json:"effort,omitempty"`
	Repos     []Repo          `yaml:"repos,omitempty" json:"repos,omitempty"`
	Snapshots []Snapshot      `yaml:"snapshots,omitempty" json:"snapshots,omitempty"`
	Auth      Auth            `yaml:"auth,omitempty" json:"auth,omitzero"`
	Resources Resources       `yaml:"resources,omitempty" json:"resources,omitzero"`
	// Env is injected into the member's sandbox as KEY=VALUE, or as a bare KEY
	// to inherit the host's value. It exists for the settings a member's CLI
	// reads from its environment and the profile has no field for — pointing an
	// agent at a recording proxy with ANTHROPIC_BASE_URL, for one.
	//
	// Credentials do not belong here: Auth carries those, by reference, so that
	// no value reaches campaign state or a host command line. A bare KEY is the
	// form to reach for when the value is sensitive.
	Env []string `yaml:"env,omitempty" json:"env,omitempty"`
}
type Defaults struct {
	Engine string `yaml:"engine,omitempty" json:"engine,omitempty"`
	// Policy is every number the dispatch machine runs on; unset fields fall
	// back to compiled-in defaults, and the resolved values are recorded on
	// the campaign and in every member.json.
	Policy protocol.Policy `yaml:"policy,omitempty" json:"policy,omitzero"`
	// Deadline is the campaign's wall clock as a Go duration from create
	// ("90m", "6h"). When set, an unset policy.elapsedSeconds derives from it —
	// the default SPEC.md §6.1 states for the elapsed backstop — and the
	// absolute instant is recorded on the campaign and in every member.json. It does
	// not stop anything by itself: the orchestrator's judgment enforces the
	// deadline; the machine only uses it as the recovery bound.
	Deadline  string    `yaml:"deadline,omitempty" json:"deadline,omitempty"`
	Resources Resources `yaml:"resources,omitempty" json:"resources,omitzero"`
	// Env applies to every member that does not set its own.
	Env []string `yaml:"env,omitempty" json:"env,omitempty"`
}
type Profile struct {
	APIVersion   string                   `yaml:"apiVersion" json:"apiVersion"`
	Kind         string                   `yaml:"kind" json:"kind"`
	Defaults     Defaults                 `yaml:"defaults,omitempty" json:"defaults,omitzero"`
	Orchestrator MemberProfile            `yaml:"orchestrator" json:"orchestrator"`
	Agents       map[string]MemberProfile `yaml:"agents" json:"agents"`
}

// A member has two addresses, and they are not interchangeable.
//
// Sandbox is the bare name: the guest hostname and the in-group DNS alias. It
// is how one member reaches another from inside the campaign network, and the
// only form that works there — a guest's generated ssh client config matches
// "Host * !*.*", so a dotted reference misses the tier key entirely.
//
// Ref is <sandbox>.<group>: the host-global reference every cs-sandbox command
// and every host-side cs-<cli>-remote -H takes. Sandbox names are unique per
// group, not per host, so the host plane must always qualify.
type Member struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	CLI     string `json:"cli"`
	Sandbox string `json:"sandbox"`
	Ref     string `json:"ref"`
	// IP is the member's address on the campaign network. Recorded because the
	// group's gateway reaches members by address, not by name: a Firecracker
	// member is not in the resolver the gateway container uses.
	IP     string `json:"ip,omitempty"`
	Branch string `json:"branch,omitempty"`
	Solo   bool   `json:"solo"`
	// Model and Effort are what was actually applied to this member's CLI, as
	// opposed to Profile's declaration. Recorded because the archive otherwise
	// says nothing about the single biggest cost/capability variable: answering
	// "what did that seat run on?" for an earlier run meant unpacking a
	// transcript tarball, and a run whose CLI merely defaulted recorded nothing
	// at all.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	// Harness is the pin verdict for the tools THIS member runs, recorded for
	// the same reason as Model/Effort: an archive that cannot answer "what
	// harness did this seat run on?" leaves the question to be re-derived from
	// timestamps later, which is how this was found. Members receive their
	// tools from the image seed, not from the host's ~/.local/bin the host-side
	// pin verifies, so this is the only record of the plane where work happens.
	Harness *HarnessCheck `json:"harness,omitempty"`
	// SeededInputs maps each operator-authored file placed in this member's
	// input channel to its sha256. Recorded because an archive that shows a
	// member misunderstood its job must be able to answer what it was actually
	// handed — "the brief was wrong" and "the brief never arrived" look
	// identical afterwards otherwise.
	SeededInputs map[string]string `json:"seededInputs,omitempty"`
	// Readback is this member's own restatement of its job, from the readback
	// turn at create. It is the readback's entire product — the evidence that a
	// member understood its briefing rather than merely had files copied near
	// it — and it used to be printed to the operator's scrollback and dropped.
	// The consequence was measured on two live runs: `inspect` held the readback
	// PROMPT and not the answer, so the only surviving copy of what a fleet said
	// it was there to do lived in an undocumented driver log directory.
	Readback *Readback     `json:"readback,omitempty"`
	Session  Session       `json:"session"`
	Profile  MemberProfile `json:"profile"`
	// StallSeconds is the resolved CS_<CLI>_STALL_SECS this member's turn
	// driver runs under, delivered via cs-sandbox create --env.
	StallSeconds int `json:"stallSeconds,omitempty"`
}

// Readback is what a member answers when asked to restate its job. The fields
// are the member's own words: the product checks their structure and never
// grades their content, so this is recorded verbatim and in full.
//
// Recorded whether the readback passed or failed. A failed one is the more
// valuable record — "briefed for the wrong job" is exactly the case where the
// answer needs re-reading afterwards — so Detail carries why it was rejected.
type Readback struct {
	Member      string    `json:"member,omitempty"`
	Role        string    `json:"role,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	Missing     []string  `json:"missing,omitempty"`
	Goal        string    `json:"goal,omitempty"`
	Scope       string    `json:"scope,omitempty"`
	Obligations string    `json:"obligations,omitempty"`
	At          time.Time `json:"at,omitzero"`
	Detail      string    `json:"detail,omitempty"`
}

// Empty reports that nothing was parsed out of the member's turn — a member that
// never answered, or answered something that was not a readback. The member's
// own words are what this record is for, so a shell of one is not worth
// recording: it would be indistinguishable from a readback that ran and said
// nothing.
func (r Readback) Empty() bool {
	return r.Member == "" && r.Role == "" && r.Branch == "" &&
		r.Goal == "" && r.Scope == "" && r.Obligations == "" && len(r.Missing) == 0
}

type Session struct {
	Name string `json:"name"`
}
type Campaign struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	// Group is the campaign's isolation boundary: one cs-sandbox group owning
	// an isolated network, its own SSH trust material and a gateway. Network is
	// derived from it rather than chosen, so the two cannot drift; it stays in
	// state because the design requires the resolved network to be recorded.
	Group   string `json:"group"`
	Network string `json:"network"`
	// Gateway is the host port publishing the campaign group's SSH jump host.
	// One stable entrance for the campaign's whole life: inside it members
	// resolve over the group's own DNS, so `ssh -L <local>:<member>:<port>
	// <group>-gw` reaches any member's service by name — no per-member forward,
	// and no hand-assigned host port per member to keep distinct.
	Gateway int    `json:"gateway,omitempty"`
	Engine  string `json:"engine"`
	// Provisioning checkpoints create alone ("creating", "create-failed",
	// "" once complete). It is never a campaign lifecycle: the campaign runs
	// for exactly as long as the mission dispatch is open, which is computed.
	Provisioning  string    `json:"provisioning,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	ProfilePath   string    `json:"profilePath,omitempty"`
	ProfileDigest string    `json:"profileDigest,omitempty"`
	Overrides     []string  `json:"overrides,omitempty"`
	// Upstream records the pin verdict at create time, so every
	// campaign carries proof of the surface it was built on — including a
	// deliberately accepted deviation, which must be auditable, never silent.
	Upstream *UpstreamCheck `json:"upstream,omitempty"`
	// Policy is the resolved dispatch-machine policy this campaign runs on —
	// profile overrides applied over compiled-in defaults, recorded so the
	// archive answers "what numbers did this run use".
	Policy protocol.Policy `json:"policy"`
	// Deadline is the absolute instant defaults.deadline resolved to at
	// create; zero when the profile declared none.
	Deadline time.Time `json:"deadline,omitzero"`
	Members  []Member  `json:"members"`
}

// HarnessCheck is one member's upstream tool surface, measured inside the
// member. Tools maps tool name -> sha256 of the file that would actually
// execute (following the move-aside for guarded tools). Deviations is empty
// when the member runs the tools cs-sandbox says it ships.
type HarnessCheck struct {
	CheckedAt  time.Time         `json:"checkedAt"`
	Tools      map[string]string `json:"tools,omitempty"`
	Deviations []string          `json:"deviations,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// UpstreamCheck is the host surface as create found it, against the go.mod
// embedded in the cs-campaign that built this campaign.
//
// There is no "pinned" flag: a built binary always carries its own manifest, so
// there is always something to compare against, and the un-validated state the
// flag used to describe cannot occur. Notes are the non-fatal findings (a
// sibling tool at another version, or absent); Deviations are what refused the
// create unless Accepted records that an operator overrode it.
type UpstreamCheck struct {
	CheckedAt      time.Time `json:"checkedAt"`
	SandboxVersion string    `json:"sandboxVersion,omitempty"`
	Deviations     []string  `json:"deviations,omitempty"`
	Notes          []string  `json:"notes,omitempty"`
	Accepted       bool      `json:"accepted,omitempty"`
}

// Sandbox is one row of `cs-sandbox ls --json`. Ref is the only field safe to
// key on: identity is (group, name), so Name alone repeats across groups and a
// map built from it silently keeps whichever row was decoded last.
type Sandbox struct {
	Ref     string `json:"ref"`
	Name    string `json:"name"`
	Group   string `json:"group"`
	Status  string `json:"status"`
	Type    string `json:"type"`
	Engine  string `json:"engine"`
	Solo    bool   `json:"solo"`
	Yolo    bool   `json:"yolo"`
	Created string `json:"created,omitempty"`
	Network string `json:"network"`
}
