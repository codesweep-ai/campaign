package main

// The readback is the dispatch create sends every agent and judges itself
// (SPEC.md R150). Once create has judged it, the manifest names it, and the
// orchestrator's tools read it as accepted: an agent whose newest dispatch is
// its readback is free, not owed a judgment. These tests drive the real wait
// and accept against the scripted world of wait_test.go. Each wait runs a
// chunk of no seconds, so a judgment wrongly read as free fails the test at
// once instead of blocking a long chunk out.

import (
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

// readbackEnv is waitEnv with dev's manifest record naming readback as the
// dispatch create judged. An empty readback is the manifest of a campaign that
// skipped its readback, or one an older create wrote.
func readbackEnv(t *testing.T, w *fakeWorld, readback string) *envState {
	t.Helper()
	env := waitEnv(t, w)
	rec := env.Manifest.Agents["dev"]
	rec.Readback = readback
	env.Manifest.Agents["dev"] = rec
	return env
}

// An agent sent no work after its readback would otherwise read as owed a
// judgment for the whole campaign: wait must block its chunk out instead.
func TestWaitReadsAJudgedReadbackAsFree(t *testing.T) {
	a := settledAgent("d001.md")
	a.replies["d001"] = true
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
	installFakeWorld(t, w)
	env := readbackEnv(t, w, "d001")

	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if strings.Contains(out, "judgment is due") || strings.Contains(out, "accept dev") {
		t.Fatalf("create judged dev's readback; wait must not ask for it again:\n%s", out)
	}
	if !strings.Contains(out, "dev is free") || !strings.Contains(out, "node-free") {
		t.Fatalf("dev must read free, ready for its first dispatch:\n%s", out)
	}
}

// Without a readback in the manifest the reply is the orchestrator's to judge,
// as any other: nothing else on the orchestrator says create judged it.
func TestWaitStillAsksForAJudgmentWithoutARecordedReadback(t *testing.T) {
	a := settledAgent("d001.md")
	a.replies["d001"] = true
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
	installFakeWorld(t, w)
	env := readbackEnv(t, w, "")

	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if !strings.Contains(out, "a judgment is due") || !strings.Contains(out, "dev replied to d001") {
		t.Fatalf("an unrecorded readback is a reply like any other:\n%s", out)
	}
}

// The readback frees only itself: a reply to the orchestrator's own dispatch
// after it is still a judgment the orchestrator owes.
func TestWaitAsksForAJudgmentOfTheDispatchAfterTheReadback(t *testing.T) {
	a := settledAgent("d001.md", "d002.md")
	a.replies["d001"] = true
	a.replies["d002"] = true
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
	installFakeWorld(t, w)
	env := readbackEnv(t, w, "d001")

	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if !strings.Contains(out, "a judgment is due") || !strings.Contains(out, "dev replied to d002") {
		t.Fatalf("d002 is the orchestrator's dispatch and its reply needs judging:\n%s", out)
	}
}

// accept on the readback says create already accepted it and records nothing:
// an acceptance in the log would be the orchestrator's judgment, and it made
// none.
func TestAcceptOfTheReadbackRecordsNothing(t *testing.T) {
	a := settledAgent("d001.md")
	a.replies["d001"] = true
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
	installFakeWorld(t, w)
	env := readbackEnv(t, w, "d001")

	out, err := captureStdout(t, func() error { return cmdAccept(env, []string{"dev"}) })
	if err != nil {
		t.Fatalf("accept: %v\n%s", err, out)
	}
	want := "dev/d001 is dev's readback, which create judged and accepted: dev is free for its next dispatch\n"
	if out != want {
		t.Fatalf("accept of a readback must say create accepted it:\n got %q\nwant %q", out, want)
	}
	for _, e := range readLog(t, env.Home) {
		if e.Kind == "accepted" {
			t.Fatalf("accept of a readback must write no acceptance: %+v", e)
		}
	}
}

// accept of the dispatch after the readback is the orchestrator's judgment, and
// it lands in the log as one.
func TestAcceptAfterTheReadbackRecordsTheAcceptance(t *testing.T) {
	a := settledAgent("d001.md", "d002.md")
	a.replies["d001"] = true
	a.replies["d002"] = true
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
	installFakeWorld(t, w)
	env := readbackEnv(t, w, "d001")

	out, err := captureStdout(t, func() error { return cmdAccept(env, []string{"dev"}) })
	if err != nil {
		t.Fatalf("accept: %v\n%s", err, out)
	}
	if !strings.Contains(out, "accepted d002 from dev") {
		t.Fatalf("accept of d002 must report the acceptance:\n%s", out)
	}
	if !protocol.AcceptedFor(readLog(t, env.Home), "dev")["d002"] {
		t.Fatalf("accept of d002 must record it: %+v", readLog(t, env.Home))
	}
}
