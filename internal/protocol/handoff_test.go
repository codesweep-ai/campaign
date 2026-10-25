package protocol

// A member with a context line hands off when its context crosses it: it
// writes its handoff, ends its turn, and the harness starts it fresh on a
// .handoff message. Everything here is computed from the node's own channel
// and turn log, as every other move is.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A .handoff message is its own kind. Read as a plain continuation it would
// spend a rung on a node that did exactly as it was asked.
func TestAHandoffMessageIsItsOwnKind(t *testing.T) {
	for _, name := range []string{"d007.003.handoff.md", "m1.001.handoff.md"} {
		m, ok := ParseMsgName(name, 7)
		if !ok {
			t.Fatalf("%s is a message name and was not read as one", name)
		}
		if !m.Handoff || m.Resume || m.Restart {
			t.Errorf("%s: got %+v, want a hand-off and nothing else", name, m)
		}
	}
	if m, _ := ParseMsgName("d007.003.handoff.md", 7); m.ID != "d007" || m.Seq != 3 {
		t.Errorf("the hand-off belongs to d007 at seq 3: %+v", m)
	}
	// The marker rides a continuation number, as .restart and .resume do.
	for _, name := range []string{"d007.handoff.md", "d007.003.handoff", "d007.003.handoffs.md"} {
		if _, ok := ParseMsgName(name, 7); ok {
			t.Errorf("%s is not a name the protocol mints, and was read as one", name)
		}
	}
}

// Each kind of continuation is counted apart, and the next hand-off takes the
// next number in the dispatch whatever kind came before it.
func TestHandOffsAreCountedApartAndNumberedInTurn(t *testing.T) {
	ms := msgs("d001.md@100", "d001.001.md@200", "d001.002.handoff.md@300", "d001.003.resume.md@400", "d001.004.restart.md@500")
	d := Current(ms)
	if d.Continues != 1 || d.Restarts != 1 || d.Resumes != 1 || d.Handoffs != 1 {
		t.Fatalf("one of each kind, counted apart: %+v", d)
	}
	if n := NextHandoffName(ms, "d001"); n != "d001.005.handoff.md" {
		t.Errorf("NextHandoffName = %s, want d001.005.handoff.md", n)
	}
	if n := NextHandoffName(msgs("d001.md@100"), "d001"); n != "d001.001.handoff.md" {
		t.Errorf("the first hand-off on a dispatch: %s", n)
	}
}

// A .commit message is its own kind too. It is what a node reads when its
// hand-off was not taken, and read as a plain continuation it would spend a
// rung on a node that handed off as asked and only forgot to commit.
func TestACommitMessageIsItsOwnKind(t *testing.T) {
	for _, name := range []string{"d003.002.commit.md", "m1.001.commit.md"} {
		m, ok := ParseMsgName(name, 7)
		if !ok {
			t.Fatalf("%s is a message name and was not read as one", name)
		}
		if !m.Commit || m.Handoff || m.Resume || m.Restart {
			t.Errorf("%s: got %+v, want a commit ask and nothing else", name, m)
		}
	}
	if m, _ := ParseMsgName("d003.002.commit.md", 7); m.ID != "d003" || m.Seq != 2 {
		t.Errorf("the commit ask belongs to d003 at seq 2: %+v", m)
	}
	for _, name := range []string{"d003.commit.md", "d003.002.commit", "d003.002.commits.md"} {
		if _, ok := ParseMsgName(name, 7); ok {
			t.Errorf("%s is not a name the protocol mints, and was read as one", name)
		}
	}
}

// A commit ask is counted apart from continues, and takes the next number in
// the dispatch whatever kind came before it.
func TestCommitAsksAreCountedApartAndNumberedInTurn(t *testing.T) {
	ms := msgs("d001.md@100", "d001.001.commit.md@200", "d001.002.md@300", "d001.003.handoff.md@400")
	d := Current(ms)
	if d.CommitAsks != 1 || d.Continues != 1 || d.Handoffs != 1 || d.Restarts != 0 || d.Resumes != 0 {
		t.Fatalf("one commit ask, one continue, one hand-off, counted apart: %+v", d)
	}
	if n := NextCommitName(ms, "d001"); n != "d001.004.commit.md" {
		t.Errorf("NextCommitName = %s, want d001.004.commit.md", n)
	}
	if n := NextCommitName(msgs("d001.md@100"), "d001"); n != "d001.001.commit.md" {
		t.Errorf("the first commit ask on a dispatch: %s", n)
	}
}

// A commit ask spends no rung, whether its turn ran or not.
func TestACommitAskIsNeverCharged(t *testing.T) {
	ms := msgs("d001.md@100", "d001.001.commit.md@200", "d001.002.md@300")
	d := Current(ms)
	f := Facts{Msgs: ms, Agent: "idle", TurnEnds: []TurnEnd{
		{At: 150, Class: ClassHandoff},
		{At: 250, Class: ClassHandoff}, // the kept session's turn ran
	}}
	if c, r := Charged(d, f); c != 1 || r != 0 {
		t.Errorf("Charged = %d continues, %d restarts; want the one continue alone", c, r)
	}
	if c, r := Charged(d, Facts{Msgs: ms}); c != 1 || r != 0 {
		t.Errorf("Charged without a turn log = %d continues, %d restarts; want 1, 0", c, r)
	}
}

// A node asked to commit that hands off again is offered the fresh session
// again, and the detail counts the asks. A rapid ask and a rapid hand-off
// count in one run against the bound, so a node that never commits is stuck
// rather than asked for ever, and the detail then names the asks, because
// raising the line would not help. Every session here after the first hands
// off within 200s of the message it began on, inside the default window.
func TestCommitAsksCountAgainstTheHandoffBound(t *testing.T) {
	now := int64(10000)
	pol := Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100000, BlindProbes: 3, SettlingSeconds: 100, Handoffs: 2}
	f := Facts{Msgs: msgs("d001.md@9000", "d001.001.commit.md@9800"), Replies: map[string]bool{}, Agent: "idle",
		TurnEnds: []TurnEnd{handoffEnd(9790), handoffEnd(9990)}}
	o := Compute(f, false, Blind{}, map[string]bool{}, pol, now)
	if o.State != StateStopped || o.NextMove != "handoff" {
		t.Fatalf("one rapid ask under a bound of two leaves room for the hand-off: %+v", o)
	}
	if !strings.Contains(o.Detail, "0 cont, 0 restarts, 0 hand-offs, 1 asked to commit · handed off") {
		t.Errorf("the detail must count the ask, and charge it as no continue: %q", o.Detail)
	}

	f.Msgs = msgs("d001.md@9000", "d001.001.handoff.md@9700", "d001.002.commit.md@9800")
	f.TurnEnds = []TurnEnd{handoffEnd(9690), handoffEnd(9790), handoffEnd(9990)}
	o = Compute(f, false, Blind{}, map[string]bool{}, pol, now)
	if o.State != StateStuck || o.NextMove != "" {
		t.Fatalf("a rapid hand-off and a rapid ask under a bound of two is node-stuck, with no move: %+v", o)
	}
	if !strings.Contains(o.Detail, "handed off 2 times in a row, each within 5m of a fresh start, 1 of them after an ask to commit first — hand-off bound tripped") {
		t.Errorf("the detail must name the run, the asks and the bound: %q", o.Detail)
	}
	if strings.Contains(o.Detail, "the line is below") {
		t.Errorf("a node that does not commit is not one whose line is too low: %q", o.Detail)
	}

	// With no ask, neither line names one.
	f.Msgs = msgs("d001.md@9000", "d001.001.handoff.md@9800")
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); !strings.Contains(o.Detail, "0 cont, 0 restarts, 1 hand-offs · handed off") ||
		strings.Contains(o.Detail, "asked") {
		t.Errorf("with no ask the detail names none: %q", o.Detail)
	}
	f.Msgs = msgs("d001.md@9000", "d001.001.handoff.md@9700", "d001.002.handoff.md@9800")
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.State != StateStuck ||
		!strings.Contains(o.Detail, "handed off 2 times in a row, each within 5m of a fresh start — hand-off bound tripped; the line is below what a fresh session costs") ||
		strings.Contains(o.Detail, "ask") {
		t.Errorf("with no ask the stuck line blames the line: %+v", o)
	}
}

// A hand-off spends no rung. A .handoff message whose turn ran must not be
// charged as a continue, or a node that handed off twice would find its
// continues spent and be restarted for doing as it was asked.
func TestAHandoffIsNeverCharged(t *testing.T) {
	ms := msgs("d001.md@100", "d001.001.handoff.md@200", "d001.002.md@300")
	d := Current(ms)
	f := Facts{Msgs: ms, Agent: "idle", TurnEnds: []TurnEnd{
		{At: 150, Class: ClassHandoff},
		{At: 250}, // the fresh session's turn ran
	}}
	if c, r := Charged(d, f); c != 1 || r != 0 {
		t.Errorf("Charged = %d continues, %d restarts; want the one continue alone", c, r)
	}
	// A node whose tools keep no turn log is charged from the listing, which
	// counts hand-offs apart too.
	if c, r := Charged(d, Facts{Msgs: ms}); c != 1 || r != 0 {
		t.Errorf("Charged without a turn log = %d continues, %d restarts; want 1, 0", c, r)
	}
}

// handoffEnd is the turn end a turn driver writes when the node ended its turn
// after crossing its line.
func handoffEnd(at int64) TurnEnd {
	return TurnEnd{At: at, Exit: 0, Class: ClassHandoff, Reason: "handoff at 70000 tokens, over the line of 60000"}
}

// A turn that ended in a hand-off, newer than the newest message, is the one
// state whose move is a fresh session on the node's handoff, and it says that
// no rung is spent.
func TestATurnEndedInAHandoffCallsForAFreshSession(t *testing.T) {
	pol := Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100000, BlindProbes: 3, SettlingSeconds: 100}
	now := int64(10000)
	f := Facts{Msgs: msgs("d001.md@9000"), Replies: map[string]bool{}, Agent: "idle", TurnEnds: []TurnEnd{handoffEnd(9990)}}
	o := Compute(f, false, Blind{}, map[string]bool{}, pol, now)
	if o.State != StateStopped || o.NextMove != "handoff" || o.Dispatch != "d001" {
		t.Fatalf("a hand-off is node-stopped with the hand-off move: %+v", o)
	}
	for _, want := range []string{"fresh session next, no rung spent", "0 hand-offs", "over the line of 60000"} {
		if !strings.Contains(o.Detail, want) {
			t.Errorf("the detail does not say %q: %q", want, o.Detail)
		}
	}
	// The move is read off the turn end, not off the settling window: a turn
	// that has ended is not one that is starting.
	f.Msgs = msgs("d001.md@9950")
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.NextMove != "handoff" {
		t.Errorf("a hand-off inside the settling window is still a hand-off: %+v", o)
	}
	// A node in a turn is working, whatever its log says about the last one.
	f.Agent = "busy"
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.State != StateWorking {
		t.Errorf("a busy node is working: %+v", o)
	}
}

// A line set below what a fresh session costs would hand off for ever, each
// fresh session soon after it started. The bound makes that node-stuck, and
// the detail names the bound so an operator knows to raise the line rather
// than look for a fault in the node.
func TestTheHandoffBoundStopsALineBelowAFreshSession(t *testing.T) {
	now := int64(10000)
	pol := Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100000, BlindProbes: 3, SettlingSeconds: 100, Handoffs: 2}
	f := Facts{Msgs: msgs("d001.md@9000", "d001.001.handoff.md@9800"), Replies: map[string]bool{}, Agent: "idle",
		TurnEnds: []TurnEnd{handoffEnd(9790), handoffEnd(9990)}}
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.State != StateStopped || o.NextMove != "handoff" || !strings.Contains(o.Detail, "1 hand-offs") {
		t.Fatalf("one rapid hand-off under a bound of two leaves room for another: %+v", o)
	}
	f.Msgs = msgs("d001.md@9000", "d001.001.handoff.md@9700", "d001.002.handoff.md@9800")
	f.TurnEnds = []TurnEnd{handoffEnd(9690), handoffEnd(9790), handoffEnd(9990)}
	o := Compute(f, false, Blind{}, map[string]bool{}, pol, now)
	if o.State != StateStuck || o.NextMove != "" {
		t.Fatalf("two rapid hand-offs under a bound of two is node-stuck, with no move: %+v", o)
	}
	if !strings.Contains(o.Detail, "handed off 2 times in a row") || !strings.Contains(o.Detail, "hand-off bound tripped") {
		t.Errorf("the detail must name the count and the bound: %q", o.Detail)
	}

	// A campaign with no line records no bound, and a zero still stops at the
	// default of ten: the machine is never unbounded.
	pol.Handoffs = 0
	f, now = handedOff(1200, slices.Repeat([]session{{"handoff", 60}}, 9)...)
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.NextMove != "handoff" {
		t.Errorf("nine rapid hand-offs are under the default bound: %+v", o)
	}
	f, now = handedOff(1200, slices.Repeat([]session{{"handoff", 60}}, 10)...)
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.State != StateStuck {
		t.Errorf("ten rapid hand-offs reach the default bound: %+v", o)
	}
}

// session is one session on a dispatch after its first: the kind of message
// it began on, handoff or commit, and how long it worked before it handed off.
type session struct {
	kind string
	secs int64
}

// handedOff lays out a dispatch whose sessions each handed off: the first
// works first seconds from the dispatch's own message, and each session after
// it begins on its message 5s after the hand-off before it. The turn log holds
// every hand-off, and now is 10s after the newest, so the move on it is due.
func handedOff(first int64, then ...session) (Facts, int64) {
	return handedOffAfter(5, first, then...)
}

// handedOffAfter is handedOff with gap seconds between each hand-off and the
// message that answers it.
func handedOffAfter(gap, first int64, then ...session) (Facts, int64) {
	at := int64(100_000)
	names := []string{fmt.Sprintf("d001.md@%d", at)}
	at += first
	ends := []TurnEnd{handoffEnd(at)}
	for i, s := range then {
		at += gap
		names = append(names, fmt.Sprintf("d001.%03d.%s.md@%d", i+1, s.kind, at))
		at += s.secs
		ends = append(ends, handoffEnd(at))
	}
	return Facts{Msgs: msgs(names...), Replies: map[string]bool{}, Agent: "idle", TurnEnds: ends}, at + 10
}

// boundPolicy is a policy whose only bound in reach is the hand-off bound.
var boundPolicy = Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 1_000_000, BlindProbes: 3, SettlingSeconds: 100}

// The bound counts rapid hand-offs in a row: ten fresh sessions that each
// handed off within five minutes of their start trip it at the numbers a
// campaign with a line records. The line says how many, and the window.
func TestTenRapidHandoffsInARowTripTheBound(t *testing.T) {
	pol := boundPolicy.WithHandoffBound(true)
	f, now := handedOff(1200, slices.Repeat([]session{{"handoff", 60}}, 9)...)
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.State != StateStopped || o.NextMove != "handoff" || !strings.Contains(o.Detail, "9 hand-offs") {
		t.Fatalf("nine rapid hand-offs leave room for a tenth: %+v", o)
	}
	f, now = handedOff(1200, slices.Repeat([]session{{"handoff", 60}}, 10)...)
	o := Compute(f, false, Blind{}, map[string]bool{}, pol, now)
	want := "handed off 10 times in a row, each within 5m of a fresh start — hand-off bound tripped; the line is below what a fresh session costs"
	if o.State != StateStuck || o.NextMove != "" || !strings.HasPrefix(o.Detail, want) {
		t.Errorf("ten rapid hand-offs in a row read %s (%q), want node-stuck with %q", o.State, o.Detail, want)
	}
}

// The orchestrator's whole mission is one dispatch, so a long mission hands
// off many times. A session that does a session's work before it hands off is
// not one the bound is for, and fifteen of them never trip it.
func TestALongMissionsHandoffsNeverTripTheBound(t *testing.T) {
	f, now := handedOff(1200, slices.Repeat([]session{{"handoff", 1200}}, 15)...)
	o := Compute(f, false, Blind{}, map[string]bool{}, boundPolicy.WithHandoffBound(true), now)
	if o.State != StateStopped || o.NextMove != "handoff" {
		t.Fatalf("fifteen hand-offs, each after twenty minutes of work, are a fresh session each: %+v", o)
	}
	if !strings.Contains(o.Detail, "15 hand-offs · handed off") || !strings.Contains(o.Detail, "fresh session next, no rung spent") {
		t.Errorf("the pending line still counts every hand-off: %q", o.Detail)
	}
}

// The run is the one at the tail of the dispatch. A session that works past
// the window ends it, so the rapid hand-offs before it are not counted with
// the ones after.
func TestALongSessionEndsTheRun(t *testing.T) {
	r, long := session{"handoff", 60}, session{"handoff", 1200}
	sessions := append([]session{r, r, long}, slices.Repeat([]session{r}, 9)...)
	f, now := handedOff(1200, sessions...)
	if o := Compute(f, false, Blind{}, map[string]bool{}, boundPolicy, now); o.State != StateStopped || o.NextMove != "handoff" {
		t.Fatalf("nine rapid hand-offs since a long session are under the bound of ten, whatever came before it: %+v", o)
	}
	f, now = handedOff(1200, append(sessions, r)...)
	if o := Compute(f, false, Blind{}, map[string]bool{}, boundPolicy, now); o.State != StateStuck || !strings.HasPrefix(o.Detail, "handed off 10 times in a row") {
		t.Errorf("a tenth rapid one since the long session trips it: %+v", o)
	}
}

// A dispatch's first session began on the dispatch itself, not on a hand-off
// or an ask, so its hand-off is never rapid however soon it came. At a bound
// of one, that first hand-off still moves, and the next rapid one trips it.
func TestADispatchsFirstSessionIsNeverRapid(t *testing.T) {
	pol := boundPolicy
	pol.Handoffs = 1
	f, now := handedOff(60)
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.State != StateStopped || o.NextMove != "handoff" {
		t.Fatalf("a hand-off a minute into a dispatch is its first session's, and is not counted: %+v", o)
	}
	// A continue is not a fresh start either: the session it carries on began
	// with the dispatch.
	f.Msgs = msgs("d001.md@100000", "d001.001.md@100030")
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.NextMove != "handoff" {
		t.Errorf("a hand-off after a continue is still the first session's: %+v", o)
	}
	f, now = handedOff(60, session{"handoff", 60})
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.State != StateStuck || !strings.HasPrefix(o.Detail, "handed off 1 times in a row") {
		t.Errorf("the first rapid hand-off of a fresh session reaches a bound of one: %+v", o)
	}
}

// A .commit message keeps the session that handed off and asks it to commit,
// so the turn it starts is short by nature. A node that is asked on every
// hand-off trips the bound, and its line counts the asks rather than blaming
// the line. A node that commits once asked is not caught: the fresh session
// after the ask does a session's work.
func TestANodeAskedToCommitOnEveryHandoffTripsTheBound(t *testing.T) {
	ask := session{"commit", 30}
	f, now := handedOff(1200, slices.Repeat([]session{ask}, 10)...)
	o := Compute(f, false, Blind{}, map[string]bool{}, boundPolicy, now)
	want := "handed off 10 times in a row, each within 5m of a fresh start, 10 of them after an ask to commit first — hand-off bound tripped"
	if o.State != StateStuck || !strings.HasPrefix(o.Detail, want) || strings.Contains(o.Detail, "the line is below") {
		t.Errorf("ten asks in a row read %s (%q), want node-stuck with %q", o.State, o.Detail, want)
	}

	// A line below a fresh session and a node that does not commit, together:
	// the run counts both, and the line names how many were asks.
	mixed := slices.Repeat([]session{{"handoff", 60}, ask}, 5)
	f, now = handedOff(1200, mixed...)
	if o := Compute(f, false, Blind{}, map[string]bool{}, boundPolicy, now); o.State != StateStuck ||
		!strings.HasPrefix(o.Detail, "handed off 10 times in a row, each within 5m of a fresh start, 5 of them after an ask to commit first") {
		t.Errorf("a run of five hand-offs and five asks: %+v", o)
	}

	ordinary := slices.Repeat([]session{ask, {"handoff", 1200}}, 8)
	f, now = handedOff(1200, append(ordinary, ask)...)
	if o := Compute(f, false, Blind{}, map[string]bool{}, boundPolicy, now); o.State != StateStopped || o.NextMove != "handoff" ||
		!strings.Contains(o.Detail, "8 hand-offs, 9 asked to commit") {
		t.Errorf("one ask before each hand-off is ordinary, however many: %+v", o)
	}
}

// An earlier session ends at its hand-off turn end. Once the turn log's tail
// no longer holds it, the session is measured to the next message, which came
// after it. So a session can read longer than it was, and never shorter: a
// slow answer to a rapid hand-off loses it from the run, and never puts a
// long session in one.
func TestASessionPastTheTurnLogReadsLongerNeverShorter(t *testing.T) {
	pol := boundPolicy
	pol.Handoffs = 3
	rapid := slices.Repeat([]session{{"handoff", 60}}, 3)
	tail := func(f Facts) Facts { f.TurnEnds = f.TurnEnds[len(f.TurnEnds)-1:]; return f }

	f, now := handedOffAfter(400, 1200, rapid...)
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.State != StateStuck {
		t.Fatalf("three rapid hand-offs, each read to its own turn end, reach a bound of three: %+v", o)
	}
	if o := Compute(tail(f), false, Blind{}, map[string]bool{}, pol, now); o.State != StateStopped || o.NextMove != "handoff" {
		t.Errorf("past the log, each earlier session runs to a message 400s after its hand-off, and is not rapid: %+v", o)
	}
	// Answered promptly, the next message is close to the turn end it answers,
	// and the run reads as it was.
	f, now = handedOff(1200, rapid...)
	if o := Compute(tail(f), false, Blind{}, map[string]bool{}, pol, now); o.State != StateStuck {
		t.Errorf("rapid sessions answered within seconds stay rapid past the log: %+v", o)
	}
	// A long session stays long past the log.
	f, now = handedOff(1200, session{"handoff", 1200}, session{"handoff", 60}, session{"handoff", 60})
	if o := Compute(tail(f), false, Blind{}, map[string]bool{}, pol, now); o.State != StateStopped || o.NextMove != "handoff" {
		t.Errorf("a long session read past the log is still long: %+v", o)
	}
}

// The window is the policy's rapidHandoffSeconds. A smaller one counts fewer
// sessions as rapid, and zero reads as the default of 300, as a campaign
// with no line records none.
func TestTheRapidWindowIsThePolicys(t *testing.T) {
	sessions := []session{{"handoff", 200}, {"handoff", 100}, {"handoff", 100}}
	f, now := handedOff(1200, sessions...)
	for _, tc := range []struct {
		window, bound int
		stuck         string
	}{
		{0, 3, "handed off 3 times in a row, each within 5m of a fresh start"},
		{300, 3, "handed off 3 times in a row, each within 5m of a fresh start"},
		{120, 3, ""},
		{120, 2, "handed off 2 times in a row, each within 2m of a fresh start"},
	} {
		pol := boundPolicy
		pol.Handoffs, pol.RapidHandoffSeconds = tc.bound, tc.window
		o := Compute(f, false, Blind{}, map[string]bool{}, pol, now)
		if tc.stuck == "" {
			if o.State != StateStopped || o.NextMove != "handoff" {
				t.Errorf("window %d, bound %d: a 200s session is past the window and ends the run at two: %+v", tc.window, tc.bound, o)
			}
			continue
		}
		if o.State != StateStuck || !strings.HasPrefix(o.Detail, tc.stuck) {
			t.Errorf("window %d, bound %d: got %s (%q), want %q", tc.window, tc.bound, o.State, o.Detail, tc.stuck)
		}
	}
}

// A hand-off counts only for the attempt it ended. Once the fresh session's
// message is delivered, the old turn end is older than it and is not read
// again: otherwise every look would start another fresh session on the same
// hand-off.
func TestAnOldHandoffIsNotReadAgain(t *testing.T) {
	pol := Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100000, BlindProbes: 3, SettlingSeconds: 100}
	now := int64(10000)
	f := Facts{Msgs: msgs("d001.md@9000", "d001.001.handoff.md@9995"), Replies: map[string]bool{}, Agent: "idle", TurnEnds: []TurnEnd{handoffEnd(9990)}}
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.State != StateWorking {
		t.Errorf("a fresh session that was just sent is starting: %+v", o)
	}
	// Past settling with no turn since: the fresh session's turn never ran,
	// which is a resume, not another hand-off.
	f.Msgs = msgs("d001.md@9000", "d001.001.handoff.md@9800")
	f.TurnEnds = []TurnEnd{handoffEnd(9700)}
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.NextMove == "handoff" || o.NextMove != "resume" {
		t.Errorf("a hand-off older than the newest message is not read as one: %+v", o)
	}
}

// A refused turn is the provider's doing, never the node's hand-off.
func TestARefusedTurnIsNeverAHandoff(t *testing.T) {
	pol := Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100000, BlindProbes: 3, SettlingSeconds: 100}
	now := int64(10000)
	f := Facts{Msgs: msgs("d001.md@9000"), Replies: map[string]bool{}, Agent: "idle",
		TurnEnds: []TurnEnd{{At: 9000 + 100, Exit: 5, Class: ClassThrottled, Reason: "Rate limit reached"}}}
	o := Compute(f, false, Blind{}, map[string]bool{}, pol, now)
	if o.State != StateRefused || o.NextMove == "handoff" {
		t.Errorf("a throttled turn is refused: %+v", o)
	}
}

// The driver adds handoff= and floor= to its CONTEXT line for a seat with a
// line. A driver from before lines prints neither, and its line still parses.
func TestTheContextLineCarriesTheHandoffLine(t *testing.T) {
	for _, tc := range []struct {
		line string
		want ContextReading
	}{
		{"CONTEXT tokens=70000 window=- compactions=0 compacted=- model=m handoff=60000 floor=-",
			ContextReading{Tokens: 70000, Model: "m", Handoff: 60000}},
		{"CONTEXT tokens=70000 window=- compactions=0 compacted=- model=m handoff=60000 floor=70000",
			ContextReading{Tokens: 70000, Model: "m", Handoff: 60000, Floor: 70000}},
		{"CONTEXT tokens=70000 window=- compactions=0 compacted=- model=m handoff=- floor=-",
			ContextReading{Tokens: 70000, Model: "m"}},
		{"CONTEXT tokens=70000 window=- compactions=0 compacted=- model=m",
			ContextReading{Tokens: 70000, Model: "m"}},
	} {
		f := ParseProbe("DRIVERS 0\n" + tc.line + "\n")
		if f.Context == nil || *f.Context != tc.want {
			t.Errorf("%q parsed as %+v, want %+v", tc.line, f.Context, tc.want)
		}
	}
}

// The note says where the node will hand off, and says when the session began
// over its line and so will never be asked.
func TestTheContextNoteNamesTheLineAndTheFloor(t *testing.T) {
	now := int64(10000)
	for _, tc := range []struct {
		c    *ContextReading
		want string
	}{
		{&ContextReading{Tokens: 70000, Handoff: 60000}, " · context 70k · hands off at 60k"},
		{&ContextReading{Tokens: 70000, Handoff: 60000, Floor: 70000},
			" · context 70k · hands off at 60k · this session began over the line, at 70k, and will not be asked"},
		{&ContextReading{Tokens: 70000}, " · context 70k"},
	} {
		if got := contextNote(tc.c, now); got != tc.want {
			t.Errorf("contextNote(%+v) = %q, want %q", *tc.c, got, tc.want)
		}
	}
	// The line is the node's to act on, through its hook. A reading over it
	// changes no state the harness computes.
	pol := Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 100000, BlindProbes: 3, SettlingSeconds: 100}
	f := Facts{Msgs: msgs("d001.md@9000"), Replies: map[string]bool{}}
	without := Compute(f, false, Blind{}, map[string]bool{}, pol, now)
	f.Context = &ContextReading{Tokens: 90000, Handoff: 60000}
	with := Compute(f, false, Blind{}, map[string]bool{}, pol, now)
	if with.State != without.State || with.NextMove != without.NextMove {
		t.Errorf("a reading over the line changed the computation: %+v, then %+v", without, with)
	}
}

// The fresh session knows nothing, so its first message names every file it
// must read, in the order it must read them, ending at the handoff.
func TestTheHandoffBodyReplaysTheDispatchAndEndsAtTheHandoff(t *testing.T) {
	names := []string{"d002.md", "d002.001.md", "d002.002.handoff.md"}
	body := HandoffBody("d002", names, false)
	for _, want := range []string{"fresh session after a hand-off", "~/" + MemberDoc, "d002", "~/" + HandoffFile,
		"When you start fresh", "cs-campaign-member reply"} {
		if !strings.Contains(body, want) {
			t.Errorf("the body does not say %q:\n%s", want, body)
		}
	}
	at := []int{strings.Index(body, "~/"+MemberDoc)}
	for _, n := range names {
		at = append(at, strings.Index(body, "~/"+InputDir+"/"+n))
	}
	at = append(at, strings.Index(body, "~/"+HandoffFile))
	if slices.Contains(at, -1) || !slices.IsSorted(at) {
		t.Errorf("member.json, then every message in order, then the handoff; positions %v:\n%s", at, body)
	}
	if strings.Contains(body, LogFile) {
		t.Errorf("an agent keeps no log, and is not pointed at one:\n%s", body)
	}

	// The orchestrator's sent dispatches and judgments are in its log, which
	// it reads before its handoff.
	orch := HandoffBody("m1", []string{"m1.md", "m1.001.handoff.md"}, true)
	log, handoff := strings.Index(orch, "~/"+LogFile), strings.Index(orch, "~/"+HandoffFile)
	if log < 0 || handoff < log {
		t.Errorf("the orchestrator reads its log, then its handoff:\n%s", orch)
	}
}

// gitEnv is the environment the check and its fixtures run under: a HOME of
// the test's own, and none of the machine's git configuration or GIT_ state.
func gitEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "HOME=") {
			env = append(env, kv)
		}
	}
	return append(env, "HOME="+home, "GIT_CONFIG_NOSYSTEM=1")
}

// git runs git in dir and returns what it printed.
func git(t *testing.T, home, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Env = gitEnv(home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes ~/name a repository with one commit, and returns that commit:
// the base the member's branch starts from.
func newRepo(t *testing.T, home, name string) string {
	t.Helper()
	dir := filepath.Join(home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, home, dir, "init", "-q")
	writeFile(t, filepath.Join(dir, "a.txt"), "one\n")
	git(t, home, dir, "add", "a.txt")
	git(t, home, dir, "commit", "-q", "-m", "base")
	return git(t, home, dir, "rev-parse", "HEAD")
}

// commitWork makes one more commit in ~/name, and returns it.
func commitWork(t *testing.T, home, name string) string {
	t.Helper()
	dir := filepath.Join(home, name)
	writeFile(t, filepath.Join(dir, "b.txt"), "work\n")
	git(t, home, dir, "add", "b.txt")
	git(t, home, dir, "commit", "-q", "-m", "work")
	return git(t, home, dir, "rev-parse", "HEAD")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The check runs on the member, so it runs here for real, against real
// repositories, under the shells a member's command meets: sh, which the
// host's exec asks for, and bash, a login shell ssh may hand it to. It exits
// 0 whatever it finds, because a failed exit reads as a check that did not
// run, and that holds the hand-off back without saying why.
func TestTheHandoffCheckReadsGitAndTheHandoff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	handoff := "~/" + HandoffFile
	for _, tc := range []struct {
		name string
		// setup builds HOME, and returns the repositories the check is asked
		// about, what it must print, and the sentences that makes.
		setup func(t *testing.T, home string) (repos map[string]string, out string, problems []string)
	}{
		{"no handoff", func(t *testing.T, home string) (map[string]string, string, []string) {
			return map[string]string{"app": newRepo(t, home, "app")}, "NOHANDOFF\n",
				[]string{"there is no handoff at " + handoff}
		}},
		{"a tracked change and an untracked file", func(t *testing.T, home string) (map[string]string, string, []string) {
			base := newRepo(t, home, "app")
			writeFile(t, filepath.Join(home, HandoffFile), "nothing committed yet\n")
			writeFile(t, filepath.Join(home, "app", "a.txt"), "changed\n")
			writeFile(t, filepath.Join(home, "app", "z.txt"), "new\n")
			return map[string]string{"app": base}, "DIRTY\tapp\t2\ta.txt\n",
				[]string{"~/app has work not committed (2 paths, the first a.txt)"}
		}},
		{"HEAD moved from its base and not named", func(t *testing.T, home string) (map[string]string, string, []string) {
			base := newRepo(t, home, "app")
			head := commitWork(t, home, "app")
			writeFile(t, filepath.Join(home, HandoffFile), "done: the parser, on my branch\n")
			return map[string]string{"app": base}, "UNNAMED\tapp\t" + head[:7] + "\n",
				[]string{"your handoff does not name the commit ~/app is at, " + head[:7]}
		}},
		{"HEAD named by a nine-character abbreviation", func(t *testing.T, home string) (map[string]string, string, []string) {
			base := newRepo(t, home, "app")
			head := commitWork(t, home, "app")
			writeFile(t, filepath.Join(home, HandoffFile), "~/app is at "+head[:9]+"\n")
			return map[string]string{"app": base}, "", nil
		}},
		{"HEAD at its base and not named", func(t *testing.T, home string) (map[string]string, string, []string) {
			base := newRepo(t, home, "app")
			writeFile(t, filepath.Join(home, HandoffFile), "read the code; changed nothing\n")
			return map[string]string{"app": base}, "", nil
		}},
		{"a repository not on the machine", func(t *testing.T, home string) (map[string]string, string, []string) {
			writeFile(t, filepath.Join(home, HandoffFile), "nothing to say\n")
			return map[string]string{"gone": "0123456789abcdef0123456789abcdef01234567"}, "", nil
		}},
		{"a name holding a single quote", func(t *testing.T, home string) (map[string]string, string, []string) {
			base := newRepo(t, home, "it's")
			head := commitWork(t, home, "it's")
			writeFile(t, filepath.Join(home, HandoffFile), "not named here\n")
			writeFile(t, filepath.Join(home, "it's", "new.txt"), "new\n")
			return map[string]string{"it's": base}, "DIRTY\tit's\t1\tnew.txt\nUNNAMED\tit's\t" + head[:7] + "\n",
				[]string{"~/it's has work not committed (1 path, the first new.txt)",
					"your handoff does not name the commit ~/it's is at, " + head[:7]}
		}},
		{"a name and a path holding spaces", func(t *testing.T, home string) (map[string]string, string, []string) {
			base := newRepo(t, home, "my app")
			head := commitWork(t, home, "my app")
			writeFile(t, filepath.Join(home, HandoffFile), "not named here\n")
			writeFile(t, filepath.Join(home, "my app", "two words.txt"), "new\n")
			return map[string]string{"my app": base}, "DIRTY\tmy app\t1\t\"two words.txt\"\nUNNAMED\tmy app\t" + head[:7] + "\n",
				[]string{"~/my app has work not committed (1 path, the first \"two words.txt\")",
					"your handoff does not name the commit ~/my app is at, " + head[:7]}
		}},
	} {
		for _, shell := range []string{"sh", "bash"} {
			t.Run(tc.name+"/"+shell, func(t *testing.T) {
				if _, err := exec.LookPath(shell); err != nil {
					t.Skipf("%s is not installed", shell)
				}
				home := t.TempDir()
				repos, want, problems := tc.setup(t, home)
				cmd := exec.Command(shell, "-c", HandoffCheckScript(repos))
				cmd.Env = gitEnv(home)
				var stderr strings.Builder
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("the check must exit 0 whatever it finds: %v\n%s%s", err, out, stderr.String())
				}
				if string(out) != want {
					t.Errorf("the check printed %q, want %q", out, want)
				}
				if got := ParseHandoffCheck(string(out)); !slices.Equal(got, problems) {
					t.Errorf("ParseHandoffCheck = %q, want %q", got, problems)
				}
			})
		}
	}
}

// A line the reader does not know is passed over, so a newer check, or a
// login banner on the member, never refuses a hand-off by itself.
func TestTheHandoffCheckIsReadALineAtATime(t *testing.T) {
	out := "Welcome to the box\nDIRTY\tapp\t3\nSOMETHING new here\nDIRTY\tapp\nUNNAMED\tlib\tabc1234\n\nNOHANDOFF\n"
	want := []string{
		"~/app has work not committed (3 paths)",
		"your handoff does not name the commit ~/lib is at, abc1234",
		"there is no handoff at ~/" + HandoffFile,
	}
	if got := ParseHandoffCheck(out); !slices.Equal(got, want) {
		t.Errorf("ParseHandoffCheck = %q, want %q", got, want)
	}
	if got := ParseHandoffCheck(""); len(got) != 0 {
		t.Errorf("nothing printed is agreement, got %q", got)
	}
}

// The node reads every problem, where its handoff goes, and that the ask
// costs it nothing.
func TestTheCommitBodyListsEachProblem(t *testing.T) {
	problems := []string{"~/app has work not committed (2 paths, the first a.txt)", "your handoff does not name the commit ~/lib is at, abc1234"}
	body := CommitBody("d003", problems)
	for _, want := range []string{"d003", "  - " + problems[0] + "\n", "  - " + problems[1] + "\n",
		"~/" + HandoffFile, "names the commit", "this session is kept", "No rung is spent"} {
		if !strings.Contains(body, want) {
			t.Errorf("the body does not say %q:\n%s", want, body)
		}
	}
}

// The bound on hand-offs is the campaign's. Resolve leaves it unset, so a
// campaign with no line carries no hand-off number. The line itself is each
// seat's, so a member may declare it.
func TestTheHandoffBoundIsTheCampaignsAndTheLineIsTheSeats(t *testing.T) {
	if got := (Policy{}).Resolve().Handoffs; got != 0 {
		t.Errorf("Resolve filled Handoffs with %d; only WithHandoffBound may", got)
	}
	if got := (Policy{Handoffs: 3}).Resolve().Handoffs; got != 3 {
		t.Errorf("Resolve kept Handoffs as %d, want the declared 3", got)
	}
	if got := (Policy{}).Resolve().HandoffTokens; got != 0 {
		t.Errorf("a seat with no line has none after Resolve, got %d", got)
	}
	if got := (Policy{HandoffTokens: 60000, StallSeconds: 240}).CampaignOnlyFields(); len(got) != 0 {
		t.Errorf("handoffTokens and stallSeconds are per seat, got %v", got)
	}
	if got := (Policy{Handoffs: 3}).CampaignOnlyFields(); !slices.Equal(got, []string{"policy.handoffs"}) {
		t.Errorf("handoffs is the campaign's alone, got %v", got)
	}
	// The window that makes a hand-off rapid is part of the same bound, and
	// resolves as the bound does.
	if got := (Policy{RapidHandoffSeconds: 120}).CampaignOnlyFields(); !slices.Equal(got, []string{"policy.rapidHandoffSeconds"}) {
		t.Errorf("rapidHandoffSeconds is the campaign's alone, got %v", got)
	}
	if got := (Policy{}).Resolve().RapidHandoffSeconds; got != 0 {
		t.Errorf("Resolve filled RapidHandoffSeconds with %d; only WithHandoffBound may", got)
	}
	if got := (Policy{RapidHandoffSeconds: 120}).Resolve().RapidHandoffSeconds; got != 120 {
		t.Errorf("Resolve kept RapidHandoffSeconds as %d, want the declared 120", got)
	}
}

// A member without a line must read the manifest bytes it read before lines
// existed, so a resolved policy that says nothing has no handoffs key and no
// rapidHandoffSeconds key. Both appear only once a seat has a line, and an
// explicit value of either is kept.
func TestTheHandoffBoundIsRecordedOnlyWhenASeatHasALine(t *testing.T) {
	resolved := (Policy{}).Resolve()
	for _, anyLine := range []bool{false, true} {
		b, err := json.Marshal(resolved.WithHandoffBound(anyLine))
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{`"handoffs"`, `"rapidHandoffSeconds"`} {
			if has := strings.Contains(string(b), key); has != anyLine {
				t.Errorf("with a line %v, the policy JSON has %s %v: %s", anyLine, key, has, b)
			}
		}
	}
	if got := resolved.WithHandoffBound(true); got.Handoffs != 10 || got.RapidHandoffSeconds != 300 {
		t.Errorf("a seat with a line gets the default bound and window, got %d and %d", got.Handoffs, got.RapidHandoffSeconds)
	}
	if got := (Policy{Handoffs: 3, RapidHandoffSeconds: 120}).WithHandoffBound(true); got.Handoffs != 3 || got.RapidHandoffSeconds != 120 {
		t.Errorf("an explicit bound and window with a line are kept, got %d and %d", got.Handoffs, got.RapidHandoffSeconds)
	}
	if got := (Policy{Handoffs: 3}).WithHandoffBound(true).RapidHandoffSeconds; got != 300 {
		t.Errorf("an explicit bound leaves the window to its default, got %d", got)
	}
	if got := (Policy{Handoffs: 3, RapidHandoffSeconds: 120}).WithHandoffBound(false); got.Handoffs != 3 || got.RapidHandoffSeconds != 120 {
		t.Errorf("an explicit bound and window without a line are kept, got %d and %d", got.Handoffs, got.RapidHandoffSeconds)
	}
}
