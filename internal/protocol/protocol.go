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
var msgName = regexp.MustCompile(`^(d[0-9]{3}|m1)(?:\.([0-9]{3})(\.restart)?)?\.md$`)

// Msg is one message file in a node's input channel, as observed.
type Msg struct {
	ID      string
	Seq     int  // 0 for the opening message
	Restart bool // this continuation is a restart's re-anchor
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
	return Msg{ID: m[1], Seq: seq, Restart: m[3] != "", MTime: mtime, Name: name}, true
}

// Dispatch is the reconstructed view of one dispatch from its messages.
type Dispatch struct {
	ID        string
	OpenedAt  int64 // earliest message mtime
	NewestMsg int64 // latest message mtime — the settling window keys on this
	Continues int   // plain continuations
	Restarts  int   // restart re-anchors
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
				if m.Restart {
					d.Restarts++
				} else {
					d.Continues++
				}
			}
		}
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

// NextMsgName names the file the next message of dispatch id gets: the opener
// when the dispatch has no messages, else the next continuation (or restart
// re-anchor when restart is set).
func NextMsgName(msgs []Msg, id string, restart bool) string {
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
	if restart {
		return fmt.Sprintf("%s.%03d.restart.md", id, seq+1)
	}
	return fmt.Sprintf("%s.%03d.md", id, seq+1)
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
	// StallSeconds reaches the turn drivers as CS_<CLI>_STALL_SECS via
	// cs-sandbox create --env (the seeded ~/.ssh/environment). Per member;
	// the orchestrator seat defaults far higher — long quiet is normal for a
	// supervisor and suspicious for everyone else.
	StallSeconds int `json:"stallSeconds,omitempty" yaml:"stallSeconds,omitempty"`
}

// DefaultPolicy is what a campaign gets for saying nothing; MANUAL.md's
// policy table is the operator-facing rendering. ElapsedSeconds' default
// plays the backstop role (the counts are the real node-level bound), sized
// to a day rather than to any campaign in particular — a declared
// defaults.deadline overrides it at create.
func DefaultPolicy() Policy {
	return Policy{
		ContinueAttempts: 2,
		Restarts:         1,
		ElapsedSeconds:   86400,
		BlindProbes:      10,
		PollSeconds:      30,
		SettlingSeconds:  300,
		StallSeconds:     180,
	}
}

// DefaultOrchestratorStallSeconds is the orchestrator seat's stall default.
const DefaultOrchestratorStallSeconds = 1800

// CampaignOnlyFields names the set numbers in p that only a campaign may
// declare, in profile spelling. StallSeconds is absent because it is the one
// number resolved per seat: it belongs to a member's own turn driver, and
// reaches it as CS_<CLI>_STALL_SECS. Every other number governs the whole
// campaign — one resolved set, recorded on the campaign and in every
// member.json, that every node is computed against.
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
	} {
		if field.value != 0 {
			out = append(out, "policy."+field.name)
		}
	}
	return out
}

// Resolve fills every zero field of p from DefaultPolicy.
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
	if p.StallSeconds == 0 {
		p.StallSeconds = d.StallSeconds
	}
	return p
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
	Campaign    string    `json:"campaign"`
	Member      string    `json:"member"`
	Role        string    `json:"role"`
	Network     string    `json:"network"`
	Branch      string    `json:"branch,omitempty"`
	Repos       []RepoRef `json:"repos,omitempty"`
	Inputs      []string  `json:"inputs"`
	InputDir    string    `json:"inputDir"`
	OutputDir   string    `json:"outputDir"`
	Orientation string    `json:"orientation"`
	Policy      Policy    `json:"policy"`
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
