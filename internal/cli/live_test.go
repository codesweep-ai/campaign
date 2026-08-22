//go:build integration

package cli

// The integration tier: real virtual machines, real model turns, real money.
//
// It answers one question the unit tier cannot ask at all — does a campaign
// drive this backend to a verdict — and it asks it once per backend, because a
// fleet that works on one adapter tells you nothing about the next. Every
// scenario that this host cannot sign in for skips with the credential it
// wants, so a run also reports what one more login would cover.
//
//	make test-integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/covmap"
)

// TestLiveMatrix runs the smallest complete campaign on each backend.
//
// Serial, not parallel: every member is a Firecracker virtual machine, and the
// scenarios share one host's memory, one fabric address range and one pool of
// gateway ports.
func TestLiveMatrix(t *testing.T) {
	requireLiveOptIn(t)
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			if ok, why := sc.available(); !ok {
				t.Skipf("%s", why)
			}
			run := runLiveCampaign(t, sc, runOptions{ceiling: 30 * time.Minute})
			if run.verdict.Outcome != "campaign-met" {
				t.Fatalf("this mission is small and mechanical, so %s should meet it; got %s: %s",
					sc.name, run.verdict.Outcome, run.verdict.Note)
			}
			proveCampaignBehaviours(t, sc.cli, covmap.TierLive)
		})
	}
}

// TestLiveHeterogeneousFleet is the one campaign whose members do not share an
// adapter. A heterogeneous fleet is a stated capability (SPEC.md R82) and no
// homogeneous scenario covers it: the orchestrator drives an agent through a
// helper that routes by declared CLI, and that routing is only exercised when
// the two differ.
func TestLiveHeterogeneousFleet(t *testing.T) {
	requireLiveOptIn(t)
	orchestrator, agent := scenarioByName(t, "codex-subscription"), scenarioByName(t, "opencode-fireworks")
	for _, sc := range []scenario{orchestrator, agent} {
		if ok, why := sc.available(); !ok {
			t.Skipf("%s", why)
		}
	}
	run := runMixedCampaign(t, orchestrator, agent, 30*time.Minute)
	if run.verdict.Outcome != "campaign-met" {
		t.Fatalf("mixed fleet should meet this mission; got %s: %s", run.verdict.Outcome, run.verdict.Note)
	}
	// The helper routed by declared CLI across a family boundary, and the audit
	// found each member's own evidence stream. Neither is provable by a
	// homogeneous fleet.
	for _, cli := range []string{orchestrator.cli, agent.cli} {
		covmap.Prove(t, "helper-control", cli, "", covmap.TierLive)
	}
	covmap.ProveCore(t, "fleet-conformance", covmap.TierLive)
}

// TestLiveRecordsACassette is `make fixtures` as a test: it drives each
// scenario through a cs-vcr in record mode and leaves the cassettes where the
// smoke tier replays them from.
//
// A test rather than a script because recording and replaying have to agree on
// every byte of the profile, and the only way to guarantee that is for both to
// come out of the same function.
//
// It records every scenario by default. Narrow it with -run when re-recording
// one, since each costs a real campaign:
//
//	make fixtures FIXTURE_TESTS='TestLiveRecordsACassette/codex-api-key'
func TestLiveRecordsACassette(t *testing.T) {
	if os.Getenv("CS_CAMPAIGN_RECORD") == "" {
		t.Skip("recording spends tokens and overwrites a committed cassette: set CS_CAMPAIGN_RECORD=1")
	}
	requireLiveOptIn(t)
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			if !sc.recordable() {
				skipUnlessStrict(t, "%s cannot be aimed at a proxy from a profile", sc.name)
			}
			if ok, why := sc.available(); !ok {
				skipUnlessStrict(t, "%s", why)
			}
			// Present is not the same as working. Asked here, before the
			// cassette is cleared and before a single machine boots.
			preflight(t, sc)
			// Recording replaces this scenario's cassette rather than adding
			// to it, and the old one goes before the campaign starts.
			//
			// cs-vcr refuses to record into a cassette keyed under a
			// superseded normalization ruleset, for the same reason it refuses
			// to replay one: entries written now would be keyed under this
			// build's rules and sit beside entries keyed under the old ones, in
			// a cassette whose header can only claim one of them. Left in
			// place, that refusal arrives once per request from inside the
			// proxy, where the campaign cannot see it — the members simply
			// never get an answer, and the run spends its whole ladder waiting
			// on turns that were never served. Which is to say: exactly the
			// hang a stale cassette causes on the replay side, in the one
			// command whose whole purpose is to end it.
			//
			// Safe to remove outright. A re-recording supersedes what it
			// replaces by definition, and the copy it replaces is in git.
			store := cassetteStore(t, sc)
			if err := os.RemoveAll(store); err != nil {
				t.Fatalf("clear %s before re-recording: %v", store, err)
			}
			if err := os.MkdirAll(store, 0o750); err != nil {
				t.Fatal(err)
			}
			// Claim the cassette before recording into it, and settle the claim
			// only once the campaign has been shown to have met its mission.
			//
			// The window between those two is where a recording can be lost
			// without looking lost. cs-vcr writes entries as they are served, so
			// a run that dies partway leaves a cassette that is complete in every
			// way a reader can check: keyed under the current ruleset, entries
			// well-formed, `cassette verify` clean. Measured — an Anthropic key
			// ran out of credit 40 requests into claude-api-key, and the
			// truncated recording of a campaign that never reached a verdict
			// passed the ruleset gate without a word.
			//
			// Written first rather than at the end because absence proves
			// nothing: a cassette recorded before this existed has no claim
			// either, and a gate that treated the two alike could only warn.
			// An unsettled claim is unambiguous — this run started and did not
			// finish — so the gate can refuse it by name.
			claimRecording(t, store, sc)
			run := runLiveCampaign(t, sc, runOptions{
				baseURL:    vcrBaseURL,
				ceiling:    30 * time.Minute,
				proxyMode:  "record",
				proxyStore: store,
				fixedName:  replayName(sc),
			})
			if run.verdict.Outcome != "campaign-met" {
				t.Fatalf("record a met campaign, or the smoke tier asserts on a failure: got %s: %s",
					run.verdict.Outcome, run.verdict.Note)
			}
			settleRecording(t, store, sc, run.verdict.Outcome)
			// The host's session records must not survive into the replay:
			// startTurn asks hostSessionFresh which branch of protocol.Trigger
			// to send, and the two emit different prompt text.
			forgetHostSessions(t, run.campaign)
			t.Logf("recorded %s into %s — commit it", sc.name, store)
		})
	}
}

// requireLiveOptIn refuses to spend money on a run nobody asked for.
//
// The build tag is not enough of a guard on its own. A contributor exploring
// the tiers types `go test -tags integration ./...`, and on a machine that
// happens to hold a subscription that boots virtual machines and starts
// charging — which is exactly how this guard came to be written. Absent
// credentials are not a safety mechanism; they are an accident of the host.
//
// It matters more than the money. A live run killed part-way — by a timeout, an
// interrupt, a lost terminal — never reaches its teardown, and leaves a group
// of machines running with their network, keys and gateway port. Recovery is
// `cs-sandbox group rm <group> -f`.
func requireLiveOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("CS_CAMPAIGN_LIVE") == "" {
		t.Skip("live tier: set CS_CAMPAIGN_LIVE=1 (boots real machines and spends model tokens)")
	}
}

// recordable reports whether this scenario can be put through cs-vcr from a
// campaign profile as things stand. It is a property of the wrapper, not of the
// agent: see the baseURLEnv field for what codex would need.
func (s scenario) recordable() bool { return s.baseURLEnv != "" }

// inheritedCredential names the file cs-sandbox copies into a member when a
// profile asks to inherit that family's host login (the sandbox repository's
// seed/agentlogin.go owns the list). It is the cs- profile rather than the
// agent's own directory: ~/.claude can exist on a host that never signed in,
// and it is not what gets carried.
var inheritedCredential = map[string]string{
	"claude":   ".cs-claude/.credentials.json",
	"codex":    ".cs-codex/auth.json",
	"opencode": ".cs-opencode/auth.json",
}

// hostLoginMissing reports why this host cannot run a scenario that inherits a
// login, or "" when it can. Only the inherited half is judged here. A credential
// that lives in an environment variable is left to the caller, because the two
// tiers disagree about it: the smoke tier substitutes fakeKey and needs no real
// one, and the integration tier needs exactly the real one.
//
// A subscription scenario is different in kind, because the agent reads the
// credential itself and will not run unattended without one. Claude Code puts up
// its sign-in screen, which cs-claude-turn reads as an authentication failure;
// Codex validates against its own backend over a hardcoded URL and takes a 401.
// Neither is reached by cs-vcr, so no cassette helps. Either way the members
// never reply and the readback spends its whole ceiling finding that out, which
// reads as a hung run rather than a missing login.
//
// Seeding a synthetic credential does not help, and was measured rather than
// assumed: a well-formed fake is carried into the member and then rejected
// exactly as an absent one is, because both agents check with their provider
// instead of trusting the file.
func hostLoginMissing(sc scenario) string {
	if sc.inherit == "" {
		return ""
	}
	rel, ok := inheritedCredential[sc.inherit]
	if !ok {
		return fmt.Sprintf("scenario inherits %q, which is not a family cs-sandbox carries", sc.inherit)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "cannot resolve the home directory to look for a login"
	}
	if _, err := os.Stat(filepath.Join(home, rel)); err != nil {
		// sc.auth already ends in "on this host", so this does not repeat it.
		return fmt.Sprintf("needs %s: no ~/%s", sc.auth, rel)
	}
	return ""
}

// strictSwitch turns a skipped scenario into a failure. A host that holds every
// credential and means to re-record the whole matrix wants it: recording none of
// them reports the same green as recording all of them.
const strictSwitch = "CS_CAMPAIGN_STRICT"

// skipUnlessStrict reports a scenario this host cannot cover, and says which
// credential it wanted. Under strictSwitch it fails instead.
func skipUnlessStrict(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv(strictSwitch) != "" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

// probePrompt is what the preflight asks for: one word, so the answer is
// unambiguous and the turn costs almost nothing.
const probePrompt = "reply with the single word: ok"

// probeAnswered matches the word on its own, never inside another. Substring
// matching is not enough for a word this short: "token" contains "ok", so an
// authentication error would read as the answer it is complaining about.
var probeAnswered = regexp.MustCompile(`(?i)\bok\b`)

// preflight runs this scenario's agent on the host, against its real provider,
// and fails the scenario if it does not answer.
//
// A campaign is expensive: two microVMs, a fabric, a dispatch ladder and real
// model turns. Discovering there that a key was revoked or a subscription token
// went stale costs all of that, and leaves a half-written cassette where a
// committed one used to be. One word first costs a few hundred tokens.
//
// The same credential the members will inherit, through the same agent binary
// they will run. Not a request to the provider's API: what has to work is the
// agent reading its profile directory and finding a login there.
func preflight(t *testing.T, sc scenario) {
	t.Helper()
	bin, err := exec.LookPath(sc.cli)
	if err != nil {
		t.Fatalf("%s: %s is not on this host's PATH, and the preflight runs it here: %v", sc.name, sc.cli, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var cmd *exec.Cmd
	switch sc.cli {
	case "claude":
		cmd = exec.CommandContext(ctx, bin, "-p", probePrompt, "--model", sc.model)
	case "codex":
		cmd = exec.CommandContext(ctx, bin, "exec", "--skip-git-repo-check", probePrompt)
	case "opencode":
		cmd = exec.CommandContext(ctx, bin, "run", "--model", sc.model, probePrompt)
	default:
		t.Fatalf("%s: no preflight for %s", sc.name, sc.cli)
	}
	// The profile directories cs-sandbox will inherit from, so this asks after
	// the same login the members are about to be given.
	cmd.Env = append(os.Environ(),
		"CLAUDE_CONFIG_DIR="+filepath.Join(agentLoginHome(t), ".cs-claude"),
		"CODEX_HOME="+filepath.Join(agentLoginHome(t), ".cs-codex"),
	)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	if err != nil || !probeAnswered.Match(out) {
		t.Fatalf("%s: %s could not reach its provider with %s, so nothing was recorded and the committed cassette is untouched.\n%v\n%s",
			sc.name, sc.cli, sc.auth, err, tailBytes(out, 15))
	}
}

// agentLoginHome is where cs-sandbox reads a login from, which a caller may move.
func agentLoginHome(t *testing.T) string {
	t.Helper()
	if d := os.Getenv("CS_SANDBOX_AGENT_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve the home directory: %v", err)
	}
	return home
}

// tailBytes is the last n lines of command output, for a failure message.
func tailBytes(b []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// available reports whether this host holds what the scenario needs, and says
// what is missing when it does not.
//
// The inherited half asks for the credential cs-sandbox would actually carry,
// rather than the agent's own directory. ~/.claude exists on a host that has
// only ever run Claude Code once and never signed in, and a run started on that
// evidence does not fail fast: it spends a whole readback ceiling, and this is
// the tier that spends real money doing it.
func (s scenario) available() (bool, string) {
	if s.keyEnv != "" && os.Getenv(s.keyEnv) == "" {
		return false, fmt.Sprintf("needs %s (%s)", s.keyEnv, s.auth)
	}
	if why := hostLoginMissing(s); why != "" {
		return false, why
	}
	return true, ""
}

// runMixedCampaign drives a fleet whose orchestrator and agent run different
// adapters. Everything but the profile is shared with runLiveCampaign.
func runMixedCampaign(t *testing.T, orchestrator, agent scenario, ceiling time.Duration) campaignRun {
	t.Helper()
	ensureGuestBinary(t)
	work := t.TempDir()
	repo := seedSubjectRepo(t, work)
	writeCampaignInputs(t, work)

	name := fmt.Sprintf("csmixed%d", time.Now().Unix()%100000)
	profilePath := filepath.Join(work, "profile.yaml")
	writeFileT(t, profilePath, mixedProfile(orchestrator, agent, repo))

	a := newLiveApp(t, work, name)
	return driveToVerdict(t, a, orchestrator, name, profilePath, filepath.Join(work, "archive"), work, runOptions{ceiling: ceiling})
}

// mixedProfile puts one scenario in the orchestrator seat and another in the
// agent's, with no proxy: a heterogeneous fleet is a live-tier question.
func mixedProfile(orchestrator, agent scenario, repo string) string {
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
%s`, memberBlock(orchestrator, repo, "", ""), indent(memberBlock(agent, repo, "", "")))
}

// scenarioByName looks one scenario up, failing loudly rather than returning a
// zero value: a typo in a tier's scenario name would otherwise run a campaign
// with no adapter and no credential, and fail somewhere unrecognisable.
func scenarioByName(t *testing.T, name string) scenario {
	t.Helper()
	for _, sc := range scenarios() {
		if sc.name == name {
			return sc
		}
	}
	t.Fatalf("no scenario named %q", name)
	return scenario{}
}

// recordingClaim is what a scenario's cassette directory carries beside its
// members, saying which run produced it and whether that run finished.
//
// Outcome is the campaign's own verdict vocabulary, plus one value of this
// file's own: "in-progress", written when recording starts and replaced when
// the campaign is shown to have met its mission. Anything still saying
// in-progress is a recording that died partway.
type recordingClaim struct {
	Scenario string `json:"scenario"`
	CLI      string `json:"cli"`
	Outcome  string `json:"outcome"`
	At       string `json:"at"`
}

// recordingClaimName is the file, at the scenario root rather than inside a
// member directory — `cs-vcr cassette verify` is addressed to member
// directories and hasCassette globs for their index files, so neither sees it.
const recordingClaimName = "recorded.json"

func claimRecording(t *testing.T, store string, sc scenario) {
	t.Helper()
	writeRecordingClaim(t, store, recordingClaim{
		Scenario: sc.name, CLI: sc.cli, Outcome: "in-progress",
		At: time.Now().UTC().Format(time.RFC3339),
	})
}

func settleRecording(t *testing.T, store string, sc scenario, outcome string) {
	t.Helper()
	writeRecordingClaim(t, store, recordingClaim{
		Scenario: sc.name, CLI: sc.cli, Outcome: outcome,
		At: time.Now().UTC().Format(time.RFC3339),
	})
}

func writeRecordingClaim(t *testing.T, store string, claim recordingClaim) {
	t.Helper()
	encoded, err := json.MarshalIndent(claim, "", "  ")
	if err != nil {
		t.Fatalf("encode the recording claim: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store, recordingClaimName), append(encoded, '\n'), 0o600); err != nil {
		t.Fatalf("write the recording claim: %v", err)
	}
}
