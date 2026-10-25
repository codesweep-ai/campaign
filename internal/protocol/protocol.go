// Package protocol is the shared implementation of the dispatch protocol:
// channel paths, dispatch identity, the reply and log artifact shapes, and the
// node-state computation. Both binaries — the host cs-campaign and the guest
// cs-campaign-member — build on this package, so the two ends of every channel
// agree by construction rather than by convention.
//
// The authority is PROTOCOL.md at the repository root. Node state is computed
// on demand and never stored; the only recorded campaign state is the
// orchestrator's append-only log.
package protocol

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Guest channel layout, $HOME-relative. The archive collects input/ and
// output/ wholesale, so everything the protocol stores lives under them.
const (
	ChannelsDir = ".local/share/cs-campaign"
	InputDir    = ChannelsDir + "/input"
	OutputDir   = ChannelsDir + "/output"
	RepliesDir  = OutputDir + "/replies"
	LogFile     = OutputDir + "/log.jsonl"
	// HandoffFile is where a member with a hand-off line writes its handoff,
	// and what a fresh session is pointed at. In the output channel, so it is
	// harvested with the rest of what the member leaves behind.
	HandoffFile = OutputDir + "/handoff.md"
	// HandoffsDir keeps every version of a member's handoff, one file per
	// hand-off, copied there before the session that wrote it is ended.
	// Fresh sessions never read it; it is the record of what each one was
	// handed.
	HandoffsDir = OutputDir + "/handoffs"

	ConfigDir    = ".config/cs-campaign"
	MemberDoc    = ConfigDir + "/member.json"
	ManifestDoc  = ConfigDir + "/manifest.json"
	GuestBinDir  = ".local/bin"
	GuestBinName = "cs-campaign-member"
)

// MissionID is the reserved ID of the host's one dispatch to the orchestrator.
// A reply closing it is the campaign's verdict and must carry an outcome.
const MissionID = "m1"

// Dispatch IDs are per-node sequences d001..d999 so lexical order is
// chronological order in every listing. The mint refuses past d999 rather
// than widening and silently breaking sort order.
const MaxDispatchSeq = 999

// msgName matches every dispatch message file in the input channel:
//
//	d007.md            opening message of dispatch d007
//	d007.001.md        first continuation
//	d007.002.restart.md	a restart's re-anchor (counts as a restart, not a continue)
//	m1.md, m1.001.md   the mission and its continuations
//
// The .restart marker exists so recovery state is countable from a directory
// listing alone — derived, never remembered (SPEC.md §4.5, R59).
//
// The .resume marker is a continuation that spends no rung. It carries on a
// dispatch whose node did nothing wrong: its provider refused the last turn and
// the wait is over, or the turn for the message before it never launched.
//
// The .handoff marker is the message a fresh session reads after the node
// handed off: its context crossed the line it was given, it wrote its handoff
// and ended its turn as asked, and the session that read this message is new.
// It spends no rung either, and is counted apart, because a bound of its own
// holds it (SPEC.md R142).
//
// The .commit marker is the message a node reads when its hand-off was not
// taken: its work was uncommitted, or its handoff did not name the commit its
// branch is at. The session is kept, because only it knows what the work was,
// and the message asks it to commit and write its handoff again. It spends no
// rung, and its turn counts toward the same bound as a hand-off's session
// when it hands off again within the rapid window (SPEC.md R142, R144).
var msgName = regexp.MustCompile(`^(d[0-9]{3}|m1)(?:\.([0-9]{3})(\.restart|\.resume|\.handoff|\.commit)?)?\.md$`)

// Msg is one message file in a node's input channel, as observed.
type Msg struct {
	ID      string
	Seq     int  // 0 for the opening message
	Restart bool // this continuation is a restart's re-anchor
	Resume  bool // this continuation spends no rung
	Handoff bool // this continuation starts a fresh session on the node's handoff
	Commit  bool // this continuation asks the node to commit before its hand-off is taken
	MTime   int64
	Name    string
}

// ParseMsgName classifies one input-channel basename. ok is false for files
// that are not dispatch messages (ORIENTATION.md, seeded briefs, roles/...).
func ParseMsgName(name string, mtime int64) (Msg, bool) {
	m := msgName.FindStringSubmatch(name)
	if m == nil {
		return Msg{}, false
	}
	seq := 0
	if m[2] != "" {
		seq, _ = strconv.Atoi(m[2])
	}
	return Msg{ID: m[1], Seq: seq, Restart: m[3] == ".restart", Resume: m[3] == ".resume", Handoff: m[3] == ".handoff", Commit: m[3] == ".commit", MTime: mtime, Name: name}, true
}

// Dispatch is the reconstructed view of one dispatch from its messages.
type Dispatch struct {
	ID        string
	OpenedAt  int64 // earliest message mtime
	NewestMsg int64 // latest message mtime — the settling window keys on this
	Continues int   // plain continuations
	Restarts  int   // restart re-anchors
	Resumes   int   // continuations that spent no rung
	Handoffs  int   // fresh sessions started on the node's handoff
	// CommitAsks are hand-offs not taken, each a .commit message asking the
	// kept session to commit first. A rapid one counts toward the hand-off
	// bound (rapidHandoffs).
	CommitAsks int
	// Msgs is every message of the dispatch, oldest first.
	Msgs []Msg
}

// Current returns the newest dispatch reconstructed from the input channel,
// or nil when no dispatch has ever been opened. "Newest" is by opening time;
// at most one dispatch is ever open, so the newest one is the current one.
func Current(msgs []Msg) *Dispatch {
	byID := map[string][]Msg{}
	for _, m := range msgs {
		byID[m.ID] = append(byID[m.ID], m)
	}
	var cur *Dispatch
	for id, ms := range byID {
		d := &Dispatch{ID: id, OpenedAt: ms[0].MTime, NewestMsg: ms[0].MTime}
		for _, m := range ms {
			if m.MTime < d.OpenedAt {
				d.OpenedAt = m.MTime
			}
			if m.MTime > d.NewestMsg {
				d.NewestMsg = m.MTime
			}
			if m.Seq > 0 {
				switch {
				case m.Restart:
					d.Restarts++
				case m.Resume:
					d.Resumes++
				case m.Handoff:
					d.Handoffs++
				case m.Commit:
					d.CommitAsks++
				default:
					d.Continues++
				}
			}
		}
		d.Msgs = append([]Msg(nil), ms...)
		SortMsgs(d.Msgs)
		if cur == nil || d.OpenedAt > cur.OpenedAt ||
			(d.OpenedAt == cur.OpenedAt && d.ID > cur.ID) {
			cur = d
		}
	}
	return cur
}

// NextDispatchID mints the ID a new dispatch gets on a node whose input
// channel holds msgs. The mission never comes from here — create opens it as
// MissionID explicitly.
func NextDispatchID(msgs []Msg) (string, error) {
	max := 0
	for _, m := range msgs {
		if strings.HasPrefix(m.ID, "d") {
			if n, err := strconv.Atoi(m.ID[1:]); err == nil && n > max {
				max = n
			}
		}
	}
	if max >= MaxDispatchSeq {
		return "", fmt.Errorf("dispatch mint exhausted: this node already holds d%03d", max)
	}
	return fmt.Sprintf("d%03d", max+1), nil
}

// SendTarget is the one rule of PROTOCOL.md §3, computed: the dispatch a send
// lands in, and whether the send opens it. An open dispatch is continued; a
// closed one, or none, means a new dispatch.
func SendTarget(facts Facts) (id string, opens bool, err error) {
	if d := Current(facts.Msgs); d != nil && !facts.Replies[d.ID] {
		return d.ID, false, nil
	}
	id, err = NextDispatchID(facts.Msgs)
	return id, true, err
}

// NextMsgName names the file the next message of dispatch id gets: the opener
// when the dispatch has no messages, else the next continuation (or restart
// re-anchor when restart is set).
func NextMsgName(msgs []Msg, id string, restart bool) string {
	return nextMsgName(msgs, id, map[bool]string{true: ".restart"}[restart])
}

// NextResumeName names the next continuation of dispatch id that spends no rung.
func NextResumeName(msgs []Msg, id string) string { return nextMsgName(msgs, id, ".resume") }

// NextHandoffName names the message a fresh session reads after the node
// handed off on dispatch id.
func NextHandoffName(msgs []Msg, id string) string { return nextMsgName(msgs, id, ".handoff") }

// NextCommitName names the message that asks a node to commit its work before
// its hand-off on dispatch id is taken.
func NextCommitName(msgs []Msg, id string) string { return nextMsgName(msgs, id, ".commit") }

// KeptHandoffPath is where the handoff a node wrote is kept at its next
// hand-off on dispatch id: handoffs/<id>-<n>.md, where n is that hand-off's
// ordinal on the dispatch, so the first is 1. It is counted from the .handoff
// messages already delivered, as Current counts Handoffs.
func KeptHandoffPath(msgs []Msg, id string) string {
	n := 1
	for _, m := range msgs {
		if m.ID == id && m.Seq > 0 && m.Handoff {
			n++
		}
	}
	return fmt.Sprintf("%s/%s-%d.md", HandoffsDir, id, n)
}

func nextMsgName(msgs []Msg, id, kind string) string {
	seq := 0
	for _, m := range msgs {
		if m.ID == id && m.Seq > seq {
			seq = m.Seq
		}
	}
	exists := false
	for _, m := range msgs {
		if m.ID == id {
			exists = true
			break
		}
	}
	if !exists {
		return id + ".md"
	}
	return fmt.Sprintf("%s.%03d%s.md", id, seq+1, kind)
}

// SortMsgs orders messages chronologically (mtime, then name — names are
// zero-padded, so the tiebreak is stable and correct).
func SortMsgs(msgs []Msg) {
	sort.Slice(msgs, func(i, j int) bool {
		if msgs[i].MTime != msgs[j].MTime {
			return msgs[i].MTime < msgs[j].MTime
		}
		return msgs[i].Name < msgs[j].Name
	})
}

// Policy is every number the machine runs on. Values come from the profile
// (defaults.policy); anything unset falls back to DefaultPolicy — constants
// compiled in, never a runtime-read defaults file. Resolved values are
// recorded on the campaign and in each member.json. StallSeconds is the one
// number a member may also declare for itself (see CampaignOnlyFields).
type Policy struct {
	ContinueAttempts int `json:"continueAttempts,omitempty" yaml:"continueAttempts,omitempty"`
	Restarts         int `json:"restarts,omitempty" yaml:"restarts,omitempty"`
	ElapsedSeconds   int `json:"elapsedSeconds,omitempty" yaml:"elapsedSeconds,omitempty"`
	BlindProbes      int `json:"blindProbes,omitempty" yaml:"blindProbes,omitempty"`
	PollSeconds      int `json:"pollSeconds,omitempty" yaml:"pollSeconds,omitempty"`
	SettlingSeconds  int `json:"settlingSeconds,omitempty" yaml:"settlingSeconds,omitempty"`
	// ProviderWaitSeconds bounds how long a dispatch may sit behind a condition
	// that is not the node's doing: a provider that refuses its turns, or a turn
	// launcher that fails. No rung is spent meanwhile, and the elapsed bound
	// still runs.
	ProviderWaitSeconds int `json:"providerWaitSeconds,omitempty" yaml:"providerWaitSeconds,omitempty"`
	// StallSeconds reaches the turn drivers as CS_<CLI>_STALL_SECS via
	// cs-sandbox create --env (the seeded ~/.ssh/environment). Per member;
	// the orchestrator seat defaults far higher — long quiet is normal for a
	// supervisor and suspicious for everyone else.
	StallSeconds int `json:"stallSeconds,omitempty" yaml:"stallSeconds,omitempty"`
	// HandoffTokens is the context line a member hands off at, in tokens, and
	// reaches its machine as CS_HANDOFF_TOKENS the same way. Per member, and
	// off when unset: a seat with no line runs as it always has. It is sized
	// from the seat's measured growth (`observe`), never from the window.
	HandoffTokens int `json:"handoffTokens,omitempty" yaml:"handoffTokens,omitempty"`
	// Handoffs bounds the rapid hand-offs in a row on one dispatch before the
	// node is node-stuck. A hand-off is rapid when the session that made it
	// began, on a .handoff or a .commit message, less than
	// RapidHandoffSeconds before it. A line below what a fresh session costs
	// hands off rapidly forever, and this is the backstop behind the hook's
	// own guard; a session that works longer than the window resets the count,
	// so a long mission's healthy hand-offs never trip it (SPEC.md R142). It
	// is recorded only when some seat has a line (WithHandoffBound), and read
	// as the default when zero.
	Handoffs int `json:"handoffs,omitempty" yaml:"handoffs,omitempty"`
	// RapidHandoffSeconds is the window that makes a hand-off rapid, recorded
	// and read the same way as Handoffs.
	RapidHandoffSeconds int `json:"rapidHandoffSeconds,omitempty" yaml:"rapidHandoffSeconds,omitempty"`
	// FreshPerDispatch starts an agent in a fresh session at each dispatch
	// that opens on it, re-anchored as a restart is, and spends no rung. Per
	// seat and off when unset, like HandoffTokens. An agent whose dispatches
	// share nothing a session must remember runs each on a clean context
	// (SPEC.md R145).
	FreshPerDispatch bool `json:"freshPerDispatch,omitempty" yaml:"freshPerDispatch,omitempty"`
}

// DefaultPolicy is what a campaign gets for saying nothing; MANUAL.md's
// policy table is the operator-facing rendering. ElapsedSeconds' default
// plays the backstop role (the counts are the real node-level bound), sized
// to a day rather than to any campaign in particular — a declared
// defaults.deadline overrides it at create.
func DefaultPolicy() Policy {
	return Policy{
		ContinueAttempts:    2,
		Restarts:            1,
		ElapsedSeconds:      86400,
		BlindProbes:         10,
		PollSeconds:         30,
		SettlingSeconds:     300,
		ProviderWaitSeconds: 3600,
		StallSeconds:        180,
		Handoffs:            10,
		RapidHandoffSeconds: 300,
	}
}

// DefaultOrchestratorStallSeconds is the orchestrator seat's stall default.
const DefaultOrchestratorStallSeconds = 1800

// CampaignOnlyFields names the set numbers in p that only a campaign may
// declare, in profile spelling. StallSeconds and HandoffTokens are absent
// because they are the numbers resolved per seat: each belongs to a member's
// own machine, and reaches it through the sandbox environment. Every other
// number governs the whole campaign — one resolved set, recorded on the
// campaign and in every member.json, that every node is computed against.
//
// It exists so validation can REFUSE a member that declares one, rather than
// writing a number nothing reads. That member would run on the campaign's
// value while its own profile said otherwise, and nothing would ever say so.
func (p Policy) CampaignOnlyFields() []string {
	var out []string
	for _, field := range []struct {
		name  string
		value int
	}{
		{"continueAttempts", p.ContinueAttempts},
		{"restarts", p.Restarts},
		{"elapsedSeconds", p.ElapsedSeconds},
		{"blindProbes", p.BlindProbes},
		{"pollSeconds", p.PollSeconds},
		{"settlingSeconds", p.SettlingSeconds},
		{"providerWaitSeconds", p.ProviderWaitSeconds},
		{"handoffs", p.Handoffs},
		{"rapidHandoffSeconds", p.RapidHandoffSeconds},
	} {
		if field.value != 0 {
			out = append(out, "policy."+field.name)
		}
	}
	return out
}

// Resolve fills every zero field of p from DefaultPolicy, except Handoffs and
// RapidHandoffSeconds. The hand-off bound is filled by WithHandoffBound alone,
// so a campaign with no context line carries no hand-off number anywhere.
func (p Policy) Resolve() Policy {
	d := DefaultPolicy()
	if p.ContinueAttempts == 0 {
		p.ContinueAttempts = d.ContinueAttempts
	}
	if p.Restarts == 0 {
		p.Restarts = d.Restarts
	}
	if p.ElapsedSeconds == 0 {
		p.ElapsedSeconds = d.ElapsedSeconds
	}
	if p.BlindProbes == 0 {
		p.BlindProbes = d.BlindProbes
	}
	if p.PollSeconds == 0 {
		p.PollSeconds = d.PollSeconds
	}
	if p.SettlingSeconds == 0 {
		p.SettlingSeconds = d.SettlingSeconds
	}
	if p.ProviderWaitSeconds == 0 {
		p.ProviderWaitSeconds = d.ProviderWaitSeconds
	}
	if p.StallSeconds == 0 {
		p.StallSeconds = d.StallSeconds
	}
	return p
}

// WithHandoffBound fills Handoffs and RapidHandoffSeconds from DefaultPolicy
// when some seat has a context line and the profile named neither. Without a
// line it leaves p alone, so every member.json stays the bytes it was before
// lines existed. An explicit value is kept either way.
func (p Policy) WithHandoffBound(anySeatHasLine bool) Policy {
	if anySeatHasLine && p.Handoffs == 0 {
		p.Handoffs = DefaultPolicy().Handoffs
	}
	if anySeatHasLine && p.RapidHandoffSeconds == 0 {
		p.RapidHandoffSeconds = DefaultPolicy().RapidHandoffSeconds
	}
	return p
}

// handoffBound is the bound the machine computes against. A zero reads as
// the default, so a policy that missed WithHandoffBound is never unbounded.
func (p Policy) handoffBound() int {
	if p.Handoffs == 0 {
		return DefaultPolicy().Handoffs
	}
	return p.Handoffs
}

// rapidWindow is the window that makes a hand-off rapid, read the same way.
func (p Policy) rapidWindow() int64 {
	if p.RapidHandoffSeconds == 0 {
		return int64(DefaultPolicy().RapidHandoffSeconds)
	}
	return int64(p.RapidHandoffSeconds)
}

// RepoRef is one repository a member holds: its guest directory name and the
// base commit its work is measured against ("tree differs from base", never
// commit count).
type RepoRef struct {
	Name string `json:"name"`
	Base string `json:"base,omitempty"`
}

// Member is member.json: the machine-readable statement of what one member
// was given. The guest binary reads its identity, role, inputs and policy
// from here; the readback walks Inputs and reports what is absent.
type Member struct {
	Campaign string    `json:"campaign"`
	Member   string    `json:"member"`
	Role     string    `json:"role"`
	Network  string    `json:"network"`
	Branch   string    `json:"branch,omitempty"`
	Repos    []RepoRef `json:"repos,omitempty"`
	// Snapshots are the frozen read-only trees shared into this member, by the
	// guest name each one landed under. A repository and a snapshot are
	// siblings upstream and arrive the same way, at ~/<name>; only one of them
	// used to be stated here, so a member given reference material had no way
	// to learn it existed. R13 counts a snapshot among what a member can see,
	// and R31 makes the enumeration of its trees the thing that answers "what
	// could this member possibly know?".
	Snapshots   []string `json:"snapshots,omitempty"`
	Inputs      []string `json:"inputs"`
	InputDir    string   `json:"inputDir"`
	OutputDir   string   `json:"outputDir"`
	Orientation string   `json:"orientation"`
	Policy      Policy   `json:"policy"`
	// Deadline is the campaign wall clock as an absolute instant, informative
	// for the orchestrator's own judgment (campaign-exhausted); the machine
	// never acts on it.
	Deadline time.Time `json:"deadline,omitzero"`
}

// AgentRecord is one roster row of the orchestrator's manifest.json.
type AgentRecord struct {
	CLI       string            `json:"cli"`
	Sandbox   string            `json:"sandbox"` // bare in-group name
	Session   string            `json:"session"`
	Repos     map[string]string `json:"repos,omitempty"` // guest name -> branch
	Bases     map[string]string `json:"bases,omitempty"` // guest name -> base commit
	Snapshots []string          `json:"snapshots,omitempty"`
	// Withhold is, per guest repository name, what the profile declared this
	// agent must never receive. A push holds what it would newly deliver
	// against it.
	Withhold map[string][]string `json:"withhold,omitempty"`
	// FreshPerDispatch is the agent's resolved policy: a dispatch that opens
	// on it starts a fresh session.
	FreshPerDispatch bool `json:"freshPerDispatch,omitempty"`
}

// Manifest is manifest.json: the orchestrator's roster and the campaign
// policy its wait loop runs on.
type Manifest struct {
	Campaign string                 `json:"campaign"`
	Network  string                 `json:"network"`
	Policy   Policy                 `json:"policy"`
	Agents   map[string]AgentRecord `json:"agents"`
}

// PollInterval is how long to nap between looks: the campaign's own number,
// unless CS_CAMPAIGN_POLL_SECONDS overrides it.
//
// The variable is spelled out at both places that read it, the way every other
// CS_ variable in this product is, so that grepping the name finds the code as
// well as the documentation.
//
// The default is sized for a real campaign, where a turn takes minutes and a
// look every 15 or 30 seconds costs nothing. Replaying a cassette answers in
// milliseconds, so that same interval becomes most of the wall clock: measured
// at 24s of a 46s campaign, spent asleep. Setting this to 2 is what the replay
// tier does about that.
func PollInterval(pol Policy) time.Duration {
	if v := os.Getenv("CS_CAMPAIGN_POLL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return time.Duration(pol.PollSeconds) * time.Second
}

// DefaultWaitSeconds is how long one `wait` blocks before returning with its
// snapshot and asking to be called again. PROTOCOL.md §8: the wait is chunked
// rather than one long block, because agent CLIs bound how long a single tool
// call may run. Claude Code's and OpenCode's default bound is 120s. Past it,
// Claude Code moves the call to the background and OpenCode kills it, so
// createArgs raises both for an orchestrator.
//
// It is not a Policy number, and deliberately so — see WaitChunk.
const DefaultWaitSeconds = 240

// WaitChunk resolves the chunk bound in seconds. secs is what the caller asked
// for: the wait's --for, else DefaultWaitSeconds.
//
// CS_CAMPAIGN_WAIT_SECONDS overrides it, --for included, for the same reason
// CS_CAMPAIGN_POLL_SECONDS overrides pollSeconds: the replay tier has to bound
// its own wall clock, and what it replays is a recorded model that asked for
// the campaign's number. An override the argument could beat would not bound
// anything.
//
// Like the poll override it is neither policy nor profile configuration. The
// campaign ID is the digest of the rendered profile and a member reads its
// policy from a file its model is shown, so a number in either would
// invalidate every committed cassette.
//
// Zero is honoured: one pass, no blocking, which is what the unit tier drives
// the ladder with.
func WaitChunk(secs int) int {
	if v := os.Getenv("CS_CAMPAIGN_WAIT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return secs
}
