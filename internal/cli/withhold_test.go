package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/model"
)

func withholdGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func withholdCommit(t *testing.T, dir, name, msg string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(msg+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withholdGit(t, dir, "add", "-A")
	withholdGit(t, dir, "commit", "-q", "-m", msg)
	return withholdGit(t, dir, "rev-parse", "--short", "HEAD")
}

func profileWithholding(repo string, decls []string, snap string, snapDecls []string) model.Profile {
	m := model.MemberProfile{CLI: "claude", Repos: []model.Repo{{Path: repo, Withhold: decls}}}
	if snap != "" {
		m.Snapshots = []model.Snapshot{{Path: snap, Withhold: snapDecls}}
	}
	return model.Profile{APIVersion: model.APIVersion, Kind: "CampaignProfile",
		Orchestrator: model.MemberProfile{CLI: "codex", Repos: []model.Repo{{Path: repo}}},
		Agents:       map[string]model.MemberProfile{"qa": m}}
}

// TestWithheldPathsAreRefusedBeforeCreate: a member's clone carries every
// branch of the host repository, so a withheld path anywhere in its history
// is refused, a deleted one included; a repository holding objects no ref
// reaches is refused until they are pruned, because the clone copies the
// store; and a snapshot is held against the tree as it stands.
func TestWithheldPathsAreRefusedBeforeCreate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	covmap.ProveCoreOnPass(t, "withhold", covmap.TierUnit)
	repo := filepath.Join(t.TempDir(), "product")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	withholdGit(t, repo, "init", "-q", "-b", "main")
	withholdCommit(t, repo, "README", "base")
	withholdGit(t, repo, "gc", "-q", "--prune=now")

	if err := checkWithheld(profileWithholding(repo, []string{"results"}, "", nil)); err != nil {
		t.Fatalf("a repository without the path must pass: %v", err)
	}

	// On a side branch, added and then deleted: the tip's tree is clean and
	// the clone is not.
	withholdGit(t, repo, "checkout", "-q", "-b", "side")
	added := withholdCommit(t, repo, "results/answers.txt", "the hold-out")
	withholdGit(t, repo, "rm", "-q", "results/answers.txt")
	withholdGit(t, repo, "commit", "-q", "-m", "drop it")
	withholdGit(t, repo, "checkout", "-q", "main")
	err := checkWithheld(profileWithholding(repo, []string{"results"}, "", nil))
	if err == nil || !strings.Contains(err.Error(), "results/answers.txt") || !strings.Contains(err.Error(), added) || !strings.Contains(err.Error(), "agent qa") {
		t.Fatalf("a withheld path on another branch's history must be refused naming the path, the commit and the member: %v", err)
	}
	if err := checkWithheld(profileWithholding(repo, []string{"docs"}, "", nil)); err != nil {
		t.Fatalf("a declaration nothing matches must pass: %v", err)
	}

	// Drop the branch: the objects remain, reachable only from the reflog,
	// and a local clone would still carry them.
	withholdGit(t, repo, "branch", "-q", "-D", "side")
	err = checkWithheld(profileWithholding(repo, []string{"results"}, "", nil))
	if err == nil || !strings.Contains(err.Error(), "no ref reaches") || !strings.Contains(err.Error(), "gc --prune=now") {
		t.Fatalf("a store with unreachable objects must be refused with the command that cleans it: %v", err)
	}
	withholdGit(t, repo, "reflog", "expire", "--expire=now", "--all")
	withholdGit(t, repo, "gc", "-q", "--prune=now")
	if err := checkWithheld(profileWithholding(repo, []string{"results"}, "", nil)); err != nil {
		t.Fatalf("after the prune the repository must pass: %v", err)
	}

	// A snapshot is a directory, and is held as it stands.
	snap := filepath.Join(t.TempDir(), "reference")
	if err := os.MkdirAll(filepath.Join(snap, "golden", "run1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "golden", "run1", "out.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = checkWithheld(profileWithholding(repo, nil, snap, []string{"**/run1"}))
	if err == nil || !strings.Contains(err.Error(), "golden/run1") {
		t.Fatalf("a withheld path in a snapshot must be refused: %v", err)
	}
	if err := checkWithheld(profileWithholding(repo, nil, snap, []string{"private"})); err != nil {
		t.Fatalf("a snapshot without the path must pass: %v", err)
	}
}

func TestValidateRefusesADeclarationThatIsNotAPath(t *testing.T) {
	for _, bad := range []string{"/etc/secret", "../outside", ""} {
		p := profileWithholding("/nonexistent", []string{bad}, "", nil)
		if err := validateProfile(p); err == nil || !strings.Contains(err.Error(), "withhold") {
			t.Errorf("withhold %q: %v", bad, err)
		}
	}
}
