package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/protocol"
)

// commitFile writes one file and commits it in dir, returning the commit.
func commitFile(t *testing.T, dir, name, body, msg string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	storeGit(t, dir, "add", "-A")
	storeGit(t, dir, "commit", "-q", "-m", msg)
	return storeGit(t, dir, "rev-parse", "HEAD")
}

// readLog returns every entry of the orchestrator's log.
func readLog(t *testing.T, home string) []protocol.Entry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, protocol.LogFile))
	if err != nil {
		return nil
	}
	var out []protocol.Entry
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		var e protocol.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

// TestPushNamesTheCommitRefusesWithheldPathsAndLogs: a push delivers the
// commit it names and nothing after it; a push whose new history carries a
// path withheld from the member is refused with nothing delivered; history
// the member already holds is not judged again; and every delivery and
// refusal is in the orchestrator's log with the member, repository and
// commit.
func TestPushNamesTheCommitRefusesWithheldPathsAndLogs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	covmap.ProveCoreOnPass(t, "withhold", covmap.TierUnit)
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com"} {
		t.Setenv(k, v)
	}
	tmp := t.TempDir()
	home := filepath.Join(tmp, "orchestrator")
	product := filepath.Join(home, "product")
	if err := os.MkdirAll(product, 0o755); err != nil {
		t.Fatal(err)
	}
	storeGit(t, product, "init", "-q", "-b", "main")
	base := commitFile(t, product, "README", "base\n", "Base")
	// The teammate's clone, reached as "<tmp>/dev:product": a slash before the
	// colon makes git read it as a path, so no ssh is involved.
	dev := filepath.Join(tmp, "dev:product")
	storeGit(t, tmp, "clone", "-q", product, dev)
	storeGit(t, dev, "checkout", "-q", "-b", "cs-sandbox/dev.g")
	env := &envState{Home: home, Manifest: &protocol.Manifest{Agents: map[string]protocol.AgentRecord{
		"dev": {Sandbox: filepath.Join(tmp, "dev"), Repos: map[string]string{"product": "cs-sandbox/dev.g"},
			Bases: map[string]string{"product": base}, Withhold: map[string][]string{"product": {"results"}}},
	}}}
	memberRef := func() string {
		out, err := exec.Command("git", "-C", dev, "rev-parse", "--verify", "-q", "refs/campaign/orchestrator").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	memberHas := func(sha string) bool {
		return exec.Command("git", "-C", dev, "cat-file", "-e", sha).Run() == nil
	}

	// 1. A named commit is delivered, and its descendant is not.
	a := commitFile(t, product, "a.txt", "a\n", "A")
	b := commitFile(t, product, "b.txt", "b\n", "B")
	if err := cmdPush(env, []string{"dev", "product", "--commit", a}); err != nil {
		t.Fatalf("push --commit: %v", err)
	}
	if got := memberRef(); got != a {
		t.Fatalf("after push --commit %s the member's ref is %q", a[:7], got)
	}
	if memberHas(b) {
		t.Fatalf("push --commit %s delivered its descendant %s", a[:7], b[:7])
	}

	// 2. New history carrying a withheld path is refused, even where the tip
	// no longer holds the path, and the member's clone is unchanged.
	c := commitFile(t, product, "results/answers.txt", "42\n", "C adds the hold-out")
	storeGit(t, product, "rm", "-q", "results/answers.txt")
	storeGit(t, product, "commit", "-q", "-m", "D drops it again")
	d := storeGit(t, product, "rev-parse", "HEAD")
	err := cmdPush(env, []string{"dev", "product"})
	if err == nil || !strings.Contains(err.Error(), "results/answers.txt") || !strings.Contains(err.Error(), "withheld") || !strings.Contains(err.Error(), c[:12]) {
		t.Fatalf("a push carrying a withheld path must be refused naming the path and the commit: %v", err)
	}
	if got := memberRef(); got != a {
		t.Fatalf("a refused push moved the member's ref to %q", got)
	}
	if memberHas(c) || memberHas(d) {
		t.Fatal("a refused push delivered objects")
	}

	// 3. What the member already holds is not delivered again, so its own
	// work at the withheld path, merged back, passes.
	own := commitFile(t, dev, "results/own.txt", "mine\n", "Dev's own results")
	if err := cmdFetch(env, []string{"dev", "product"}); err != nil {
		t.Fatal(err)
	}
	storeGit(t, product, "checkout", "-q", "-b", "integration", b)
	storeGit(t, product, "merge", "-q", "--no-edit", "refs/remotes/campaign/dev/product")
	integration := storeGit(t, product, "rev-parse", "HEAD")
	if err := cmdPush(env, []string{"dev", "product", "--commit", "integration"}); err != nil {
		t.Fatalf("a merge of the member's own work must pass: %v", err)
	}
	if got := memberRef(); got != integration {
		t.Fatalf("after the merge push the member's ref is %q", got)
	}
	_ = own

	// 4. A push git refuses is logged as refused too.
	err = cmdPush(env, []string{"dev", "product", "--commit", a})
	if err == nil || !strings.Contains(err.Error(), "non-fast-forward") {
		t.Fatalf("a push behind the member's ref must be refused by git: %v", err)
	}

	// 5. A commit the clone does not have is refused before anything is read
	// or logged.
	if err := cmdPush(env, []string{"dev", "product", "--commit", "nonesuch"}); err == nil || !strings.Contains(err.Error(), "not a commit") {
		t.Fatalf("an unknown --commit: %v", err)
	}

	// The log holds every delivery and refusal, with whom, what and which.
	log := readLog(t, home)
	var kinds []string
	for _, e := range log {
		kinds = append(kinds, e.Kind)
		if e.Member != "dev" || e.Repo != "product" || len(e.Commit) != 40 {
			t.Errorf("log entry lacks member, repo or commit: %+v", e)
		}
	}
	if want := []string{"delivered", "refused", "delivered", "refused"}; strings.Join(kinds, " ") != strings.Join(want, " ") {
		t.Fatalf("log kinds %v, want %v", kinds, want)
	}
	if log[0].Commit != a || log[1].Commit != d || log[2].Commit != integration || log[3].Commit != a {
		t.Errorf("log commits are not the commits pushed: %+v", log)
	}
	if !strings.Contains(log[1].Text, "results/answers.txt") || !strings.Contains(log[3].Text, "refused by git") {
		t.Errorf("refusal texts: %q, %q", log[1].Text, log[3].Text)
	}
}

// A push to a teammate without a declaration never reads what it holds, and
// note cannot write a delivery by hand.
func TestNoteCannotWriteADelivery(t *testing.T) {
	_, env := fakeHome(t, "orchestrator")
	err := cmdNote(env, []string{"delivered", "--file", "-"})
	if err == nil || !strings.Contains(err.Error(), "plan or assessment") {
		t.Fatalf("note delivered: %v", err)
	}
}
