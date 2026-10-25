package cli

// The documentation claims a reader would act on and could not check: state
// safety, and how a credential reaches a member. The rest of this file used to
// pin the await/journal machinery, which the protocol deleted — completion is a
// reply artifact now, and no dispatch state is stored to be journaled or
// clobbered.

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
// without updating the block is the drift this catches. Since SAC-093 the
// example names a repository that need not exist, so the run ends at exit 0
// and orientation and plan can render it.
func TestTheExampleCampaignValidatesAsTheSpecShows(t *testing.T) {
	covmap.ProveCoreOnPass(t, "profile-validation", covmap.TierUnit)
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	// The two lines the example is there to prove: the profile decodes, and the
	// mission and both briefs are found beside it.
	const stdout = "valid CampaignProfile dc6986ba9118\nmission e6802c6e662f, 2 role briefs\n"

	var out, errOut strings.Builder
	c := (&app{}).validateCmd()
	// As root.go runs it: the root command silences both, so a refusal prints
	// the error and not a usage dump. Executing the subcommand alone would put
	// usage on stdout and the transcript would not be the one an operator sees.
	c.SilenceUsage, c.SilenceErrors = true, true
	c.SetOut(&out)
	c.SetErr(&errOut)
	c.SetArgs([]string{filepath.Join(root, "testdata", "example-campaign", "profile.yaml")})
	if err = c.Execute(); err != nil {
		t.Fatalf("validate refused the example, and SPEC.md's console block shows it passing: %v", err)
	}
	if out.String() != stdout {
		t.Errorf("validate printed:\n%s\nSPEC.md's console block shows:\n%s", out.String(), stdout)
	}

	// SPEC says orientation and plan run on it too, and that nothing is made.
	profile := filepath.Join(root, "testdata", "example-campaign", "profile.yaml")
	t.Setenv("HOME", t.TempDir())
	if text, _, err := runOrientation(t, "example", "--profile", profile); err != nil || !strings.Contains(text, "===== worker (agent)") {
		t.Errorf("orientation did not render the example's members (%v):\n%s", err, text)
	}
	plan := (&app{store: store.Store{Dir: t.TempDir()}}).createCmd(true)
	plan.SilenceUsage, plan.SilenceErrors = true, true
	var planned strings.Builder
	plan.SetOut(&planned)
	plan.SetErr(new(strings.Builder))
	plan.SetArgs([]string{"example", "--profile", profile})
	if err := plan.Execute(); err != nil || !strings.Contains(planned.String(), `"initialize": true`) {
		t.Errorf("plan did not plan the example's absent repository as a first commit (%v):\n%s", err, planned.String())
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "src", "report-generator")); !os.IsNotExist(err) {
		t.Errorf("planning the example made its repository: %v", err)
	}

	// And the document itself, so the block cannot drift away from the run.
	spec, readErr := os.ReadFile(filepath.Join(root, "SPEC.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, claim := range []string{
		"$ cs-campaign validate --profile testdata/example-campaign/profile.yaml\n" + stdout + "```",
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
// the operator the orchestrator waits in the foreground, which is what keeps its
// turns ones a driver records (SAC-067). The orientation is where the
// orchestrator itself is told so (SAC-069).
func TestTheDocumentedIdleOrchestratorLineIsTheOneComputed(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	const line = "idle with no turn driven, and its session record still for 12m,\npast one wait chunk and its margin (9m)"
	for doc, want := range map[string]string{
		"SPEC.md":     "Once its session record has been still for longer than one `wait` chunk (R126) plus the\nsettling window, it **MUST** read `node-stuck`",
		"MANUAL.md":   line,
		"PLAYBOOK.md": "**It calls `wait` in the foreground**",
		"PROTOCOL.md": "**The orchestrator calls the wait in the foreground**",
		"internal/cli/assets/orientation.md.tmpl": "**Never end your turn without either calling `wait` in the\nforeground or replying.**",
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

// The orientation is the only document an orchestrator reads, so a verb it
// does not name does not exist for the model. It used to teach `fetch` and
// never `push`, and campaigns improvised the handoff (SAC-029). Both halves
// carry it now: the orchestrator is told how to deliver work, and the agent
// where delivered work lands.
func TestTheOrientationTeachesPush(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, "internal/cli/assets/orientation.md.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"`cs-campaign-member push <member> [repo] --commit <sha>`",
		"a dispatch may name `refs/campaign/orchestrator` in\n  your clone",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the orientation no longer carries %q", want)
		}
	}
}

// MANUAL.md: "**The page's digests are the ones `create` records**". A reader
// holding a packet of a campaign already created settles whether it shows what
// the members were given by comparing the page against the campaign record, so
// every digest create records has to appear on the page as the same string.
func TestTheDocumentedPacketDigestsAreTheOnesCreateRecords(t *testing.T) {
	if !strings.Contains(campaign.ManualMD, "**The page's digests are the ones `create` records**") {
		t.Errorf("MANUAL.md no longer states that the packet's digests are create's; this test names the sentence it keeps true")
	}
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	profile := filepath.Join(root, "testdata", "example-campaign", "profile.yaml")
	page := filepath.Join(t.TempDir(), "review.html")
	if _, err := runPacket(t, false, "example", "--profile", profile, "--file", page); err != nil {
		t.Fatal(err)
	}
	data, err := readPacket(page)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{store: store.Store{Dir: t.TempDir()}}
	run, decoded, err := a.planCampaign(createOpts{profile: profile}, "example", true)
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := loadCampaignInputs(profile, decoded)
	if err != nil {
		t.Fatal(err)
	}
	if data.Campaign.ProfileDigest != run.ProfileDigest || data.Profile.Digest != run.ProfileDigest {
		t.Errorf("create records profile %s, and the page shows %s and %s", run.ProfileDigest, data.Campaign.ProfileDigest, data.Profile.Digest)
	}
	onPage := map[string]string{}
	for _, f := range data.Files {
		onPage[f.Path] = f.Digest
	}
	for _, m := range run.Members {
		recorded := inputs.digests(m)
		if len(recorded) == 0 {
			t.Fatalf("%s: create records no seeded inputs, so this test proves nothing", m.Name)
		}
		for name, digest := range recorded {
			// A member's own brief is recorded by its file name, and the page
			// keys it by its path beside the profile.
			shown, ok := onPage[name]
			if !ok {
				shown = onPage[rolesDirName+"/"+name]
			}
			if shown != digest {
				t.Errorf("%s: create records %s as %s, and the page shows %q", m.Name, name, digest, shown)
			}
		}
	}
}

// MANUAL.md quotes an observe line that carries the node's context reading, and
// SPEC.md says the reading decides no state. An operator reads the note to see
// a compaction coming, so the quoted line has to be the one Compute prints, and
// the reading must leave the state where it was.
func TestTheDocumentedContextNoteIsOnTheLine(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	const line = "open 4210s · 1 cont, 0 restarts · context 142k · compacted 2 times, last 38m ago"
	for doc, want := range map[string]string{
		"MANUAL.md": line,
		"SPEC.md":   "That report **MUST NOT** decide a state or a ladder move.",
	} {
		body, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatal(err)
		}
		// Prose is wrapped where its lines happen to fall, so compare words.
		if !strings.Contains(strings.Join(strings.Fields(string(body)), " "), want) {
			t.Errorf("%s no longer carries %q", doc, want)
		}
	}
	now := int64(1_700_000_000)
	facts := protocol.Facts{
		Msgs: []protocol.Msg{
			{ID: "m1", MTime: now - 4210, Name: "m1.md"},
			{ID: "m1", Seq: 1, MTime: now - 3000, Name: "m1.001.md"},
		},
		Replies: map[string]bool{},
		Drivers: 1,
		Context: &protocol.ContextReading{Tokens: 142_000, Compactions: 2, Compacted: now - 38*60},
	}
	o := protocol.Compute(facts, false, protocol.Blind{}, map[string]bool{}, protocol.Policy{}, now)
	if o.State != protocol.StateWorking {
		t.Fatalf("a context reading must not move the state: %+v", o)
	}
	if o.Detail != line {
		t.Errorf("the working line reads %q, and MANUAL.md quotes %q", o.Detail, line)
	}
}

// docWords reads one document from the tree with its whitespace collapsed,
// because prose is wrapped where its lines happen to fall.
func docWords(t *testing.T, root, doc string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, doc))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(string(body)), " ")
}

// MANUAL.md quotes the sentence a member is asked to hand off with, and says it
// states no number. SPEC.md R146 holds the same of everything a member reads,
// because a member that knows how far it is from its line rations its context
// against it. So the quote has to be the ask, the ask has to carry no figure,
// and a lined seat's orientation must state its line in no spelling while it
// says what R146 asks it to say instead.
func TestTheDocumentedAskStatesNoNumber(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	for doc, want := range map[string]string{
		"MANUAL.md": handoffAsk,
		"SPEC.md":   "**R146.** What a member reads **MUST NOT** state its context line or its context reading.",
	} {
		if !strings.Contains(docWords(t, root, doc), want) {
			t.Errorf("%s no longer carries %q", doc, want)
		}
	}
	if strings.ContainsAny(handoffAsk, "0123456789{}") {
		t.Errorf("the ask states a number, or keeps a placeholder for one: %s", handoffAsk)
	}
	run := &model.Campaign{Name: "acme", Members: []model.Member{
		{Name: "orchestrator", Role: "orchestrator", CLI: "claude", HandoffTokens: 61_234},
		{Name: "dev", Role: "agent", CLI: "claude", HandoffTokens: 61_234},
	}}
	for _, member := range run.Members {
		text, _, err := buildOrientation(run, member, campaignInputs{})
		if err != nil {
			t.Fatal(err)
		}
		for _, figure := range []string{"61234", "61,234", "61k", "61.2k"} {
			if strings.Contains(text, figure) {
				t.Errorf("%s's orientation states its line as %q", member.Name, figure)
			}
		}
		flat := strings.Join(strings.Fields(text), " ")
		for _, want := range []string{"never ration your context", "commit your work on your branch", "trust git"} {
			if !strings.Contains(flat, want) {
				t.Errorf("%s's orientation no longer says %q, which SPEC.md R146 asks of a lined seat", member.Name, want)
			}
		}
	}
}

// MANUAL.md and SPEC.md name the message a member reads when its hand-off was
// not taken, and MANUAL.md quotes what that message lists and what the check
// reads. An operator goes by the name in a member's input channel, and by the
// sentence in `wait`'s and `resume`'s lines, so each has to be what the code
// writes.
func TestTheDocumentedCommitAskIsTheOneSent(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	manual := docWords(t, root, "MANUAL.md")
	for doc, body := range map[string]string{"MANUAL.md": manual, "SPEC.md": docWords(t, root, "SPEC.md")} {
		if !strings.Contains(body, "`d007.005.commit.md`") {
			t.Errorf("%s no longer names the .commit message `d007.005.commit.md`", doc)
		}
	}
	var msgs []protocol.Msg
	for i, name := range []string{"d007.md", "d007.001.md", "d007.002.restart.md", "d007.003.resume.md", "d007.004.handoff.md"} {
		m, ok := protocol.ParseMsgName(name, int64(100*(i+1)))
		if !ok {
			t.Fatalf("%s is not read as a message", name)
		}
		msgs = append(msgs, m)
	}
	if got := protocol.NextCommitName(msgs, "d007"); got != "d007.005.commit.md" {
		t.Errorf("the documents name d007.005.commit.md, and the code mints %s", got)
	}
	if m, ok := protocol.ParseMsgName("d007.005.commit.md", 600); !ok || !m.Commit {
		t.Errorf("d007.005.commit.md is not read as a commit ask: %+v", m)
	}
	const dirty = "~/product has work not committed (2 paths, the first notes.md)"
	if got := protocol.ParseHandoffCheck("DIRTY\tproduct\t2\tnotes.md\n"); len(got) != 1 || got[0] != dirty {
		t.Fatalf("MANUAL.md quotes %q, and the check reads as %q", dirty, got)
	}
	body := protocol.CommitBody("d007", []string{dirty})
	for _, want := range []string{dirty, ".git/info/exclude", "No rung is spent."} {
		if !strings.Contains(body, want) {
			t.Errorf("the .commit message does not say %q, which MANUAL.md promises:\n%s", want, body)
		}
	}
	script := protocol.HandoffCheckScript(map[string]string{"product": "0123456789abcdef"})
	for want, said := range map[string]string{
		"git -C \"$d\" status --porcelain": "`git status --porcelain`",
		"printf %.7s":                      "the hash's first seven characters",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("MANUAL.md says the check reads %s, and the script has no %q", said, want)
		}
		if !strings.Contains(manual, said) {
			t.Errorf("MANUAL.md no longer carries %q", said)
		}
	}
}

// MANUAL.md quotes what `resume` prints when it keeps the orchestrator's
// session, and tells the operator to run it again at the next `fresh session
// next`. This runs the host's move against a member whose check finds work not
// committed. The lines are the quoted ones, the session is carried on rather
// than ended, and the message it reads is a .commit one.
func TestTheDocumentedKeptSessionLinesAreWhatResumePrints(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	calls, remote := filepath.Join(dir, "calls"), filepath.Join(dir, "remote")
	now := time.Now().Unix()
	probe := fmt.Sprintf(`MSG %d m1.md\nDRIVERS 0\nAGENT idle\nTURNEND %d 0 handoff - context full\n`, now-700, now-5)
	sandbox := filepath.Join(dir, "fake-sandbox")
	fake := "#!/bin/sh\ncase \"$5\" in\n" +
		"  *DRIVERS*) printf '" + probe + "' ;;\n" +
		"  *NOHANDOFF*) printf 'DIRTY\\tproduct\\t2\\tnotes.md\\n' ;;\n" +
		"  *) echo \"$5\" >> " + calls + "; cat > /dev/null ;;\nesac\n"
	if err := os.WriteFile(sandbox, []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	orch := model.Member{Name: "orchestrator", Role: "orchestrator", CLI: "claude", Sandbox: "box", Ref: "orch.g", Session: model.Session{Name: "camp-orch"}}
	home := t.TempDir()
	t.Setenv("HOME", home)
	sessions := filepath.Join(home, ".cs-claude-remote-sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, orch.Session.Name), []byte("live\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for _, tool := range []string{"cs-claude-remote", "cs-claude-remote-forget"} {
		stub := "#!/bin/sh\necho \"" + tool + " $*\" >> " + remote + "\n"
		if err := os.WriteFile(filepath.Join(bin, tool), []byte(stub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	a := &app{store: store.Store{Dir: filepath.Join(dir, "state")}, sandbox: sandboxCLI{Bin: sandbox}}
	pol := protocol.Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100_000, ProviderWaitSeconds: 3600, BlindProbes: 1, PollSeconds: 1, SettlingSeconds: 1, Handoffs: 10}

	said, err := a.hostResumeOrchestrator(t.Context(), orch, pol)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	want := "kept the orchestrator's session: its work and its handoff do not agree (~/product has work not committed (2 paths, the first notes.md))\n" +
		"m1 carried on in the same session, asked to commit first, no rung spent; resume again when observe says fresh session next"
	if said != want {
		t.Errorf("resume said %q, and MANUAL.md quotes %q", said, want)
	}
	manual := docWords(t, root, "MANUAL.md")
	for line := range strings.SplitSeq(want, "\n") {
		if !strings.Contains(manual, line) {
			t.Errorf("MANUAL.md no longer quotes %q", line)
		}
	}
	// A file nothing wrote reads as empty, which fails the checks below.
	sent, _ := os.ReadFile(calls)
	if !strings.Contains(string(sent), "m1.001.commit.md") {
		t.Errorf("the kept session must read m1.001.commit.md; the host wrote:\n%s", sent)
	}
	ranBytes, _ := os.ReadFile(remote)
	ran := string(ranBytes)
	if strings.Contains(ran, "--kill") || strings.Contains(ran, "-forget") || !strings.Contains(ran, "--resume "+orch.Session.Name) {
		t.Errorf("a kept session is carried on, never ended:\n%s", ran)
	}
}

// handedOffAt observes dispatch id under pol. Its first session worked first
// seconds, and each later one began on a message of the kind given, 5s after
// the hand-off before it, and worked secs before it handed off too. The look
// is 10s after the newest hand-off.
func handedOffAt(t *testing.T, pol protocol.Policy, id string, first, secs int64, kinds ...string) protocol.Observation {
	t.Helper()
	at := int64(1_700_000_000)
	msgs := []protocol.Msg{{ID: id, MTime: at, Name: id + ".md"}}
	at += first
	ends := []protocol.TurnEnd{{At: at, Class: protocol.ClassHandoff}}
	for i, kind := range kinds {
		at += 5
		name := fmt.Sprintf("%s.%03d.%s.md", id, i+1, kind)
		m, ok := protocol.ParseMsgName(name, at)
		if !ok {
			t.Fatalf("%s is not read as a message", name)
		}
		msgs = append(msgs, m)
		at += secs
		ends = append(ends, protocol.TurnEnd{At: at, Class: protocol.ClassHandoff})
	}
	facts := protocol.Facts{Msgs: msgs, Replies: map[string]bool{}, Agent: "idle", TurnEnds: ends}
	return protocol.Compute(facts, false, protocol.Blind{}, map[string]bool{}, pol, at+10)
}

// lineBoundPolicy is the policy of a campaign with a line, as create records
// it, with every other bound out of reach.
var lineBoundPolicy = protocol.Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100_000, ProviderWaitSeconds: 3600,
	BlindProbes: 1, PollSeconds: 1, SettlingSeconds: 1}.WithHandoffBound(true)

// MANUAL.md quotes the observe line of a member that keeps handing off with
// its work not committed, and the count a pending hand-off carries once one was
// not taken. SPEC.md R142 bounds the rapid hand-offs in a row, and R144 counts
// the rapid hand-off of a kept session among them. Both quotes have to be
// what Compute prints for sessions that each handed off a minute in.
func TestTheDocumentedCommitCountsAreOnTheLine(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	const stuck = "handed off 10 times in a row, each within 5m of a fresh start, 4 of them after an ask to commit first — hand-off bound tripped"
	const pending = "2 hand-offs, 1 asked to commit"
	manual := docWords(t, root, "MANUAL.md")
	for _, want := range []string{stuck, pending} {
		if !strings.Contains(manual, want) {
			t.Errorf("MANUAL.md no longer carries %q", want)
		}
	}
	spec := docWords(t, root, "SPEC.md")
	for _, want := range []string{
		"The rapid hand-offs in a row at the tail of one dispatch **MUST** be bounded by `handoffs`.",
		"The hand-off its turn ends in counts against `handoffs` when it is rapid, as any other does (R142).",
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("SPEC.md no longer says %q", want)
		}
	}
	kinds := append(slices.Repeat([]string{"handoff", "commit"}, 4), "handoff", "handoff")
	if o := handedOffAt(t, lineBoundPolicy, "d003", 3600, 60, kinds...); o.State != protocol.StateStuck || !strings.HasPrefix(o.Detail, stuck) {
		t.Errorf("six rapid hand-offs and four rapid commit asks read %s (%s); MANUAL.md quotes %q", o.State, o.Detail, stuck)
	}
	if o := handedOffAt(t, lineBoundPolicy, "d003", 3600, 60, "handoff", "commit", "handoff"); o.State != protocol.StateStopped || o.NextMove != "handoff" || !strings.Contains(o.Detail, pending) {
		t.Errorf("a pending hand-off after one ask reads %s (%s); MANUAL.md quotes %q", o.State, o.Detail, pending)
	}
}

// The documents say the hand-off bound counts only rapid hand-offs in a row,
// give its two numbers, and say what a long mission and a member that never
// commits do against it. Each claim is held to what create records and what
// Compute prints.
func TestTheDocumentedHandoffBoundCountsOnlyRapidHandoffs(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	says := func(doc string, sentences ...string) {
		t.Helper()
		words := docWords(t, root, doc)
		for _, want := range sentences {
			if !strings.Contains(words, want) {
				t.Errorf("%s no longer says %q", doc, want)
			}
		}
	}

	// The numbers: recorded only with a line, as 10 and 300.
	rows := []string{"| `handoffs` | 10 when any seat has a line, else absent |", "| `rapidHandoffSeconds` | 300 when any seat has a line, else absent |"}
	says("MANUAL.md", append(rows, "Both numbers are recorded only when some seat has a line, as 10 and 300 unless the profile names them.")...)
	says("SPEC.md", append(rows, "When any seat has a line, `handoffs` defaults to 10 and `rapidHandoffSeconds` to 300, and an explicit value of either is kept.")...)
	resolved := protocol.Policy{}.Resolve()
	if got := resolved.WithHandoffBound(true); got.Handoffs != 10 || got.RapidHandoffSeconds != 300 {
		t.Errorf("a campaign with a line records handoffs %d and rapidHandoffSeconds %d; the documents say 10 and 300", got.Handoffs, got.RapidHandoffSeconds)
	}
	if got := resolved.WithHandoffBound(false); got.Handoffs != 0 || got.RapidHandoffSeconds != 0 {
		t.Errorf("a campaign with no line records handoffs %d and rapidHandoffSeconds %d; the documents say neither", got.Handoffs, got.RapidHandoffSeconds)
	}

	// A long mission: fifteen hand-offs, each after twenty minutes of work.
	says("MANUAL.md", "So a long dispatch, such as the orchestrator's mission, hands off as often as its work needs.")
	says("PROTOCOL.md", "So a long dispatch hands off as often as its work needs.")
	says("PLAYBOOK.md", "A long mission's hand-offs each follow a session's work, so they do not count.")
	if o := handedOffAt(t, lineBoundPolicy, "m1", 1200, 1200, slices.Repeat([]string{"handoff"}, 15)...); o.State != protocol.StateStopped || o.NextMove != "handoff" {
		t.Errorf("a mission of fifteen long sessions reads %s (%s); the documents say it hands off as often as it needs", o.State, o.Detail)
	}

	// A dispatch's first session: at a bound of one, its quick hand-off moves.
	says("MANUAL.md", "A dispatch's first session is never rapid, and a session that works longer ends the run.")
	says("SPEC.md", "A dispatch's first session began on neither, so its hand-off is never rapid.")
	one := lineBoundPolicy
	one.Handoffs = 1
	if o := handedOffAt(t, one, "d003", 30, 0); o.State != protocol.StateStopped || o.NextMove != "handoff" {
		t.Errorf("a first session that handed off 30s in reads %s (%s) at a bound of one; the documents say it is never rapid", o.State, o.Detail)
	}

	// A member that never commits is asked on every hand-off.
	says("PLAYBOOK.md", "A seat that never commits is asked again on every hand-off, and each ask's turn is rapid, so it still trips the `handoffs` bound.")
	says("PROTOCOL.md", "A node that never commits what its handoff describes is asked again at every hand-off. Each ask's turn is rapid, so that node ends there too.")
	if o := handedOffAt(t, lineBoundPolicy, "d003", 1200, 30, slices.Repeat([]string{"commit"}, 10)...); o.State != protocol.StateStuck ||
		!strings.Contains(o.Detail, "10 of them after an ask to commit first — hand-off bound tripped") {
		t.Errorf("ten asks in a row read %s (%s); the documents say they trip the bound", o.State, o.Detail)
	}
}

// keptAgentLine is the line the guest's `wait` prints when it keeps an agent's
// session, as MANUAL.md quotes it.
const keptAgentLine = "kept %s's session on %s: its work and its handoff do not agree (%s); asked it to commit first, no rung spent"

// MANUAL.md quotes two lines the guest binary prints: `send`'s receipt for a
// dispatch opened in a fresh session, and `wait`'s line when it keeps an
// agent's session. The guest is its own package, so its lines are held here by
// their text in its source. The send line must still hold the receipt
// `<member>/dNNN opened`, which the viewer joins a trace on.
func TestTheDocumentedGuestLinesAreTheOnesItPrints(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(root, "cmd", "cs-campaign-member", "dispatcher.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`line += " opened"`, `line += " in a fresh session"`, `"` + keptAgentLine + `"`} {
		if !strings.Contains(string(src), want) {
			t.Errorf("the guest no longer prints %s, which MANUAL.md quotes", want)
		}
	}
	manual := docWords(t, root, "MANUAL.md")
	const sent = "backend/d004 opened in a fresh session"
	if !strings.Contains(manual, "`"+sent+"`") || !strings.HasPrefix(sent, "backend/d004 opened") ||
		!strings.Contains(manual, "`<member>/dNNN opened` from `send`") {
		t.Errorf("MANUAL.md no longer quotes %q beside the receipt the viewer joins on", sent)
	}
	kept := fmt.Sprintf(keptAgentLine, "backend", "d003", "~/product has work not committed (2 paths, the first notes.md)")
	if !strings.Contains(manual, kept) {
		t.Errorf("MANUAL.md no longer quotes %q", kept)
	}
}

// MANUAL.md quotes the refusal `validate` prints for freshPerDispatch on the
// orchestrator, and says the switch is on for an agent when its own policy or
// defaults.policy sets it. SPEC.md R145 says the orchestrator never has it, so
// a default under defaults.policy must not reach it either.
func TestTheDocumentedFreshPerDispatchIsWhatValidateAndCreateDo(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	p, err := profileFromFlags("codex", []string{"backend=codex", "qa=codex"}, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	p.Orchestrator.Policy.FreshPerDispatch = true
	refused := validateProfile(p)
	if refused == nil {
		t.Fatal("validate must refuse freshPerDispatch on the orchestrator")
	}
	if !strings.Contains(docWords(t, root, "MANUAL.md"), "cs-campaign: "+refused.Error()) {
		t.Errorf("MANUAL.md does not quote the refusal as validate prints it: %v", refused)
	}
	if !strings.Contains(docWords(t, root, "SPEC.md"), "`validate` **MUST** refuse the switch on the orchestrator") {
		t.Error("SPEC.md R145 no longer says validate refuses the switch on the orchestrator")
	}
	p.Orchestrator.Policy.FreshPerDispatch = false
	backend := p.Agents["backend"]
	backend.Policy.FreshPerDispatch = true
	p.Agents["backend"] = backend
	fresh := func() map[string]bool {
		if err := validateProfile(p); err != nil {
			t.Fatalf("an agent may have freshPerDispatch, on its own or by default: %v", err)
		}
		got := map[string]bool{}
		for _, m := range buildCampaign("acme", p, "", "", time.Unix(1_800_000_000, 0)).Members {
			got[m.Name] = m.FreshPerDispatch
		}
		return got
	}
	if got := fresh(); !got["backend"] || got["qa"] || got["orchestrator"] {
		t.Errorf("an agent's own switch must reach that agent alone: %v", got)
	}
	p.Defaults.Policy.FreshPerDispatch = true
	if got := fresh(); !got["backend"] || !got["qa"] || got["orchestrator"] {
		t.Errorf("defaults.policy must reach every agent and never the orchestrator: %v", got)
	}
}

// A comment that cites a SPEC.md rule sends its reader to that rule. A rule
// cited before it is written, or after it is renumbered, sends them nowhere.
// cs-lint refs holds section citations, and this holds rule citations.
func TestEverySpecRuleTheSourceCitesIsWritten(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := os.ReadFile(filepath.Join(root, "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	cite := regexp.MustCompile(`SPEC\.md,? (R[0-9]+[a-z]?)\b`)
	seen := 0
	for _, dir := range []string{"internal", "cmd", "dispatch-viewer"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range cite.FindAllStringSubmatch(string(body), -1) {
				seen++
				if !bytes.Contains(spec, []byte("**"+m[1]+".**")) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s cites SPEC.md %s, which SPEC.md does not write", rel, m[1])
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if seen == 0 {
		t.Fatal("the source cites no SPEC.md rule, so this test proves nothing")
	}
}
