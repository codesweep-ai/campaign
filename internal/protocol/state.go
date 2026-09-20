package protocol

import (
	"fmt"
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
)

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
// "RECORD <mtime>", or "NOCHANNELS" when the channel root does not exist yet.
// RECORD is left out for a family with no known record, and it reads 0 when the
// member has written none, so an older probe and a newer parser agree.
func ProbeScript(cli string) string {
	pattern := "cs-[" + cli[:1] + "]" + cli[1:] + "-turn"
	script := `cd "$HOME/` + ChannelsDir + `" 2>/dev/null || { echo NOCHANNELS; exit 0; }; ` +
		`for f in input/*.md; do [ -e "$f" ] || continue; printf 'MSG %s %s\n' "$(stat -c %Y "$f" 2>/dev/null || echo 0)" "${f#input/}"; done; ` +
		`for r in output/replies/*.json; do [ -e "$r" ] || continue; b="${r#output/replies/}"; printf 'REPLY %s\n' "${b%.json}"; done; ` +
		`printf 'DRIVERS %s\n' "$(pgrep -fc '` + pattern + ` ' 2>/dev/null || echo 0)"`
	if g, ok := recordGlob[cli]; ok {
		script += `; printf 'RECORD %s\n' "$(find "$HOME/` + g[0] + `" -type f -name '` + g[1] + `' -printf '%T@\n' 2>/dev/null | sort -n | tail -1 | cut -d. -f1 | grep . || echo 0)"`
	}
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
	// "restart", or "" when the next arrow is nothing or a judgment.
	NextMove string
}

// Compute derives one node's state. accepted is the orchestrator's acceptance
// record — the single input from outside the node. blindRun counts
// consecutive failed probes and is observer-local; probeFailed marks this
// look as one of them.
//
// Order is load-bearing (SPEC.md §7.3): reachability precedes everything, and
// the reply check precedes the liveness check — a node that replied and then
// exited is node-replied, not node-stopped.
func Compute(f Facts, probeFailed bool, blindRun int, accepted map[string]bool, pol Policy, now int64) Observation {
	pol = pol.Resolve()
	if probeFailed {
		if blindRun >= pol.BlindProbes {
			return Observation{State: StateStuck, Detail: fmt.Sprintf("unreachable for %d probes — machine gone", blindRun)}
		}
		// Honest wording, not a verdict: below the threshold the observer has
		// learned nothing about the node — only that this look (and the run
		// before it) failed.
		return Observation{State: StateUnreachable, Detail: fmt.Sprintf("%d failed look(s); %d consecutive required to conclude the machine is gone", blindRun, pol.BlindProbes)}
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
	if f.Drivers > 0 {
		return Observation{State: StateWorking, Dispatch: d.ID,
			Detail: fmt.Sprintf("open %ds · %d cont, %d restarts", clampAge(now-d.OpenedAt), d.Continues, d.Restarts)}
	}
	// Inactive without a reply. The elapsed bound stays first among the stuck
	// checks — it is the protocol's "whichever trips first" backstop, runs from
	// dispatch open, and no continuation resets it. The settling window comes
	// BEFORE the ladder counts: it re-arms on every send, and a restart
	// re-anchor is itself a send with the same cold start as any other.
	// Checking the counts before settling made the restart rung dead on
	// arrival — the re-anchor incremented the restart count and the next poll
	// read node-stuck while the restarted session was still booting
	// (adversarial review, finding 1).
	if now-d.OpenedAt >= int64(pol.ElapsedSeconds) {
		return Observation{State: StateStuck, Dispatch: d.ID,
			Detail: fmt.Sprintf("open %ds — elapsed bound tripped", clampAge(now-d.OpenedAt))}
	}
	if now-d.NewestMsg < int64(pol.SettlingSeconds) {
		return Observation{State: StateWorking, Dispatch: d.ID,
			Detail: fmt.Sprintf("turn starting (%ds into the %ds settling window)", clampAge(now-d.NewestMsg), pol.SettlingSeconds)}
	}
	if d.Restarts >= pol.Restarts && d.Continues >= pol.ContinueAttempts {
		return Observation{State: StateStuck, Dispatch: d.ID,
			Detail: fmt.Sprintf("%d continues and %d restarts spent", d.Continues, d.Restarts)}
	}
	move := "continue"
	if d.Continues >= pol.ContinueAttempts {
		move = "restart"
	}
	return Observation{State: StateStopped, Dispatch: d.ID, NextMove: move,
		Detail: fmt.Sprintf("%d cont, %d restarts · %s next", d.Continues, d.Restarts, move) + recordNote(f, now)}
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
