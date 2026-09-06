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
