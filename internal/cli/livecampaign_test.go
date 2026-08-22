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
	"strings"
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
	// keyEnv is the environment variable the credential lives in, empty when
	// this scenario inherits a host login instead.
	keyEnv string
	// inherit is the adapter family whose host login this fleet inherits,
	// empty when it authenticates with a key.
	inherit string
	// baseURLEnv is the base-URL variable this adapter is aimed with, and the
	// one thing that decides whether a scenario can be recorded. It is the
	// agent's own name rather than a project-specific one: claude and opencode
	// read theirs directly, and cs-codex builds codex a provider declaration
	// out of OPENAI_BASE_URL, since codex reads no such variable itself.
	//
	// A member reaches its proxy through the profile's env block, so nothing
	// here needs to know which mechanism the wrapper used.
	baseURLEnv string
	// urlSuffix is what this client appends to the base URL it is given.
	urlSuffix string
	// vcrProvider and vcrUpstream say where the real provider lives, for the
	// recording half. They differ per scenario in a way the client's own
	// surface does not reveal: codex on a subscription talks to the ChatGPT
	// backend rather than the API, and the same client with a key talks to the
	// API. Replay reads neither — a cassette is served without ever resolving
	// where it came from.
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
			model: "claude-sonnet-5", inherit: "claude",
			baseURLEnv:  "ANTHROPIC_BASE_URL",
			vcrProvider: "anthropic", vcrUpstream: "https://api.anthropic.com",
		},
		{
			// No /v1: codex's subscription path authenticates as itself rather
			// than with a header, and its provider takes the bare base URL.
			// The same adapter as the scenario above, signed in the other way.
			// Claude Code takes an API key in a header where a subscription takes
			// OAuth, so the two send different requests and only running both
			// covers the pair. The key is granted to this member and no other:
			// every scenario declares the credential it needs, and the product
			// grants nothing a profile did not ask for.
			name: "claude-api-key", cli: "claude",
			auth:  "an Anthropic API key",
			model: "claude-sonnet-5", keyEnv: "ANTHROPIC_API_KEY",
			baseURLEnv:  "ANTHROPIC_BASE_URL",
			vcrProvider: "anthropic", vcrUpstream: "https://api.anthropic.com",
		},
		{
			name: "codex-subscription", cli: "codex",
			auth:  "a ChatGPT subscription on this host",
			model: "gpt-5.6-sol", effort: "medium", inherit: "codex",
			baseURLEnv:  "OPENAI_BASE_URL",
			vcrProvider: "openai", vcrUpstream: "https://chatgpt.com/backend-api/codex",
		},
		{
			name: "codex-api-key", cli: "codex",
			auth:  "an OpenAI API key",
			model: "gpt-5.6-sol", effort: "medium", keyEnv: "OPENAI_API_KEY",
			baseURLEnv: "OPENAI_BASE_URL", urlSuffix: "/v1",
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
			// Fireworks speaks the OpenAI wire protocol, which is why the
			// provider here is openai while the upstream is not.
			name: "opencode-fireworks", cli: "opencode",
			auth:  "a Fireworks API key",
			model: "fireworks-ai/accounts/fireworks/models/kimi-k3", effort: "high",
			keyEnv:     "FIREWORKS_API_KEY",
			baseURLEnv: "OPENCODE_BASE_URL", urlSuffix: "/v1",
			vcrProvider: "openai", vcrUpstream: "https://api.fireworks.ai/inference",
		},
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
	ensureGuestBinary(t)
	if opts.fakeKey != "" && sc.keyEnv != "" {
		t.Setenv(sc.keyEnv, opts.fakeKey)
	}
	// A scenario that inherits a login has no key to substitute, so the replay
	// half writes the credential itself.
	if opts.fakeKey != "" && sc.inherit != "" {
		fabricatedLogins(t, opts.fakeKey)
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

// fabricatedLogins writes the profile tree a replaying member inherits its
// login from, and points cs-sandbox at it.
//
// A subscription scenario has no key to substitute. The agent reads a credential
// FILE and will not run unattended without one: Claude Code puts up its sign-in
// screen, which cs-claude-turn reports as an authentication failure. So the
// replay half writes the shape each agent requires, with values that
// authenticate nothing.
//
// It works because the members reach no provider that could refuse it. cs-vcr
// serves the model calls from the cassette and refuses the hosts the agents
// contact on their own, so a fabricated token is never presented to anyone able
// to say no. Given an open network the same token fails: the OAuth check answers
// 401 and the agent believes it.
//
// CS_SANDBOX_AGENT_HOME moves only where a login is READ from. Pointing HOME at
// this tree would take the instance directory and the caches with it.
func fabricatedLogins(t *testing.T, token string) {
	t.Helper()
	home := t.TempDir()
	far := time.Now().Add(365 * 24 * time.Hour).UnixMilli()

	// scopes is load-bearing: Claude Code checks it for the inference scope
	// before it will send anything, and without it fails as "not logged in".
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
		t.Fatal(err)
	}
	writeSecretT(t, filepath.Join(home, ".cs-claude", ".credentials.json"), claude)

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
		t.Fatal(err)
	}
	writeSecretT(t, filepath.Join(home, ".cs-codex", "auth.json"), codex)

	t.Setenv("CS_SANDBOX_AGENT_HOME", home)
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

// writeSecretT writes a credential the way the agent expects to find one.
func writeSecretT(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
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

	// The proxy joins a network create has not made yet, so it is launched
	// alongside create rather than before it: startVCR waits for the fabric,
	// and the window between the network appearing and the first model turn —
	// the d001 readback, inside create — is tens of seconds. `plan` resolves
	// the group without allocating anything, which is how the launcher knows
	// which network to wait for.
	type launch struct {
		proxy *vcrProxy
		err   error
	}
	var launched chan launch
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
		launched = make(chan launch, 1)
		go func() {
			proxy, err := startVCR(t, sc, planned.Group, opts.proxyMode, opts.proxyStore, scratch)
			launched <- launch{proxy, err}
		}()
	}

	create := a.createCmd(false)
	create.SilenceUsage = true
	var out strings.Builder
	create.SetOut(&out)
	create.SetErr(&out)
	create.SetArgs([]string{name, "--profile", profilePath})
	createErr := create.Execute()

	run := campaignRun{name: name, createOut: out.String()}
	if launched != nil {
		l := <-launched
		switch {
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
				break
			}
		}
		time.Sleep(15 * time.Second)
	}
	if run.verdict == nil {
		keepEvidence(t, a, campaign, "no-verdict")
		t.Fatalf("no mission reply within %s", opts.ceiling)
	}

	run.archive = archiveRoot
	if _, err := a.archiveCampaign(context.Background(), campaign, run.archive); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if incomplete, _ := archiveIncomplete(run.archive); len(incomplete) > 0 {
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
	t.Logf("evidence kept in %s (%s)", dir, why)
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

// scenarioProfile renders the profile for a homogeneous fleet.
//
// The profile is hashed into the campaign ID, so every name a run produces
// derives from this text. A cassette is bound to the profile that recorded it,
// and editing this function invalidates the recording.
func scenarioProfile(sc scenario, repo, baseURL, name string) string {
	return fmt.Sprintf(`apiVersion: codesweep.ai/v1alpha1
kind: CampaignProfile
defaults:
  engine: firecracker
  deadline: 1h
  resources:
    cpus: 2
    memoryMiB: 2048
  policy:
    pollSeconds: 15
orchestrator:
%sagents:
  dev:
%s`,
		memberBlock(sc, repo, baseURL, name+"-orchestrator"),
		indent(memberBlock(sc, repo, baseURL, name+"-dev")))
}

// memberBlock renders one member's profile block. cassette names the cassette
// this member records into, and is empty when there is no proxy.
func memberBlock(sc scenario, repo, baseURL, cassette string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "    cli: %s\n    model: %s\n", sc.cli, sc.model)
	if sc.effort != "" {
		fmt.Fprintf(&b, "    effort: %s\n", sc.effort)
	}
	fmt.Fprintf(&b, "    repos:\n      - path: %s\n", repo)
	b.WriteString("    auth:\n")
	if sc.keyEnv != "" {
		fmt.Fprintf(&b, "      apiKeyFromEnv: [%s]\n", sc.keyEnv)
	}
	if sc.inherit != "" {
		fmt.Fprintf(&b, "      inheritAgentLogin: [%s]\n", sc.inherit)
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
	if baseURL != "" && sc.baseURLEnv != "" && cassette != "" {
		env = append(env, fmt.Sprintf("%s=%s/c/%s%s", sc.baseURLEnv, baseURL, cassette, sc.urlSuffix))
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
		env = append(env,
			"HTTP_PROXY="+baseURL,
			"HTTPS_PROXY="+baseURL,
			"ALL_PROXY="+baseURL,
			"NO_PROXY="+vcrHost+",127.0.0.1,localhost",
			"no_proxy="+vcrHost+",127.0.0.1,localhost",
		)
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
