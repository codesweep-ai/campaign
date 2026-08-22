package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
)

func testProfile() model.Profile {
	return model.Profile{
		APIVersion: model.APIVersion, Kind: "CampaignProfile",
		Orchestrator: model.MemberProfile{CLI: "codex"},
		Agents: map[string]model.MemberProfile{
			"backend": {CLI: "codex"},
			"qa":      {CLI: "opencode"},
		},
	}
}

// A missing brief must name itself. The whole point of checking at validate is
// that it costs nothing and happens before a VM exists — an error that says
// only "invalid" would send the operator hunting for what the product already
// knows.
func TestMissingBriefsAreNamedIndividually(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile.yaml")
	if err := os.WriteFile(profile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadCampaignInputs(profile, testProfile())
	if err == nil {
		t.Fatal("expected missing briefs to fail")
	}
	for _, want := range []string{"mission.md", "roles/orchestrator.md", "roles/backend.md", "roles/qa.md"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q:\n%s", want, err)
		}
	}
}

// There is deliberately no roles/_all.md fallback. A member that silently
// inherited a generic brief would restate that brief perfectly in its readback,
// so the check that exists to catch an unbriefed member could not see it.
func TestNoCatchAllBriefSatisfiesAMember(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile.yaml")
	if err := os.WriteFile(profile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, missionFileName), []byte("m"), 0o600); err != nil {
		t.Fatal(err)
	}
	roles := filepath.Join(dir, rolesDirName)
	if err := os.MkdirAll(roles, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"_all", "orchestrator", "backend"} {
		if err := os.WriteFile(filepath.Join(roles, n+".md"), []byte("b"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := loadCampaignInputs(profile, testProfile())
	if err == nil || !strings.Contains(err.Error(), "roles/qa.md") {
		t.Fatalf("_all.md must not satisfy qa; got %v", err)
	}
}

// Flag-path creates carry no profile and therefore no briefs. They are a
// harness smoke test rather than a briefed campaign, so they must resolve to
// no inputs rather than an error — otherwise the quickest way to prove the
// plumbing works stops working.
func TestFlagPathCreateNeedsNoBriefs(t *testing.T) {
	in, err := loadCampaignInputs("", testProfile())
	if err != nil {
		t.Fatalf("flag path must not require briefs: %v", err)
	}
	if in.Declared {
		t.Error("flag-path inputs should not report as declared")
	}
	if cmd := in.seedCommand(model.Member{Name: "backend", Role: "agent"}); cmd != "" {
		t.Errorf("flag path seeded something: %q", cmd)
	}
}

func completeInputs(t *testing.T) campaignInputs {
	t.Helper()
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile.yaml")
	if err := os.WriteFile(profile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	seedBriefsFor(t, profile, "backend", "qa")
	in, err := loadCampaignInputs(profile, testProfile())
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// Visibility is per principal: an agent receives its own brief and nothing
// else. It must not learn the mission or its peers' scopes by accident, because
// the operator — not the product — decides what each member can see.
func TestAgentReceivesOnlyItsOwnBrief(t *testing.T) {
	in := completeInputs(t)
	got := in.seedCommand(model.Member{Name: "backend", Role: "agent"})
	if !strings.Contains(got, guestInputDir+"/backend.md") {
		t.Errorf("agent did not receive its own brief:\n%s", got)
	}
	for _, forbidden := range []string{"qa.md", missionFileName, guestRolesDir} {
		if strings.Contains(got, forbidden) {
			t.Errorf("agent was seeded %q, which the operator did not grant it:\n%s", forbidden, got)
		}
	}
}

// The orchestrator receives every agent's brief automatically. It allocates the
// work and cannot do that without knowing what each teammate owns; the
// alternative is an operator hand-copying a team table into the orchestrator's
// own brief, which drifts the moment either file is edited.
func TestOrchestratorReceivesEveryAgentBriefAndTheMission(t *testing.T) {
	in := completeInputs(t)
	got := in.seedCommand(model.Member{Name: "orchestrator", Role: "orchestrator"})
	for _, want := range []string{
		guestInputDir + "/" + missionFileName,
		guestInputDir + "/orchestrator.md",
		guestRolesDir + "/backend.md",
		guestRolesDir + "/qa.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("orchestrator missing %q:\n%s", want, got)
		}
	}
}

// A digest per seeded file, recorded on the campaign. Without it an archive
// showing a member that misunderstood its job cannot distinguish "the brief was
// wrong" from "the brief never arrived".
func TestSeededInputsAreRecordedWithDigests(t *testing.T) {
	in := completeInputs(t)
	d := in.digests(model.Member{Name: "orchestrator", Role: "orchestrator"})
	for _, want := range []string{missionFileName, "orchestrator.md", "roles/backend.md", "roles/qa.md"} {
		sum, ok := d[want]
		if !ok {
			t.Errorf("no digest recorded for %q", want)
			continue
		}
		if len(sum) != 64 {
			t.Errorf("digest for %q is not a sha256: %q", want, sum)
		}
	}
	if got := in.digests(model.Member{Name: "backend", Role: "agent"}); len(got) != 1 {
		t.Errorf("agent should record exactly its own brief, got %v", got)
	}
}

// The orientation is the only copy of the operating model the fleet reads, so
// two things it was silent about have to be in it. Both were found by live
// probes: the orientation pushed the orchestrator toward long dispatches through
// the one channel that mangled them and named no safe way to send one, and it
// left an orchestrator to guess which exit state an impossible mission belongs
// in — which one of them got wrong, reporting `Converged` for a self-
// contradictory specification it had correctly proved could not be satisfied.
func TestOrchestratorOrientationCoversDispatchSafetyAndImpossibleMissions(t *testing.T) {
	in := completeInputs(t)
	campaign := &model.Campaign{Name: "c1", Network: "net", Members: []model.Member{
		{Name: "orchestrator", Role: "orchestrator", CLI: "codex", Branch: "cs-sandbox/orch-a.c1-a"},
		{Name: "backend", Role: "agent", CLI: "codex", Branch: "cs-sandbox/backend-a.c1-a"},
	}}
	orch, _, err := buildOrientation(campaign, campaign.Members[0], in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"send <member> --file", // the dispatch path its own shell cannot rewrite
		"--file -",             // and the stdin form
		"$(",                   // it must name the construct that corrupts a dispatch
	} {
		if !strings.Contains(orch, want) {
			t.Errorf("orchestrator orientation missing %q:\n%s", want, orch)
		}
	}
	// The outcome vocabulary moved out of standing doctrine and into the
	// mission dispatch itself — stated at the exact decision site, every
	// campaign — with the impossible-mission cell named so an orchestrator
	// never infers it under pressure at the end of a run, which an earlier one did.
	mission := missionDispatchBody(in)
	for _, want := range []string{
		"campaign-met", "campaign-converged", "campaign-exhausted", "campaign-blocked",
		"impossible as written",
		"--unmet",
	} {
		if !strings.Contains(mission, want) {
			t.Errorf("mission dispatch body missing %q:\n%s", want, mission)
		}
	}
}

// The orientation must carry the facts a member cannot derive. The branch is
// the sharp case: committing to the right one decides whether the member's work
// survives teardown, and nothing else in its environment names it.
func TestOrientationNamesBranchInputsAndTeam(t *testing.T) {
	in := completeInputs(t)
	campaign := &model.Campaign{Name: "c1", Network: "net", Members: []model.Member{
		{Name: "orchestrator", Role: "orchestrator", CLI: "codex", Branch: "cs-sandbox/orch-a.c1-a",
			Profile: model.MemberProfile{Repos: []model.Repo{{Path: "/src/myproject"}}}},
		{Name: "backend", Role: "agent", CLI: "codex", Branch: "cs-sandbox/backend-a.c1-a",
			Profile: model.MemberProfile{Repos: []model.Repo{{Path: "/src/myproject"}}}},
		{Name: "qa", Role: "agent", CLI: "opencode", Branch: "cs-sandbox/qa-a.c1-a"},
	}}

	orch, manifest, err := buildOrientation(campaign, campaign.Members[0], in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"cs-sandbox/orch-a.c1-a", // its branch
		"~/myproject",            // where the clone actually landed
		"`backend` (codex)",      // teammate and its CLI, paired
		"`qa` (opencode)",
		"roles/backend.md",   // a pointer into the operator's intent
		"`m1`",               // its mission dispatch, whose reply ends the campaign
		"reply --file",       // the reporting obligation: a reply, not a summary file
		"cs-campaign-member", // the only sanctioned control path
	} {
		if !strings.Contains(orch, want) {
			t.Errorf("orchestrator orientation missing %q", want)
		}
	}
	if manifest.Branch != "cs-sandbox/orch-a.c1-a" {
		t.Errorf("manifest branch = %q", manifest.Branch)
	}

	agent, agentManifest, err := buildOrientation(campaign, campaign.Members[1], in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(agent, "send <member>") || strings.Contains(agent, "wait") {
		t.Error("an agent must not be told it can drive peers; it cannot")
	}
	if !strings.Contains(agent, "reply --file") {
		t.Error("an agent must be told the one verb everything depends on: reply")
	}
	if strings.Contains(agent, "`qa` (opencode)") {
		t.Error("an agent must not be handed the roster")
	}
	if !strings.Contains(agent, "cs-sandbox/backend-a.c1-a") {
		t.Error("agent orientation does not name its own branch")
	}
	// Orientation first, then the operator's files: one list covering everything
	// this member must read, so the readback can report any of them missing.
	if len(agentManifest.Inputs) != 2 || agentManifest.Inputs[0] != orientationFileName || agentManifest.Inputs[1] != "backend.md" {
		t.Errorf("agent manifest inputs = %v; want the orientation then its own brief", agentManifest.Inputs)
	}
}

// member.json is the readback's contract: the member walks Inputs and confirms
// each file is present before reading it, which makes a seeding failure
// something the member reports rather than something we infer from a thin
// answer. That only works if the manifest lists what was actually seeded.
func TestMemberManifestListsWhatWasSeeded(t *testing.T) {
	in := completeInputs(t)
	campaign := &model.Campaign{Name: "c1", Members: []model.Member{
		{Name: "orchestrator", Role: "orchestrator", CLI: "codex"},
		{Name: "backend", Role: "agent", CLI: "codex"},
	}}
	_, manifest, err := buildOrientation(campaign, campaign.Members[0], in)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var round protocol.Member
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatalf("member.json is not valid JSON: %v", err)
	}
	if round.Orientation != guestOrientationFile {
		t.Errorf("manifest does not point at the orientation: %q", round.Orientation)
	}
	want := map[string]bool{orientationFileName: true, missionFileName: true, "orchestrator.md": true, "roles/backend.md": true, "roles/qa.md": true}
	for _, got := range round.Inputs {
		delete(want, got)
	}
	if len(want) != 0 {
		t.Errorf("manifest omits seeded files: %v (listed %v)", want, round.Inputs)
	}
}

// The product owns a handful of basenames inside the input channel. An operator
// file that took one would overwrite a member's only account of its own
// obligations, and the result would read as a model that ignored instructions.
func TestReservedInputNamesAreRefused(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile.yaml")
	if err := os.WriteFile(profile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, missionFileName), []byte("m"), 0o600); err != nil {
		t.Fatal(err)
	}
	roles := filepath.Join(dir, rolesDirName)
	if err := os.MkdirAll(roles, 0o700); err != nil {
		t.Fatal(err)
	}
	// A member legitimately named "ORIENTATION" would collide with the product's file.
	p := model.Profile{APIVersion: model.APIVersion, Kind: "CampaignProfile",
		Orchestrator: model.MemberProfile{CLI: "codex"},
		Agents:       map[string]model.MemberProfile{"ORIENTATION": {CLI: "codex"}}}
	for _, n := range []string{"orchestrator", "ORIENTATION"} {
		if err := os.WriteFile(filepath.Join(roles, n+".md"), []byte("b"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := loadCampaignInputs(profile, p); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved basename must be refused, got %v", err)
	}
}
