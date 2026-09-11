//go:build integration || smoke

package cli

// The driver both live tiers share: one small campaign, run end to end, with
// the backend it runs on chosen by a scenario. `integration` drives real
// providers; `smoke` drives the same code against a recorded cassette.
//
// It lives in package cli rather than in a test/ tree so the commands run in
// process. That is what makes a live run count toward this repository's
// coverage, and it is the only way an assertion can read a computed
// observation rather than parse the table it prints.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
	"github.com/codesweep-ai/campaign/internal/store"
)

// scenario is one fleet on one backend, signed in one way.
//
// The fleet is homogeneous — orchestrator and agent on the same adapter —
// because the matrix exists to answer "does this backend drive a campaign in
// either role", and a mixed fleet answers that for neither. Heterogeneity has
// its own test, since it is a separate promise (SPEC.md R82).
type scenario struct {
	// name is the subtest, and the cassette when this scenario is recorded.
	name string
	// cli is the adapter every member of this fleet runs.
	cli string
	// auth says how the fleet signs in, for the skip and failure messages.
	auth string
	// model is pinned. A member that takes its adapter's default sends a
	// different request the day that default moves, which is a cassette that
	// stops matching for a reason no diff explains.
	model string
	// effort is passed through to the adapter, empty where it has none.
	effort string
	// login is the adapter family whose host login this fleet signs in with,
	// and keyProvider the provider whose host-held key it spends. Exactly one
	// is set: a member is granted one credential.
	login, keyProvider string
	// orch describes the orchestrator when it differs from the agent, and is
	// nil for the homogeneous fleets that are the rest of this matrix.
	orch *seat
	// agentMemoryMiB overrides the campaign default for the agent alone.
	agentMemoryMiB int
	// verb is how that credential reaches the members. Empty is the product
	// default, which means the profile spells no verb at all and the grant is
	// lent — the state most of this matrix is in, deliberately, because the
	// default is the thing worth recording. model.CredentialInherit spells the
	// fused inherit form and puts the credential inside the members.
	//
	// It decides the whole shape of the run, because the two paths reach the
	// provider differently. See vcrPlacement.
	verb string
	// baseURLEnv is the base-URL variable this adapter is aimed with, and the
	// one thing that decides whether a scenario can be recorded. It is the
	// agent's own name rather than a project-specific one: claude and opencode
	// read theirs directly, and cs-codex builds codex a provider declaration
	// out of OPENAI_BASE_URL, since codex reads no such variable itself.
	//
	// A member reaches its proxy through the profile's env block, so nothing
	// here needs to know which mechanism the wrapper used.
	baseURLEnv string
	// urlSuffix is what this client appends to the base URL it is given, and it
	// belongs to a scenario the GUEST aims at the recorder.
	//
	// A lent scenario must leave it empty. The lender composes the upstream
	// path as joinPath(origin, ensureVersion(slot.Version, guestPath)), and
	// every slot but the Codex subscription carries a version of its own — so
	// an origin that also ends in /v1 produces /v1/v1 and a request no
	// provider recognises. Measured: codex-api-key recorded 48 requests that
	// cs-vcr classified as "surface unknown", each one an empty response and a
	// member that gave up in fifteen seconds. TestScenarioProfilesSpellTheir-
	// Credential holds the rule so the next one costs nothing.
	urlSuffix string
	// vcrProvider is the cs-vcr entry this scenario's base URL names, and the
	// prefix carries it: /c/<provider>/<cassette>. It is a key the deployment
	// chooses, so it may name the endpoint rather than the wire protocol.
	//
	// vcrUpstream is where that entry points, which is the recording half's
	// business alone. The two differ per scenario in a way the client's own
	// surface does not reveal: codex on a subscription talks to the ChatGPT
	// backend rather than the API, and the same client with a key talks to the
	// API. Replay reads neither — a cassette is served without ever resolving
	// where it came from, and the provider segment goes unread.
	vcrProvider, vcrUpstream string
}

// scenarios is the matrix. Every combination is listed whether or not this
// host can sign in for it: a scenario that skips says which credential is
// missing, and that is the only way a contributor learns what one more login
// would cover.
func scenarios() []scenario {
	return []scenario{
		{
			name: "claude-subscription", cli: "claude",
			auth:  "a Claude Pro/Max subscription on this host",
			model: "claude-sonnet-5", login: "claude",
			baseURLEnv:  "ANTHROPIC_BASE_URL",
			vcrProvider: "anthropic", vcrUpstream: "https://api.anthropic.com",
		},
		{
			// The same adapter as the scenario above, signed in the other way.
			// Claude Code takes an API key in a header where a subscription
			// takes OAuth, so the two send different requests and only running
			// both covers the pair.
			name: "claude-api-key", cli: "claude",
			auth:  "an Anthropic API key in ~/.cs-keys/anthropic",
			model: "claude-sonnet-5", keyProvider: "anthropic",
			baseURLEnv:  "ANTHROPIC_BASE_URL",
			vcrProvider: "anthropic", vcrUpstream: "https://api.anthropic.com",
		},
		{
			// No /v1: codex's subscription path authenticates as itself rather
			// than with a header, and its provider takes the bare base URL.
			name: "codex-subscription", cli: "codex",
			auth:  "a ChatGPT subscription on this host",
			model: "gpt-5.6-sol", effort: "medium", login: "codex",
			baseURLEnv:  "OPENAI_BASE_URL",
			vcrProvider: "chatgpt", vcrUpstream: "https://chatgpt.com/backend-api/codex",
		},
		{
			name: "codex-api-key", cli: "codex",
			auth:  "an OpenAI API key in ~/.cs-keys/openai",
			model: "gpt-5.6-sol", effort: "medium", keyProvider: "openai",
			baseURLEnv:  "OPENAI_BASE_URL",
			vcrProvider: "openai", vcrUpstream: "https://api.openai.com",
		},
		{
			// The third way an adapter takes a base URL, and the reason the
			// field is named after the agent rather than the provider.
			// OpenCode's base URL is per provider: only its openai and
			// anthropic providers read a standard variable, so a model on the
			// fireworks provider ignores OPENAI_BASE_URL entirely (measured —
			// a recording made with it set ran a whole campaign against the
			// real provider while the proxy sat idle). What works for every
			// provider is a baseURL in opencode's own config, so cs-opencode
			// derives the provider from the pinned model and writes that
			// config inline from OPENCODE_BASE_URL.
			//
			// Fireworks speaks the OpenAI wire protocol, and the entry is named
			// for the endpoint rather than for the protocol.
			// The one mixed fleet in the matrix, and it is a memory budget
			// rather than a preference. OpenCode is the heaviest adapter here:
			// it needs 2 GiB to get a turn started, and two of it does not fit
			// a hosted runner alongside the campaign's own processes. So it
			// runs where it is being tested — as the agent — under a Claude
			// orchestrator that fits in the campaign default.
			//
			// The cost is real and deliberate: this scenario no longer answers
			// "does opencode drive a campaign as the orchestrator". That half
			// is what the mixed-fleet test covers (SPEC.md R82).
			name: "opencode-fireworks", cli: "opencode",
			auth:  "a Fireworks API key in ~/.cs-keys/fireworks",
			model: "fireworks-ai/accounts/fireworks/models/kimi-k3", effort: "high",
			keyProvider: "fireworks", agentMemoryMiB: 2048,
			baseURLEnv:  "OPENCODE_BASE_URL",
			vcrProvider: "fireworks", vcrUpstream: "https://api.fireworks.ai/inference",
			orch: &seat{
				cli: "claude", model: "claude-sonnet-5", login: "claude",
				baseURLEnv:  "ANTHROPIC_BASE_URL",
				vcrProvider: "anthropic", vcrUpstream: "https://api.anthropic.com",
			},
		},
		{
			// The one scenario that puts a credential inside its members, so
			// the path an operator opts into is recorded rather than only
			// unit-tested. Claude because it is the adapter whose login is most
			// often inherited, and because the fabricated-credential trick the
			// replay half turns on is best understood on it.
			name: "claude-inherit", cli: "claude",
			auth:  "a Claude Pro/Max subscription on this host",
			model: "claude-sonnet-5", login: "claude", verb: model.CredentialInherit,
			baseURLEnv:  "ANTHROPIC_BASE_URL",
			vcrProvider: "anthropic", vcrUpstream: "https://api.anthropic.com",
		},
	}
}

// lends reports whether this scenario's members hold a loan token rather than
// the credential itself, which is what decides where cs-vcr has to sit.
func (s scenario) lends() bool { return s.verb != model.CredentialInherit }

// seat is one member's half of a scenario: the adapter it runs, the credential
// it spends, and the recorder entry its traffic is addressed to.
//
// A homogeneous scenario has one, taken by both members. Only a fleet whose two
// seats differ needs the distinction, and exactly one does: opencode is the
// heaviest adapter and two of it will not fit a hosted runner, so it runs as
// the agent under a lighter orchestrator.
type seat struct {
	cli, model, effort       string
	login, keyProvider       string
	baseURLEnv, urlSuffix    string
	vcrProvider, vcrUpstream string
	// memoryMiB overrides the campaign default for this member alone, for an
	// adapter that does not fit in it. Zero takes the default.
	memoryMiB int
}

// agentSeat is what the scenario's own fields describe, which is the agent.
func (s scenario) agentSeat() seat {
	return seat{
		cli: s.cli, model: s.model, effort: s.effort,
		login: s.login, keyProvider: s.keyProvider,
		baseURLEnv: s.baseURLEnv, urlSuffix: s.urlSuffix,
		vcrProvider: s.vcrProvider, vcrUpstream: s.vcrUpstream,
		memoryMiB: s.agentMemoryMiB,
	}
}

// orchSeat is the orchestrator's, which mirrors the agent's unless the scenario
// names its own.
func (s scenario) orchSeat() seat {
	if s.orch != nil {
		return *s.orch
	}
	return s.agentSeat()
}

// seats is both, orchestrator first. A homogeneous scenario yields the same one
// twice, which every caller here is happy to see.
func (s scenario) seats() []seat { return []seat{s.orchSeat(), s.agentSeat()} }

// clis is the distinct adapters this scenario runs, for the checks that are
// per-adapter rather than per-member.
func (s scenario) clis() []string {
	out := []string{s.orchSeat().cli}
	if agent := s.agentSeat().cli; agent != out[0] {
		out = append(out, agent)
	}
	return out
}

// TestWorkflowRunsEveryScenario holds CI's matrices against this file's.
//
// The tier is one job per scenario, so the workflow names them one by one and
// a scenario added here would otherwise just stop being run there — silently,
// because a leg that does not exist reports nothing at all. Cheap to check and
// impossible to notice by eye.
//
// Every matrix, not one: the tier runs three times over — firecracker and
// podman on hosted runners, podman again on the self-hosted Mac — and a
// scenario added to one list and not the others is the same silence in a
// smaller place.
func TestWorkflowRunsEveryScenario(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Skip("repo root not found")
	}
	path := filepath.Join(root, ".github", "workflows", "ci.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no workflow to check: %v", err)
	}
	// Each `scenario:` key in the file, with the line it is on, so a mismatch
	// says which of the three matrices is short.
	type matrix struct {
		line   int
		listed []string
	}
	var matrices []matrix
	open := -1
	for i, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "scenario:":
			matrices = append(matrices, matrix{line: i + 1})
			open = len(matrices) - 1
		case open >= 0 && strings.HasPrefix(trimmed, "- "):
			matrices[open].listed = append(matrices[open].listed, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
		case open >= 0 && trimmed != "" && !strings.HasPrefix(trimmed, "#"):
			open = -1
		}
	}
	var want []string
	for _, sc := range scenarios() {
		want = append(want, sc.name)
	}
	slices.Sort(want)
	if len(matrices) == 0 {
		t.Fatalf("%s runs no scenario matrix at all, so nothing in it replays a cassette", path)
	}
	for _, m := range matrices {
		listed := slices.Clone(m.listed)
		slices.Sort(listed)
		if !slices.Equal(listed, want) {
			t.Errorf("the smoke matrix at ci.yml:%d runs %v, but scenarios() defines %v.\n"+
				"A scenario missing from the workflow is never replayed in CI, and its leg does not exist to say so.", m.line, listed, want)
		}
	}
}

// TestScenarioProfilesSpellTheirCredential checks the matrix without booting
// anything: every scenario's generated profile is one `create` would accept,
// and it grants the credential the scenario says it does, by the verb it says.
//
// Cheap on purpose. The tier below this one costs two microVMs and real model
// turns per scenario, and a matrix that names a provider cs-sandbox does not
// lend is a refusal six campaigns into a recording run.
func TestScenarioProfilesSpellTheirCredential(t *testing.T) {
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			body := scenarioProfile(sc, t.TempDir(), vcrBaseURL, replayName(sc))
			path := filepath.Join(t.TempDir(), "profile.yaml")
			writeFileT(t, path, body)
			profile, _, err := readProfile(path)
			if err != nil {
				t.Fatalf("generated profile does not validate: %v\n%s", err, body)
			}
			applyDefaults(&profile)
			for name, m := range map[string]model.MemberProfile{"orchestrator": profile.Orchestrator, "dev": profile.Agents["dev"]} {
				want := model.CredentialLend
				if !sc.lends() {
					want = model.CredentialInherit
				}
				if got := m.Auth.Credentials; got != want {
					t.Errorf("%s resolved to %q, want %q", name, got, want)
				}
				grants := append(m.Auth.APIKeys(), m.Auth.AgentLogins()...)
				if len(grants) != 1 {
					t.Errorf("%s must hold exactly one grant, got %v", name, grants)
				}
				if len(m.Auth.APIKeyEnvs()) != 0 {
					t.Errorf("%s still grants an environment key: %v", name, m.Auth.APIKeyEnvs())
				}
			}
			// One recorder, one alias, both chains: an inheriting member
			// dials it, and a lent member's lender dials it from the same
			// network. Neither names a host port.
			if !strings.Contains(body, vcrBaseURL) {
				t.Errorf("base URL does not name the recorder on the fabric:\n%s", body)
			}
			// And a lent one leaves the API version to the lender, which adds
			// its slot's own. See urlSuffix.
			if sc.lends() && sc.urlSuffix != "" {
				t.Errorf("a lent scenario must not set urlSuffix (%q): the lender adds the version, and an origin carrying one too sends /v1/v1", sc.urlSuffix)
			}
		})
	}
}

// replayName is the campaign name a scenario records and replays under.
//
// Fixed rather than clock-derived, because the campaign ID is the sha256 of the
// resolved profile and the profile carries this name. A name that moved would
// rename the group, both sandboxes, the branches and the sessions, and those
// names reach the wire inside tool-call arguments — which cs-vcr matches
// exactly, and rightly.
//
// Short: the whole composed socket path is bounded at 108 bytes.
func replayName(sc scenario) string {
	switch sc.name {
	case "claude-subscription":
		return "csrclaude"
	case "claude-api-key":
		return "csrclkey"
	case "codex-subscription":
		return "csrcxsub"
	case "codex-api-key":
		return "csrcxkey"
	case "opencode-fireworks":
		return "csrocfw"
	case "claude-inherit":
		return "csrclinh"
	}
	return "csr" + sc.cli
}

// cassetteStore is where the committed cassettes live: one directory per
// scenario, holding one per member underneath, which is the shape cs-vcr
// reads. A scenario's own store keeps a re-recording of one from touching the
// rest.
func cassetteStore(t *testing.T, sc scenario) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "test", "cassettes", sc.name))
	if err != nil {
		t.Fatalf("resolve the cassette store: %v", err)
	}
	return abs
}

// recordingClaimName is the file, at the scenario root rather than inside a
// member directory — `cs-vcr cassette verify` is addressed to member
// directories and hasCassette globs for their index files, so neither sees it.
//
// Shared by the tier that writes it and the tier that reads it, which are
// behind different build tags and would otherwise each spell it out.
const recordingClaimName = "recorded.json"

// sandboxImage is the image reference this run boots, resolved once.
//
// CS_SANDBOX_IMAGE where the caller named one — every Makefile target that
// reaches this code does — and the slim variant otherwise, because every tier
// that touches a cassette boots that one. The name is asked of the binary
// rather than written down: each variant carries the version of the cs-sandbox
// that built it, so only that binary can say what this host would boot.
//
// Best effort. A host with no cs-sandbox on PATH gets "", and every gate
// reading this stands down rather than guessing.
var sandboxImage = sync.OnceValue(func() string {
	if img := os.Getenv("CS_SANDBOX_IMAGE"); img != "" {
		return img
	}
	b, err := exec.Command("cs-sandbox", "version", "--images").Output()
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "image-slim" {
			return f[1]
		}
	}
	return ""
})

// imageVariant reduces an image reference to the one thing a cassette has to
// agree with: "slim" or "shipped".
//
// Not the reference itself, which carries the cs-sandbox version that built it
// and so moves on every bump — a gate on the whole name would fire on every one
// and say nothing the agent-version gate does not already say better. What a
// cassette is bound to is which BINARIES its recorded commands found, and that
// is the variant: the slim image is the shipped one with the developer
// toolchains dropped, so the two disagree about a set of names, and a recorded
// turn that used one of them replays only where it was made.
func imageVariant(ref string) string {
	if ref == "" {
		return ""
	}
	name := ref
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.IndexAny(name, ":@"); i >= 0 {
		name = name[:i]
	}
	if strings.Contains(name, "slim") {
		return "slim"
	}
	return "shipped"
}

// agentCLIVersions asks the sandbox image which version of each agent CLI it
// carries, once per process.
//
// The IMAGE rather than any pin, and the difference is the point: an agent CLI
// is pinned in the sandbox repository and reaches this one only when a new
// image is published and adopted, so a checkout routinely names versions its
// image does not hold. What a cassette was recorded against is what booted.
//
// Best effort. A host with no podman, or one whose image is not built, gets
// nothing back and the gate that reads this stands down — every tier reaching
// for it already needs that image for something larger and owns the message
// when it is missing, so a second report here would only be noise.
var agentCLIVersions = sync.OnceValue(func() map[string]string {
	out := map[string]string{}
	img := sandboxImage()
	if img == "" {
		return out
	}
	const probe = `for a in claude codex opencode; do ` +
		`v=$("$a" --version 2>/dev/null | head -1); printf '%s %s\n' "$a" "$v"; done`
	// --pull=never: the resolved name is what this cs-sandbox WOULD boot, which
	// is not always a published tag, and a probe that reaches the registry to
	// find that out would put a network round trip in front of every replay.
	b, err := exec.Command("podman", "run", "--rm", "--pull=never",
		"--entrypoint", "sh", img, "-c", probe).Output()
	if err != nil {
		return out
	}
	// The first dotted triple on the line. The three say it differently —
	// `2.1.258 (Claude Code)`, `codex-cli 0.152.1`, a bare `1.18.22` — and the
	// number is the shape they agree on.
	semver := regexp.MustCompile(`[0-9]+\.[0-9]+\.[0-9]+`)
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if name, rest, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			out[name] = semver.FindString(rest)
		}
	}
	return out
})

// campaignRun is what one run of the driver produced, for a tier to assert on.
type campaignRun struct {
	name      string
	campaign  *model.Campaign
	verdict   *protocol.Reply
	archive   string
	createOut string
	proxy     *vcrProxy
}

// runOptions is the half of a run that differs between the tiers.
type runOptions struct {
	// baseURL aims every member's adapter at a proxy. Empty means the real
	// provider, which is what the integration tier wants.
	baseURL string
	// ceiling bounds the wait for the mission reply. A replay answers in a
	// fraction of a live run, and a live run should not be held to that.
	ceiling time.Duration
	// fakeKey is set for the replay tier, where the credential must exist and
	// must not be the developer's.
	fakeKey string
	// proxyMode runs a cs-vcr on the campaign's own fabric in "record" or
	// "replay", with cassettes in proxyStore. Empty means no proxy.
	proxyMode, proxyStore string
	// fixedName pins the campaign name, which pins the campaign ID through the
	// profile digest. The record and replay tiers both set it.
	fixedName string
}

// runLiveCampaign drives one homogeneous fleet to its verdict and leaves
// nothing behind.
func runLiveCampaign(t *testing.T, sc scenario, opts runOptions) campaignRun {
	t.Helper()
	ensureMountableTempDir(t)
	ensureLiveGuestBinary(t)
	ensureLiveLenderBinary(t)
	// The replay half authenticates with credentials that authenticate nothing.
	// Every scenario needs them now: a lent one because the LENDER reads the
	// host's credential and swaps it in, and the inheriting one because
	// cs-sandbox copies that same file into its members.
	if opts.fakeKey != "" {
		fabricatedCredentials(t, opts.fakeKey)
	}

	work := t.TempDir()
	repo := seedSubjectRepo(t, work)
	writeCampaignInputs(t, work)

	name := campaignName(sc, opts)
	profilePath := filepath.Join(work, "profile.yaml")
	writeFileT(t, profilePath, scenarioProfile(sc, repo, opts.baseURL, name))

	a := newLiveApp(t, work, name)
	return driveToVerdict(t, a, sc, name, profilePath, filepath.Join(work, "archive"), work, opts)
}

// fabricatedCredentials writes the credential tree a replay authenticates
// with, and points cs-sandbox at it.
//
// A replay must present something. The agents read a credential FILE and will
// not run unattended without one: Claude Code puts up its sign-in screen, which
// cs-claude-turn reports as an authentication failure. So the replay half
// writes the shape each one requires, with values that authenticate nothing.
//
// Both halves of the matrix read this tree, from opposite ends. A LENT scenario
// never puts any of it inside a member: the lender runs on this host, reads
// these files per call, and swaps a loan token for what it finds here before
// forwarding to cs-vcr. An INHERITING one has cs-sandbox copy the login into
// the member, which then presents it itself.
//
// It works because nothing these values reach can refuse them. cs-vcr serves
// the model calls from the cassette, and a lent member's side calls are refused
// by cs-sandbox before they leave. Given an open network the same token fails:
// the OAuth check answers 401 and the agent believes it.
//
// The lender also attaches headers the member never sent — the OAuth beta
// header for Claude, the account id for Codex — so what reaches cs-vcr differs
// between a recording and its replay by more than the token. That is already
// true of the committed codex-subscription cassette, whose account id was real
// when recorded and is zeroes when replayed, and it replays clean: cs-vcr keys
// on none of it.
//
// CS_SANDBOX_AGENT_HOME moves only where a login or a key is READ from.
// Pointing HOME at this tree would take the instance directory and the caches
// with it.
func fabricatedCredentials(t *testing.T, token string) {
	t.Helper()
	home := credentialTree(t, token)
	// Same reason as ensureGuestBinary: one tree for the process, planted by
	// whoever gets here first, and a parallel scenario must not try to plant it
	// again.
	if os.Getenv("CS_SANDBOX_AGENT_HOME") == home {
		return
	}
	t.Setenv("CS_SANDBOX_AGENT_HOME", home)
}

// credentialTree builds the fabricated tree ONCE for the whole process, and
// every scenario is pointed at that same one.
//
// Per-scenario would be the obvious thing and is wrong, though the reason has
// moved. It used to be that the lender was one host daemon shared by every
// scenario, so the second scenario's tree was read by a lender still holding
// the first. Each group runs its own lender container now, and that container
// bind-mounts this tree read-only for as long as the group lives — so a tree
// under t.TempDir would be deleted out from under a lender whose campaign is
// still running, which is the same failure by a shorter route. Measured before
// the move: two scenarios dead at `requests 0` while the lender logged "no
// anthropic key to lend".
//
// It is also what lets scenarios run side by side. Every lender mounts the same
// path, so no scenario's teardown can take another's credentials away.
var (
	credentialTreeOnce sync.Once
	credentialTreeDir  string
	credentialTreeErr  error
)

func credentialTree(t *testing.T, token string) string {
	t.Helper()
	credentialTreeOnce.Do(func() {
		credentialTreeDir, credentialTreeErr = os.MkdirTemp("", "cs-campaign-replay-creds-")
		if credentialTreeErr == nil {
			credentialTreeErr = writeFabricatedCredentials(credentialTreeDir, token)
		}
	})
	if credentialTreeErr != nil {
		t.Fatalf("build the fabricated credential tree: %v", credentialTreeErr)
	}
	return credentialTreeDir
}

func writeFabricatedCredentials(home, token string) error {
	far := time.Now().Add(365 * 24 * time.Hour).UnixMilli()

	// Two fields are load-bearing, for two different readers.
	//
	// scopes, because Claude Code checks it for the inference scope before it
	// will send anything, and without it fails as "not logged in". That is the
	// INHERITING scenario, where the agent reads this file itself.
	//
	// expiresAt, because the lender refuses a login it can see has expired
	// (internal/lend/cred.go) and reports it as a stale host login rather than
	// as an upstream 401. That is every LENT scenario, where the agent never
	// sees this file and the lender reads it per call. A year out satisfies
	// both. Nothing on either path checks a signature, which is what lets a
	// value that authenticates nothing stand in for one that does.
	claude, err := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{
		"accessToken":           "sk-ant-oat01-" + token,
		"refreshToken":          "sk-ant-ort01-" + token,
		"expiresAt":             far,
		"refreshTokenExpiresAt": far,
		"scopes":                []string{"user:inference", "user:profile"},
		"subscriptionType":      "max",
		"rateLimitTier":         "default_max_20x",
	}})
	if err != nil {
		return err
	}
	err = writeSecret(filepath.Join(home, ".cs-claude", ".credentials.json"), claude)
	if err != nil {
		return err
	}

	// Codex reads its tokens as JWTs, so these have to parse: three base64url
	// parts carrying the account claims it looks for. Nothing this run reaches
	// checks the signature.
	codex, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt", "OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token": fakeJWT(token), "access_token": fakeJWT(token),
			"refresh_token": "rt-" + token,
			"account_id":    "00000000-0000-0000-0000-000000000000",
		},
		"last_refresh": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	if err != nil {
		return err
	}
	err = writeSecret(filepath.Join(home, ".cs-codex", "auth.json"), codex)
	if err != nil {
		return err
	}

	// And the provider keys, in the one-key-per-file shape cs-sandbox lends
	// from: the whole file is the key, and the lender asks only that it not be
	// empty. Written for every provider rather than only this scenario's: the
	// tree costs nothing, and a scenario that grew a second grant would
	// otherwise fail as an authentication error rather than as a missing file.
	for _, provider := range model.APIKeyProviders {
		if err := writeSecret(filepath.Join(home, ".cs-keys", provider), []byte(token)); err != nil {
			return err
		}
	}
	return nil
}

// fakeJWT is a structurally valid token that authenticates nothing.
func fakeJWT(token string) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	now := time.Now().Unix()
	claims := map[string]any{
		"aud": "campaign-replay", "iss": "https://auth.openai.com",
		"sub": "campaign-replay", "iat": now, "exp": now + 365*24*3600,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "00000000-0000-0000-0000-000000000000",
			"chatgpt_plan_type":  "pro",
		},
	}
	return enc(map[string]any{"alg": "RS256", "kid": "replay", "typ": "JWT"}) + "." +
		enc(claims) + "." + base64.RawURLEncoding.EncodeToString([]byte(token))
}

// writeSecret writes a credential the way the agent expects to find one.
func writeSecret(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// newLiveApp builds the app under test with its own state directory, and
// registers the teardown twice over. The machines are real, and a leaked group
// takes a network, a key pair and a gateway port with it.
//
// t.Cleanup covers a test that fails. It does not cover a test that is killed,
// and a live run is killed often: a `timeout` around it, an interrupt, a lost
// terminal. `go test` dies on SIGTERM without running a single cleanup, and the
// campaign it started keeps running — machines, model turns and all. So the
// signal is caught too, and a teardown runs before the process leaves.
//
// SIGKILL cannot be caught, and nothing here pretends otherwise. Recovery from
// that one is `cs-sandbox group rm <group> -f`, which CONTRIBUTING.md names.
//
// The two teardowns are deliberately different. t.Cleanup runs the destroy
// command, which takes the campaign lock and tears the campaign down by its
// record. The signal path must not: an interrupt lands while create is holding
// that same lock, and flock is per open file description, so opening the lock
// file a second time blocks against this process's own hold — forever, with no
// signal left to break it, because the waiter and the holder are one process.
// A run that hit that stopped answering Ctrl+C at all. So the signal path
// reclaims the group directly, touching no lock.
func newLiveApp(t *testing.T, work, name string) *app {
	t.Helper()
	a := &app{store: store.Store{Dir: filepath.Join(work, "state")}, sandbox: newSandbox()}
	destroy := func() {
		cmd := a.destroyCmd()
		cmd.SetArgs([]string{name, "--force"})
		_ = cmd.Execute()
	}
	t.Cleanup(destroy)

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig, ok := <-signals
		if !ok {
			return
		}
		group := a.interruptedGroup(name)
		// Re-arm before reclaiming anything. signal.Notify has already
		// displaced Go's default die-on-SIGINT, so from here nothing else will
		// ever kill this process on its own: with no second reader, a reclaim
		// that wedges swallows every further Ctrl+C and the run can only be
		// ended from another terminal. This goroutine is what makes the second
		// interrupt mean "stop trying, I will clean up myself".
		go func() {
			if s, ok := <-signals; ok {
				fmt.Fprintf(os.Stderr, "\n%s: leaving %s up — reclaim it with `cs-sandbox group rm %s -f`\n", s, name, group)
			}
			os.Exit(1)
		}()
		if group == "" {
			fmt.Fprintf(os.Stderr, "\n%s: %s has no group on record yet — nothing to reclaim\n", sig, name)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "\n%s: reclaiming group %s before exiting — interrupt again to leave it up\n", sig, group)
		if err := reclaimGroupForce(context.Background(), a.sandbox, group); err != nil {
			fmt.Fprintf(os.Stderr, "reclaim %s: %v\n", group, err)
		}
		os.Exit(1)
	}()
	t.Cleanup(func() { signal.Stop(signals); close(signals) })
	return a
}

// reclaimGroupForce removes a group and every sandbox still inside it in one
// call, without first proving it empty.
//
// The lifecycle's own reclaimGroup cannot serve here. That one destroys the
// members the campaign record names and then removes a group it has shown to
// be empty — deliberately, as a second check. But an interrupt lands mid-create,
// where the record is a step behind the machines: a member provisioned since
// the last save is in the group and not yet on the record, and destroying by
// record would walk straight past it. `group rm --force` is addressed to the
// group rather than to a list of members, so it also reclaims whatever the
// record has not caught up with.
//
// It lives here, behind the live tags, because the interrupt handler is its
// only caller and `make deadcode` rightly refuses an unreachable one in
// production code.
func reclaimGroupForce(ctx context.Context, s sandboxCLI, group string) error {
	if s.Dry || group == "" {
		return nil
	}
	// The replay proxy sits on this group's network and is not a member
	// cs-sandbox knows about, so reclaiming the group leaves it running. Its
	// name is derived from the group, so the signal path can name it without
	// having been told: an interrupted run takes its proxy with it, the same
	// way it takes its machines.
	_ = exec.CommandContext(ctx, "podman", "rm", "-f", "cs-vcr-"+group).Run()
	groups, err := s.groups(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.Name == group {
			return s.run(ctx, "group", "rm", group, "--force")
		}
	}
	return nil
}

// driveToVerdict runs the steps an operator takes: create (which runs the
// doctor, the readback as d001 and opens the mission as m1), observe until the
// mission reply appears, archive, audit. The registered destroy closes it.
func driveToVerdict(t *testing.T, a *app, sc scenario, name, profilePath, archiveRoot, scratch string, opts runOptions) campaignRun {
	t.Helper()

	// A guest-dialled recorder joins the campaign's own network, which create
	// has not made yet, so the launch overlaps create rather than preceding it.
	// startVCR waits for the fabric; the window between the network appearing
	// and the first model turn is tens of seconds.
	type launch struct {
		proxy *vcrProxy
		err   error
	}
	var launched chan launch
	var abandoned chan struct{}
	if opts.proxyMode != "" {
		planned, _, err := a.planCampaign(createOpts{profile: profilePath}, name, true)
		if err != nil {
			t.Fatalf("plan, to learn the group: %v", err)
		}
		// Start cold, always.
		//
		// A recorded scenario has a fixed name, and the campaign ID is the
		// sha256 of the resolved profile — so every run of one scenario mints
		// the same session names as the last. startTurn asks hostSessionFresh
		// which branch of protocol.Trigger to send, and a warm session gets the
		// continuation prompt rather than the opening one.
		//
		// Left alone, the second replay of a cassette resumes the FIRST
		// replay's sessions: the agents still hold the whole campaign in
		// context, answer from it, and never reach the proxy at all. Measured
		// as two replayed steps for a 56-step pair of cassettes, with dev
		// writing its d002 summary where the harness had asked for the d001
		// readback. It reads exactly like a mis-served step, and is not one.
		//
		// Recording needs it as much as replay: a re-recording made against a
		// warm session records the continuation prompt, and then nothing can
		// replay it cold.
		forgetHostSessions(t, planned)
		// On its own goroutine, because startVCR waits for the campaign's
		// network and create is what makes it. Started before create, it would
		// wait ten minutes for a fabric nobody is building. The window between
		// the network appearing and the first model turn — the d001 readback,
		// inside create — is tens of seconds, which is the room this has.
		launched = make(chan launch, 1)
		// Closed when create fails, so the launch stops waiting for a fabric
		// nobody is building any more. Without it the recorder spends its whole
		// ten-minute deadline and the test then reports THAT, while the create
		// error which explains everything is never printed. Measured: six
		// scenarios each failing at 601s on "network never appeared", with the
		// real cause invisible in all six.
		abandoned = make(chan struct{})
		go func() {
			started, err := startVCR(t, sc, planned.Group, opts.proxyMode, opts.proxyStore, scratch, abandoned)
			launched <- launch{started, err}
		}()
	}

	create := a.createCmd(false)
	create.SilenceUsage = true
	var out strings.Builder
	create.SetOut(&out)
	create.SetErr(&out)
	create.SetArgs([]string{name, "--profile", profilePath})
	createErr := create.Execute()
	// Before collecting the launch, because the launch is waiting on a fabric
	// that create was supposed to build: a create that failed has already
	// decided the recorder's fate, and its error is the one worth reading.
	if createErr != nil && abandoned != nil {
		close(abandoned)
	}

	run := campaignRun{name: name, createOut: out.String()}
	if launched != nil {
		l := <-launched
		switch {
		case createErr != nil:
			// The launch was abandoned above; whatever it says is a consequence.
		case errors.Is(l.err, errVCRUnavailable):
			t.Skipf("%v", l.err)
		case l.err != nil:
			t.Fatalf("cs-vcr: %v", l.err)
		}
		run.proxy = l.proxy
	}
	if createErr != nil {
		// create is where the readback lives, so a failure here is usually a
		// member that never answered — and the member is about to be torn
		// down with the only record of why.
		if campaign, err := a.store.Load(name); err == nil {
			keepEvidence(t, a, campaign, "create-failed")
		}
		t.Fatalf("create: %v\n%s", createErr, out.String())
	}
	for _, want := range []string{"ok  readback orchestrator", "ok  readback dev", "mission m1 opened"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("create output missing %q:\n%s", want, out.String())
		}
	}

	campaign, err := a.store.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	run.campaign = campaign

	// The campaign runs itself; the host only observes. The mission reply is
	// the completion signal — its existence, and nothing else.
	//
	// Two things end the wait early, and both keep the evidence first. A stuck
	// node is terminal by definition. A stopped orchestrator is the operator's
	// to recover, and this driver is not an operator: past its settling window
	// it will not restart itself, so waiting out the ceiling buys nothing and
	// costs the whole ceiling. One such run cost half an hour.
	deadline := time.Now().Add(opts.ceiling)
	stopped := 0
	busy := ""
	for time.Now().Before(deadline) {
		obs, oerr := a.observeCampaign(context.Background(), campaign)
		if oerr == nil {
			orchestratorStopped := false
			for _, n := range obs.Derived {
				t.Logf("%s %s %s %s", n.Name, n.State, n.Dispatch, n.Detail)
				if n.State == string(protocol.StateStuck) {
					keepEvidence(t, a, campaign, "stuck")
					t.Fatalf("%s is stuck: %s", n.Name, n.Detail)
				}
				if n.Role == "orchestrator" && n.State == string(protocol.StateStopped) {
					orchestratorStopped = true
				}
			}
			if orchestratorStopped {
				stopped++
			} else {
				stopped = 0
			}
			if stopped >= stalledPolls {
				keepEvidence(t, a, campaign, "orchestrator-stalled")
				t.Fatalf("the orchestrator has been stopped for %d consecutive looks with no reply; "+
					"recovering it is the operator's move and this driver makes none", stopped)
			}
			if obs.MissionErr != "" {
				keepEvidence(t, a, campaign, "unreadable-verdict")
				t.Fatalf("the orchestrator replied to the mission but its verdict could not be read: %s", obs.MissionErr)
			}
			if obs.Mission != nil {
				run.verdict = obs.Mission
				// The verdict ends the campaign, not the machines. Archiving
				// now would read a member that is still running its turn: on a
				// host short of CPU the agent's guest saturates, every
				// collection burns its whole bound against it, and the run
				// fails on INCOMPLETE markers that describe the archive rather
				// than the campaign.
				//
				// The orchestrator is replayed here, so its judgment costs a
				// cassette lookup while the agent's work costs real seconds —
				// the verdict routinely lands first. Waiting for the workers to
				// go quiet is what makes the archive a reading of a finished
				// campaign.
				if busy = stillWorking(obs.Derived); busy == "" {
					break
				}
			}
		}
		time.Sleep(15 * time.Second)
	}
	if run.verdict == nil {
		keepEvidence(t, a, campaign, "no-verdict")
		t.Fatalf("no mission reply within %s", opts.ceiling)
	}
	if busy != "" {
		keepEvidence(t, a, campaign, "agent-still-working")
		t.Fatalf("the campaign reached its verdict but %s is still working after %s; "+
			"archiving a member mid-turn reports the collection's deadline rather than the campaign",
			busy, opts.ceiling)
	}

	run.archive = archiveRoot
	if _, err := a.archiveCampaign(context.Background(), campaign, run.archive); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if incomplete, _ := archiveIncomplete(run.archive); len(incomplete) > 0 {
		// The markers name which collections failed; their bodies say why, and
		// the driver logs beside them say what the member was doing at the
		// time. All three live under archiveRoot, which is a t.TempDir — so
		// without this the one artifact that explains the failure is written
		// and then deleted by the assertion that reads its filename.
		keepEvidence(t, a, campaign, "archive-incomplete")
		t.Fatalf("archive incomplete: %v", incomplete)
	}
	if findings := a.verifyFleetLive(context.Background(), campaign); len(findings) > 0 {
		t.Fatalf("audit: %+v", findings)
	}
	return run
}

// forgetHostSessions drops the host's session records for a campaign's members,
// so the next run of it starts cold. Best effort: a session that was never
// created is not an error, and forgetting one is not worth failing a run over.
func forgetHostSessions(t *testing.T, campaign *model.Campaign) {
	t.Helper()
	s := newSandbox()
	for _, member := range campaign.Members {
		s.forgetSession(context.Background(), member)
	}
}

// proveCampaignBehaviours records what a campaign that reached its verdict has
// demonstrably done for one adapter. Called after the outcome assertion, never
// before it: a cell filled by a test that then failed is a cell nobody can
// trust.
func proveCampaignBehaviours(t *testing.T, cli string, tier covmap.Tier) {
	t.Helper()
	// create seeded the credential and the member answered a turn with it;
	// the readback was delivered and awaited; the orchestrator dispatched to
	// an agent and judged the reply; the archive collected complete evidence.
	for _, behaviour := range []string{"auth-provisioning", "prompt-await", "model-delegation", "archive-evidence"} {
		covmap.Prove(t, behaviour, cli, "", tier)
	}
	covmap.ProveCore(t, "fleet-conformance", tier)
}

// stalledPolls is how many consecutive looks may show a stopped orchestrator
// before the run gives up. The polls are 15 seconds apart and the state only
// reports stopped once the settling window has passed, so this is a minute of
// genuine silence rather than a turn starting up.
const stalledPolls = 4

// stillWorking names an agent that is mid-turn, or "" when the workers have
// all gone quiet. The orchestrator is excluded: it has replied by the time this
// is asked, and its own rung is above.
//
// node-working counts, and so does node-unreachable: a member too busy to
// answer its probe is the one the archive least wants to read, and treating a
// look that learned nothing as permission to proceed would archive exactly the
// machine this waits for. node-free and node-replied are a member at rest with
// nothing running on its machine, which is all the archive needs.
//
// An earlier version of this driver failed a node for holding node-working past
// its stall threshold and settling window, on the theory that nothing else
// bounds that state. Reproducing the CI failure showed why that is wrong: a
// starved agent holds node-working for many minutes while making steady
// progress, and its state is indistinguishable from a wedged one. The campaign
// ceiling is the honest bound, and what the run actually needs is not to
// archive underneath a member that is still running.
func stillWorking(nodes []nodeView) string {
	for _, n := range nodes {
		if n.Role == "orchestrator" {
			continue
		}
		if n.State == string(protocol.StateWorking) || n.State == string(protocol.StateUnreachable) {
			return n.Name
		}
	}
	return ""
}

// keepEvidence archives a failing campaign somewhere that outlives the test.
//
// A run's working tree is a t.TempDir, removed when the test ends — and with it
// the one artifact that says what went wrong. The product's own rule is to
// archive before destroy; a test that destroys without archiving is asking a
// question and discarding the answer.
//
// Best effort throughout: a failure in here must never mask the failure that
// brought us here.
func keepEvidence(t *testing.T, a *app, campaign *model.Campaign, why string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".tmp", "live-evidence"))
	if err != nil {
		t.Logf("evidence: %v", err)
		return
	}
	dir := filepath.Join(root, fmt.Sprintf("%s-%s-%d", campaign.Name, why, time.Now().Unix()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Logf("evidence: %v", err)
		return
	}
	if _, err := a.archiveCampaign(context.Background(), campaign, dir); err != nil {
		t.Logf("evidence: archive: %v", err)
	}
	keepDriverLogs(t, a, campaign, dir)
	keepAgentPanes(t, a, campaign, dir)
	keepLenderLog(t, campaign.Group, dir)
	t.Logf("evidence kept in %s (%s)", dir, why)
}

// keepAgentPanes captures each member's agent TUI exactly as it stands.
//
// The driver reports a stalled turn as "JSONL unchanged for 180s, TUI state
// 'ready'" and tells a human to `tmux attach`. On a developer's machine that
// works; on a runner the member is gone before anyone can, and the one thing
// that would say WHAT the agent is sitting on is the screen itself. A state
// name is a classification of that screen, and every stall this tier has cost a
// day to has come down to not having the screen behind it.
//
// Best effort throughout: a member with no session has no pane, which is not a
// failure worth reporting over the one being investigated.
func keepAgentPanes(t *testing.T, a *app, campaign *model.Campaign, dir string) {
	t.Helper()
	for _, member := range campaign.Members {
		// Every pane of every session: the driver names one session per turn,
		// and which one stalled is exactly what is not known yet.
		const capture = `for s in $(tmux list-sessions -F '#{session_name}' 2>/dev/null); do ` +
			`printf '\n===== %s =====\n' "$s"; tmux capture-pane -p -t "$s" 2>&1; done`
		out, err := a.sandbox.memberOutput(context.Background(), member, capture)
		if err != nil || len(out) == 0 {
			continue
		}
		path := filepath.Join(dir, "pane-"+member.Name+".txt")
		if err := os.WriteFile(path, out, 0o600); err != nil {
			t.Logf("evidence: %s: %v", path, err)
		}
	}
}

// keepLenderLog saves this group's lender log beside the rest.
//
// It is the only account of the hop between a member and the recorder, and it
// is the log that named the last two causes outright: loans failing with "no
// anthropic key to lend", and an upstream refused at an address the lender
// could not reach. Every other log showed a member simply going quiet.
//
// From the container rather than from a file. cs-sandbox runs one lender per
// group now, so the log is the container's own and goes away with it, which is
// why it is copied here while the group still stands.
func keepLenderLog(t *testing.T, group, dir string) {
	t.Helper()
	if group == "" {
		return
	}
	out, err := exec.Command("podman", "logs", groupNetwork(group)+"-lender").CombinedOutput()
	if err != nil || len(out) == 0 {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "lender.log"), out, 0o600); err != nil {
		t.Logf("evidence: lender.log: %v", err)
	}
}

// keepDriverLogs saves each member's host-side agent-driver log beside the archive.
//
// The archive collects what the MEMBER produced, and a member whose driver never started
// produced nothing — which is the failure this tier is most likely to hit and least able to
// explain. Turns are dispatched with `-b`, so the driver's exit code is never seen: a launch
// failure and a slow turn both read as a node that went quiet. The reason is written on the
// host, in the log the remote tool keeps, and it says which of the two happened.
//
// Best effort, like everything else here: a member that never opened a session has no log,
// and that is not a failure worth reporting over the one being investigated.
func keepDriverLogs(t *testing.T, a *app, campaign *model.Campaign, dir string) {
	t.Helper()
	for _, member := range campaign.Members {
		var buf strings.Builder
		if err := a.sandbox.sessionLog(context.Background(), &buf, member); err != nil || buf.Len() == 0 {
			continue
		}
		path := filepath.Join(dir, "driver-"+member.Name+".log")
		if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
			t.Logf("evidence: %s: %v", path, err)
		}
	}
}

// campaignName names the run. A recording and its replay must produce the same
// campaign ID, and the ID is the sha256 of the resolved profile — which
// carries this name — so the replay tier passes a fixed one rather than a
// clock.
func campaignName(sc scenario, opts runOptions) string {
	if opts.fixedName != "" {
		return opts.fixedName
	}
	return fmt.Sprintf("cs%s%d", sc.cli, time.Now().Unix()%100000)
}

// seedSubjectRepo builds the repository the fleet works in: one commit, so a
// member's branch has a base its reply can be measured against.
func seedSubjectRepo(t *testing.T, work string) string {
	t.Helper()
	repo := filepath.Join(work, "subject")
	mustRun(t, work, "git", "init", "-q", repo)
	writeFileT(t, filepath.Join(repo, "README.md"), "campaign subject\n")
	mustRun(t, repo, "git", "-c", "user.email=t@t", "-c", "user.name=t", "add", "-A")
	mustRun(t, repo, "git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "base")
	return repo
}

// writeCampaignInputs writes the mission and the two role briefs. The mission
// is deliberately small and its acceptance mechanical: every tier asserts on
// `campaign-met`, so a mission whose completion is a judgement call would make
// that assertion a coin toss.
func writeCampaignInputs(t *testing.T, work string) {
	t.Helper()
	writeFileT(t, filepath.Join(work, "mission.md"), `# Mission: greet, tested

Deliver src/greet.py with greet(name) returning "Hello, <name>!" and
tests/test_greet.py covering a normal name and the empty string, with
python3 -m unittest discover -s tests exiting 0.

All three satisfied is met. This is a small mission: if it stalls, wrap up
honestly rather than continuing.
`)
	writeFileT(t, filepath.Join(work, "roles", "orchestrator.md"), `You run this campaign and judge it; you write no code yourself.

Dispatch the task to dev with everything it needs, judge the reply by fetching
the branch rather than believing the report, accept only work that meets every
criterion, then reply to your mission with the honest outcome.
`)
	writeFileT(t, filepath.Join(work, "roles", "dev.md"), `You own the subject repository.

Implement exactly what each dispatch asks, run the tests you write, commit to
your own branch before replying, and tell the truth in your reply.
`)
}

// ensureMountableTempDir puts this process's temporary files somewhere a member
// can actually be given.
//
// Every path these tiers hand a member is a bind mount in the end — the subject
// repository, the fabricated credential tree the lender reads, the cassette
// store and the recorder's config. On Linux any path will do. On macOS the
// engine is a container inside the podman machine, and the only host tree that
// VM mounts is $HOME: cs-sandbox refuses a --repo outside it outright ("must be
// under $HOME"), which is where this was found, and the mounts it does not
// check would have arrived empty.
//
// os.TempDir under macOS is /var/folders/…, so t.TempDir() lands outside $HOME
// by default and every scenario fails at create. Moving TMPDIR moves all of
// them at once, because they are all t.TempDir() or os.MkdirTemp underneath.
//
// The directory is kept rather than removed: Go cleans up what it creates
// inside it, and a fixed parent is what makes a leftover from a killed run
// findable.
func ensureMountableTempDir(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve the home directory a member's mounts must live under: %v", err)
	}
	dir := filepath.Join(home, ".cache", "cs-campaign", "tmp")
	// Same reason as ensureGuestBinary below: one value for the process,
	// planted by whoever gets here first, and a parallel scenario must not
	// try to plant it again.
	if os.Getenv("TMPDIR") == dir {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("make the temporary directory members can be given: %v", err)
	}
	t.Setenv("TMPDIR", dir)
}

// ensureLiveGuestBinary plants the guest binary these tiers ship into a member.
//
// A tier of its own rather than ensureGuestBinary, and the difference is what
// runs the thing. The unit suite installs it into a FAKE guest — a directory on
// this host — and executes it here, so it has to be a binary for this host. A
// live member is Linux whatever this host is, so the binary it is handed has to
// be one too, and on a Mac the host build is a Mach-O the guest cannot exec at
// all.
//
// GOARCH is this host's because the image is: cs-sandbox builds and pulls the
// variant native to the machine, so an arm64 Mac runs an arm64 guest and this
// runner an amd64 one. CGO_ENABLED=0 for the same reason `make guestbin` sets
// it — a dynamically linked binary is a bet on the guest's loader.
var liveGuestBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "guestbin-live")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "cs-campaign-member")
	if err := buildForGuest(path, "../../cmd/cs-campaign-member"); err != nil {
		return "", err
	}
	return path, nil
})

func ensureLiveGuestBinary(t *testing.T) {
	t.Helper()
	path, err := liveGuestBinary()
	if err != nil {
		t.Fatalf("cannot build the guest binary these members will run: %v", err)
	}
	// The guard ensureGuestBinary carries, for the same reason: t.Setenv
	// refuses to run on a test that has called t.Parallel, which is every
	// scenario in the smoke tier. The parent plants this before releasing them.
	if os.Getenv("CS_CAMPAIGN_GUEST_BIN") == path {
		return
	}
	t.Setenv("CS_CAMPAIGN_GUEST_BIN", path)
}

// ensureLiveLenderBinary hands the group's lender a cs-sandbox it can run.
//
// A lent scenario puts a lender container on the campaign's network, and that
// container runs `cs-sandbox lender`. cs-sandbox mounts its OWN executable in
// where it can — this host's, on Linux, so the lender under test is the build
// the run is testing — and otherwise leaves the image to supply one. The slim
// image carries no cs-sandbox at all, so on a Mac the container starts and dies
// as "executable file `cs-sandbox` not found in $PATH", with the campaign
// reporting only that create failed.
//
// CS_SANDBOX_LENDER_BIN is the documented way in, and a cross build is what it
// is documented for. Set here for the same reason the two binaries above are:
// it is a property of running these tiers on a Mac, not of this repository's
// CI, and a developer's machine hits it identically.
func ensureLiveLenderBinary(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "linux" {
		return
	}
	path, err := liveLenderBinary()
	if err != nil {
		t.Fatalf("cannot build the cs-sandbox this campaign's lender will run: %v", err)
	}
	if os.Getenv("CS_SANDBOX_LENDER_BIN") == path {
		return
	}
	t.Setenv("CS_SANDBOX_LENDER_BIN", path)
}

var liveLenderBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "lenderbin")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "cs-sandbox")
	// The pin, like everything else this tier runs: go.mod names the version,
	// `make tools` installs that one on PATH, and this compiles the same source
	// for the guest.
	if err := buildForGuest(path, "github.com/codesweep-ai/sandbox/cmd/cs-sandbox"); err != nil {
		return "", err
	}
	return path, nil
})

// buildForGuest compiles one package for the guest: Linux, this host's
// architecture, and static. It is what the two binaries these tiers put INSIDE
// a machine are built with — the member helper above and the recorder in
// vcrfabric_test.go — and it is a no-op difference on a Linux host, which is
// why the failure it prevents only ever shows up on a Mac.
func buildForGuest(out, pkg string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build %s for linux/%s: %w\n%s", pkg, runtime.GOARCH, err, out)
	}
	return nil
}

// liveEngine is the sandbox engine every member of these campaigns runs on.
//
// Firecracker unless the environment says otherwise, because a microVM is what
// this product is for and what every cassette was recorded against. CI is the
// caller that says otherwise: it runs the same replays a second time on podman,
// which is the only engine a machine without KVM has — a macOS host among them.
//
// A changed engine does not invalidate a cassette. The engine reaches the
// campaign ID, and the ID and every name derived from it are captured and
// blanked by the normalize block in writeVCRConfig; what would invalidate one
// is profile CONTENT that reaches a prompt, which the engine is not.
func liveEngine() string {
	if e := os.Getenv("CS_CAMPAIGN_ENGINE"); e != "" {
		return e
	}
	return "firecracker"
}

// scenarioProfile renders the profile for a homogeneous fleet.
//
// The profile is hashed into the campaign ID, so every name a run produces
// derives from this text. A cassette is bound to the profile that recorded it,
// and editing this function invalidates the recording — with the one exception
// liveEngine documents.
func scenarioProfile(sc scenario, repo, baseURL, name string) string {
	return fmt.Sprintf(`apiVersion: codesweep.ai/v1alpha1
kind: CampaignProfile
defaults:
  engine: %s
  deadline: 1h
  resources:
    cpus: 2
    memoryMiB: 1024
  policy:
    pollSeconds: 15
orchestrator:
%sagents:
  dev:
%s`,
		liveEngine(),
		memberBlock(sc, sc.orchSeat(), repo, baseURL, name+"-orchestrator"),
		indent(memberBlock(sc, sc.agentSeat(), repo, baseURL, name+"-dev")))
}

// memberBlock renders one member's profile block. cassette names the cassette
// this member records into, and is empty when there is no proxy.
func memberBlock(sc scenario, st seat, repo, baseURL, cassette string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "    cli: %s\n    model: %s\n", st.cli, st.model)
	if st.effort != "" {
		fmt.Fprintf(&b, "    effort: %s\n", st.effort)
	}
	// A member the campaign default does not fit. Declared per member rather
	// than raising the default for everyone: the default is what the other
	// five scenarios run in, and it is sized for a hosted runner.
	if st.memoryMiB != 0 {
		fmt.Fprintf(&b, "    resources:\n      memoryMiB: %d\n", st.memoryMiB)
	}
	fmt.Fprintf(&b, "    repos:\n      - path: %s\n", repo)
	b.WriteString("    auth:\n")
	// The grant, in the spelling this scenario is here to exercise. A lending
	// scenario spells no verb at all: it takes the campaign's, which is the
	// product default and the thing worth recording.
	switch {
	case st.keyProvider != "" && sc.lends():
		fmt.Fprintf(&b, "      apiKey: [%s]\n", st.keyProvider)
	case st.keyProvider != "":
		fmt.Fprintf(&b, "      inheritApiKey: [%s]\n", st.keyProvider)
	case sc.lends():
		fmt.Fprintf(&b, "      agentLogin: [%s]\n", st.login)
	default:
		fmt.Fprintf(&b, "      inheritAgentLogin: [%s]\n", st.login)
	}
	// A fixed authorship for every commit a member makes. Without it the guest
	// takes the operator's git identity, and their real name and address end up
	// in a recorded turn the moment an agent runs `git log` — in a file this
	// repository commits. Fixed rather than absent so the value is also the
	// same on the machine that replays.
	env := []string{
		// Keeps the member off the account features that vary with the
		// network — telemetry, error reporting, the claude.ai surfaces the
		// binary names in its own strings. Fewer things that can differ
		// between a recording and its replay.
		//
		// It does NOT stop Claude Code's title-generation turn, which was the
		// first guess: the haiku turn count was 3 before and 3 after. That
		// turn is absorbed by the proxy's lookahead window instead, and a
		// replay was measured serving it in order.
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"GIT_AUTHOR_NAME=campaign test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=campaign test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	}
	if baseURL != "" && st.baseURLEnv != "" && cassette != "" {
		env = append(env, fmt.Sprintf("%s=%s/c/%s/%s%s",
			st.baseURLEnv, baseURL, st.vcrProvider, cassette, st.urlSuffix))
		// And the traffic a base URL does not govern. Claude Code checks its
		// OAuth session against api.anthropic.com and Codex reaches chatgpt.com,
		// whatever they were pointed at, and what those answer changes the
		// prompt. A real login makes them succeed and a fabricated one makes
		// them 401. cs-vcr answers CONNECT on the same address, refusing that
		// handful and tunnelling the rest, so the member's tools keep their
		// network.
		//
		// Set while recording as well as while replaying. Refused in both
		// halves, the two runs ask the same question, which is what lets a
		// session recorded under a real subscription replay under a fabricated
		// one.
		//
		// NO_PROXY carries the proxy's own host, so the model calls above go
		// straight to the base URL rather than through the tunnel.
		//
		// Only where the GUEST dials the recorder, which is cs-sandbox's own
		// rule for this (its agent matrix sets exactly these). A lent member
		// needs none of it: its base URL is read at create and becomes the
		// loan's upstream, and cs-sandbox points the member's proxy variables
		// at the lender itself, which refuses the side calls. Setting them here
		// would aim a lent member at a proxy no real campaign gives it.
		if !sc.lends() {
			for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
				env = append(env, k+"="+baseURL)
			}
			for _, k := range []string{"NO_PROXY", "no_proxy"} {
				env = append(env, k+"="+vcrHost+",127.0.0.1,localhost")
			}
		}
	}
	b.WriteString("    env:\n")
	for _, e := range env {
		fmt.Fprintf(&b, "      - %s\n", e)
	}
	return b.String()
}

// indent shifts a member block two spaces right, which is where an agent's
// block sits relative to the orchestrator's.
func indent(block string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(strings.TrimRight(block, "\n"), "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

func mustRun(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
