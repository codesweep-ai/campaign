package main

// The orchestrator's wait starts an agent fresh on its own handoff, as it
// restarts one: the old session is ended and forgotten, and the new one reads
// a .handoff message. Unlike a restart, it spends no rung.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

// handedOff is the probe tail of an agent whose last turn ended in a hand-off,
// five seconds ago.
func handedOff() []string {
	return []string{"AGENT idle",
		fmt.Sprintf("TURNEND %d 0 handoff - handoff at 70000 tokens, over the line of 60000", time.Now().Unix()-5)}
}

// The kill and the forget are what make the next turn a new session; the
// .handoff message is what makes it a continuation rather than a cold start.
func TestWaitStartsAHandedOffAgentFreshOnItsHandoff(t *testing.T) {
	a := settledAgent("d001.md")
	a.tail = handedOff()
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
	installFakeWorld(t, w)
	env := waitEnv(t, w)

	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if len(w.checks) != 1 {
		t.Fatalf("the work and the handoff are checked once before the swap: %q", w.checks)
	}
	if len(w.sessionCmds) != 2 ||
		!strings.Contains(w.sessionCmds[0], "--kill sess-dev") ||
		!strings.Contains(w.sessionCmds[1], "cs-codex-remote-forget sess-dev") {
		t.Fatalf("a hand-off must end then forget the old session: %v", w.sessionCmds)
	}
	if len(w.delivered) != 1 || w.delivered[0].name != "d001.001.handoff.md" {
		t.Fatalf("want exactly one .handoff message in the open dispatch, got %+v", w.delivered)
	}
	body := w.delivered[0].body
	for _, want := range []string{"fresh session after a hand-off", "d001.md", "~/" + protocol.HandoffFile} {
		if !strings.Contains(body, want) {
			t.Errorf("the fresh session's message does not name %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, protocol.LogFile) {
		t.Errorf("an agent has no log to be pointed at:\n%s", body)
	}
	if len(w.started) != 1 || !strings.HasSuffix(w.started[0], " d001") {
		t.Fatalf("the fresh session's turn must start on d001: %v", w.started)
	}
	if !strings.Contains(out, "started dev fresh on its handoff") || !strings.Contains(out, "no rung spent") {
		t.Fatalf("wait must report the hand-off it performed:\n%s", out)
	}
}

// The handoff file is rewritten at every hand-off, so the version the old
// session wrote is copied aside before that session is ended, under the
// ordinal of this hand-off on the dispatch: the second one here.
func TestWaitKeepsTheHandoffBeforeItEndsTheSession(t *testing.T) {
	a := settledAgent("d001.md", "d001.001.handoff.md")
	a.tail = handedOff()
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
	installFakeWorld(t, w)
	env := waitEnv(t, w)

	if out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) }); err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	want := "mkdir -p ~/" + protocol.HandoffsDir + " && cp ~/" + protocol.HandoffFile + " ~/" + protocol.HandoffsDir + "/d001-2.md"
	if len(w.kept) != 1 || w.kept[0].command != want {
		t.Fatalf("want one copy of the handoff to d001-2.md:\n%q\ngot %+v", want, w.kept)
	}
	if w.kept[0].sessionBefore != 0 || len(w.sessionCmds) != 2 {
		t.Fatalf("the copy must come before the kill: %+v, session commands %v", w.kept, w.sessionCmds)
	}
	if len(w.delivered) != 1 || w.delivered[0].name != "d001.002.handoff.md" {
		t.Fatalf("the fresh session still starts on the next .handoff message: %+v", w.delivered)
	}
	if strings.Contains(w.delivered[0].body, protocol.HandoffsDir) {
		t.Errorf("a fresh session reads the current handoff alone, not the kept ones:\n%s", w.delivered[0].body)
	}
}

// A copy that fails is passed over: the fresh session matters more.
func TestWaitStartsTheFreshSessionWhenTheCopyFails(t *testing.T) {
	a := settledAgent("d001.md")
	a.tail = handedOff()
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
	installFakeWorld(t, w)
	fake := sshOut
	sshOut = func(host, command, payload string) ([]byte, error) {
		if strings.Contains(command, protocol.HandoffsDir+"/") {
			return []byte("cp: cannot stat"), errors.New("exit status 1")
		}
		return fake(host, command, payload)
	}
	env := waitEnv(t, w)

	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if len(w.sessionCmds) != 2 || len(w.delivered) != 1 || len(w.started) != 1 {
		t.Fatalf("a failed copy must not stop the hand-off: %v %+v %v", w.sessionCmds, w.delivered, w.started)
	}
}

// A hand-off whose work is not committed, or whose handoff does not name the
// commit, is not taken. The session is kept, because only it knows what the
// work was, and a .commit message in the same session asks it to commit and
// write its handoff again. The check is asked about every repository the
// agent has, at the base its branch started from.
func TestAHandoffThatDisagreesWithGitKeepsTheSession(t *testing.T) {
	a := settledAgent("d001.md")
	a.tail = handedOff()
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}, check: "DIRTY\tapp\t2\tsrc/main.go\nUNNAMED\tapp\tabc1234\n"}
	installFakeWorld(t, w)
	env := waitEnv(t, w)
	const base = "0123456789abcdef0123456789abcdef01234567"
	rec := env.Manifest.Agents["dev"]
	rec.Repos = map[string]string{"app": "cs-sandbox/dev", "lib": "cs-sandbox/dev"}
	rec.Bases = map[string]string{"app": base}
	facts, failed := probeAgent(rec)
	if failed {
		t.Fatal("probe failed")
	}

	said, err := doHandoffPrepared(env, "dev", rec, facts)
	if err != nil {
		t.Fatalf("a hand-off not taken is a move, not a failure: %v", err)
	}
	if len(w.checks) != 1 || w.checks[0] != protocol.HandoffCheckScript(map[string]string{"app": base, "lib": ""}) {
		t.Errorf("the check must ask about each repository at its base, and lib's is unknown: %q", w.checks)
	}
	if len(w.sessionCmds) != 0 || len(w.kept) != 0 {
		t.Fatalf("the session is kept, and its handoff is not copied aside: %v %+v", w.sessionCmds, w.kept)
	}
	problems := protocol.ParseHandoffCheck(w.check)
	if len(w.delivered) != 1 || w.delivered[0].name != "d001.001.commit.md" || w.delivered[0].body != protocol.CommitBody("d001", problems) {
		t.Fatalf("want one .commit message asking it to commit, got %+v", w.delivered)
	}
	if len(w.started) != 1 || !strings.HasSuffix(w.started[0], " d001") {
		t.Fatalf("the kept session's turn must start on d001: %v", w.started)
	}
	if !strings.HasPrefix(said, "kept dev's session on d001") || !strings.Contains(said, problems[0]) ||
		!strings.Contains(said, problems[1]) || !strings.HasSuffix(said, "asked it to commit first, no rung spent") {
		t.Errorf("the orchestrator must be told the session was kept, and why: %s", said)
	}
}

// A check that cannot run says nothing about the work, so nothing is done on
// it: no session is ended, and no message is sent. The next look tries again.
func TestAHandoffCheckThatFailsToRunMovesNothing(t *testing.T) {
	a := settledAgent("d001.md")
	a.tail = handedOff()
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a},
		check: "ssh: connect to host dev-box port 22: Connection refused", checkErr: errors.New("exit status 255")}
	installFakeWorld(t, w)
	env := waitEnv(t, w)
	rec := env.Manifest.Agents["dev"]
	facts, _ := probeAgent(rec)

	_, err := doHandoffPrepared(env, "dev", rec, facts)
	if err == nil || !strings.Contains(err.Error(), "check dev's work against its handoff") || !strings.Contains(err.Error(), "Connection refused") {
		t.Fatalf("a check that did not run is an error naming the check and the cause: %v", err)
	}
	if len(w.sessionCmds) != 0 || len(w.kept) != 0 || len(w.delivered) != 0 || len(w.started) != 0 {
		t.Fatalf("nothing moves on a check that did not run: %v %+v %+v %v", w.sessionCmds, w.kept, w.delivered, w.started)
	}
}

// Through wait: the first hand-off is not taken and wait says so. The kept
// session commits, writes its handoff again and hands off a second time, and
// the next look, finding git and the handoff in agreement, makes the swap.
func TestWaitAsksAnAgentToCommitThenStartsItFresh(t *testing.T) {
	a := settledAgent("d001.md")
	a.tail = handedOff()
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}, check: "DIRTY\tapp\t1\tnotes.txt\n"}
	installFakeWorld(t, w)
	env := waitEnv(t, w)

	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if !strings.Contains(out, "kept dev's session on d001: its work and its handoff do not agree (~/app has work not committed (1 path, the first notes.txt)); asked it to commit first, no rung spent") {
		t.Fatalf("wait must report the hand-off it did not take, and why:\n%s", out)
	}
	if len(w.sessionCmds) != 0 || len(w.delivered) != 1 || w.delivered[0].name != "d001.001.commit.md" {
		t.Fatalf("the first look keeps the session and asks it to commit: %v %+v", w.sessionCmds, w.delivered)
	}

	// The kept session's turn on the ask ends in a second hand-off.
	a.msgs["d001.001.commit.md"] = time.Now().Unix() - 100
	a.tail = handedOff()
	w.check = ""
	out, err = captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if len(w.sessionCmds) != 2 || !strings.Contains(w.sessionCmds[0], "--kill sess-dev") ||
		!strings.Contains(w.sessionCmds[1], "cs-codex-remote-forget sess-dev") {
		t.Fatalf("once git and the handoff agree, the old session is ended and forgotten: %v", w.sessionCmds)
	}
	if len(w.kept) != 1 || !strings.HasSuffix(w.kept[0].command, "/d001-1.md") {
		t.Errorf("the handoff is kept as this dispatch's first: %+v", w.kept)
	}
	if len(w.delivered) != 2 || w.delivered[1].name != "d001.002.handoff.md" ||
		!strings.Contains(w.delivered[1].body, "d001.001.commit.md") {
		t.Fatalf("the fresh session reads every message of the dispatch, the ask too: %+v", w.delivered)
	}
	if !strings.Contains(out, "started dev fresh on its handoff (d001), no rung spent") {
		t.Fatalf("wait must report the hand-off it performed:\n%s", out)
	}
}

// Past the bound, a node that keeps handing off soon after each fresh start
// is a judgment: its line is below what a fresh session costs, and only a
// person can raise it. Both fresh sessions here handed off within 100s.
func TestWaitReturnsOnAnAgentPastItsHandoffBound(t *testing.T) {
	now := time.Now().Unix()
	a := &fakeAgent{name: "dev", cli: "codex", replies: map[string]bool{}, msgs: map[string]int64{
		"d001.md": now - 1000, "d001.001.handoff.md": now - 200, "d001.002.handoff.md": now - 100}}
	a.tail = []string{"AGENT idle"}
	for _, at := range []int64{now - 210, now - 110, now - 5} {
		a.tail = append(a.tail, fmt.Sprintf("TURNEND %d 0 handoff - handoff at 70000 tokens, over the line of 60000", at))
	}
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
	installFakeWorld(t, w)
	env := waitEnv(t, w)
	env.Manifest.Policy.Handoffs = 2

	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "600"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if !strings.Contains(out, "judgment is due") || !strings.Contains(out, "dev is stuck") ||
		!strings.Contains(out, "hand-off bound tripped") {
		t.Fatalf("a spent hand-off bound is a judgment, and must say so:\n%s", out)
	}
	if len(w.delivered) != 0 || len(w.sessionCmds) != 0 || len(w.started) != 0 {
		t.Fatalf("nothing is sent past the bound: %+v %v %v", w.delivered, w.sessionCmds, w.started)
	}
	if w.probes != len(w.agents) {
		t.Fatalf("a judgment on the first snapshot must return at once, got %d probes", w.probes)
	}
}
