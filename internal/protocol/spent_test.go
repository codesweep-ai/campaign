package protocol

// A turn whose sandbox's token budget stopped it ends in class spent. Only the
// operator can raise a budget, and while it stays spent the node's machine
// refuses a turn before sending anything, so the move is a long wait and
// another try, never a rung (SPEC.md R151).

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// spentPolicy is a policy whose every other bound is out of reach, with the
// default spent wait.
var spentPolicy = Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 1_000_000, BlindProbes: 3, SettlingSeconds: 100, ProviderWaitSeconds: 3600}

// refusedSpent is the turn end a driver writes when it would not start a turn,
// or the lender refused one partway through: exit 5, class spent.
func refusedSpent(at int64) TurnEnd {
	return TurnEnd{At: at, Exit: 5, Class: ClassSpent, Reason: "token budget spent: cached 1.004G of 1G"}
}

// handedOffSpent is the turn end a driver writes when the node stopped at its
// budget's line and wrote its handoff: exit 0, class spent.
func handedOffSpent(at int64) TurnEnd {
	return TurnEnd{At: at, Exit: 0, Class: ClassSpent, Reason: "token budget spent: output 5.0M of 5M, handed off"}
}

// A refused turn is held for the wait, says so, and is then carried on in its
// session. It is never laddered, and the line tells the operator what the node
// needs.
func TestARefusedSpentTurnIsHeldThenCarriedOnInItsSession(t *testing.T) {
	f := Facts{Msgs: msgs("d001.md@9000"), Replies: map[string]bool{}, Agent: "idle", TurnEnds: []TurnEnd{refusedSpent(10000)}}
	o := Compute(f, false, Blind{}, map[string]bool{}, spentPolicy, 10060)
	if o.State != StateRefused || o.NextMove != "" || !o.Spent || o.Dispatch != "d001" {
		t.Fatalf("a spent budget is held under the refusal overlay, with no move yet: %+v", o)
	}
	want := `token budget spent (cached) 60s ago, session kept · trying again in 29m, no rung spent · raising its token budget on the host is all it needs · "token budget spent: cached 1.004G of 1G"`
	if o.Detail != want {
		t.Errorf("the line must say the budget is spent, name the type, the wait and what the node needs:\n got %q\nwant %q", o.Detail, want)
	}
	o = Compute(f, false, Blind{}, map[string]bool{}, spentPolicy, 10000+1800)
	if o.State != StateRefused || o.NextMove != "resume" || !o.Spent {
		t.Fatalf("once the wait is over the move is a resume in the same session: %+v", o)
	}
	if !strings.Contains(o.Detail, "resume next, no rung spent") {
		t.Errorf("the line must say the resume is next and free: %q", o.Detail)
	}
	// The operator's number is the wait.
	short := spentPolicy
	short.SpentRetrySeconds = 600
	if o := Compute(f, false, Blind{}, map[string]bool{}, short, 10600); o.NextMove != "resume" {
		t.Errorf("spentRetrySeconds 600 makes the try due at 600s: %+v", o)
	}
	// A reason older than the newest message is not held against it.
	f.Msgs = msgs("d001.md@9000", "d001.001.md@10100")
	if o := Compute(f, false, Blind{}, map[string]bool{}, spentPolicy, 10300); o.Spent || o.State != StateStopped {
		t.Errorf("a spent end before the newest message must not hold the node: %+v", o)
	}
}

// A node that handed off at its budget's line waits out the same hold, then
// takes the hand-off move, because a session resumed after a long wait sends
// its whole context again.
func TestASpentHandoffTakesTheHandoffMoveOnItsNextTry(t *testing.T) {
	f := Facts{Msgs: msgs("d001.md@9000"), Replies: map[string]bool{}, Agent: "idle", TurnEnds: []TurnEnd{handedOffSpent(10000)}}
	o := Compute(f, false, Blind{}, map[string]bool{}, spentPolicy, 10060)
	if o.State != StateRefused || o.NextMove != "" || !o.Spent {
		t.Fatalf("a spent hand-off is held first: %+v", o)
	}
	if want := "token budget spent (output) 60s ago, handed off · fresh session on its handoff in 29m, no rung spent · raising its token budget on the host is all it needs"; !strings.HasPrefix(o.Detail, want) {
		t.Errorf("the held line:\n got %q\nwant prefix %q", o.Detail, want)
	}
	o = Compute(f, false, Blind{}, map[string]bool{}, spentPolicy, 11800)
	if o.State != StateStopped || o.NextMove != "handoff" || !o.Spent {
		t.Fatalf("once the wait is over the move is the hand-off: %+v", o)
	}
	if want := "0 cont, 0 restarts, 0 hand-offs · token budget spent (output) 30m ago, handed off · fresh session next, no rung spent"; !strings.HasPrefix(o.Detail, want) {
		t.Errorf("the due line:\n got %q\nwant prefix %q", o.Detail, want)
	}
}

// The hand-off move's own turn is refused while the budget stays spent. The
// fresh session never ran, so the next try names the .handoff message it was
// given, and the line says how long the node has been held.
func TestASpentFreshSessionIsTriedOnItsHandoffMessage(t *testing.T) {
	ms := msgs("d001.md@9000", "d001.001.handoff.md@11800")
	f := Facts{Msgs: ms, Replies: map[string]bool{}, Agent: "idle", TurnEnds: []TurnEnd{handedOffSpent(10000), refusedSpent(11801)}}
	o := Compute(f, false, Blind{}, map[string]bool{}, spentPolicy, 11801+1800)
	if o.State != StateRefused || o.NextMove != "resume" {
		t.Fatalf("the refused fresh session is carried on: %+v", o)
	}
	if !strings.HasPrefix(o.Detail, "token budget spent (cached) 60m ago, last tried 30m ago, session kept · resume next") {
		t.Errorf("the line must say since when the node is held: %q", o.Detail)
	}
	body := ResumeBodyFor(f, "d001", true)
	if !strings.Contains(body, "~/"+InputDir+"/d001.001.handoff.md") || !strings.Contains(body, "token budget") {
		t.Fatalf("the resume must point the session at the message its refused turn was given:\n%s", body)
	}
	// The try after that is refused too. It still names the .handoff message,
	// and not the resume before it, which asked nothing of its own.
	f.Msgs = msgs("d001.md@9000", "d001.001.handoff.md@11800", "d001.002.resume.md@13601")
	f.TurnEnds = append(f.TurnEnds, refusedSpent(13602))
	if body := ResumeBodyFor(f, "d001", true); !strings.Contains(body, "/d001.001.handoff.md") {
		t.Errorf("a resume adds nothing to what the node was asked, and must not be the one named:\n%s", body)
	}
	// Any other resume reads as it always has.
	f.TurnEnds = append(f.TurnEnds, TurnEnd{At: 13700, Exit: 5, Class: ClassThrottled})
	if body := ResumeBodyFor(f, "d001", true); body != ResumeBody("d001", true) {
		t.Errorf("a throttle's resume must be ResumeBody: %s", body)
	}
}

// No provider bound holds a spent budget, and the elapsed bound does not count
// the hold: that time is the operator's. Each hold runs from a spent turn end
// to the try after it, or to now while it lasts.
func TestTheElapsedBoundLeavesSpentHoldsOut(t *testing.T) {
	ends := []TurnEnd{refusedSpent(10000)}
	ms := []string{"d001.md@9000"}
	for i := int64(1); i <= 4; i++ {
		ms = append(ms, fmt.Sprintf("d001.%03d.resume.md@%d", i, 10000+i*1800))
		ends = append(ends, refusedSpent(10001+i*1800))
	}
	f := Facts{Msgs: msgs(ms...), Replies: map[string]bool{}, Agent: "idle", TurnEnds: ends}
	now := int64(10001 + 4*1800 + 60)
	bounded := spentPolicy
	bounded.ElapsedSeconds = 3600
	for name, pol := range map[string]Policy{"the default bound": spentPolicy, "a bound of an hour": bounded} {
		if o := Compute(f, false, Blind{}, map[string]bool{}, pol, now); o.State != StateRefused || !o.Spent {
			t.Fatalf("%s: two hours held, past providerWaitSeconds and past an hour's bound, is still a hold: %+v", name, o)
		}
	}
	if c, r := Charged(Current(f.Msgs), f); c != 0 || r != 0 {
		t.Errorf("a spent hold spends nothing, got %d continues and %d restarts", c, r)
	}
	// 1000s of work, 1800s held, then the raised budget's turn ran and stopped
	// without replying. The node's own time reaches the hour at 14400.
	f = Facts{Msgs: msgs("d001.md@9000", "d001.001.resume.md@11800"), Replies: map[string]bool{}, Agent: "idle",
		TurnEnds: []TurnEnd{refusedSpent(10000), {At: 14000, Exit: 0}}}
	if o := Compute(f, false, Blind{}, map[string]bool{}, bounded, 14399); o.State == StateStuck {
		t.Errorf("3599s of the node's own time must not trip an hour: %+v", o)
	}
	o := Compute(f, false, Blind{}, map[string]bool{}, bounded, 14400)
	if o.State != StateStuck || !strings.HasPrefix(o.Detail, "open 5400s, 30m of it held on a spent token budget and not counted — elapsed bound tripped") {
		t.Errorf("an hour of the node's own time trips the bound, and the line says what was left out: %+v", o)
	}
	if strings.Contains(o.Detail, spentAsk) {
		t.Errorf("a node whose last turn was not stopped by its budget is not told to raise it: %q", o.Detail)
	}
}

// The turn log the probe tails holds sixteen ends, eight hours of tries at the
// default wait. The probe also lists every spent end the log keeps, and a hold
// longer than the tail is left out whole.
func TestAHoldLongerThanTheTailIsLeftOutWhole(t *testing.T) {
	var ms []string
	var ends []TurnEnd
	var all []int64
	ms = append(ms, "d001.md@1000")
	for i := range int64(40) { // twenty hours of tries
		at := 2000 + i*1800
		if i > 0 {
			ms = append(ms, fmt.Sprintf("d001.%03d.resume.md@%d", i, at-1))
		}
		ends = append(ends, refusedSpent(at))
		all = append(all, at)
	}
	now := int64(2000 + 40*1800)
	d := Current(msgs(ms...))
	tail := Facts{Msgs: d.Msgs, TurnEnds: ends[len(ends)-16:]}
	whole := tail
	whole.SpentEnds = all
	if got := heldSeconds(d, tail, now); got > 16*1800 {
		t.Fatalf("the tail alone sees at most sixteen holds, got %ds", got)
	}
	if got, want := heldSeconds(d, whole, now), now-2000-39; got != want {
		t.Errorf("every hold, from each spent end to the try after it: got %ds, want %ds", got, want)
	}
}

// A node held on a spent budget that is stuck for another reason says the
// budget is spent, and ends on what the operator has to do besides recovering it.
func TestAStuckNodeWhoseBudgetIsSpentSaysSo(t *testing.T) {
	bounded := spentPolicy
	bounded.ElapsedSeconds = 3600
	// An hour and two minutes of work, then the budget stopped the turn.
	f := Facts{Msgs: msgs("d001.md@1000"), Replies: map[string]bool{}, Agent: "idle", TurnEnds: []TurnEnd{refusedSpent(4720)}}
	o := Compute(f, false, Blind{}, map[string]bool{}, bounded, 4780)
	want := "open 3780s, 60s of it held on a spent token budget and not counted — elapsed bound tripped · token budget spent (cached) 60s ago" + spentStuckAsk
	if o.State != StateStuck || o.Detail != want {
		t.Errorf("the stuck line:\n got %q\nwant %q", o.Detail, want)
	}
	// Launches that fail after a try are stuck in the same words.
	f = Facts{Msgs: msgs("d001.md@1000", "d001.001.resume.md@11800"), Replies: map[string]bool{}, Agent: "idle",
		Budget: "ask output", TurnEnds: []TurnEnd{refusedSpent(10000)}}
	o = Compute(f, false, Blind{}, map[string]bool{}, spentPolicy, 11800+3600)
	if o.State != StateStuck || !strings.Contains(o.Detail, "every launch failed · token budget spent (output) 90m ago") || !strings.HasSuffix(o.Detail, spentStuckAsk) {
		t.Errorf("a stuck line after a try names the budget, the reading's types first, and ends on the ask: %q", o.Detail)
	}
	// The ask ends the line even when the node reports a context reading.
	f.Context = &ContextReading{Tokens: 1000}
	if o := Compute(f, false, Blind{}, map[string]bool{}, spentPolicy, 11800+3600); !strings.HasSuffix(o.Detail, spentStuckAsk) {
		t.Errorf("the ask must end the line: %q", o.Detail)
	}
}

// A reading that says a turn would be sent means the budget was raised: the
// try comes on this look, not at the end of the wait. Every other reading,
// and an old driver's silence, leaves the wait as it was.
func TestABudgetWithRoomBringsTheTryForward(t *testing.T) {
	for _, exit := range []int{5, 0} {
		end := refusedSpent(10000)
		end.Exit = exit
		f := Facts{Msgs: msgs("d001.md@9000"), Replies: map[string]bool{}, Agent: "idle", TurnEnds: []TurnEnd{end}}
		for _, reading := range []string{"", "none", "off", "ask cached", "ask input,output"} {
			f.Budget = reading
			if o := Compute(f, false, Blind{}, map[string]bool{}, spentPolicy, 10060); o.NextMove != "" || o.State != StateRefused {
				t.Errorf("exit %d, reading %q: the wait stands: %+v", exit, reading, o)
			}
			if o := Compute(f, false, Blind{}, map[string]bool{}, spentPolicy, 11800); o.NextMove == "" {
				t.Errorf("exit %d, reading %q: the wait is still the fallback, and ends: %+v", exit, reading, o)
			}
		}
		f.Budget = "ok"
		o := Compute(f, false, Blind{}, map[string]bool{}, spentPolicy, 10060)
		wantState, wantMove := StateRefused, "resume"
		if exit == 0 {
			wantState, wantMove = StateStopped, "handoff"
		}
		if o.State != wantState || o.NextMove != wantMove || !o.Spent {
			t.Errorf("exit %d: a reading with room makes the try due now: %+v", exit, o)
		}
		if !strings.Contains(o.Detail, "its budget has room again") || strings.Contains(o.Detail, spentAsk) {
			t.Errorf("exit %d: the line says the budget has room, and asks the operator for nothing: %q", exit, o.Detail)
		}
	}
	// A reading that still asks says so, and names its own types.
	f := Facts{Msgs: msgs("d001.md@9000"), Replies: map[string]bool{}, Agent: "idle", TurnEnds: []TurnEnd{refusedSpent(10000)}, Budget: "ask input,output"}
	want := "token budget spent (input, output) 60s ago, session kept · still spent on its latest reading · trying again in 29m, no rung spent" + spentAsk
	if o := Compute(f, false, Blind{}, map[string]bool{}, spentPolicy, 10060); !strings.HasPrefix(o.Detail, want) {
		t.Errorf("the held line under a reading that asks:\n got %q\nwant %q", o.Detail, want)
	}
}

// The probe asks the driver for its reading, and the line parses. An empty
// line is an old driver, and means not known. A driver answering --budget is a
// question and not a turn, so the driver count leaves it out, and every spent
// end the log keeps comes back on its own line.
func TestTheProbeReadsTheBudget(t *testing.T) {
	script := ProbeScript("claude")
	for _, want := range []string{`--budget 2>/dev/null`, `' --(state|context|budget)'`, `printf 'BUDGET %s\n'`, `spent ' "$HOME/.cs-turns/claude.log"`} {
		if !strings.Contains(script, want) {
			t.Errorf("the probe does not carry %q:\n%s", want, script)
		}
	}
	for line, want := range map[string]string{"BUDGET ok": "ok", "BUDGET ask cached": "ask cached", "BUDGET ask input,output": "ask input,output", "BUDGET none": "none", "BUDGET off": "off", "BUDGET ": "", "BUDGET": ""} {
		if f := ParseProbe("DRIVERS 0\n" + line + "\n"); f.Budget != want {
			t.Errorf("%q parsed as %q, want %q", line, f.Budget, want)
		}
	}
	if got := (Facts{Budget: "ask input, output"}).BudgetAsks(); len(got) != 2 || got[0] != "input" || got[1] != "output" {
		t.Errorf("the reading's types: %v", got)
	}
	if got := (Facts{Budget: "ok"}).BudgetAsks(); got != nil {
		t.Errorf("a reading with room names no type: %v", got)
	}
	f := ParseProbe("SPENT 100\nSPENT 200\nSPENT soon\nTURNEND 200 5 spent - token budget spent\n")
	if len(f.SpentEnds) != 2 || f.SpentEnds[0] != 100 || f.SpentEnds[1] != 200 || len(f.TurnEnds) != 1 {
		t.Errorf("spent ends parse beside the tail: %+v", f)
	}
}

// Run against a scratch home, the probe asks a driver that answers --budget,
// and one too old to know it.
func TestTheProbeAsksTheDriverForItsBudget(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("the probe needs pgrep, as a member has")
	}
	for answer, want := range map[string]string{
		`case "$1" in --budget) echo 'ask cached';; *) exit 1;; esac`: "ask cached",
		`case "$1" in --budget) echo ok;; *) exit 1;; esac`:           "ok",
		`echo "cs-codex-turn: unknown argument: $1" >&2; exit 1`:      "",
	} {
		home := t.TempDir()
		for dir, files := range map[string]map[string]string{
			ChannelsDir + "/input": {"d001.md": ""},
			".local/bin":           {"cs-codex-turn": "#!/bin/sh\n" + answer + "\n"},
			".cs-turns":            {"codex.log": "100 0 - - -\n200 5 spent - token budget spent: cached\n300 5 throttled - spent a while\n"},
		} {
			if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
				t.Fatal(err)
			}
			for name, body := range files {
				if err := os.WriteFile(filepath.Join(home, dir, name), []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
		}
		cmd := exec.Command("sh", "-c", ProbeScript("codex"))
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("the probe failed: %v", err)
		}
		f := ParseProbe(string(out))
		if f.Budget != want {
			t.Errorf("driver %q: Budget %q, want %q\n%s", answer, f.Budget, want, out)
		}
		if len(f.SpentEnds) != 1 || f.SpentEnds[0] != 200 {
			t.Errorf("the spent ends are the spent lines alone: %v\n%s", f.SpentEnds, out)
		}
	}
}

// A turn the budget refused never ran as far as the rung is concerned, so the
// continue that met it is given back. A provider's refusal is not: that turn
// ran. A spent hand-off is not either, since the node did its work.
func TestATurnTheBudgetRefusedIsNotCharged(t *testing.T) {
	ms := msgs("d001.md@1000", "d001.001.md@2000", "d001.002.resume.md@3800")
	d := Current(ms)
	for _, tc := range []struct {
		name  string
		first TurnEnd
		want  int
	}{
		{"refused by the budget", refusedSpent(2001), 0},
		{"refused by the provider", TurnEnd{At: 2001, Exit: 5, Class: ClassThrottled}, 1},
		{"handed off at the budget's line", handedOffSpent(2001), 1},
	} {
		f := Facts{Msgs: ms, Agent: "idle", TurnEnds: []TurnEnd{{At: 1500}, tc.first, {At: 3900}}}
		if c, r := Charged(d, f); c != tc.want || r != 0 {
			t.Errorf("%s: charged %d continues and %d restarts, want %d and 0", tc.name, c, r, tc.want)
		}
	}
}

// A spent ending is never a rapid hand-off, and a session that met one ends
// the run: the operator's top-ups set its pace, not the line. Under a bound of
// two, three rapid hand-offs in a row trip it, and the same three with the
// middle one spent do not.
func TestASpentEndingIsNeverARapidHandoff(t *testing.T) {
	pol := spentPolicy
	pol.Handoffs, pol.SpentRetrySeconds = 2, 60
	ms := msgs("d001.md@1000", "d001.001.handoff.md@5005", "d001.002.handoff.md@5070", "d001.003.handoff.md@5190")
	look := func(middle TurnEnd) Observation {
		f := Facts{Msgs: ms, Replies: map[string]bool{}, Agent: "idle",
			TurnEnds: []TurnEnd{handoffEnd(5000), handoffEnd(5065), middle, handoffEnd(5250)}}
		return Compute(f, false, Blind{}, map[string]bool{}, pol, 5260)
	}
	if o := look(handoffEnd(5130)); o.State != StateStuck {
		t.Fatalf("three rapid hand-offs under a bound of two must trip it: %+v", o)
	}
	if o := look(handedOffSpent(5130)); o.State != StateStopped || o.NextMove != "handoff" {
		t.Errorf("a spent hand-off in the run must end it: %+v", o)
	}
	// Nor does a spent hand-off check the bound for itself, however rapid.
	f := Facts{Msgs: ms[:3], Replies: map[string]bool{}, Agent: "idle",
		TurnEnds: []TurnEnd{handoffEnd(5000), handoffEnd(5065), handedOffSpent(5130)}}
	if o := Compute(f, false, Blind{}, map[string]bool{}, pol, 5200); o.State != StateStopped || o.NextMove != "handoff" {
		t.Errorf("a rapid spent hand-off takes the hand-off move: %+v", o)
	}
}

// A launch that fails after a spent hold is bounded from the try, not from the
// turn end that began the hold: nothing was launched while the hold ran.
func TestALaunchThatFailsAfterASpentHoldIsBoundedFromTheTry(t *testing.T) {
	f := Facts{Msgs: msgs("d001.md@1000", "d001.001.resume.md@11800"), Replies: map[string]bool{}, Agent: "idle",
		TurnEnds: []TurnEnd{refusedSpent(10000)}}
	if o := Compute(f, false, Blind{}, map[string]bool{}, spentPolicy, 11800+3000); o.State != StateStopped || o.NextMove != "resume" {
		t.Fatalf("50m of failed launches is inside providerWaitSeconds: %+v", o)
	}
	if o := Compute(f, false, Blind{}, map[string]bool{}, spentPolicy, 11800+3600); o.State != StateStuck || !strings.Contains(o.Detail, "every launch failed") {
		t.Errorf("an hour of failed launches after the try is the bound: %+v", o)
	}
}

// The line names a type when the driver's words do, as a word, and nothing
// when they name none.
func TestTheSpentLineNamesTheTypesTheReasonNames(t *testing.T) {
	for reason, want := range map[string]string{
		"token budget spent: cached 1.004G of 1G": " (cached)",
		"cs-sandbox token budget spent: sandbox ci has used 2.00M of its 2.00M output tokens. The operator can raise it": " (output)",
		"has used 1.00G of its 1.00G cached-input tokens":                                                                " (cached)",
		"input and output past their limits":                                                                             " (input, output)",
		"Output 5M, OUTPUT again":                                                                                        " (output)",
		"inputs outputted":                                                                                               "",
		"token budget spent":                                                                                             "",
		"-":                                                                                                              "",
	} {
		if got := spentTypes(Facts{}, &TurnEnd{Reason: reason}); got != want {
			t.Errorf("spentTypes(%q) = %q, want %q", reason, got, want)
		}
	}
	// A reading that still asks names the types itself, and wins over the
	// reason's words.
	f := Facts{Budget: "ask input,output"}
	if got := spentTypes(f, &TurnEnd{Reason: "token budget spent: cached 1.004G of 1G"}); got != " (input, output)" {
		t.Errorf("the reading's types must be taken first, got %q", got)
	}
	for _, reading := range []string{"ok", "none", "off", ""} {
		if got := spentTypes(Facts{Budget: reading}, &TurnEnd{Reason: "cached"}); got != " (cached)" {
			t.Errorf("a reading of %q names no type, and the reason's words stand; got %q", reading, got)
		}
	}
}

// The wait is recorded only when the profile names it, so a campaign that
// says nothing writes the member documents it always wrote. It is read as the
// default when absent, and a member may not declare it.
func TestTheSpentWaitIsRecordedOnlyWhenNamed(t *testing.T) {
	if got := (Policy{}).Resolve(); got.SpentRetrySeconds != 0 || got.SpentRetry() != 1800 {
		t.Errorf("an unnamed wait must resolve to nothing recorded and read as 1800: %d, %d", got.SpentRetrySeconds, got.SpentRetry())
	}
	if got := (Policy{SpentRetrySeconds: 900}).Resolve(); got.SpentRetrySeconds != 900 || got.SpentRetry() != 900 {
		t.Errorf("a named wait is kept: %+v", got)
	}
	if fields := (Policy{SpentRetrySeconds: 900}).CampaignOnlyFields(); len(fields) != 1 || fields[0] != "policy.spentRetrySeconds" {
		t.Errorf("the wait governs the whole campaign, and a member may not declare it: %v", fields)
	}
}
