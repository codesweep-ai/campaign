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

// THE gate. A scaffolder whose output its own validator rejects is worse than no
// scaffolder: the operator's first two commands contradict each other, and the
// example cannot drift from the parser only if the parser is what checks it.
//
// This also pins the convention from both ends — init writes where
// loadCampaignInputs looks, and nothing but this test says so.
func TestValidateAcceptsWhatInitEmits(t *testing.T) {
	repo := t.TempDir()
	mustInitRepo(t, repo)
	a, dir := scaffold(t, "demo", "--orchestrator", "codex", "--agent", "backend=codex", "--agent", "qa=opencode", "--repo", repo)

	cmd := a.validateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{filepath.Join(dir, "profile.yaml")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("validate rejected what init emitted: %v", err)
	}
	if !strings.Contains(out.String(), "3 role briefs") {
		t.Errorf("validate did not find a brief per member:\n%s", out.String())
	}
}

// The stubs must be visibly unfinished. A scaffolder that emitted shippable
// prose would produce fleets briefed with boilerplate nobody edited — and a
// member restates boilerplate as faithfully as it restates real intent, so the
// readback could not tell the difference.
func TestScaffoldedBriefsAreVisiblyIncomplete(t *testing.T) {
	_, dir := scaffold(t, "demo", "--orchestrator", "codex", "--agent", "backend=codex")
	for _, name := range []string{missionFileName, "roles/orchestrator.md", "roles/backend.md"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "<!--") {
			t.Errorf("%s carries no guidance comments for the operator to replace", name)
		}
		if !strings.Contains(string(body), "\n- \n") && !strings.HasSuffix(strings.TrimRight(string(body), "\n"), "- ") {
			t.Errorf("%s has no blanks left to fill; a stub that reads as finished will ship unedited", name)
		}
	}
	body, _ := os.ReadFile(filepath.Join(dir, "roles", "backend.md"))
	if !strings.Contains(string(body), "backend") {
		t.Error("an agent's brief should name the member it belongs to")
	}
	if !strings.Contains(string(body), "committed on your own branch") {
		t.Error("the one obligation whose failure is irreversible must survive in the stub")
	}
}

// Re-running init over filled-in briefs would replace considered work with
// blanks, and the resulting campaign would still validate and still create — so
// the damage would only surface as a fleet that could not say what it was for.
func TestInitRefusesToOverwrite(t *testing.T) {
	a, dir := scaffold(t, "demo", "--orchestrator", "codex", "--agent", "backend=codex")
	brief := filepath.Join(dir, rolesDirName, "backend.md")
	if err := os.WriteFile(brief, []byte("# carefully written\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := a.initCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"demo", "--orchestrator", "codex", "--agent", "backend=codex", "--dir", dir})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("init overwrote an existing campaign")
	}
	if !strings.Contains(err.Error(), "backend.md") {
		t.Errorf("refusal must name what it would have destroyed: %v", err)
	}
	body, _ := os.ReadFile(brief)
	if string(body) != "# carefully written\n" {
		t.Error("init clobbered a brief it claimed to refuse")
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
	for _, want := range []string{"apiKeyFromEnv", "inheritAgentLogin", "REQUIRED"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("scaffolded profile never mentions %q", want)
		}
	}
	if strings.Contains(string(body), "auth:\n    apiKeyFromEnv") {
		t.Error("the hint must stay a comment; a live placeholder would validate and then fail at create")
	}
}
