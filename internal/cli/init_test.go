package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/store"
)

// mustInitRepo makes a real git repository, because validate resolves every
// declared repo path exactly as create does.
func mustInitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@e", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "base"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func scaffold(t *testing.T, args ...string) (*app, string) {
	t.Helper()
	dir := t.TempDir()
	a := &app{store: store.Store{Dir: filepath.Join(dir, "state")}}
	cmd := a.initCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs(append(args, "--dir", filepath.Join(dir, "camp")))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}
	return a, filepath.Join(dir, "camp")
}

// THE gate, inverted by SAC-022. This test used to assert that a scaffold
// validates, which was the defect: `init` wrote a mission and a brief per
// member, the check asks only whether the file exists, and so a campaign nobody
// had briefed passed on blanks.
//
// What it pins now is the pair. `init` writes the profile and no seeded
// document, and validate refuses until an author writes them, naming each one.
// The convention is still pinned from both ends: init writes where
// loadCampaignInputs looks, and nothing but this test says so.
func TestValidateRefusesWhatInitEmitsUntilTheDocumentsAreWritten(t *testing.T) {
	repo := t.TempDir()
	mustInitRepo(t, repo)
	a, dir := scaffold(t, "demo", "--orchestrator", "codex", "--agent", "backend=codex", "--agent", "qa=opencode", "--repo", repo)

	run := func() (string, error) {
		cmd := a.validateCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{filepath.Join(dir, "profile.yaml")})
		err := cmd.Execute()
		return out.String(), err
	}
	_, err := run()
	if err == nil {
		t.Fatal("validate accepted a campaign whose mission and briefs nobody wrote")
	}
	for _, named := range []string{missionFileName, "roles/orchestrator.md", "roles/backend.md", "roles/qa.md"} {
		if !strings.Contains(err.Error(), named) {
			t.Errorf("the refusal does not name %s, so an author cannot act on it:\n%v", named, err)
		}
	}
	// Written, however briefly, and the same profile validates. This is the half
	// that keeps init's paths and the loader's expectations in agreement.
	for _, named := range []string{missionFileName, "roles/orchestrator.md", "roles/backend.md", "roles/qa.md"} {
		if err := os.WriteFile(filepath.Join(dir, named), []byte("# written\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run()
	if err != nil {
		t.Fatalf("validate rejected a campaign whose documents exist: %v", err)
	}
	if !strings.Contains(out, "3 role briefs") {
		t.Errorf("validate did not find a brief per member:\n%s", out)
	}
}

// The scaffolder must not write a document a member is seeded with. That is the
// whole of SAC-022: a blank one passes the existence check that refuses an
// unbriefed fleet, and a member restates boilerplate as faithfully as it
// restates real intent. The profile is the exception, because it is never
// seeded and its comments reach nobody.
func TestInitScaffoldsOnlyTheProfile(t *testing.T) {
	_, dir := scaffold(t, "demo", "--orchestrator", "codex", "--agent", "backend=codex")
	for _, seeded := range []string{missionFileName, "roles/orchestrator.md", "roles/backend.md"} {
		if _, err := os.Stat(filepath.Join(dir, seeded)); !os.IsNotExist(err) {
			t.Errorf("init wrote %s, which a member is seeded with", seeded)
		}
	}
	body, err := os.ReadFile(filepath.Join(dir, "profile.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "<!") && !strings.Contains(string(body), "#") {
		t.Error("the profile lost the guidance it can safely carry")
	}
	if _, err := os.Stat(filepath.Join(dir, rolesDirName)); err != nil {
		t.Errorf("init should still make the roles directory it names: %v", err)
	}
}

// Re-running init over filled-in briefs would replace considered work with
// blanks, and the resulting campaign would still validate and still create — so
// the damage would only surface as a fleet that could not say what it was for.
func TestInitRefusesToOverwrite(t *testing.T) {
	a, dir := scaffold(t, "demo", "--orchestrator", "codex", "--agent", "backend=codex")
	profile := filepath.Join(dir, "profile.yaml")

	cmd := a.initCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"demo", "--orchestrator", "codex", "--agent", "backend=codex", "--dir", dir})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("init overwrote an existing campaign")
	}
	if !strings.Contains(err.Error(), "profile.yaml") {
		t.Errorf("refusal must name what it would have destroyed: %v", err)
	}
	body, _ := os.ReadFile(profile)
	if !strings.Contains(string(body), "CampaignProfile") {
		t.Error("init clobbered a profile it claimed to refuse")
	}
}

// init and create must not be able to disagree about what a fleet is, which is
// why both parse the same flags through the same function.
func TestInitRejectsAFleetCreateWouldReject(t *testing.T) {
	dir := t.TempDir()
	a := &app{}
	cmd := a.initCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"demo", "--orchestrator", "codex", "--agent", "backend=nonsense", "--dir", dir})
	if err := cmd.Execute(); err == nil {
		t.Fatal("init accepted an unsupported CLI that create would refuse")
	}
	if _, err := os.Stat(filepath.Join(dir, "profile.yaml")); err == nil {
		t.Error("init wrote a profile for a fleet it had already rejected")
	}
}

// A scaffolded profile declares no credentials — there is no safe default to
// guess — and a fleet without them boots and then fails at its first turn
// looking exactly like a broken fleet. The file must say so itself, because the
// operator reading it is the last person who can fix it cheaply.
func TestScaffoldedProfileSaysWhereCredentialsGo(t *testing.T) {
	_, dir := scaffold(t, "demo", "--orchestrator", "codex", "--agent", "backend=codex")
	body, err := os.ReadFile(filepath.Join(dir, "profile.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// inheritApiKeyFromEnv rather than the plain spelling: a scaffolded profile
	// resolves to lend, where the plain one is refused, so teaching it here
	// would scaffold the mistake.
	for _, want := range []string{"inheritApiKeyFromEnv", "apiKeyFromEnv", "agentLogin", "inheritAgentLogin", "credentials", "REQUIRED"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("scaffolded profile never mentions %q", want)
		}
	}
	if strings.Contains(string(body), "auth:\n    apiKeyFromEnv") {
		t.Error("the hint must stay a comment; a live placeholder would validate and then fail at create")
	}
}
