package main

// The wait ladder — §3.6's Layer 2, correction by code with no model in the
// loop — previously ran only under TestLiveDispatchSmoke (skipped without a
// VM). These tests run the real cmdWait loop against a scripted in-memory
// world: probes serve MSG/REPLY/DRIVERS lines from it, deliveries mutate it,
// and the seams (sshOut, startTurn, runSessionCmd) are the only fakes.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

type fakeAgent struct {
	name    string
	cli     string
	msgs    map[string]int64 // input-channel basename -> mtime
	replies map[string]bool
	drivers int
}

type delivery struct {
	host, name, body string
}

type fakeWorld struct {
	agents      map[string]*fakeAgent // keyed by sandbox host
	delivered   []delivery
	started     []string // "msgPath id" per startTurn
	sessionCmds []string
	// afterProbe runs after a probe is served and before the next command —
	// the gap in which the world can change under the snapshot (finding 4).
	afterProbe func()
	// collide holds message names a concurrent sender claims first: the next
	// delivery to such a name materialises the winner's file and fails with
	// the collision marker, as the noclobber script does.
	collide map[string]bool
}

// putRe matches the delivery fragment of PutFileScript: the base64 payload
// and the $HOME-relative target path.
var putRe = regexp.MustCompile(`printf %s ([A-Za-z0-9+/=]+) \| base64 -d >\|? ~/([^ ;]+)`)

func (w *fakeWorld) sshOut(host, command string) ([]byte, error) {
	a, ok := w.agents[host]
	if !ok {
		return nil, fmt.Errorf("unscripted host %q", host)
	}
	if command == protocol.ProbeScript(a.cli) {
		var b strings.Builder
		for name, mt := range a.msgs {
			fmt.Fprintf(&b, "MSG %d %s\n", mt, name)
		}
		for id, present := range a.replies {
			if present {
				fmt.Fprintf(&b, "REPLY %s\n", id)
			}
		}
		fmt.Fprintf(&b, "DRIVERS %d\n", a.drivers)
		if w.afterProbe != nil {
			w.afterProbe()
		}
		return []byte(b.String()), nil
	}
	if m := putRe.FindStringSubmatch(command); m != nil {
		body, err := base64.StdEncoding.DecodeString(m[1])
		if err != nil {
			return nil, fmt.Errorf("undecodable delivery: %v", err)
		}
		name := strings.TrimPrefix(m[2], protocol.InputDir+"/")
		if w.collide[name] {
			delete(w.collide, name)
			a.msgs[name] = time.Now().Unix() // the winner's file, not ours
			return []byte("CS_MSG_EXISTS ~/" + m[2] + "\n"), errors.New("exit status 1")
		}
		a.msgs[name] = time.Now().Unix()
		w.delivered = append(w.delivered, delivery{host: host, name: name, body: string(body)})
		return nil, nil
	}
	return nil, fmt.Errorf("unscripted command for %s: %s", host, command)
}

func installFakeWorld(t *testing.T, w *fakeWorld) {
	t.Helper()
	origSSH, origStart, origRun := sshOut, startTurn, runSessionCmd
	t.Cleanup(func() { sshOut, startTurn, runSessionCmd = origSSH, origStart, origRun })
	sshOut = w.sshOut
	startTurn = func(home string, rec protocol.AgentRecord, msgPath, id string) error {
		w.started = append(w.started, msgPath+" "+id)
		return nil
	}
	runSessionCmd = func(name string, args ...string) error {
		w.sessionCmds = append(w.sessionCmds, name+" "+strings.Join(args, " "))
		return nil
	}
}

// waitEnv builds an orchestrator envState whose roster is the fake world.
// PollSeconds 1 so the real loop runs fast; SettlingSeconds 1 so messages a
// test backdates count as settled without mocking time.
func waitEnv(t *testing.T, w *fakeWorld) *envState {
	t.Helper()
	agents := map[string]protocol.AgentRecord{}
	for host, a := range w.agents {
		agents[a.name] = protocol.AgentRecord{CLI: a.cli, Sandbox: host, Session: "sess-" + a.name}
	}
	return &envState{
		Home:   t.TempDir(),
		Member: protocol.Member{Role: "orchestrator"},
		Manifest: &protocol.Manifest{
			Policy: protocol.Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100000,
				BlindProbes: 10, PollSeconds: 1, SettlingSeconds: 1},
			Agents: agents,
		},
	}
}

func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = wr
	ferr := f()
	os.Stdout = old
	wr.Close()
	b, _ := io.ReadAll(r)
	return string(b), ferr
}

// settledAgent builds the fake agent every wait test drives: named "dev",
// with its messages far enough in the past to sit outside the settling window.
func settledAgent(msgNames ...string) *fakeAgent {
	past := time.Now().Unix() - 1000
	a := &fakeAgent{name: "dev", cli: "codex", msgs: map[string]int64{}, replies: map[string]bool{}}
	for i, n := range msgNames {
		a.msgs[n] = past + int64(i) // strictly ordered, all far outside settling
	}
	return a
}

// A node-stopped agent with budget left gets the templated continue delivered
// into the SNAPSHOT's open dispatch — a continuation of the open ID, never a
// new dispatch.
func TestWaitContinuesStoppedNode(t *testing.T) {
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": settledAgent("d001.md")}}
	installFakeWorld(t, w)
	env := waitEnv(t, w)

	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if len(w.delivered) != 1 {
		t.Fatalf("want exactly one delivery, got %+v", w.delivered)
	}
	d := w.delivered[0]
	if d.name != "d001.001.md" {
		t.Fatalf("continue must be a continuation of the open dispatch, got %q", d.name)
	}
	if d.body != protocol.ContinueBody("d001") {
		t.Fatalf("continue body must be the template: %q", d.body)
	}
	if len(w.started) != 1 || !strings.HasSuffix(w.started[0], " d001") {
		t.Fatalf("the continue must start a turn on d001: %v", w.started)
	}
	if !strings.Contains(out, "continued dev (d001)") {
		t.Fatalf("wait must report the recovery it performed:\n%s", out)
	}
	if strings.Contains(out, "nothing actionable") || !strings.Contains(out, "recovery performed, no judgment due yet") {
		t.Fatalf("a chunk that ran the ladder must not claim nothing was actionable:\n%s", out)
	}
}

// Continues spent: the ladder escalates to restart — kill and forget the
// session, then deliver a .restart re-anchor into the still-open dispatch.
func TestWaitEscalatesToRestart(t *testing.T) {
	w := &fakeWorld{agents: map[string]*fakeAgent{
		"dev-box": settledAgent("d001.md", "d001.001.md", "d001.002.md"),
	}}
	installFakeWorld(t, w)
	env := waitEnv(t, w)

	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if len(w.sessionCmds) != 2 ||
		!strings.Contains(w.sessionCmds[0], "--kill sess-dev") ||
		!strings.Contains(w.sessionCmds[1], "cs-codex-remote-forget sess-dev") {
		t.Fatalf("restart must kill then forget the session: %v", w.sessionCmds)
	}
	if len(w.delivered) != 1 || w.delivered[0].name != "d001.003.restart.md" {
		t.Fatalf("restart must deliver a .restart re-anchor continuation, got %+v", w.delivered)
	}
	if !strings.Contains(w.delivered[0].body, "Your session was restarted") ||
		!strings.Contains(w.delivered[0].body, "d001.002.md") {
		t.Fatalf("re-anchor must replay the open dispatch's messages:\n%s", w.delivered[0].body)
	}
	if !strings.Contains(out, "restarted dev") {
		t.Fatalf("wait must report the restart:\n%s", out)
	}
}

// A reply is a judgment event: wait returns with the node-replied guidance
// and performs no mechanical move.
func TestWaitReturnsOnReply(t *testing.T) {
	a := settledAgent("d001.md")
	a.replies["d001"] = true
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
	installFakeWorld(t, w)
	env := waitEnv(t, w)

	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "600"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if len(w.delivered) != 0 {
		t.Fatalf("a replied node needs judgment, not recovery: %+v", w.delivered)
	}
	if !strings.Contains(out, "a judgment is due") || !strings.Contains(out, "dev replied to d001") ||
		!strings.Contains(out, "accept dev") {
		t.Fatalf("wait must return with the node-replied judgment guidance:\n%s", out)
	}
}

// Finding-4 regression: the node replies in the gap between the snapshot and
// the delivery. wait must act on the snapshot's own facts — the continue lands
// as a continuation of the open dispatch, and no new dispatch is minted.
func TestWaitReplyRaceMintsNoNewDispatch(t *testing.T) {
	a := settledAgent("d001.md")
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
	w.afterProbe = func() { a.replies["d001"] = true } // the race, every gap
	installFakeWorld(t, w)
	env := waitEnv(t, w)

	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if len(w.delivered) != 1 || w.delivered[0].name != "d001.001.md" {
		t.Fatalf("the continue must land in the snapshot's dispatch, got %+v", w.delivered)
	}
	for _, d := range w.delivered {
		if strings.HasPrefix(d.name, "d002") {
			t.Fatalf("a mid-cycle reply must not mint a new dispatch: %+v", w.delivered)
		}
	}
	for _, s := range w.started {
		if strings.HasSuffix(s, " d002") {
			t.Fatalf("no turn may start on a fabricated dispatch: %v", w.started)
		}
	}
	_ = out
}

// The ID-mint TOCTOU: two concurrent sends both mint d001. The loser's
// delivery hits the noclobber refusal, re-probes — the winner's opener is in
// the listing now — and lands as an ordinary continuation of the winner's
// dispatch. No message is overwritten, no ID is double-assigned.
func TestSendRetriesOnceOnMintCollision(t *testing.T) {
	a := settledAgent() // node-free: this send will mint d001
	w := &fakeWorld{
		agents:  map[string]*fakeAgent{"dev-box": a},
		collide: map[string]bool{"d001.md": true},
	}
	installFakeWorld(t, w)
	env := waitEnv(t, w)

	res, err := sendBody(env, "dev", "the loser's task\n")
	if err != nil {
		t.Fatalf("a lost mint race must be retried, not failed: %v", err)
	}
	if res.ID != "d001" || res.Opened || !res.Raced {
		t.Fatalf("the retry must reclassify into the winner's open dispatch and record the race: %+v", res)
	}
	if len(w.delivered) != 1 || w.delivered[0].name != "d001.001.md" || w.delivered[0].body != "the loser's task\n" {
		t.Fatalf("the loser's body must land as a continuation: %+v", w.delivered)
	}
	if len(w.started) != 1 || !strings.HasSuffix(w.started[0], " d001") {
		t.Fatalf("the turn must start on the dispatch actually delivered into: %v", w.started)
	}
}

// A send that reaches a WORKING node (live driver) delivers its continuation
// but starts no second turn — §1.3 has no deliver-into-a-running-turn case;
// the running turn, or the ladder after it stops, picks the message up. The
// racing-sends live run showed each raced round leaking one extra guest
// session from exactly this double start.
func TestSendSkipsTurnStartWhenTurnIsRunning(t *testing.T) {
	a := settledAgent("d001.md")
	a.drivers = 1
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
	installFakeWorld(t, w)
	env := waitEnv(t, w)

	res, err := sendBody(env, "dev", "midstream note\n")
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "d001" || res.Opened || res.Started {
		t.Fatalf("a continuation into a running turn must not start a turn: %+v", res)
	}
	if len(w.delivered) != 1 || w.delivered[0].name != "d001.001.md" {
		t.Fatalf("the message must still be delivered: %+v", w.delivered)
	}
	if len(w.started) != 0 {
		t.Fatalf("no turn may start on a working node: %v", w.started)
	}

	// A NEW dispatch always starts its turn, even if a stale driver lingers
	// from the previous one — fresh work must not strand behind the ladder.
	a.replies["d001"] = true
	res, err = sendBody(env, "dev", "next task\n")
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "d002" || !res.Opened || !res.Started || len(w.started) != 1 {
		t.Fatalf("an opened dispatch must start its turn: %+v %v", res, w.started)
	}
}

// The send output must name both repairs so an audit can tell them from a
// deliberate continuation.
func TestSendOutputNamesRaceAndSkippedStart(t *testing.T) {
	a := settledAgent()
	a.drivers = 1 // the winner's turn is running by the time the loser retries
	w := &fakeWorld{
		agents:  map[string]*fakeAgent{"dev-box": a},
		collide: map[string]bool{"d001.md": true},
	}
	installFakeWorld(t, w)
	env := waitEnv(t, w)

	out, err := captureStdout(t, func() error { return cmdSend(env, []string{"dev", "the losing task"}) })
	if err != nil {
		t.Fatalf("send: %v\n%s", err, out)
	}
	if !strings.Contains(out, "dev/d001 continued") ||
		!strings.Contains(out, "lost a mint race") ||
		!strings.Contains(out, "a turn is already running") {
		t.Fatalf("the output must name the race and the skipped start:\n%s", out)
	}
	if len(w.started) != 0 {
		t.Fatalf("no turn may start here: %v", w.started)
	}
}

// A second collision in a row is not retried — one retry closes the two-
// parallel-tool-calls race; anything past it is a real fault to surface.
func TestSendGivesUpAfterOneCollisionRetry(t *testing.T) {
	a := settledAgent()
	w := &fakeWorld{
		agents:  map[string]*fakeAgent{"dev-box": a},
		collide: map[string]bool{"d001.md": true, "d001.001.md": true},
	}
	installFakeWorld(t, w)
	env := waitEnv(t, w)

	if _, err := sendBody(env, "dev", "task\n"); err == nil ||
		!strings.Contains(err.Error(), "deliver d001.001.md") {
		t.Fatalf("a second collision must surface as an error: %v", err)
	}
}

// startTurn's flock is the airtight half of the double-start fix: two racing
// send processes serialize on the session lock, so the loser observes the
// winner's session marker instead of spawning a duplicate --new. Proven here
// with the REAL startTurn against a fake tool that records entry/exit — the
// two concurrent calls must never overlap.
func TestStartTurnSerializesPerSession(t *testing.T) {
	home := t.TempDir()
	bin := t.TempDir()
	trace := filepath.Join(home, "trace")
	// Fake setsid: record entry, hold the "turn start" open, record exit.
	script := "#!/bin/sh\necho S >> " + trace + "\nsleep 0.3\necho E >> " + trace + "\n"
	if err := os.WriteFile(filepath.Join(bin, "setsid"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	rec := protocol.AgentRecord{CLI: "codex", Sandbox: "dev-box", Session: "sess-dev"}
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- startTurn(home, rec, "input/d001.md", "d001") }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(b)); !slices.Equal(got, []string{"S", "E", "S", "E"}) {
		t.Fatalf("turn starts must serialize (S E S E), got %v", got)
	}
}

// Nothing actionable: every node working, the chunk elapses, wait says so and
// touches nothing.
func TestWaitChunkElapsesQuietly(t *testing.T) {
	a := settledAgent("d001.md")
	a.drivers = 1
	w := &fakeWorld{agents: map[string]*fakeAgent{"dev-box": a}}
	installFakeWorld(t, w)
	env := waitEnv(t, w)

	out, err := captureStdout(t, func() error { return cmdWait(env, []string{"--for", "0"}) })
	if err != nil {
		t.Fatalf("wait: %v\n%s", err, out)
	}
	if len(w.delivered) != 0 || len(w.sessionCmds) != 0 {
		t.Fatalf("a working node must be left alone: %+v %v", w.delivered, w.sessionCmds)
	}
	if !strings.Contains(out, "nothing actionable") || strings.Contains(out, "judgment is due") {
		t.Fatalf("an elapsed chunk must say nothing was actionable:\n%s", out)
	}
}
