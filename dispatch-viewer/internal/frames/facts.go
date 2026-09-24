package frames

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Facts are the numbers a reader of a run asks for first, computed once from
// the same run the page draws, so an answer quotes them rather than working
// them out. Every fact that names a thing on the page carries its address.
// Nothing here is a judgement: a stall is reported as the harness's own
// continue or restart, with the gap before it and the run's policy beside it.
type Facts struct {
	SchemaVersion int      `json:"schemaVersion"`
	Campaign      string   `json:"campaign"`
	Elapsed       Elapsed  `json:"elapsed"`
	Totals        Totals   `json:"totals"`
	Members       []Member `json:"members"`
	// Dispatches are longest first. One never replied to runs to the end of
	// the run.
	Dispatches    []DispatchTime `json:"dispatches"`
	IdleGaps      []IdleGap      `json:"idleGaps"`
	FailedSteps   []FailedStep   `json:"failedSteps"`
	Recovery      Recovery       `json:"recovery"`
	Subagents     []Subagent     `json:"subagents"`
	RepeatedCalls []RepeatedCall `json:"repeatedCalls"`
}

// FactsSchema is facts.json's own version.
const FactsSchema = 1

// Dur is a duration in milliseconds, with the same span as h:mm:ss beside it.
type Dur struct {
	Ms  int64  `json:"ms"`
	HMS string `json:"hms"`
}

func dur(ms int64) Dur {
	s := ms / 1000
	return Dur{Ms: ms, HMS: fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)}
}

// Elapsed runs from the first protocol event to the last.
type Elapsed struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Duration Dur    `json:"duration"`
	FromAddr string `json:"fromAddr"`
	ToAddr   string `json:"toAddr"`
}

type Totals struct {
	ToolCalls int `json:"toolCalls"`
	// ToolOutputBytes is what tools returned to the models. OutputTokens is
	// what the models wrote. They measure different things.
	ToolOutputBytes int64          `json:"toolOutputBytes"`
	OutputTokens    int            `json:"outputTokens"`
	FailedSteps     map[string]int `json:"failedSteps"`
}

// Member is one node's time and output across its sessions. Idle is a share
// of the member's own time, never summed across members.
type Member struct {
	Node string `json:"node"`
	Role string `json:"role"`
	CLI  string `json:"cli"`
	// Work and Idle sum the tracer's totals for each session. Wait is the
	// part of Work spent in the harness's wait call, which is how the
	// orchestrator spends most of its turn.
	Work            Dur       `json:"work"`
	Idle            Dur       `json:"idle"`
	Wait            Dur       `json:"wait"`
	IdleShare       float64   `json:"idleShare"`
	Steps           int       `json:"steps"`
	ToolCalls       int       `json:"toolCalls"`
	ToolOutputBytes int64     `json:"toolOutputBytes"`
	OutputTokens    int       `json:"outputTokens"`
	Sessions        []SessTot `json:"sessions"`
}

type SessTot struct {
	ID              string `json:"id"`
	Source          string `json:"source"`
	Model           string `json:"model,omitempty"`
	Work            Dur    `json:"work"`
	Idle            Dur    `json:"idle"`
	Steps           int    `json:"steps"`
	ToolCalls       int    `json:"toolCalls"`
	ToolOutputBytes int64  `json:"toolOutputBytes"`
	OutputTokens    int    `json:"outputTokens"`
	Page            string `json:"page,omitempty"`
}

type DispatchTime struct {
	Node      string `json:"node"`
	Dispatch  string `json:"dispatch"`
	Phase     string `json:"phase,omitempty"`
	OpenedAt  string `json:"openedAt"`
	RepliedAt string `json:"repliedAt,omitempty"`
	Duration  Dur    `json:"duration"`
	Continues int    `json:"continues"`
	Restarts  int    `json:"restarts"`
	Resumes   int    `json:"resumes"`
	Addr      string `json:"addr"`
}

// StepRef names one step: where it is and what it was.
type StepRef struct {
	Node    string `json:"node"`
	Session string `json:"session"`
	At      string `json:"at"`
	Kind    string `json:"kind"`
	Label   string `json:"label,omitempty"`
	Error   bool   `json:"error,omitempty"`
	Text    string `json:"text,omitempty"`
	Addr    string `json:"addr"`
	Page    string `json:"page,omitempty"`
}

// IdleGap is the idle after one step, longest first; After is that step.
type IdleGap struct {
	Idle  Dur     `json:"idle"`
	Addr  string  `json:"addr"`
	After StepRef `json:"after"`
}

// FailedStep is a step the tracer marked as an error, with its class:
// provider (the model's server failed), account (a rate limit), guard (the
// member's CLI refused the call), command (the member's own command failed)
// or other.
type FailedStep struct {
	StepRef
	Class  string `json:"class"`
	Result string `json:"result,omitempty"`
}

// Recovery is every continue, restart and resume sent, each with the time
// since that member's last recorded step, beside the policy it acts under.
type Recovery struct {
	Policy RecoveryPolicy  `json:"policy"`
	Events []RecoveryEvent `json:"events"`
}

type RecoveryPolicy struct {
	ContinueAttempts    int `json:"continueAttempts"`
	Restarts            int `json:"restarts"`
	StallSeconds        int `json:"stallSeconds"`
	ProviderWaitSeconds int `json:"providerWaitSeconds"`
}

type RecoveryEvent struct {
	Type     string `json:"type"`
	Node     string `json:"node"`
	Dispatch string `json:"dispatch"`
	At       string `json:"at"`
	Addr     string `json:"addr"`
	// SinceLastStep is the gap from LastStep to this event. Both are absent
	// when the member has no step before it. TurnError is the failed step
	// that ended LastStep's turn, when one did.
	SinceLastStep *Dur     `json:"sinceLastStep,omitempty"`
	LastStep      *StepRef `json:"lastStep,omitempty"`
	TurnError     *StepRef `json:"turnError,omitempty"`
}

type Subagent struct {
	StepRef
	Child string `json:"child,omitempty"`
}

// RepeatedCall is one member calling one tool with the same input three times
// or more. Polling marks the harness's wait call, which is meant to repeat.
type RepeatedCall struct {
	Node    string   `json:"node"`
	Tool    string   `json:"tool"`
	Input   string   `json:"input"`
	Count   int      `json:"count"`
	Polling bool     `json:"polling,omitempty"`
	Addrs   []string `json:"addrs"`
}

const (
	idleGapsKept = 10
	repeatAt     = 3
	resultLimit  = 200
	inputLimit   = 200
)

// ComputeFacts reads the run after its traces are attached and its addresses
// written. A run with no traces has only its protocol facts.
func ComputeFacts(run *Run) *Facts {
	f := &Facts{SchemaVersion: FactsSchema, Campaign: run.Campaign.Name,
		Totals: Totals{FailedSteps: map[string]int{}}}
	var end time.Time
	if n := len(run.Events); n > 0 {
		first, last := run.Events[0], run.Events[n-1]
		end = parseTS(last.At)
		f.Elapsed = Elapsed{From: first.At, To: last.At, FromAddr: first.Addr, ToAddr: last.Addr,
			Duration: dur(end.Sub(parseTS(first.At)).Milliseconds())}
	}

	for _, s := range run.Spans {
		if s.OpenedAt == "" {
			continue
		}
		stop := end
		if s.RepliedAt != "" {
			stop = parseTS(s.RepliedAt)
		}
		f.Dispatches = append(f.Dispatches, DispatchTime{Node: s.Node, Dispatch: s.ID, Phase: s.Phase,
			OpenedAt: s.OpenedAt, RepliedAt: s.RepliedAt, Continues: s.Continues, Restarts: s.Restarts, Resumes: s.Resumes,
			Addr: s.Addr, Duration: dur(max(0, stop.Sub(parseTS(s.OpenedAt)).Milliseconds()))})
	}
	slices.SortStableFunc(f.Dispatches, func(a, b DispatchTime) int {
		return cmp.Or(cmp.Compare(b.Duration.Ms, a.Duration.Ms), cmp.Compare(a.Node, b.Node), cmp.Compare(a.Dispatch, b.Dispatch))
	})

	pol := run.Campaign.Policy
	f.Recovery.Policy = RecoveryPolicy{ContinueAttempts: pol.ContinueAttempts, Restarts: pol.Restarts,
		StallSeconds: pol.StallSeconds, ProviderWaitSeconds: pol.ProviderWaitSeconds}
	f.Recovery.Events = []RecoveryEvent{}
	f.Subagents = []Subagent{}
	f.IdleGaps = []IdleGap{}
	f.FailedSteps = []FailedStep{}
	f.RepeatedCalls = []RepeatedCall{}

	var sessions []Session
	if run.Traces != nil {
		sessions = run.Traces.Sessions
	}
	for _, n := range run.Nodes {
		m := Member{Node: n.Name, Role: n.Role, CLI: n.CLI, Sessions: []SessTot{}}
		var work, idle, wait int64
		for si := range sessions {
			s := &sessions[si]
			if s.Node != n.Name {
				continue
			}
			st := sessionFacts(f, s)
			m.Sessions = append(m.Sessions, st.tot)
			work += st.tot.Work.Ms
			idle += st.tot.Idle.Ms
			wait += st.wait
			m.Steps += st.tot.Steps
			m.ToolCalls += st.tot.ToolCalls
			m.ToolOutputBytes += st.tot.ToolOutputBytes
			m.OutputTokens += st.tot.OutputTokens
		}
		m.Work, m.Idle, m.Wait = dur(work), dur(idle), dur(wait)
		if work+idle > 0 {
			m.IdleShare = float64(idle*1000/(work+idle)) / 1000
		}
		f.Totals.ToolCalls += m.ToolCalls
		f.Totals.ToolOutputBytes += m.ToolOutputBytes
		f.Totals.OutputTokens += m.OutputTokens
		f.Members = append(f.Members, m)
	}
	slices.SortStableFunc(f.IdleGaps, func(a, b IdleGap) int {
		return cmp.Or(cmp.Compare(b.Idle.Ms, a.Idle.Ms), cmp.Compare(a.Addr, b.Addr))
	})
	if len(f.IdleGaps) > idleGapsKept {
		f.IdleGaps = f.IdleGaps[:idleGapsKept]
	}
	for _, fs := range f.FailedSteps {
		f.Totals.FailedSteps[fs.Class]++
	}
	slices.SortStableFunc(f.RepeatedCalls, func(a, b RepeatedCall) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Node, b.Node), cmp.Compare(a.Tool, b.Tool), cmp.Compare(a.Input, b.Input))
	})

	for _, e := range run.Events {
		if e.Type != "continue" && e.Type != "restart" && e.Type != "resume" {
			continue
		}
		ev := RecoveryEvent{Type: e.Type, Node: e.Node, Dispatch: e.Dispatch, At: e.At, Addr: e.Addr}
		if last, ended := lastStepBefore(sessions, e.Node, parseTS(e.At)); last != nil {
			gap := dur(parseTS(e.At).Sub(parseTS(last.At)).Milliseconds())
			ev.SinceLastStep, ev.LastStep, ev.TurnError = &gap, last, ended
		}
		f.Recovery.Events = append(f.Recovery.Events, ev)
	}
	return f
}

type sessionTally struct {
	tot  SessTot
	wait int64
}

// sessionFacts totals one session and adds its idle gaps, failed steps,
// subagents and repeated calls to f.
func sessionFacts(f *Facts, s *Session) sessionTally {
	t := sessionTally{tot: SessTot{ID: s.ID, Source: s.Source, Model: s.Model, Page: s.Page,
		Steps: len(s.Strip), OutputTokens: s.totals.Output}}
	byI := map[int]tracerEvent{}
	for _, e := range s.events {
		byI[e.I] = e
		if e.Kind == "tool_call" {
			t.tot.ToolCalls++
			if e.Result != nil {
				t.tot.ToolOutputBytes += int64(len(e.Result.Text))
			}
		}
	}
	type callKey struct{ tool, input string }
	calls := map[callKey]*RepeatedCall{}
	var order []callKey
	for _, st := range s.Strip {
		ref := stepRef(s, st)
		if st.WorkMs != nil && st.Wait {
			t.wait += *st.WorkMs
		}
		if st.IdleMs != nil && *st.IdleMs > 0 {
			f.IdleGaps = append(f.IdleGaps, IdleGap{Idle: dur(*st.IdleMs), Addr: st.IdleAddr, After: ref})
		}
		e, hasEvent := byI[st.I]
		if st.Error {
			fs := FailedStep{StepRef: ref, Class: failureClass(st, e)}
			if hasEvent && e.Result != nil {
				fs.Result = clip(e.Result.Text, resultLimit)
			}
			f.FailedSteps = append(f.FailedSteps, fs)
		}
		if st.Subtask || st.Child != "" {
			f.Subagents = append(f.Subagents, Subagent{StepRef: ref, Child: st.Child})
		}
		if hasEvent && e.Kind == "tool_call" && e.Tool != nil {
			input := e.Tool.Command
			if input == "" {
				input = string(e.Tool.Input)
			}
			k := callKey{e.Tool.Name, input}
			rc := calls[k]
			if rc == nil {
				rc = &RepeatedCall{Node: s.Node, Tool: e.Tool.Name, Input: clip(input, inputLimit), Polling: isWaitCall(e)}
				calls[k] = rc
				order = append(order, k)
			}
			rc.Count++
			rc.Addrs = append(rc.Addrs, st.Addr)
		}
	}
	for _, k := range order {
		if rc := calls[k]; rc.Count >= repeatAt {
			f.RepeatedCalls = append(f.RepeatedCalls, *rc)
		}
	}
	t.tot.Work, t.tot.Idle = dur(s.totals.Time.WorkMs), dur(s.totals.Time.IdleMs)
	return t
}

// failureClass reads the class from the tracer's label and the call's own
// result. A label names the provider's error kind; a refused call carries its
// CLI's refusal as the result.
func failureClass(st Step, e tracerEvent) string {
	label := strings.ToLower(st.Label)
	switch {
	case label == "rate_limit_exceeded" || strings.Contains(label, "rate_limit"):
		return "account"
	case label == "server_error" || strings.Contains(label, "overloaded"):
		return "provider"
	case st.Kind == "tool_call" || e.Tool != nil:
		if e.Result != nil {
			r := strings.TrimSpace(e.Result.Text)
			if strings.HasPrefix(r, "<tool_use_error>") || strings.Contains(r, "PermissionDenied") {
				return "guard"
			}
		}
		return "command"
	}
	return "other"
}

func stepRef(s *Session, st Step) StepRef {
	r := StepRef{Node: s.Node, Session: s.ID, At: st.TS, Kind: st.Kind, Label: st.Label,
		Error: st.Error, Text: st.Text, Addr: st.Addr}
	if s.Page != "" {
		r.Page = fmt.Sprintf("%s#ev-%d", s.Page, st.I)
	}
	return r
}

// turnError is the failed step that ended the turn holding strip[at], if one
// did: the latest error walking back from it to the user turn that began it.
func turnError(s *Session, at int) *StepRef {
	for i := at; i >= 0; i-- {
		st := s.Strip[i]
		if st.Error {
			r := stepRef(s, st)
			return &r
		}
		if st.Kind == "user" {
			break
		}
	}
	return nil
}

// lastStepBefore is node's latest step, across its sessions, strictly before at.
// It returns the failed step that ended that step's turn beside it.
func lastStepBefore(sessions []Session, node string, at time.Time) (*StepRef, *StepRef) {
	var best, ended *StepRef
	var bestAt time.Time
	for si := range sessions {
		s := &sessions[si]
		if s.Node != node {
			continue
		}
		for i, st := range s.Strip {
			ts := parseTS(st.TS)
			if st.TS == "" || !ts.Before(at) || (best != nil && !ts.After(bestAt)) {
				continue
			}
			r := stepRef(s, st)
			best, ended, bestAt = &r, turnError(s, i), ts
		}
	}
	return best, ended
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}
