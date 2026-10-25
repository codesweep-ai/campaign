package frames

import (
	"path/filepath"
	"testing"
	"time"
)

// A .handoff message is drawn as a hand-off, counted on its span apart from
// continues and resumes, and listed with the recovery a reader looks through.
// Read as a continue it would show a node that did as it was asked spending a
// rung it never spent.
func TestAHandoffIsItsOwnEvent(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }
	write(t, filepath.Join(root, "campaign.json"), `{
	  "name":"ho","id":"ho-1","createdAt":"2026-10-20T09:00:00Z","updatedAt":"2026-10-20T10:00:00Z",
	  "policy":{"continueAttempts":2,"restarts":1,"handoffs":10},
	  "members":[{"name":"orchestrator","role":"orchestrator","cli":"claude"},{"name":"dev","role":"agent","cli":"claude"}]}`, at(0))
	write(t, filepath.Join(root, "orchestrator", "input", "m1.md"), "# mission", at(1))
	d := filepath.Join(root, "agents", "dev")
	write(t, filepath.Join(d, "input", "d001.md"), "# work", at(1))
	write(t, filepath.Join(d, "input", "d001.001.handoff.md"), "You are starting a fresh session after a hand-off", at(20))
	write(t, filepath.Join(d, "output", "replies", "d001.json"),
		`{"dispatch":"d001","phase":"done","note":"did it","at":"2026-10-20T09:40:00Z"}`, at(40))
	run, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}

	var handoffs []Event
	for _, e := range run.Events {
		if e.Node == "dev" && e.Type == "handoff" {
			handoffs = append(handoffs, e)
		}
		if e.Node == "dev" && (e.Type == "continue" || e.Type == "resume" || e.Type == "restart") {
			t.Errorf("a hand-off was drawn as a %s: %+v", e.Type, e)
		}
	}
	if len(handoffs) != 1 || handoffs[0].Dispatch != "d001" {
		t.Fatalf("want one hand-off event on dev/d001, got %+v", handoffs)
	}

	var span *Span
	for i := range run.Spans {
		if run.Spans[i].Node == "dev" && run.Spans[i].ID == "d001" {
			span = &run.Spans[i]
		}
	}
	if span == nil || span.Handoffs != 1 || span.Continues != 0 || span.Resumes != 0 || span.Restarts != 0 {
		t.Fatalf("the span counts the hand-off apart: %+v", span)
	}

	f := ComputeFacts(run)
	found := false
	for _, ev := range f.Recovery.Events {
		if ev.Type == "handoff" && ev.Node == "dev" && ev.Dispatch == "d001" {
			found = true
		}
	}
	if !found {
		t.Errorf("the hand-off is missing from the recovery events: %+v", f.Recovery.Events)
	}
	counted := -1
	for _, dt := range f.Dispatches {
		if dt.Node == "dev" && dt.Dispatch == "d001" {
			counted = dt.Handoffs
		}
	}
	if counted != 1 {
		t.Errorf("the dispatch's facts count %d hand-offs, want 1 (-1: dev/d001 is missing)", counted)
	}
}

// A .commit message is a hand-off that was not taken: the session is kept and
// asked to commit first. Read as a continue it would show a rung spent, and
// read as a hand-off it would show a fresh session that never started.
func TestACommitAskIsItsOwnEvent(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }
	write(t, filepath.Join(root, "campaign.json"), `{
	  "name":"ca","id":"ca-1","createdAt":"2026-10-24T09:00:00Z","updatedAt":"2026-10-24T10:00:00Z",
	  "policy":{"continueAttempts":2,"restarts":1,"handoffs":10},
	  "members":[{"name":"orchestrator","role":"orchestrator","cli":"claude"},{"name":"dev","role":"agent","cli":"claude"}]}`, at(0))
	write(t, filepath.Join(root, "orchestrator", "input", "m1.md"), "# mission", at(1))
	d := filepath.Join(root, "agents", "dev")
	write(t, filepath.Join(d, "input", "d001.md"), "# work", at(1))
	write(t, filepath.Join(d, "input", "d001.001.commit.md"), "Your hand-off on dispatch d001 was not taken, and this session is kept", at(20))
	write(t, filepath.Join(d, "input", "d001.002.handoff.md"), "You are starting a fresh session after a hand-off", at(25))
	write(t, filepath.Join(d, "output", "replies", "d001.json"),
		`{"dispatch":"d001","phase":"done","note":"did it","at":"2026-10-24T09:40:00Z"}`, at(40))
	run, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}

	var asks []Event
	for _, e := range run.Events {
		if e.Node == "dev" && e.Type == "commit" {
			asks = append(asks, e)
		}
		if e.Node == "dev" && (e.Type == "continue" || e.Type == "resume" || e.Type == "restart") {
			t.Errorf("an ask to commit was drawn as a %s: %+v", e.Type, e)
		}
	}
	if len(asks) != 1 || asks[0].Dispatch != "d001" || asks[0].File != "d001.001.commit.md" {
		t.Fatalf("want one commit event on dev/d001, got %+v", asks)
	}

	var span *Span
	for i := range run.Spans {
		if run.Spans[i].Node == "dev" && run.Spans[i].ID == "d001" {
			span = &run.Spans[i]
		}
	}
	if span == nil || span.CommitAsks != 1 || span.Handoffs != 1 || span.Continues != 0 || span.Resumes != 0 || span.Restarts != 0 {
		t.Fatalf("the span counts the ask to commit apart: %+v", span)
	}

	f := ComputeFacts(run)
	found := false
	for _, ev := range f.Recovery.Events {
		if ev.Type == "commit" && ev.Node == "dev" && ev.Dispatch == "d001" {
			found = true
		}
	}
	if !found {
		t.Errorf("the ask to commit is missing from the recovery events: %+v", f.Recovery.Events)
	}
	counted := -1
	for _, dt := range f.Dispatches {
		if dt.Node == "dev" && dt.Dispatch == "d001" {
			counted = dt.CommitAsks
		}
	}
	if counted != 1 {
		t.Errorf("the dispatch's facts count %d asks to commit, want 1 (-1: dev/d001 is missing)", counted)
	}
}
