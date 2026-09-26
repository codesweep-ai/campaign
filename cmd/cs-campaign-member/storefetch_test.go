package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

// storeGit runs git in dir as a fixed identity, with none of the host's
// configuration, and fails the test on an error.
func storeGit(t *testing.T, dir string, args ...string) string {
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

// record adds one build's entry to a store clone, as make ci does there.
func record(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "status", name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status", name, "c.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	storeGit(t, dir, "add", "-A")
	storeGit(t, dir, "commit", "-q", "-m", "Record "+name)
}

// TestFetchTakesABuildStoreIn: the campaign's build store is carried like a
// project repository, but a fetch of it lands in the orchestrator's own clone
// at once, so the next push takes the builds on to the other members. A fast
// forward where the orchestrator has taken in nothing since, a merge where it
// has, and nothing where it already holds them. A project repository is left
// on its remote-tracking ref for the orchestrator to judge.
func TestFetchTakesABuildStoreIn(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com"} {
		t.Setenv(k, v)
	}
	tmp := t.TempDir()
	home := filepath.Join(tmp, "orchestrator")
	store := filepath.Join(home, "cs-builds")
	if err := os.MkdirAll(store, 0o755); err != nil {
		t.Fatal(err)
	}
	storeGit(t, store, "init", "-q", "-b", "main")
	record(t, store, "seed")
	// The teammate's clone. A slash before the colon makes git read
	// "<tmp>/dev:cs-builds" as a path, so no ssh is involved.
	dev := filepath.Join(tmp, "dev:cs-builds")
	storeGit(t, tmp, "clone", "-q", store, dev)
	storeGit(t, dev, "checkout", "-q", "-b", "cs-sandbox/dev.g")
	t.Setenv("CS_BUILD_STORE", store)
	env := &envState{Home: home, Manifest: &protocol.Manifest{Agents: map[string]protocol.AgentRecord{
		"dev": {Sandbox: filepath.Join(tmp, "dev"), Repos: map[string]string{"cs-builds": "cs-sandbox/dev.g"}},
	}}}

	record(t, dev, "lint")
	if err := cmdFetch(env, []string{"dev", "cs-builds"}, false); err != nil {
		t.Fatal(err)
	}
	if got := storeGit(t, store, "log", "-1", "--format=%s"); got != "Record lint" {
		t.Fatalf("after a fast forward the store's head is %q", got)
	}

	record(t, store, "own")
	record(t, dev, "ledger")
	if err := cmdFetch(env, []string{"dev", "cs-builds"}, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lint", "ledger", "own"} {
		if _, err := os.Stat(filepath.Join(store, "status", name, "c.json")); err != nil {
			t.Errorf("the merged store lacks %s: %v", name, err)
		}
	}
	if parents := strings.Fields(storeGit(t, store, "log", "-1", "--format=%P")); len(parents) != 2 {
		t.Errorf("want a merge commit, got parents %v", parents)
	}

	head := storeGit(t, store, "rev-parse", "HEAD")
	if err := cmdFetch(env, []string{"dev", "cs-builds"}, false); err != nil {
		t.Fatal(err)
	}
	if got := storeGit(t, store, "rev-parse", "HEAD"); got != head {
		t.Errorf("a fetch of nothing new moved the store to %s", got)
	}

	// Named anything else, it is a project repository, and nothing is merged.
	t.Setenv("CS_BUILD_STORE", filepath.Join(tmp, "elsewhere"))
	record(t, dev, "tracer")
	if err := cmdFetch(env, []string{"dev", "cs-builds"}, false); err != nil {
		t.Fatal(err)
	}
	if got := storeGit(t, store, "rev-parse", "HEAD"); got != head {
		t.Errorf("a repository that is not the store was merged: head %s", got)
	}
}
