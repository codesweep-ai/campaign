package cli

// The documentation claims a reader would act on and could not check: state
// safety, and how a credential reaches a member. The rest of this file used to
// pin the await/journal machinery, which the protocol deleted — completion is a
// reply artifact now, and no dispatch state is stored to be journaled or
// clobbered.

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign"
	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
	"github.com/codesweep-ai/campaign/internal/store"
)

// MANUAL.md: "A grant is **lent**", and a plain grant takes the campaign's
// verb. README.md: "No member holds the credential that pays for it." It is
// the sentence an operator decides on — whether their own credential enters a
// microVM an unattended agent runs in — and the manual is carried inside the
// binary, so a reader has no second source to check it against. The claim is
// kept honest here against a profile that declares a grant and nothing else,
// which is the shape both sentences describe.
func TestTheDocumentedCredentialDefaultIsWhatAProfileGets(t *testing.T) {
	covmap.ProveCoreOnPass(t, "profile-validation", covmap.TierUnit)
	for _, claim := range []string{
		"A grant is **lent**",
		"The plain name takes the campaign's verb",
		"**A member speaks one verb.**",
	} {
		if !strings.Contains(campaign.ManualMD, claim) {
			t.Errorf("MANUAL.md no longer states %q; this test names the sentence it keeps true", claim)
		}
	}
	// README.md is not embedded, so it is read from the tree. A clone that has
	// it must not have it saying something else.
	if root, err := covmap.FindRepoRoot("."); err == nil {
		readme, readErr := os.ReadFile(filepath.Join(root, "README.md"))
		claim := "**No member holds the credential that pays for it.**"
		if readErr == nil && !strings.Contains(string(readme), claim) {
			t.Errorf("README.md no longer states %q; this test names the sentence it keeps true", claim)
		}
	}
	p := &model.Profile{
		Orchestrator: model.MemberProfile{CLI: "claude", Auth: model.Auth{AgentLogin: []string{"claude"}}},
		Agents: map[string]model.MemberProfile{
			"backend": {CLI: "codex", Auth: model.Auth{APIKey: []string{"openai"}}},
		},
	}
	applyDefaults(p)
	for name, member := range map[string]model.MemberProfile{"orchestrator": p.Orchestrator, "backend": p.Agents["backend"]} {
		args := createArgs(&model.Campaign{Engine: "firecracker", Group: "g"}, model.Member{Sandbox: "box", CLI: member.CLI, Profile: member})
		if slices.Contains(args, "--inherit-agent-login") || slices.Contains(args, "--inherit-api-key") {
			t.Fatalf("%s: a profile that declared no mode must not copy a credential in: %#v", name, args)
		}
		if !slices.Contains(args, "--lend-agent-login") && !slices.Contains(args, "--lend-api-key") {
			t.Fatalf("%s: a declared grant must reach cs-sandbox as a loan: %#v", name, args)
		}
	}
}

func TestCorruptStateIsNotSilentlyOmittedFromLs(t *testing.T) {
	covmap.ProveCoreOnPass(t, "state-safety", covmap.TierUnit)
	stateDir := t.TempDir()
	a := &app{store: store.Store{Dir: stateDir}}
	good := &model.Campaign{Name: "good"}
	if err := a.store.Save(good); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	cmd := a.lsCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(out.String(), "broken.json") || !strings.Contains(out.String(), "good") {
		t.Fatalf("ls output must list good campaigns and warn about corrupt ones:\n%s", out.String())
	}
}

// MANUAL.md, "What the product already tells every member", promises that
// `cs-campaign orientation` prints the text the member will be given, and its
// Files table names ORIENTATION.md as the file that carries it. The promise is
// worth only as much as the path it names, so this holds the document against
// the constant the create path actually writes to.
//
// The claim it protects is the reason the command exists: a brief author who
// reads a description instead of the text is still guessing.
func TestManualNamesTheOrientationFileTheProductWrites(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	manual, err := os.ReadFile(filepath.Join(root, "MANUAL.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(manual)
	if !strings.Contains(body, "~/"+guestOrientationFile) {
		t.Errorf("MANUAL.md does not name %q, the path create writes the orientation to", "~/"+guestOrientationFile)
	}
	for _, want := range []string{
		"cs-campaign orientation acme --profile acme/profile.yaml --member backend",
		"Do not restate",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("MANUAL.md no longer carries %q, which the authoring guidance depends on", want)
		}
	}
}

// MANUAL.md and PLAYBOOK.md tell an operator what happens to a repository path
// that is not there yet, which is the ordinary case for a new application. It is
// the claim an author acts on when deciding where the work will live, and
// nothing else states it: SPEC R123 phrases it as an obligation on the tool
// rather than as guidance. This holds both documents against the behaviour.
func TestTheDocumentedNewRepositoryPathIsWhatPlanDoes(t *testing.T) {
	covmap.ProveCoreOnPass(t, "repo-adoption", covmap.TierUnit)
	for _, claim := range []string{
		"Every member needs at least one repository, and `validate` refuses one without it",
		"planned as an empty repository and created at `create`",
		"adopted, on `main`",
	} {
		if !strings.Contains(campaign.ManualMD, claim) {
			t.Errorf("MANUAL.md no longer states %q; this test names the sentence it keeps true", claim)
		}
	}
	for _, claim := range []string{
		"has to live in a repository on the host",
		"a campaign that declares none has nowhere to put its work",
	} {
		if !strings.Contains(campaign.PlaybookMD, claim) {
			t.Errorf("PLAYBOOK.md no longer states %q", claim)
		}
	}
	// And the behaviour those sentences describe. Planning an absent path pins a
	// base and creates nothing, which is what makes it safe to document as the
	// way to start something new.
	repo := filepath.Join(t.TempDir(), "not-here-yet")
	p, err := profileFromFlags("codex", []string{"worker=codex"}, "", 0, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	applyDefaults(&p)
	if err := resolveRepoRefs(&p); err != nil {
		t.Fatalf("an absent repository path must plan, not fail: %v", err)
	}
	if got := p.Agents["worker"].Repos[0]; !got.Initialize || got.ResolvedCommit != initialRepoCommit() {
		t.Fatalf("absent path did not plan as an empty repository: %+v", got)
	}
	if _, err := os.Stat(repo); !os.IsNotExist(err) {
		t.Fatalf("planning created the repository: %v", err)
	}
}

// SPEC.md § "The example campaign" carries a console block, and claims `make
// check` validates the example so "an example a reader copies stays true".
// Nothing did: b967ffb made a repository mandatory without revisiting either the
// example or the transcript, so the block showed a run that exits 0 when the
// command had started refusing. This is what makes the sentence true. It pins
// the digests too, because SPEC prints them: editing the profile or the mission
// without updating the block is the drift this catches.
func TestTheExampleCampaignValidatesAsTheSpecShows(t *testing.T) {
	covmap.ProveCoreOnPass(t, "profile-validation", covmap.TierUnit)
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	// The two lines the example is there to prove: the profile decodes, and the
	// mission and both briefs are found beside it.
	const stdout = "valid CampaignProfile ee13c41a4d1e\nmission e6802c6e662f, 2 role briefs\n"
	// It declares no `repos:` on purpose — the repository is the operator's to
	// supply — so the refusal is the documented end of this run, not a failure.
	const refusal = "no repository for orchestrator, worker"

	var out, errOut strings.Builder
	c := (&app{}).validateCmd()
	// As root.go runs it: the root command silences both, so a refusal prints
	// the error and not a usage dump. Executing the subcommand alone would put
	// usage on stdout and the transcript would not be the one an operator sees.
	c.SilenceUsage, c.SilenceErrors = true, true
	c.SetOut(&out)
	c.SetErr(&errOut)
	c.SetArgs([]string{filepath.Join(root, "testdata", "example-campaign", "profile.yaml")})
	err = c.Execute()
	if err == nil {
		t.Fatal("the example declares no repository, so validate must refuse it")
	}
	if got, _, _ := strings.Cut(err.Error(), "\n"); got != refusal {
		t.Errorf("validate refused with %q; SPEC.md's console block shows %q", got, refusal)
	}
	if out.String() != stdout {
		t.Errorf("validate printed:\n%s\nSPEC.md's console block shows:\n%s", out.String(), stdout)
	}

	// And the document itself, so the block cannot drift away from the run.
	spec, readErr := os.ReadFile(filepath.Join(root, "SPEC.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, claim := range []string{
		"$ cs-campaign validate --profile testdata/example-campaign/profile.yaml\n" + stdout + "cs-campaign: " + refusal,
		"It allocates nothing, and `make check` validates it",
	} {
		if !strings.Contains(string(spec), claim) {
			t.Errorf("SPEC.md no longer states %q; this test names the sentence it keeps true", claim)
		}
	}
}

// MANUAL.md and SPEC.md §6.1 tell the author of a replay that the wait chunk can
// be raised as well as lowered, and that a replay of real work should raise it.
// The author acts on that sentence: a chunk that elapses where the recorded one
// returned a reply leaves a recorded orchestrator accepting work that is not
// there, with no miss to show for it. So the sentence has to stay, and the
// override has to keep beating a smaller number from either direction.
func TestTheDocumentedWaitOverrideCanBeRaised(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range []string{"MANUAL.md", "SPEC.md"} {
		body, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "The override works in both directions") {
			t.Errorf("%s no longer says that CS_CAMPAIGN_WAIT_SECONDS can be raised as well as lowered", doc)
		}
	}
	t.Setenv("CS_CAMPAIGN_WAIT_SECONDS", "840")
	if got := protocol.WaitChunk(protocol.DefaultWaitSeconds); got != 840 {
		t.Errorf("an override above the default must stand, got %d", got)
	}
	if got := protocol.WaitChunk(60); got != 840 {
		t.Errorf("an override must beat a smaller --for, got %d", got)
	}
}

// MANUAL.md tells an operator to read `destroy --dry-run` before the one
// command that cannot be undone, and PLAYBOOK.md puts it in the harvest order.
// The operator acts on two promises: that the flag exists, and that a preview
// removes nothing. The second is held by
// TestDestroyDryRunNamesWhatItWouldRemoveAndRemovesNothing. This holds the first,
// and that the documents still say it.
func TestTheDocumentedDestroyPreviewExists(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	for doc, want := range map[string]string{
		"MANUAL.md":   "cs-campaign destroy <campaign> [--archive] [--archive-output DIR] [--force] [--dry-run]",
		"PLAYBOOK.md": "cs-campaign destroy acme --dry-run",
	} {
		body, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), want) {
			t.Errorf("%s no longer carries %q", doc, want)
		}
	}
	flag := (&app{}).destroyCmd().Flags().Lookup("dry-run")
	if flag == nil {
		t.Fatal("destroy has no --dry-run, which MANUAL.md and PLAYBOOK.md tell an operator to run first")
	}
	if flag.Usage != "resolve only; destroy nothing" {
		t.Errorf("destroy --dry-run describes itself as %q", flag.Usage)
	}
}

// MANUAL.md and PLAYBOOK.md tell an operator to read the record age on a
// `node-stopped` line before nudging an orchestrator. The operator acts on
// that: a nudge sent to a working orchestrator is the most expensive kind of
// intervention. So the documents have to keep the sentence, and the line has
// to keep carrying the age, without the age ever moving the state.
func TestTheDocumentedRecordAgeIsOnAStoppedLine(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	for doc, want := range map[string]string{
		"MANUAL.md":   "session record changed 40s ago",
		"PLAYBOOK.md": "It says when the orchestrator's own session record",
		"SPEC.md":     "That report **MUST NOT** decide a ladder move, and **MUST NOT** decide a state outside the",
	} {
		body, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), want) {
			t.Errorf("%s no longer carries %q", doc, want)
		}
	}
	now := int64(1_700_000_000)
	facts := protocol.Facts{
		Msgs:    []protocol.Msg{{ID: "m1", MTime: now - 5000, Name: "m1.md"}},
		Replies: map[string]bool{},
		Record:  now - 40,
	}
	o := protocol.Compute(facts, false, protocol.Blind{}, map[string]bool{}, protocol.Policy{}, now)
	if o.State != protocol.StateStopped {
		t.Fatalf("a record that changed 40s ago must not move the state: %+v", o)
	}
	if !strings.HasSuffix(o.Detail, "session record changed 40s ago") {
		t.Errorf("the stopped line reads %q, and the documents promise the record's age on it", o.Detail)
	}
}

// SPEC.md R64 lets the record's age decide one state: an idle orchestrator whose
// record has been still past one wait chunk and the settling window is stuck.
// MANUAL.md quotes the line at the defaults, and PLAYBOOK.md and PROTOCOL.md tell
// the operator to have the orchestrator wait in the foreground, which is what
// keeps its turns ones a driver records (SAC-067).
func TestTheDocumentedIdleOrchestratorLineIsTheOneComputed(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	const line = "idle with no turn driven, and its session record still for 12m,\npast one wait chunk and its margin (9m)"
	for doc, want := range map[string]string{
		"SPEC.md":     "Once its session record has been still for longer than one `wait` chunk (R126) plus the\nsettling window, it **MUST** read `node-stuck`",
		"MANUAL.md":   line,
		"PLAYBOOK.md": "**Have it call `wait` in the foreground**",
		"PROTOCOL.md": "**The orchestrator calls the wait in the foreground**",
	} {
		body, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), want) {
			t.Errorf("%s no longer carries %q", doc, want)
		}
	}
	t.Setenv("CS_CAMPAIGN_WAIT_SECONDS", "")
	now := int64(1_700_000_000)
	facts := protocol.Facts{
		Msgs:     []protocol.Msg{{ID: protocol.MissionID, MTime: now - 5000, Name: "m1.md"}},
		Replies:  map[string]bool{},
		Agent:    "idle",
		Record:   now - 12*60,
		TurnEnds: []protocol.TurnEnd{{At: now - 4000}},
	}
	o := protocol.Compute(facts, false, protocol.Blind{}, map[string]bool{}, protocol.Policy{}, now)
	if o.State != protocol.StateStuck || !strings.Contains(o.Detail, strings.ReplaceAll(line, "\n", " ")) {
		t.Errorf("the documents quote a line the code does not print: %+v", o)
	}
}

// The probe and the fleet audit each name where a CLI family keeps its session
// record inside a member. They are one fact written twice, in two packages, and
// a family added to one and not the other would leave its stopped line silent.
func TestTheProbeAndTheAuditAgreeOnWhereARecordLives(t *testing.T) {
	for cli, audited := range evidenceGlob {
		dir := protocol.RecordDir(cli)
		if dir == "" {
			t.Errorf("the audit knows where %s keeps its record and the probe does not", cli)
			continue
		}
		if audited != dir && filepath.Dir(audited) != dir {
			t.Errorf("%s: the audit reads %q and the probe reads under %q", cli, audited, dir)
		}
	}
}

// MANUAL.md: "A member whose readback passed is not asked again, as long as its
// seeded files are unchanged, and `create` prints `confirmed its briefing in
// d001 on an earlier create`." An operator resuming a create reads that line to
// know nobody was briefed twice.
func TestTheDocumentedResumedReadbackLineIsWhatCreatePrints(t *testing.T) {
	claim := "confirmed its briefing in d001 on an earlier create"
	if !strings.Contains(campaign.ManualMD, "A member whose readback passed is not asked again") || !strings.Contains(campaign.ManualMD, claim) {
		t.Errorf("MANUAL.md no longer states what a resumed create does with a readback that passed")
	}
	a, state := readbackMemberApp(t, "unused")
	run, member := readbackCampaign()
	earlierReadback(t, state, true, goodReadback)
	member.Readback = recorded(t, "")
	var out strings.Builder
	if detail, _ := a.readbackOne(t.Context(), &out, run, member); detail != "" || !strings.Contains(out.String(), claim) {
		t.Errorf("a resumed create printed %q (detail %q); MANUAL.md promises %q", out.String(), detail, claim)
	}
}

// MANUAL.md: "The mission dispatch states the deadline as an instant", and on a
// resumed create "tells the orchestrator that a deadline read before it is out
// of date". The orchestrator is the one enforcing the deadline, so this is the
// sentence an operator relies on after resuming a create.
func TestTheDocumentedMissionDeadlineIsWhatTheMissionSays(t *testing.T) {
	for _, claim := range []string{"The mission dispatch states the deadline as an instant.", "a deadline read before it is out of date"} {
		if !strings.Contains(campaign.ManualMD, claim) {
			t.Errorf("MANUAL.md no longer states %q; this test names the sentence it keeps true", claim)
		}
	}
	mission := missionDispatchBody(completeInputs(t), time.Date(2026, 9, 22, 15, 32, 51, 0, time.UTC), true)
	if !strings.Contains(mission, "deadline is 2026-09-22T15:32:51Z") || !strings.Contains(mission, "is out of date") {
		t.Errorf("a resumed create's mission does not say what MANUAL.md promises:\n%s", mission)
	}
}

// MANUAL.md: "Without `--continue`, a send to an agent whose dispatch is open
// is refused", and "Nothing is delivered on a refusal". The guest binary's own
// tests, TestSendRefusesToContinueAnOpenDispatchUnasked and
// TestSendRefusesToOpenWhenAskedToContinue, hold the behaviour. This holds the
// sentences, so neither side moves without the other being looked at.
func TestTheDocumentedSendRefusalNamesTheFlag(t *testing.T) {
	for _, claim := range []string{
		"Without `--continue`, a send to an agent whose dispatch is open is refused",
		"With it, a send to an agent that has replied is refused",
		"Nothing is delivered on a refusal",
	} {
		if !strings.Contains(campaign.ManualMD, claim) {
			t.Errorf("MANUAL.md no longer states %q; this test names the sentence it keeps true", claim)
		}
	}
}

// MANUAL.md quotes the warning a copied agent login gets, and says a held
// credential is repaired inside the member where a lent one is repaired on the
// host (SAC-068). The operator acts on both, so the quote has to be what the
// code prints and the repair has to stay split.
func TestTheDocumentedCopiedLoginWarningIsTheOnePrinted(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	manual, err := os.ReadFile(filepath.Join(root, "MANUAL.md"))
	if err != nil {
		t.Fatal(err)
	}
	playbook, err := os.ReadFile(filepath.Join(root, "PLAYBOOK.md"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	warnCopiedCredentials(&buf, []model.Member{{Name: "orchestrator", Profile: model.MemberProfile{Auth: model.Auth{
		InheritAgentLogin: []string{"claude"}, Credentials: model.CredentialInherit}}}})
	var printed string
	for line := range strings.SplitSeq(buf.String(), "\n") {
		if strings.Contains(line, "copied agent login") {
			printed = line
		}
	}
	flat := strings.Join(strings.Fields(string(manual)), " ")
	if printed == "" || !strings.Contains(flat, printed) {
		t.Errorf("MANUAL.md does not quote the login warning as printed: %q", printed)
	}
	for doc, wants := range map[string][]string{
		"MANUAL.md": {"A held one is a copy inside the member, and neither a sign-in on the host nor a restart reaches it.",
			"A copied one, granted with `inheritAgentLogin`, does not renew and is not read again."},
		"PLAYBOOK.md": {"A held one needs a sign-in inside the member, or the seat recreated.",
			"A copied login does not renew."},
	} {
		body := string(manual)
		if doc == "PLAYBOOK.md" {
			body = string(playbook)
		}
		body = strings.Join(strings.Fields(body), " ")
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s no longer carries %q", doc, want)
			}
		}
	}
}
