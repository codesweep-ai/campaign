package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fetch from a machine that never answers has to fail, and say so, rather
// than run until something above it gives up.
//
// This is what cost a campaign its cassette: `fetch dev` hung, the adapter's
// shell tool killed it at 120s with no output, and the orchestrator went on to
// judge a branch it had never fetched — against a ref that did not exist, which
// read as the agent having delivered nothing.
func TestFetchGivesUpOnAMachineThatNeverAnswers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	// A routable address that drops packets: the connection never completes
	// and never refuses, which is the shape a wedged member has.
	bound := 3 * time.Second
	restore := memberBoundForTest(bound)
	defer restore()

	start := time.Now()
	_, err := gitCmd(dir, "fetch", "10.255.255.1:repo", "main:refs/remotes/campaign/dev/repo")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a fetch from a machine that never answers reported success")
	}
	// Tight on purpose. ssh has its own ConnectTimeout, so a loose assertion
	// here passes whether the bound works or not: at 30s this test passed
	// while the context kill was doing nothing and ssh alone ended the wait.
	if limit := bound + 4*time.Second; elapsed > limit {
		t.Fatalf("fetch ran for %s, past %s: the bound is not what ended it", elapsed, limit)
	}
	if !strings.Contains(err.Error(), "gave up") {
		t.Fatalf("the failure does not name the bound that ended it: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, ".keep"), nil, 0o600)
}
