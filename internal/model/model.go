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

// CredentialLend and CredentialInherit are the two verbs, and they are
// cs-sandbox's own: lending hands the sandbox a loan token and keeps the
// credential on the host, and inheriting copies the credential in. Lending is
// the default, because a member that never holds the value cannot leak,
// refresh or revoke it, and destroying the sandbox ends the loan.
const (
	CredentialLend    = "lend"
	CredentialInherit = "inherit"
)

// CredentialVerbs are how a credential reaches a member, in display order.
var CredentialVerbs = []string{CredentialLend, CredentialInherit}

// APIKeyProviders are the providers whose key this host keeps for cs-sandbox to
// lend or copy, in display order.
var APIKeyProviders = []string{"anthropic", "openai", "fireworks"}

// LendableAgentLogin reports whether a CLI family has a login that can be lent.
// OpenCode has none: it authenticates from a provider key, so a member running
// it takes apiKey or apiKeyFromEnv instead.
func LendableAgentLogin(cli string) bool { return cli == "claude" || cli == "codex" }

// ValidCredentialVerb reports whether verb is one of them.
func ValidCredentialVerb(verb string) bool { return slices.Contains(CredentialVerbs, verb) }

// ValidAPIKeyProvider reports whether provider is a supported key provider.
func ValidAPIKeyProvider(provider string) bool { return slices.Contains(APIKeyProviders, provider) }

// Auth is a member's model credentials, held by reference.
//
// A grant names what the member is given. The SPELLING says how it arrives:
// the neutral spellings take the campaign's verb, and the fused ones are
// cs-sandbox's own flag names, stating the verb for this member.
//
//	agentLogin: [claude]           the campaign's verb, whatever it is
//	lendAgentLogin: [claude]       --lend-agent-login claude
//	inheritAgentLogin: [claude]    --inherit-agent-login claude
//
// A member speaks one verb. Mixing a lend spelling with an inherit one, or a
// neutral spelling with a fused one, declares two modes for a seat that can
// only have one, and validation refuses it.
//
// One credential is granted, never a blend: the first apiKeyFromEnv variable
// actually set, else the key grant, else the login grant. A key displaces a
// login because an agent that finds a key in its environment spends that
// instead of the subscription it was signed into, and the member would then
// run on a credential its profile did not name.
type Auth struct {
	// APIKeyFromEnv names host environment variables, and the first one
	// actually set is granted. It takes no verb: the lender reads a file this
	// host keeps, not the environment of whoever ran create.
	APIKeyFromEnv []string `yaml:"apiKeyFromEnv,omitempty" json:"apiKeyFromEnv,omitempty"`

	// The key grant, in its three spellings. Values are APIKeyProviders.
	APIKey        []string `yaml:"apiKey,omitempty" json:"apiKey,omitempty"`
	LendAPIKey    []string `yaml:"lendApiKey,omitempty" json:"lendApiKey,omitempty"`
	InheritAPIKey []string `yaml:"inheritApiKey,omitempty" json:"inheritApiKey,omitempty"`

	// The login grant, in its three spellings. Values are AdapterCLIs.
	AgentLogin        []string `yaml:"agentLogin,omitempty" json:"agentLogin,omitempty"`
	LendAgentLogin    []string `yaml:"lendAgentLogin,omitempty" json:"lendAgentLogin,omitempty"`
	InheritAgentLogin []string `yaml:"inheritAgentLogin,omitempty" json:"inheritAgentLogin,omitempty"`

	// Credentials is the verb this seat resolved to, recorded so the member
	// record says how it was actually granted. Resolved rather than declared:
	// a member states its verb by spelling a grant, and accepting one here
	// would be a third way to say the same thing.
	Credentials string `yaml:"-" json:"credentials,omitempty"`
}

// APIKeys and AgentLogins are a member's grants, however they were spelled.
// At most one spelling of each is populated on a validated profile.
func (a Auth) APIKeys() []string {
	return slices.Concat(a.APIKey, a.LendAPIKey, a.InheritAPIKey)
}
func (a Auth) AgentLogins() []string {
	return slices.Concat(a.AgentLogin, a.LendAgentLogin, a.InheritAgentLogin)
}

// DeclaredCredentials is the verb this member's own spellings state, empty
// where it uses the neutral ones and takes the campaign's. A member declaring
// both verbs is refused by validation, so lend is reported first here rather
// than guessed at.
func (a Auth) DeclaredCredentials() string {
	switch {
	case len(a.LendAPIKey) > 0 || len(a.LendAgentLogin) > 0:
		return CredentialLend
	case len(a.InheritAPIKey) > 0 || len(a.InheritAgentLogin) > 0:
		return CredentialInherit
	}
	return ""
}

// Spellings reports which of the three forms this member used, which is what
// the one-verb-per-member rule is checked against.
func (a Auth) Spellings() (neutral, lend, inherit bool) {
	return len(a.APIKey) > 0 || len(a.AgentLogin) > 0,
		len(a.LendAPIKey) > 0 || len(a.LendAgentLogin) > 0,
		len(a.InheritAPIKey) > 0 || len(a.InheritAgentLogin) > 0
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
	// Credentials is the campaign's verb, lend or inherit, for every member
	// that does not spell one of its own. Unset means lend.
	Credentials string `yaml:"credentials,omitempty" json:"credentials,omitempty"`
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
	// Harness is the verdict for the tools THIS member runs, recorded for the
	// same reason as Model/Effort: an archive that cannot answer "what harness
	// did this seat run on?" leaves the question to be re-derived from
	// timestamps later, which is how this was found. Members receive their
	// tools from the image seed, not from the host's ~/.local/bin the upstream
	// check verifies, so this is the only record of the plane where work
	// happens.
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
	//
	// The gateway is still the campaign's one entrance, and it is not recorded
	// here because there is nothing to record: cs-sandbox binds it to no host
	// port, so it is reached as `ssh <group>-gw` through the engine's own
	// channel. Inside it members resolve over the group's own DNS, which is
	// what makes one entrance enough for a whole fleet.
	Group   string `json:"group"`
	Network string `json:"network"`
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
// flag used to describe cannot occur. Deviations are what refused the create
// unless Accepted records that an operator overrode it. Warnings are true
// findings that refuse nothing, such as a sibling tool at a version this build
// does not name. Notes are the good news, a matching or absent sibling
// included, recorded so the archive can say what the host actually held.
type UpstreamCheck struct {
	CheckedAt      time.Time `json:"checkedAt"`
	SandboxVersion string    `json:"sandboxVersion,omitempty"`
	Deviations     []string  `json:"deviations,omitempty"`
	Warnings       []string  `json:"warnings,omitempty"`
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
