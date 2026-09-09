package cli

// The documentation claims a reader would act on and could not check: state
// safety, and how a credential reaches a member. The rest of this file used to
// pin the await/journal machinery, which the protocol deleted — completion is a
// reply artifact now, and no dispatch state is stored to be journaled or
// clobbered.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign"
	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/model"
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
