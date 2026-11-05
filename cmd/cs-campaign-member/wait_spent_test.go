package main

// An agent whose token budget is spent is held by the orchestrator's wait and
// tried again once the wait is over, at no cost to it. Only the operator can
// raise the budget, so the wait says so and sends nothing meanwhile.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

// spentAgent is an agent on d001, opened an hour and a half ago, whose last
// turn ended spent ago seconds back: refused with exit 5, or handed off at
// its budget's line with exit 0.
func spentAgent(exit int, ago int64) *fakeAgent {
	now := time.Now().Unix()
	return &fakeAgent{name: "dev", cli: "codex", replies: map[string]bool{}, msgs: map[string]int64{"d001.md": now - 5400},
		tail: []string{"AGENT idle", fmt.Sprintf("TURNEND %d %d spent - token budget spent: cached 1.004G of 1G", now-ago, exit)}}
}

// While the wait runs nothing is sent, and the orchestrator is told the node
// is held, why, and that its work stays where it is.
func TestWaitHoldsASpentAgentAndSendsNothing(t *testing.T) {
	for _, exit := range []int{5, 0} {
		w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": spentAgent(exit, 60)}}
		installFakeWorld(t, w)
		env := waitEnv(t, w)

		out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
		if err != nil {
			t.Fatalf("wait: %v\n%s", err, out)
		}
		if len(w.delivered) != 0 || len(w.sessionCmds) != 0 || len(w.started) != 0 || len(w.checks) != 0 {
			t.Fatalf("exit %d: nothing is sent while the wait runs: %+v %v %v %v", exit, w.delivered, w.sessionCmds, w.started, w.checks)
		}
		for _, want := range []string{"node-refused", "token budget spent (cached)", "no rung spent",
			"raising its token budget on the host is all it needs", "until the operator raises it on the host", "do not reassign its work"} {
			if !strings.Contains(out, want) {
				t.Errorf("exit %d: wait's report on a spent node is missing %q:\n%s", exit, want, out)
			}
		}
		if strings.Contains(out, "judgment is due") || strings.Contains(out, "provider refused") {
			t.Errorf("exit %d: a spent budget is neither a judgment nor a provider's refusal:\n%s", exit, out)
		}
	}
}

// A refused turn is carried on in the same session: no session is ended, and
// the resume names the message the refused turn was given.
func TestWaitTriesARefusedSpentAgentAgainInItsSession(t *testing.T) {
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": spentAgent(5, 1900)}}
	installFakeWorld(t, w)
	env := waitEnv(t, w)

	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if len(w.sessionCmds) != 0 || len(w.checks) != 0 {
		t.Fatalf("a refused turn keeps its session: %v %v", w.sessionCmds, w.checks)
	}
	if len(w.delivered) != 1 || w.delivered[0].name != "d001.001.resume.md" || w.delivered[0].body != protocol.SpentResumeBody("d001", "d001.md") {
		t.Fatalf("want one resume naming d001.md, got %+v", w.delivered)
	}
	if len(w.started) != 1 || !strings.HasSuffix(w.started[0], " d001") {
		t.Fatalf("the try's turn must start on d001: %v", w.started)
	}
	if !strings.Contains(out, "tried dev (d001) again after its token budget was spent, no rung spent") {
		t.Errorf("wait must report the try it made:\n%s", out)
	}
}

// A node that handed off at its budget's line starts fresh on its handoff at
// the next try, after the same check a context hand-off gets.
func TestWaitStartsASpentHandedOffAgentFreshOnItsHandoff(t *testing.T) {
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": spentAgent(0, 1900)}}
	installFakeWorld(t, w)
	env := waitEnv(t, w)

	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if len(w.checks) != 1 {
		t.Fatalf("the work and the handoff are checked before the swap: %q", w.checks)
	}
	if len(w.sessionCmds) != 2 || !strings.Contains(w.sessionCmds[0], "--kill sess-dev") ||
		!strings.Contains(w.sessionCmds[1], "cs-codex-remote-forget sess-dev") {
		t.Fatalf("the old session is ended and forgotten: %v", w.sessionCmds)
	}
	if len(w.delivered) != 1 || w.delivered[0].name != "d001.001.handoff.md" {
		t.Fatalf("want one .handoff message, got %+v", w.delivered)
	}
	if !strings.Contains(out, "started dev fresh on its handoff (d001), no rung spent") {
		t.Errorf("wait must report the hand-off it performed:\n%s", out)
	}
}

// A reading that says a turn would be sent means the operator raised the
// budget: the try comes on this look, a minute after the refusal and not half
// an hour later. A reading that still asks leaves the hold, and the line says
// the budget is still spent.
func TestWaitTriesASpentAgentAtOnceWhenItsBudgetHasRoom(t *testing.T) {
	for exit, want := range map[int]string{5: "d001.001.resume.md", 0: "d001.001.handoff.md"} {
		a := spentAgent(exit, 60)
		a.tail = append(a.tail, "BUDGET ok")
		w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
		installFakeWorld(t, w)
		env := waitEnv(t, w)
		out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
		if err != nil {
			t.Fatalf("wait: %v\n%s", err, out)
		}
		if len(w.delivered) != 1 || w.delivered[0].name != want || len(w.started) != 1 {
			t.Errorf("exit %d: a budget with room is tried on this look, with %s: %+v %v", exit, want, w.delivered, w.started)
		}
	}

	a := spentAgent(5, 60)
	a.tail = append(a.tail, "BUDGET ask cached")
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
	installFakeWorld(t, w)
	env := waitEnv(t, w)
	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if len(w.delivered) != 0 || len(w.started) != 0 {
		t.Errorf("a reading that still asks leaves the hold: %+v %v", w.delivered, w.started)
	}
	if !strings.Contains(out, "token budget spent (cached) 60s ago, session kept · still spent on its latest reading · trying again in 29m") {
		t.Errorf("the line must say the budget is still spent:\n%s", out)
	}
}
