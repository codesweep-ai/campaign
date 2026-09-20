package protocol

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// State is one node's state with respect to its current dispatch. Computed on
// demand from the node's own machine plus the acceptance record — never
// stored, never accumulated.
type State string

const (
	StateFree    State = "node-free"
	StateWorking State = "node-working"
	StateStopped State = "node-stopped"
	StateReplied State = "node-replied"
	StateStuck   State = "node-stuck"
	// StateUnreachable is an overlay, not a node state: a fact about the
	// observation. A run of them past Policy.BlindProbes is the conclusion
	// that the machine is gone (StateStuck).
	StateUnreachable State = "node-unreachable"
	// StateRefused is the second overlay: the node's provider refused its last
	// turn for a reason that waiting cures. It is a fact about the node's
	// surroundings, as unreachability is a fact about the observation. The move
	// is to wait and then carry on, and no rung is spent (PROTOCOL.md §5).
	StateRefused State = "node-refused"
)

// The failure classes a turn driver records. Only these five change a move;
// anything else is reported and left to the ladder.
const (
	ClassThrottled    = "throttled"
	ClassCapacity     = "capacity"
	ClassUnauthorized = "unauthorized"
	ClassContext      = "context"
	// ClassUnreachable is a turn that ended because no provider answered at
	// all: the node's network was down, or the proxy in front of its provider
	// was gone.
	ClassUnreachable = "unreachable"
)

// TurnEnd is one line of the node's own turn log: how a turn ended, written on
// the node's machine by the turn driver.
type TurnEnd struct {
	At         int64
	Exit       int
	Class      string // "" when the driver named none
	RetryAfter int64  // seconds the provider asked for, 0 when it named none
	Reason     string
}

// Refused says the turn ended for a reason that lies outside the node and that
// waiting cures: the provider throttled it, was overloaded, or could not be
// reached.
func (e TurnEnd) Refused() bool {
	return e.Class == ClassThrottled || e.Class == ClassCapacity || e.Class == ClassUnreachable
}

// Facts is everything one probe of one node returns — one round trip, per
// PROTOCOL.md §6: connect, ask, done.
type Facts struct {
	Msgs    []Msg
	Replies map[string]bool // dispatch ID -> reply file exists
	Drivers int             // turn-driver processes alive for this node's family
	// Record is when the family's own session record last changed, in epoch
	// seconds, or 0 when the node has none. It is reported and never decides a
	// state: see recordNote.
	Record int64
	// Agent is what the family's turn driver answers when asked whether the
	// agent is in a turn: busy, busy retrying, idle, blocked, unknown or absent.
	// It sees a turn whoever started it, which a driver count cannot. "" means
	// the node's tools are too old to say, and the driver count then stands alone.
	Agent string
	// TurnEnds is the tail of the node's turn log, oldest first.
	TurnEnds []TurnEnd
}

// knowsTurns says the node's tools keep a turn log, so the absence of a line
// means something.
func (f Facts) knowsTurns() bool { return f.Agent != "" || len(f.TurnEnds) > 0 }

// endSince returns the newest turn end at or after t, or nil.
func (f Facts) endSince(t int64) *TurnEnd {
	for i := len(f.TurnEnds) - 1; i >= 0; i-- {
		if f.TurnEnds[i].At >= t {
			return &f.TurnEnds[i]
		}
	}
	return nil
}

// recordGlob names, for one CLI family, where its session record lives inside
// a member and which files are the record. The directories are the profiles
// cs-sandbox's wrappers keep (cs-claude, cs-codex, cs-opencode).
//
// internal/cli's fleet audit names the same places for the same reason, and
// TestTheProbeAndTheAuditAgreeOnWhereARecordLives keeps the two together.
var recordGlob = map[string][2]string{
	"claude":   {".cs-claude/projects", "*.jsonl"},
	"codex":    {".cs-codex/sessions", "*.jsonl"},
	"opencode": {".cs-opencode", "opencode.db*"},
}

// ProbeScript is the single shell command a dispatcher runs inside a node to
// gather Facts. cli names the node's declared family so the driver count
// matches only that family's turn driver; the [b]racket keeps the pattern
// from matching the probe's own command line.
//
// Output lines: "MSG <mtime> <name>", "REPLY <id>", "DRIVERS <n>",
// "RECORD <mtime>", "AGENT <word> [word]", "TURNEND <epoch> <exit> <class>
// <retry_after> <reason>", or "NOCHANNELS" when the channel root does not exist
// yet. AGENT and TURNEND come from the sandbox's turn drivers. A node whose
// tools are older prints an empty AGENT and no TURNEND, and is computed as before.
// RECORD is left out for a family with no known record, and it reads 0 when the
// member has written none, so an older probe and a newer parser agree.
func ProbeScript(cli string) string {
	pattern := "cs-[" + cli[:1] + "]" + cli[1:] + "-turn"
	script := `cd "$HOME/` + ChannelsDir + `" 2>/dev/null || { echo NOCHANNELS; exit 0; }; ` +
		`for f in input/*.md; do [ -e "$f" ] || continue; printf 'MSG %s %s\n' "$(stat -c %Y "$f" 2>/dev/null || echo 0)" "${f#input/}"; done; ` +
		`for r in output/replies/*.json; do [ -e "$r" ] || continue; b="${r#output/replies/}"; printf 'REPLY %s\n' "${b%.json}"; done; ` +
		// A driver asked for --state is a question and not a turn. It matches the
		// pattern, and a second observer's probe can be running one at this moment,
		// so it is left out of the count.
		`printf 'DRIVERS %s\n' "$(pgrep -fa '` + pattern + ` ' 2>/dev/null | grep -vc -- ' --state' || true)"`
	if g, ok := recordGlob[cli]; ok {
		script += `; printf 'RECORD %s\n' "$(find "$HOME/` + g[0] + `" -type f -name '` + g[1] + `' -printf '%T@\n' 2>/dev/null | sort -n | tail -1 | cut -d. -f1 | grep . || echo 0)"`
	}
	driver := "cs-" + cli + "-turn"
	script += `; d="$HOME/.local/bin/` + driver + `"; [ -x "$d" ] || d=` + driver +
		`; printf 'AGENT %s\n' "$("$d" --state 2>/dev/null | head -n 1)"` +
		`; tail -n 16 "$HOME/.cs-turns/` + cli + `.log" 2>/dev/null | cut -c1-240 | sed 's/^/TURNEND /'`
	return script
}

// ParseProbe turns ProbeScript output into Facts. Unrecognized lines are
// ignored rather than fatal: the probe shares a stream with whatever the
// transport prints around it.
func ParseProbe(out string) Facts {
	f := Facts{Replies: map[string]bool{}}
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 3 && fields[0] == "MSG":
			mtime, _ := strconv.ParseInt(fields[1], 10, 64)
			if m, ok := ParseMsgName(fields[2], mtime); ok {
				f.Msgs = append(f.Msgs, m)
			}
		case len(fields) == 2 && fields[0] == "REPLY":
			f.Replies[fields[1]] = true
		case len(fields) == 2 && fields[0] == "DRIVERS":
			f.Drivers, _ = strconv.Atoi(fields[1])
		case len(fields) == 2 && fields[0] == "RECORD":
			f.Record, _ = strconv.ParseInt(fields[1], 10, 64)
		case len(fields) >= 2 && fields[0] == "AGENT":
			f.Agent = strings.Join(fields[1:], " ")
		case len(fields) >= 5 && fields[0] == "TURNEND":
			at, err1 := strconv.ParseInt(fields[1], 10, 64)
			exit, err2 := strconv.Atoi(fields[2])
			if err1 != nil || err2 != nil {
				continue
			}
			e := TurnEnd{At: at, Exit: exit, Reason: strings.Join(fields[5:], " ")}
			if fields[3] != "-" {
				e.Class = fields[3]
			}
			if secs, err := strconv.ParseFloat(fields[4], 64); err == nil && secs > 0 {
				e.RetryAfter = int64(math.Ceil(secs))
			}
			f.TurnEnds = append(f.TurnEnds, e)
		}
	}
	return f
}

// Observation is a computed state plus the mechanical detail a dispatcher
// (or an operator reading `observe`) acts on.
type Observation struct {
	State    State
	Dispatch string // current dispatch ID, "" when none
	Detail   string
	// NextMove is the mechanical move the state calls for: "continue",
	// "restart", "resume" (carry on and spend no rung), or "" when the next
	// arrow is nothing, a wait, or a judgment.
	NextMove string
}

// Blind is what an observer knows about its own failed looks at one node. It
// is the observer's knowledge and never the node's (PROTOCOL.md §5).
//
// Looks is a run of consecutive failed probes, which is how the host counts:
// a burst inside one observe. Seconds is how long the node has gone unseen
// while the observer could see at least one other node, which is how a
// dispatcher counts, because its looks are spread over many wait calls and a
// count cannot outlive the call that made it.
type Blind struct {
	Looks   int
	Seconds int64
}

// Compute derives one node's state. accepted is the orchestrator's acceptance
// record — the single input from outside the node. blind is observer-local;
// probeFailed marks this look as a failed one.
//
// Order is load-bearing (SPEC.md §7.3): reachability precedes everything, and
// the reply check precedes the liveness check — a node that replied and then
// exited is node-replied, not node-stopped.
func Compute(f Facts, probeFailed bool, blind Blind, accepted map[string]bool, pol Policy, now int64) Observation {
	pol = pol.Resolve()
	if probeFailed {
		goneAfter := int64(pol.BlindProbes) * int64(pol.PollSeconds)
		switch {
		case blind.Looks >= pol.BlindProbes:
			return Observation{State: StateStuck, Detail: fmt.Sprintf("unreachable for %d probes — machine gone", blind.Looks)}
		case blind.Seconds >= goneAfter:
			return Observation{State: StateStuck, Detail: fmt.Sprintf("unreachable for %s while other nodes answered — machine gone", shortAge(blind.Seconds))}
		case blind.Seconds > 0:
			return Observation{State: StateUnreachable, Detail: fmt.Sprintf("unseen for %s; concluded gone at %s", shortAge(blind.Seconds), shortAge(goneAfter))}
		}
		// Honest wording, not a verdict: below the threshold the observer has
		// learned nothing about the node — only that this look (and the run
		// before it) failed.
		return Observation{State: StateUnreachable, Detail: fmt.Sprintf("%d failed look(s); %d consecutive required to conclude the machine is gone", blind.Looks, pol.BlindProbes)}
	}
	d := Current(f.Msgs)
	if d == nil {
		return Observation{State: StateFree, Detail: "no open dispatch"}
	}
	if f.Replies[d.ID] {
		if accepted[d.ID] {
			return Observation{State: StateFree, Dispatch: d.ID, Detail: d.ID + " accepted"}
		}
		return Observation{State: StateReplied, Dispatch: d.ID, Detail: "reply present, not accepted"}
	}
	// Active is a live driver, or an agent that says it is in a turn. The second
	// sees what the first cannot: a turn the agent started by itself.
	busy := strings.HasPrefix(f.Agent, "busy")
	if f.Drivers > 0 || busy {
		detail := fmt.Sprintf("open %ds · %d cont, %d restarts", clampAge(now-d.OpenedAt), d.Continues, d.Restarts)
		switch {
		case f.Agent == "busy retrying":
			detail += " · the agent is waiting out a provider error"
		case f.Drivers == 0:
			detail += " · in a turn the agent started itself"
		case f.Agent == "absent" && now-d.NewestMsg >= int64(pol.SettlingSeconds):
			detail += " · a driver is alive and no agent session exists"
		}
		return Observation{State: StateWorking, Dispatch: d.ID, Detail: detail}
	}
	// Inactive without a reply. The elapsed bound stays first among the stuck
	// checks — it is the protocol's "whichever trips first" backstop, runs from
	// dispatch open, and no continuation resets it.
	if now-d.OpenedAt >= int64(pol.ElapsedSeconds) {
		return Observation{State: StateStuck, Dispatch: d.ID,
			Detail: fmt.Sprintf("open %ds — elapsed bound tripped", clampAge(now-d.OpenedAt))}
	}
	// Why the last turn ended, when the node's machine says. A reason counts only
	// if it is newer than the newest message: an old reason must never be held
	// against a new attempt. This comes before the settling window, because a
	// turn that has ended is not a turn that is starting.
	continues, restarts := Charged(d, f)
	if last := f.endSince(d.NewestMsg); last != nil {
		ago := shortAge(clampAge(now - last.At))
		switch {
		case last.Class == ClassUnauthorized:
			return Observation{State: StateStuck, Dispatch: d.ID,
				Detail: "the provider rejected this node's credential " + ago + " ago — the operator's repair, which no continue or restart reaches" + reasonNote(last)}
		case last.Refused():
			first, n := refusedRun(f.TurnEnds, d.OpenedAt)
			if now-first >= int64(pol.ProviderWaitSeconds) {
				return Observation{State: StateStuck, Dispatch: d.ID,
					Detail: fmt.Sprintf("the provider refused %d turns over %s (%s) — provider wait bound tripped", n, shortAge(now-first), last.Class) + reasonNote(last)}
			}
			hold := max(last.RetryAfter, RefusalBackoff(n))
			detail := fmt.Sprintf("%s %s ago · %d refused in a row", last.Class, ago, n)
			if last.RetryAfter > 0 {
				detail += fmt.Sprintf(" · provider asked for %ds", last.RetryAfter)
			}
			if due := last.At + hold; now < due {
				return Observation{State: StateRefused, Dispatch: d.ID,
					Detail: detail + fmt.Sprintf(" · resuming in %ds, no rung spent", due-now)}
			}
			return Observation{State: StateRefused, Dispatch: d.ID, NextMove: "resume",
				Detail: detail + " · resume next, no rung spent"}
		case last.Class == ClassContext:
			// A continue re-sends the context that was too long. A restart is
			// the one remedy that shortens it.
			if restarts >= pol.Restarts {
				return Observation{State: StateStuck, Dispatch: d.ID,
					Detail: "the context is too long for the model, and the restart is spent" + reasonNote(last)}
			}
			return Observation{State: StateStopped, Dispatch: d.ID, NextMove: "restart",
				Detail: fmt.Sprintf("%d cont, %d restarts · the context is too long for the model · restart next", continues, restarts)}
		}
	}
	// The settling window comes BEFORE the ladder counts: it re-arms on every
	// send, and a restart re-anchor is itself a send with the same cold start as
	// any other. Checking the counts before settling made the restart rung dead
	// on arrival — the re-anchor incremented the restart count and the next poll
	// read node-stuck while the restarted session was still booting
	// (adversarial review, finding 1).
	//
	// The window is for a turn that may still be starting. A turn that has
	// recorded its own end since the newest message is not starting: it ran, and
	// it is over. Holding the ladder for the rest of the window then hides a
	// dead agent behind "turn starting" for minutes, which is how an agent the
	// kernel killed for memory came to look like a slow model.
	if f.endSince(d.NewestMsg) == nil && now-d.NewestMsg < int64(pol.SettlingSeconds) {
		return Observation{State: StateWorking, Dispatch: d.ID,
			Detail: fmt.Sprintf("turn starting (%ds into the %ds settling window)", clampAge(now-d.NewestMsg), pol.SettlingSeconds)}
	}
	// No turn has ended since the newest message, on a node that records every
	// turn end: the turn for that message never ran. The node did nothing wrong,
	// so carrying on spends no rung. Bounded like a provider's refusal.
	if f.knowsTurns() && f.endSince(d.NewestMsg) == nil {
		since := d.OpenedAt
		if n := len(f.TurnEnds); n > 0 && f.TurnEnds[n-1].At > since {
			since = f.TurnEnds[n-1].At
		}
		if now-since >= int64(pol.ProviderWaitSeconds) {
			return Observation{State: StateStuck, Dispatch: d.ID,
				Detail: fmt.Sprintf("no turn has run for %s — every launch failed", shortAge(now-since))}
		}
		return Observation{State: StateStopped, Dispatch: d.ID, NextMove: "resume",
			Detail: fmt.Sprintf("%d cont, %d restarts · no turn ran for the newest message · resume next, no rung spent", continues, restarts)}
	}
	if restarts >= pol.Restarts && continues >= pol.ContinueAttempts {
		return Observation{State: StateStuck, Dispatch: d.ID,
			Detail: fmt.Sprintf("%d continues and %d restarts spent", continues, restarts)}
	}
	move := "continue"
	if continues >= pol.ContinueAttempts {
		move = "restart"
	}
	detail := fmt.Sprintf("%d cont, %d restarts · %s next", continues, restarts, move)
	if f.Agent == "blocked" {
		detail += " · the agent is on a screen that needs a person"
	}
	if last := f.endSince(d.NewestMsg); last != nil {
		detail += fmt.Sprintf(" · last turn ended %s ago, exit %d", shortAge(clampAge(now-last.At)), last.Exit) + reasonNote(last)
	}
	return Observation{State: StateStopped, Dispatch: d.ID, NextMove: move, Detail: detail + recordNote(f, now)}
}

// RefusalBackoff is how long to hold a node after its nth refused turn in a
// row, when the provider asked for less or for nothing: 30s doubling to 10m.
// Every resumed turn re-sends the node's context, so the hold grows while the
// refusals last.
func RefusalBackoff(n int) int64 {
	hold := int64(30)
	for i := 1; i < n && hold < 600; i++ {
		hold *= 2
	}
	return min(hold, 600)
}

// refusedRun finds the run of refused turns at the tail of the log, no older
// than since: when it began, and how many turns it holds.
func refusedRun(ends []TurnEnd, since int64) (first int64, n int) {
	for i := len(ends) - 1; i >= 0 && ends[i].Refused() && ends[i].At >= since; i-- {
		first, n = ends[i].At, n+1
	}
	return first, n
}

// Charged counts the rungs a dispatch has really spent. A rung is a turn: a
// continue or a restart whose message was delivered and whose turn never ran
// spent nothing (seen as a launcher failing while file delivery still worked).
// A message is known to have had no turn when the node records every turn end,
// the log reaches back that far, and no turn ended between it and the message
// after it. The newest message is always charged: its turn may yet run.
func Charged(d *Dispatch, f Facts) (continues, restarts int) {
	if !f.knowsTurns() || len(f.TurnEnds) == 0 {
		return d.Continues, d.Restarts
	}
	oldest := f.TurnEnds[0].At
	for i, m := range d.Msgs {
		if m.Seq == 0 || m.Resume {
			continue
		}
		ran := true
		if i+1 < len(d.Msgs) && m.MTime >= oldest {
			ran = false
			for _, e := range f.TurnEnds {
				if e.At >= m.MTime && e.At <= d.Msgs[i+1].MTime {
					ran = true
					break
				}
			}
		}
		switch {
		case !ran:
		case m.Restart:
			restarts++
		default:
			continues++
		}
	}
	return continues, restarts
}

// reasonNote quotes the node's own words for why a turn ended.
func reasonNote(e *TurnEnd) string {
	if e.Reason == "" || e.Reason == "-" {
		return ""
	}
	return " · " + strconv.Quote(e.Reason)
}

// RecordDir is where one CLI family keeps its session record, relative to a
// member's home, and "" for a family with no known record.
func RecordDir(cli string) string { return recordGlob[cli][0] }

// recordNote says when a stopped node's own session record last changed.
//
// node-stopped means no turn driver is alive, and a driver wraps only a turn
// the host started. An agent CLI can start a turn of its own: a background
// task it left running finishes, and its runtime hands the result to the
// session as a new turn. No driver wraps that turn, so the node reads stopped
// for as long as it works. Seen live, for the last forty minutes of a campaign.
//
// The note is evidence beside the state and never part of it. A record that
// changed seconds ago tells an operator not to nudge, and it changes no state
// and no ladder move, because a record is output, and output is a poor measure
// of silent work (R64).
func recordNote(f Facts, now int64) string {
	if f.Record <= 0 {
		return ""
	}
	return " · session record changed " + shortAge(clampAge(now-f.Record)) + " ago"
}

// shortAge prints an age the way an operator reads one: 40s, 12m, 3h.
func shortAge(secs int64) string {
	switch {
	case secs < 120:
		return fmt.Sprintf("%ds", secs)
	case secs < 7200:
		return fmt.Sprintf("%dm", secs/60)
	default:
		return fmt.Sprintf("%dh", secs/3600)
	}
}

// clampAge keeps displayed ages non-negative: message mtimes come from the
// node's clock and now from the observer's, and a second or two of skew must
// not print as a negative age. The comparisons above deliberately use the raw
// difference — skew small enough to matter to them is smaller than any sane
// policy value.
func clampAge(d int64) int64 {
	if d < 0 {
		return 0
	}
	return d
}
