// Package frames turns one campaign run archive into the event model the
// viewer shell renders: nodes, protocol events, dispatch spans, and the
// findings of every integrity check. The archive is the sole input; nothing
// is inferred around defective data — a defective archive yields findings,
// not an approximated timeline (MANUAL.md has the findings reference).
package frames

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

// SchemaVersion gates the shell against payloads it does not understand.
const SchemaVersion = 1

type Node struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	CLI    string `json:"cli"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// Event is one observed protocol artifact, timestamped from content (replies,
// log) or from preserved guest mtimes (messages).
type Event struct {
	Node     string `json:"node"`
	Type     string `json:"type"` // open|continue|restart|reply|accept|plan|assessment|verdict
	At       string `json:"at"`   // RFC3339
	Dispatch string `json:"dispatch,omitempty"`
	Seq      int    `json:"seq,omitempty"`
	File     string `json:"file,omitempty"`
	Phase    string `json:"phase,omitempty"`
	Outcome  string `json:"outcome,omitempty"`
	Text     string `json:"text,omitempty"` // log entry text / message body
}

type Span struct {
	ID         string `json:"id"`
	Node       string `json:"node"`
	OpenedAt   string `json:"openedAt"`
	RepliedAt  string `json:"repliedAt,omitempty"`
	AcceptedAt string `json:"acceptedAt,omitempty"`
	Phase      string `json:"phase,omitempty"`
	Continues  int    `json:"continues"`
	Restarts   int    `json:"restarts"`
}

type Issue struct {
	Severity string `json:"severity"` // info|warning|severe|error
	Code     string `json:"code"`
	Message  string `json:"message"`
	Node     string `json:"node,omitempty"`
	Dispatch string `json:"dispatch,omitempty"`
}

type Reply struct {
	Dispatch string          `json:"dispatch"`
	Phase    string          `json:"phase"`
	Note     string          `json:"note"`
	Outcome  string          `json:"outcome,omitempty"`
	Unmet    []string        `json:"unmet,omitempty"` // mission reply only
	At       time.Time       `json:"at"`
	Repos    json.RawMessage `json:"repos,omitempty"`
	// Raw is the reply file verbatim — what the inspector's raw mode shows.
	// The decomposed fields above are conveniences over this artifact.
	Raw string `json:"raw"`
}

// Run is the complete payload injected into the shell.
type Run struct {
	SchemaVersion int                          `json:"schemaVersion"`
	Campaign      CampaignMeta                 `json:"campaign"`
	Nodes         []Node                       `json:"nodes"`
	Events        []Event                      `json:"events"`
	Spans         []Span                       `json:"spans"`
	Log           []LogEntry                   `json:"log"`
	Replies       map[string]map[string]*Reply `json:"replies"` // node -> dispatch -> reply
	Messages      map[string]map[string]string `json:"messages"`
	Readback      json.RawMessage              `json:"readback,omitempty"`
	FleetVerdict  json.RawMessage              `json:"fleetVerdict,omitempty"`
	Issues        []Issue                      `json:"issues"`
	// TimelineValid is false when the archive cannot honestly be drawn as a
	// timeline (clobbered mtimes). The findings say why; the shell must show
	// them instead of lanes.
	TimelineValid bool `json:"timelineValid"`
	// IssueDefs maps every finding code to its one-line definition, so the
	// page can explain its own findings (hover on an issue row).
	IssueDefs map[string]string `json:"issueDefs"`
}

// issueDefs is the findings registry — one line per code, the same meanings
// the manual's reference table carries.
var issueDefs = map[string]string{
	"collection-incomplete":   "an INCOMPLETE-* marker: part of the archive failed to collect",
	"fleet-anomaly":           "the fleet audit recorded findings at archive time",
	"fleet-not-clean":         "fleet-verdict.json is not clean",
	"mtimes-clobbered":        "message times are collection-time, not event-time; no timeline is drawn",
	"reply-unparseable":       "a reply artifact is not valid JSON",
	"log-unparseable":         "a log.jsonl line is not valid JSON",
	"log-missing":             "the orchestrator's log.jsonl is absent",
	"reply-without-dispatch":  "a reply exists for a dispatch no message ever opened",
	"reply-name-mismatch":     "a reply's filename and content name different dispatches; the filename wins",
	"reply-before-open":       "a reply is timestamped before its dispatch opened",
	"accept-unknown":          "the log accepts a dispatch that does not exist",
	"no-reply":                "a dispatch was opened and never answered",
	"no-verdict":              "the mission has no outcome-bearing reply",
	"campaign-not-met":        "the verdict is anything other than campaign-met",
	"reply-not-accepted":      "an agent reply the orchestrator never accepted; severe when it is the node's final dispatch",
	"continues-exceed-policy": "recovery spent more continues than the policy allows",
	"restarts-exceed-policy":  "recovery spent more restarts than the policy allows",
	"accept-before-reply":     "an acceptance logged before the reply it judges — clock skew or judgment of unanswered work",
	"readback-absent":         "a member never restated its briefing",
	"accept-of-readback":      "the log accepts d001, a host-issued, host-judged dispatch",
	"accept-of-own-channel":   "the log accepts a dispatch on the orchestrator's own channel, which the host judges",
	"accept-ambiguous":        "a bare accept matches several nodes' dispatches; shown attached to all of them",
	"accepted-twice":          "the same dispatch accepted twice; the later entry is shown",
	"ambiguous-order":         "lifecycle events share one second; rendered order is arbitrary",
}

type CampaignMeta struct {
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
	Outcome   string          `json:"outcome,omitempty"`
	OutcomeAt string          `json:"outcomeAt,omitempty"`
	Policy    protocol.Policy `json:"policy"`
}

type LogEntry struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
}

// campaignDoc is the subset of campaign.json the viewer displays.
type campaignDoc struct {
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
	Policy    protocol.Policy `json:"policy"`
	Members   []Node          `json:"members"`
}

// Load reads a run archive. dir may be the archive root itself or a run
// directory holding archive/.
func Load(dir string) (*Run, error) {
	root := dir
	if _, err := os.Stat(filepath.Join(root, "campaign.json")); err != nil {
		nested := filepath.Join(root, "archive")
		if _, err2 := os.Stat(filepath.Join(nested, "campaign.json")); err2 != nil {
			return nil, fmt.Errorf("%s: no campaign.json here or under archive/ — not a run archive", dir)
		}
		root = nested
	}
	raw, err := os.ReadFile(filepath.Join(root, "campaign.json"))
	if err != nil {
		return nil, err
	}
	var doc campaignDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("campaign.json: %w", err)
	}
	run := &Run{
		SchemaVersion: SchemaVersion,
		Campaign: CampaignMeta{Name: doc.Name, ID: doc.ID, CreatedAt: doc.CreatedAt,
			UpdatedAt: doc.UpdatedAt, Policy: doc.Policy.Resolve()},
		Nodes:         doc.Members,
		Replies:       map[string]map[string]*Reply{},
		Messages:      map[string]map[string]string{},
		TimelineValid: true,
		IssueDefs:     issueDefs,
	}
	// Orchestrator first: its lane leads the timeline.
	sort.SliceStable(run.Nodes, func(i, j int) bool {
		return run.Nodes[i].Role == "orchestrator" && run.Nodes[j].Role != "orchestrator"
	})

	if b, err := os.ReadFile(filepath.Join(root, "readback.json")); err == nil {
		run.Readback = json.RawMessage(b)
	}
	if b, err := os.ReadFile(filepath.Join(root, "fleet-verdict.json")); err == nil {
		run.FleetVerdict = json.RawMessage(b)
	}

	var allMsgs []protocol.Msg
	for _, n := range run.Nodes {
		base := filepath.Join(root, "agents", n.Name)
		if n.Role == "orchestrator" {
			base = filepath.Join(root, "orchestrator")
		}
		msgs, err := loadMessages(run, n.Name, filepath.Join(base, "input"))
		if err != nil {
			return nil, err
		}
		allMsgs = append(allMsgs, msgs...)
		if err := loadReplies(run, n.Name, filepath.Join(base, "output", "replies")); err != nil {
			return nil, err
		}
		if n.Role == "orchestrator" {
			if err := loadLog(run, filepath.Join(base, "output", "log.jsonl")); err != nil {
				return nil, err
			}
		}
	}
	buildEventsAndSpans(run)
	check(run, root, allMsgs)
	sort.SliceStable(run.Events, func(i, j int) bool { return run.Events[i].At < run.Events[j].At })
	return run, nil
}

func loadMessages(run *Run, node, dir string) ([]protocol.Msg, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	var msgs []protocol.Msg
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		m, ok := protocol.ParseMsgName(e.Name(), info.ModTime().Unix())
		if !ok {
			continue
		}
		m.Name = node + "/" + m.Name // qualify for cross-node uniqueness in checks
		msgs = append(msgs, m)
		body, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		if run.Messages[node] == nil {
			run.Messages[node] = map[string]string{}
		}
		run.Messages[node][e.Name()] = string(body)
		typ := "open"
		if m.Seq > 0 {
			typ = "continue"
			if m.Restart {
				typ = "restart"
			}
		}
		run.Events = append(run.Events, Event{Node: node, Type: typ,
			At:       time.Unix(m.MTime, 0).UTC().Format(time.RFC3339),
			Dispatch: m.ID, Seq: m.Seq, File: e.Name()})
	}
	return msgs, nil
}

func loadReplies(run *Run, node, dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // a node that never replied has no replies dir
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		var r Reply
		if err := json.Unmarshal(raw, &r); err != nil {
			run.Issues = append(run.Issues, Issue{Severity: "error", Code: "reply-unparseable",
				Message: fmt.Sprintf("%s/%s: %v", node, e.Name(), err), Node: node})
			continue
		}
		// The filename is what closes a dispatch — the probe reports REPLY <id>
		// from the directory listing (protocol/state.go). The content's
		// "dispatch" field is the reply's own claim; a disagreement is a
		// protocol anomaly, and the filename stays authoritative here too.
		id := strings.TrimSuffix(e.Name(), ".json")
		if r.Dispatch != id {
			run.Issues = append(run.Issues, Issue{Severity: "error", Code: "reply-name-mismatch",
				Message: fmt.Sprintf("%s/%s closes dispatch %s by name, but its content claims %q",
					node, e.Name(), id, r.Dispatch), Node: node, Dispatch: id})
		}
		if run.Replies[node] == nil {
			run.Replies[node] = map[string]*Reply{}
		}
		r.Raw = string(raw)
		run.Replies[node][id] = &r
		ev := Event{Node: node, Type: "reply", At: r.At.UTC().Format(time.RFC3339),
			Dispatch: id, Phase: r.Phase}
		if id == protocol.MissionID && r.Outcome != "" {
			ev.Type = "verdict"
			ev.Outcome = r.Outcome
			run.Campaign.Outcome = r.Outcome
			run.Campaign.OutcomeAt = ev.At
		}
		run.Events = append(run.Events, ev)
	}
	return nil
}

func loadLog(run *Run, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			run.Issues = append(run.Issues, Issue{Severity: "severe", Code: "log-missing",
				Message: "orchestrator log.jsonl is absent"})
			return nil
		}
		return err
	}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e LogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			run.Issues = append(run.Issues, Issue{Severity: "error", Code: "log-unparseable",
				Message: fmt.Sprintf("log.jsonl: %v", err)})
			continue
		}
		run.Log = append(run.Log, e)
		switch e.Kind {
		case "accepted":
			// Current binaries write node-qualified accepts ("dev/d003",
			// review fix b82b0b4); earlier logs carry the bare ID.
			target := strings.TrimSpace(e.Text)
			node := ""
			if before, after, ok := strings.Cut(target, "/"); ok {
				node, target = before, after
			}
			run.Events = append(run.Events, Event{Node: orchestratorName(run), Type: "accept",
				At: e.At.UTC().Format(time.RFC3339), Dispatch: target, Text: node})
		case "plan", "assessment":
			run.Events = append(run.Events, Event{Node: orchestratorName(run), Type: e.Kind,
				At: e.At.UTC().Format(time.RFC3339), Text: e.Text})
		}
	}
	return nil
}

// allOn reports whether every matched span lives on node — used to classify a
// bare accept that can only refer to the orchestrator's own channel.
func allOn(spans []*Span, node string) bool {
	for _, s := range spans {
		if s.Node != node {
			return false
		}
	}
	return len(spans) > 0
}

// m1OpenAt is the handoff moment: the mission opening on the orchestrator's
// channel. Empty when the mission never opened (a failed create) — then the
// whole record is create-phase.
func m1OpenAt(run *Run, orch string) string {
	for _, e := range run.Events {
		if e.Node == orch && e.Type == "open" && e.Dispatch == protocol.MissionID {
			return e.At
		}
	}
	return ""
}

func orchestratorName(run *Run) string {
	for _, n := range run.Nodes {
		if n.Role == "orchestrator" {
			return n.Name
		}
	}
	return "orchestrator"
}

func buildEventsAndSpans(run *Run) {
	spans := map[string]*Span{} // node/id
	for _, ev := range run.Events {
		if ev.Dispatch == "" {
			continue
		}
		key := ev.Node + "/" + ev.Dispatch
		if ev.Type == "accept" {
			continue // accept events carry the orchestrator node; resolved below
		}
		s := spans[key]
		if s == nil {
			s = &Span{ID: ev.Dispatch, Node: ev.Node}
			spans[key] = s
		}
		switch ev.Type {
		case "open":
			s.OpenedAt = ev.At
		case "continue":
			s.Continues++
		case "restart":
			s.Restarts++
		case "reply", "verdict":
			s.RepliedAt = ev.At
			s.Phase = ev.Phase
		}
	}
	// Node-qualified accepts ("dev/d003", ev.Text = node) attach to exactly
	// that node's span; legacy bare IDs attach to every span carrying the ID.
	// Every silent resolution the attachment makes is reported as a finding,
	// and every claim is checked against the authority model: the orchestrator
	// judges only what it authored — agent-channel dispatches past the
	// readback. The host judges its own issues: d001 everywhere, and the
	// orchestrator's whole channel (m1 and any post-mission send, which mints
	// d002+ there).
	orch := orchestratorName(run)
	for _, ev := range run.Events {
		if ev.Type != "accept" {
			continue
		}
		name := ev.Dispatch
		if ev.Text != "" {
			name = ev.Text + "/" + ev.Dispatch
		}
		var matched []*Span
		for _, s := range spans {
			if s.ID == ev.Dispatch && (ev.Text == "" || ev.Text == s.Node) {
				matched = append(matched, s)
			}
		}
		if len(matched) == 0 {
			run.Issues = append(run.Issues, Issue{Severity: "error", Code: "accept-unknown",
				Message:  fmt.Sprintf("log accepts %q but no such dispatch exists", name),
				Dispatch: ev.Dispatch})
			continue
		}
		if ev.Text == "" && len(matched) > 1 {
			holders := make([]string, 0, len(matched))
			for _, s := range matched {
				holders = append(holders, s.Node)
			}
			sort.Strings(holders)
			run.Issues = append(run.Issues, Issue{Severity: "info", Code: "accept-ambiguous",
				Message: fmt.Sprintf("log accepts %q with no node qualifier and %d nodes hold that ID (%s) — shown as accepting all of them, because the artifacts cannot say which was meant",
					ev.Dispatch, len(matched), strings.Join(holders, ", ")), Dispatch: ev.Dispatch})
		}
		for _, s := range matched {
			if s.AcceptedAt != "" {
				run.Issues = append(run.Issues, Issue{Severity: "info", Code: "accepted-twice",
					Message: fmt.Sprintf("%s/%s was accepted again at %s (first at %s) — the later entry is shown as the acceptance time",
						s.Node, s.ID, ev.At, s.AcceptedAt), Node: s.Node, Dispatch: s.ID})
			}
			s.AcceptedAt = ev.At
		}
		// Authority: a claim of judgment over a host-judged dispatch.
		switch {
		case ev.Dispatch == "d001":
			run.Issues = append(run.Issues, Issue{Severity: "info", Code: "accept-of-readback",
				Message: fmt.Sprintf("log accepts %s, but d001 is the create-time readback — host-issued and host-judged; the orchestrator owes it no acceptance", name),
				Node:    ev.Text, Dispatch: ev.Dispatch})
		case ev.Text == orch || ev.Dispatch == protocol.MissionID || allOn(matched, orch):
			run.Issues = append(run.Issues, Issue{Severity: "info", Code: "accept-of-own-channel",
				Message: fmt.Sprintf("log accepts %s, a dispatch on the orchestrator's own channel — the host authors and judges that channel; this acceptance has no protocol standing", name),
				Node:    orch, Dispatch: ev.Dispatch})
		}
	}
	for _, s := range spans {
		run.Spans = append(run.Spans, *s)
	}
	sort.Slice(run.Spans, func(i, j int) bool {
		if run.Spans[i].Node != run.Spans[j].Node {
			return run.Spans[i].Node < run.Spans[j].Node
		}
		return run.Spans[i].ID < run.Spans[j].ID
	})
}

// check runs every integrity check in the manual's findings reference.
// Findings accumulate;
// only clobbered mtimes invalidate the timeline itself.
//
// TODO(formalism): the finding codes here are invented vocabulary, not drawn
// from PROTOCOL.md, which formalizes only the per-node state machine. They
// fall into three classes — authority violations (the trace holds something
// nobody had standing to produce), unmet obligations (something owed was
// never delivered, graded by finality), and epistemic limits (the record
// under-determines the truth; the renderer's choice is reported) — and the
// severity scheme follows that taxonomy. If this list grows or gains a second
// consumer (audit, another viewer), promote it to PROTOCOL.md as an authority
// relation + per-dispatch lifecycle grammar + numbered property list, and
// make this function the property checker for archives, as the simulator is
// for the state-machine arrows.
func check(run *Run, root string, msgs []protocol.Msg) {
	// INCOMPLETE markers and FLEET-ANOMALY are archive-collection verdicts.
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.Contains(d.Name(), "INCOMPLETE") {
			rel, _ := filepath.Rel(root, path)
			body, _ := os.ReadFile(path)
			run.Issues = append(run.Issues, Issue{Severity: "error", Code: "collection-incomplete",
				Message: rel + ": " + strings.TrimSpace(string(body))})
		}
		if d.Name() == "FLEET-ANOMALY.txt" {
			body, _ := os.ReadFile(path)
			run.Issues = append(run.Issues, Issue{Severity: "error", Code: "fleet-anomaly",
				Message: strings.TrimSpace(string(body))})
		}
		return nil
	})
	if len(run.FleetVerdict) > 0 {
		var v struct {
			Clean bool   `json:"clean"`
			Error string `json:"error"`
		}
		if json.Unmarshal(run.FleetVerdict, &v) == nil && !v.Clean {
			run.Issues = append(run.Issues, Issue{Severity: "error", Code: "fleet-not-clean",
				Message: "fleet-verdict.json is not clean: " + v.Error})
		}
	}

	// Clobbered mtimes: collection stamps every message file within seconds of
	// one extraction moment, while real dispatch traffic spans minutes. A
	// near-zero spread across several files is collection time, not event
	// time, and no timeline can honestly be drawn from it.
	if len(msgs) >= 3 {
		min, max := msgs[0].MTime, msgs[0].MTime
		for _, m := range msgs[1:] {
			if m.MTime < min {
				min = m.MTime
			}
			if m.MTime > max {
				max = m.MTime
			}
		}
		if max-min <= 5 {
			run.TimelineValid = false
			run.Issues = append(run.Issues, Issue{Severity: "error", Code: "mtimes-clobbered",
				Message: fmt.Sprintf("all %d message files fall within %ds — collection time, not event time; this archive predates the mtime-preserving extractor, regenerate the run with the current cs-campaign", len(msgs), max-min)})
		}
	}

	// Per-span checks.
	lastByNode := map[string]string{}
	for _, s := range run.Spans {
		if s.ID > lastByNode[s.Node] {
			lastByNode[s.Node] = s.ID
		}
	}
	pol := run.Campaign.Policy
	orch := orchestratorName(run)
	m1At := m1OpenAt(run, orch)
	for _, s := range run.Spans {
		// Create-phase dispatches are host-issued and host-judged: d001
		// always, and any agent dispatch opened before the mission existed —
		// a resumed create re-runs the readback and mints d002+. When mtimes
		// are bogus only the ID form is trustworthy.
		hostPhase := s.ID == "d001" ||
			(run.TimelineValid && s.Node != orch && (m1At == "" || (s.OpenedAt != "" && s.OpenedAt < m1At)))
		// Recovery spend is checked before the reply gate: an exhausted
		// ladder that never got an answer is the case that matters most.
		if s.Continues > pol.ContinueAttempts {
			run.Issues = append(run.Issues, Issue{Severity: "warning", Code: "continues-exceed-policy",
				Message: fmt.Sprintf("%s/%s took %d continues; policy allows %d", s.Node, s.ID, s.Continues, pol.ContinueAttempts),
				Node:    s.Node, Dispatch: s.ID})
		}
		if s.Restarts > pol.Restarts {
			run.Issues = append(run.Issues, Issue{Severity: "warning", Code: "restarts-exceed-policy",
				Message: fmt.Sprintf("%s/%s took %d restarts; policy allows %d", s.Node, s.ID, s.Restarts, pol.Restarts),
				Node:    s.Node, Dispatch: s.ID})
		}
		if s.RepliedAt == "" {
			run.Issues = append(run.Issues, Issue{Severity: "severe", Code: "no-reply",
				Message: fmt.Sprintf("%s/%s was opened and never replied to", s.Node, s.ID),
				Node:    s.Node, Dispatch: s.ID})
			continue
		}
		// An orphan reply: the tool cannot write a reply without an open
		// dispatch, so a reply with no message behind it was made some other
		// way. Timing checks are skipped for it — there is no open to time
		// against.
		if s.OpenedAt == "" {
			run.Issues = append(run.Issues, Issue{Severity: "error", Code: "reply-without-dispatch",
				Message: fmt.Sprintf("%s/%s has a reply but no message ever opened it — replies written through the tool require an open dispatch", s.Node, s.ID),
				Node:    s.Node, Dispatch: s.ID})
		}
		// Meaningless when open times are collection-time: the clobbered-mtime
		// finding is the root cause and stands alone.
		if run.TimelineValid && s.OpenedAt != "" && s.RepliedAt < s.OpenedAt {
			run.Issues = append(run.Issues, Issue{Severity: "error", Code: "reply-before-open",
				Message: fmt.Sprintf("%s/%s reply at %s precedes its opening at %s", s.Node, s.ID, s.RepliedAt, s.OpenedAt),
				Node:    s.Node, Dispatch: s.ID})
		}
		// Acceptance recorded before the reply it judges: either cross-node
		// clock skew (log times come from the orchestrator's clock, reply
		// times from the replying node's) or judgment of an unanswered
		// dispatch. The archive cannot distinguish the two.
		if s.AcceptedAt != "" && s.AcceptedAt < s.RepliedAt {
			run.Issues = append(run.Issues, Issue{Severity: "warning", Code: "accept-before-reply",
				Message: fmt.Sprintf("%s/%s was accepted at %s, before its reply at %s — clock skew between the orchestrator's log and %s's reply stamp, or an acceptance of unanswered work",
					s.Node, s.ID, s.AcceptedAt, s.RepliedAt, s.Node), Node: s.Node, Dispatch: s.ID})
		}
		// Acceptance is owed only for orchestrator-authored dispatches: agent
		// channels past the readback. The host judges d001 everywhere, and the
		// orchestrator's whole channel (m1, and post-mission sends mint d002+
		// there — the ID alone does not name the author).
		if s.Node != orch && !hostPhase && s.AcceptedAt == "" {
			sev, why := "severe", "and it is the node's final dispatch"
			if s.ID < lastByNode[s.Node] {
				sev, why = "warning", "(superseded by a later dispatch — likely rework)"
			}
			run.Issues = append(run.Issues, Issue{Severity: sev, Code: "reply-not-accepted",
				Message: fmt.Sprintf("%s/%s was replied to but never accepted in the log %s", s.Node, s.ID, why),
				Node:    s.Node, Dispatch: s.ID})
		}
	}

	// Ambiguous ordering inside one dispatch's lifecycle: message times are
	// whole-second mtimes, so two lifecycle events landing in the same second
	// have no derivable order — the timeline renders them in archive-listing
	// order, and that choice is reported rather than presented as sequence.
	// Skipped when the timeline is invalid: bogus mtimes make it meaningless.
	if run.TimelineValid {
		bySpan := map[string]map[string]int{}
		for _, ev := range run.Events {
			if ev.Dispatch == "" || ev.Type == "accept" {
				continue
			}
			key := ev.Node + "/" + ev.Dispatch
			if bySpan[key] == nil {
				bySpan[key] = map[string]int{}
			}
			bySpan[key][ev.At]++
		}
		keys := make([]string, 0, len(bySpan))
		for k := range bySpan {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for at, n := range bySpan[k] {
				if n > 1 {
					run.Issues = append(run.Issues, Issue{Severity: "info", Code: "ambiguous-order",
						Message: fmt.Sprintf("%s: %d lifecycle events share the second %s — rendered in archive-listing order; sub-second order is not derivable because message times are whole-second mtimes", k, n, at)})
				}
			}
		}
	}

	// Verdict.
	switch run.Campaign.Outcome {
	case "":
		run.Issues = append(run.Issues, Issue{Severity: "severe", Code: "no-verdict",
			Message: "the mission dispatch m1 has no outcome-bearing reply — the campaign never concluded"})
	case "campaign-met":
	default:
		run.Issues = append(run.Issues, Issue{Severity: "severe", Code: "campaign-not-met",
			Message: "campaign verdict is " + run.Campaign.Outcome})
	}

	// Readback absences are named in readback.json by design.
	if len(run.Readback) > 0 {
		var rb struct {
			Members []struct {
				Member string `json:"member"`
				Absent string `json:"absent"`
			} `json:"members"`
		}
		if json.Unmarshal(run.Readback, &rb) == nil {
			for _, m := range rb.Members {
				if m.Absent != "" {
					run.Issues = append(run.Issues, Issue{Severity: "warning", Code: "readback-absent",
						Message: m.Member + ": " + m.Absent, Node: m.Member})
				}
			}
		}
	}
	sort.SliceStable(run.Issues, func(i, j int) bool { return sevRank(run.Issues[i].Severity) < sevRank(run.Issues[j].Severity) })
}

func sevRank(s string) int {
	switch s {
	case "error":
		return 0
	case "severe":
		return 1
	case "warning":
		return 2
	}
	return 3
}
