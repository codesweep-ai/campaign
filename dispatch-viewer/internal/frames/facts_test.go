package frames

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

func ms(v int64) *int64 { return new(v) }

// The page numbers steps after the events, two slots a step, and spends the
// slots of a step it does not draw (model.ts stepMarks). An address that
// drifts from that opens the page on the wrong step.
func TestAddressesNumberStepsAsThePageDoes(t *testing.T) {
	run := &Run{
		Events: []Event{{Type: "open"}, {Type: "reply"}, {Type: "accept"}},
		Spans:  []Span{{ID: "d002", Node: "dev", OpenedAt: "t"}, {ID: "d003", Node: "dev"}},
		Traces: &Traces{Sessions: []Session{
			{ID: "a", Strip: []Step{{I: 0, TS: "t"}, {I: 1}, {I: 2, TS: "t", IdleMs: ms(5)}}},
			{ID: "b", Strip: []Step{{I: 0, TS: "t", IdleMs: ms(0)}, {I: 1, Kind: "turn_end", TS: "t", IdleMs: ms(5)}, {I: 2, Kind: "meta", TS: "t"}}},
		}},
	}
	addresses(run)
	var got []string
	for _, e := range run.Events {
		got = append(got, e.Addr)
	}
	for _, s := range run.Spans {
		got = append(got, s.Addr)
	}
	for _, s := range run.Traces.Sessions {
		for _, st := range s.Strip {
			got = append(got, st.Addr+"|"+st.IdleAddr)
		}
	}
	want := []string{"e/0", "e/1", "e/2", "m/dev/d002", "",
		"traces/e/3|", "|", "traces/e/7|traces/e/8", "traces/e/9|", "|traces/e/12", "|"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("addresses:\n got %q\nwant %q", got, want)
	}
}

func TestFailureClassReadsTheLabelAndTheResult(t *testing.T) {
	call := func(result string) tracerEvent {
		return tracerEvent{Kind: "tool_call", Tool: &tracerTool{Name: "Bash"}, Result: &tracerResult{Text: result, IsError: true}}
	}
	cases := []struct {
		step Step
		e    tracerEvent
		want string
	}{
		{Step{Kind: "assistant", Label: "server_error"}, tracerEvent{}, "provider"},
		{Step{Kind: "turn_end", Label: "rate_limit_exceeded"}, tracerEvent{}, "account"},
		{Step{Kind: "tool_call", Label: "Bash"}, call("<tool_use_error>Blocked: sleep 240"), "guard"},
		{Step{Kind: "tool_call", Label: "write"}, call("PermissionDenied: FileSystem.writeFile (/tmp/x)"), "guard"},
		{Step{Kind: "tool_call", Label: "Bash"}, call("Exit code 1\nTraceback"), "command"},
		{Step{Kind: "assistant", Label: "refusal"}, tracerEvent{}, "other"},
	}
	for _, c := range cases {
		if got := failureClass(c.step, c.e); got != c.want {
			t.Errorf("%s/%s: class %q, want %q", c.step.Kind, c.step.Label, got, c.want)
		}
	}
}

// A continue is reported with the gap since the member's last step, and with
// the error that ended that turn, which is what the reader needs to see.
func TestRecoveryNamesTheGapAndTheErrorThatEndedTheTurn(t *testing.T) {
	run := &Run{
		Campaign: CampaignMeta{Policy: policyOf(180, 3600)},
		Nodes:    []Node{{Name: "orch", Role: "orchestrator"}},
		Events: []Event{
			{Node: "orch", Type: "open", Dispatch: "m1", At: "2026-09-22T00:00:00Z"},
			{Node: "orch", Type: "continue", Dispatch: "m1", At: "2026-09-22T03:10:00Z"},
		},
		Traces: &Traces{Sessions: []Session{{ID: "s", Node: "orch", Page: "tracer/traces/s.html", Strip: []Step{
			{I: 0, Kind: "user", TS: "2026-09-22T00:00:01Z"},
			{I: 1, Kind: "assistant", TS: "2026-09-22T00:10:00Z", Error: true, Label: "server_error"},
			{I: 2, Kind: "turn_end", TS: "2026-09-22T00:10:00.5Z", IdleMs: ms(10_799_500)},
			{I: 3, Kind: "user", TS: "2026-09-22T03:10:01Z"},
		}}}},
	}
	addresses(run)
	f := ComputeFacts(run)
	if len(f.Recovery.Events) != 1 {
		t.Fatalf("recovery events: %+v", f.Recovery.Events)
	}
	ev := f.Recovery.Events[0]
	if ev.Addr != "e/1" || ev.SinceLastStep == nil || ev.SinceLastStep.HMS != "2:59:59" {
		t.Errorf("event %+v, gap %+v", ev, ev.SinceLastStep)
	}
	// A turn end is not drawn, so it has no address; the error before it does.
	if ev.LastStep == nil || ev.LastStep.Kind != "turn_end" || ev.LastStep.Addr != "" {
		t.Errorf("last step %+v", ev.LastStep)
	}
	if ev.TurnError == nil || ev.TurnError.Label != "server_error" || ev.TurnError.Addr != "traces/e/4" || ev.TurnError.Page != "tracer/traces/s.html#ev-1" {
		t.Errorf("turn error %+v", ev.TurnError)
	}
	if f.Recovery.Policy.StallSeconds != 180 || f.Recovery.Policy.ProviderWaitSeconds != 3600 {
		t.Errorf("policy %+v", f.Recovery.Policy)
	}
	if len(f.FailedSteps) != 1 || f.FailedSteps[0].Class != "provider" || f.Totals.FailedSteps["provider"] != 1 {
		t.Errorf("failed steps %+v, totals %+v", f.FailedSteps, f.Totals.FailedSteps)
	}
}

func policyOf(stall, providerWait int) (p protocol.Policy) {
	p.StallSeconds, p.ProviderWaitSeconds = stall, providerWait
	return p
}

// A member's time is the tracer's own totals per session, its idle a share of
// its own time, and tool output and output tokens are kept apart.
func TestMemberTotalsKeepTheirMeasuresApart(t *testing.T) {
	sess := func(id string, work, idle int64, out int, events ...tracerEvent) Session {
		s := Session{ID: id, Node: "dev", events: events}
		s.totals.Time.WorkMs, s.totals.Time.IdleMs, s.totals.Output = work, idle, out
		for _, e := range events {
			s.Strip = append(s.Strip, Step{I: e.I, Kind: e.Kind, TS: "2026-09-22T00:00:00Z"})
		}
		return s
	}
	call := func(i int, result string) tracerEvent {
		return tracerEvent{I: i, Kind: "tool_call", Tool: &tracerTool{Name: "read", Input: json.RawMessage(`{"f":1}`)}, Result: &tracerResult{Text: result}}
	}
	run := &Run{
		Nodes: []Node{{Name: "dev", Role: "agent", CLI: "opencode"}},
		Traces: &Traces{Sessions: []Session{
			sess("a", 60_000, 0, 100, call(0, "12345")),
			sess("b", 30_000, 90_000, 50, call(0, "123"), call(1, "123"), call(2, "123")),
		}},
	}
	addresses(run)
	f := ComputeFacts(run)
	m := f.Members[0]
	if m.Work.Ms != 90_000 || m.Idle.Ms != 90_000 || m.IdleShare != 0.5 {
		t.Errorf("work %v idle %v share %v", m.Work, m.Idle, m.IdleShare)
	}
	if m.ToolOutputBytes != 14 || m.OutputTokens != 150 || m.ToolCalls != 4 || len(m.Sessions) != 2 {
		t.Errorf("bytes %d tokens %d calls %d sessions %d", m.ToolOutputBytes, m.OutputTokens, m.ToolCalls, len(m.Sessions))
	}
	if f.Totals.ToolOutputBytes != 14 || f.Totals.OutputTokens != 150 {
		t.Errorf("totals %+v", f.Totals)
	}
	// Three reads with one input in one session are counted; the fourth is
	// in another session and is not added to them.
	if len(f.RepeatedCalls) != 1 || f.RepeatedCalls[0].Count != 3 || f.RepeatedCalls[0].Polling {
		t.Errorf("repeated calls %+v", f.RepeatedCalls)
	}
}

func TestRepeatedWaitCallsAreMarkedPolling(t *testing.T) {
	wait := func(i int) tracerEvent {
		return tracerEvent{I: i, Kind: "tool_call", Tool: &tracerTool{Name: "Bash", Command: "cs-campaign-member wait"}}
	}
	s := Session{ID: "o", Node: "orch", events: []tracerEvent{wait(0), wait(1), wait(2)}}
	for _, e := range s.events {
		s.Strip = append(s.Strip, Step{I: e.I, Kind: e.Kind, TS: "2026-09-22T00:00:00Z", Wait: true})
	}
	run := &Run{Nodes: []Node{{Name: "orch", Role: "orchestrator"}}, Traces: &Traces{Sessions: []Session{s}}}
	addresses(run)
	f := ComputeFacts(run)
	if len(f.RepeatedCalls) != 1 || !f.RepeatedCalls[0].Polling || len(f.RepeatedCalls[0].Addrs) != 3 {
		t.Errorf("repeated calls %+v", f.RepeatedCalls)
	}
}

// Dispatches come longest first, and one never replied to runs to the end.
func TestDispatchesAreLongestFirst(t *testing.T) {
	run, err := Load(fixture(t, false))
	if err != nil {
		t.Fatal(err)
	}
	f := ComputeFacts(run)
	var got []string
	for _, d := range f.Dispatches {
		got = append(got, d.Node+"/"+d.Dispatch+" "+d.Duration.HMS)
	}
	// The run ends at the last event, the accept at 07:41. dev/d003 opened at
	// 07:35 and was never answered, so it runs to 07:41.
	want := []string{"orchestrator/m1 0:38:00", "dev/d002 0:15:00", "dev/d003 0:06:00",
		"dev/d001 0:03:00", "orchestrator/d001 0:02:00"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dispatches:\n got %q\nwant %q", got, want)
	}
	if f.Elapsed.Duration.HMS != "0:40:00" || f.Elapsed.FromAddr != "e/0" {
		t.Errorf("elapsed %+v", f.Elapsed)
	}
	if len(f.Recovery.Events) != 4 {
		t.Errorf("recovery: %+v", f.Recovery.Events)
	}
}

// A site keeps each trajectory as the tracer wrote it, with its member added.
func TestWriteTrajectoriesKeepsTheTracersDocument(t *testing.T) {
	dir := t.TempDir()
	tr := &Traces{Sessions: []Session{{ID: "s1", Node: "dev",
		summary: json.RawMessage(`{"schemaVersion":3,"totals":{"output":7}}`),
		raw:     []json.RawMessage{json.RawMessage(`{"i":0,"kind":"user","text":"hi"}`)}}}}
	if err := WriteTrajectories(tr, dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "s1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SchemaVersion int               `json:"schemaVersion"`
		Node          string            `json:"node"`
		Totals        map[string]int    `json:"totals"`
		Events        []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != 3 || doc.Node != "dev" || doc.Totals["output"] != 7 || len(doc.Events) != 1 {
		t.Errorf("trajectory: %s", raw)
	}
}
