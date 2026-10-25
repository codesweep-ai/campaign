package protocol

import (
	"testing"
)

// SPEC.md: an observation reports how full the node's conversation is, and that
// report MUST NOT decide a state or a ladder move. A CLI compacts a long
// conversation by itself, which is not a failure, so a full context or a fresh
// compaction must leave every row of the computation where it was, and only
// add the note.
func TestAContextReadingDecidesNoState(t *testing.T) {
	pol := Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 1000, BlindProbes: 3, SettlingSeconds: 100}
	now := int64(10000)
	reading := &ContextReading{Tokens: 160000, Window: 200000, Compactions: 3, Compacted: now - 60, Model: "m"}
	for _, tc := range []struct {
		name string
		f    Facts
		acc  map[string]bool
	}{
		{"free", Facts{}, nil},
		{"replied", Facts{Msgs: msgs("d001.md@9000"), Replies: map[string]bool{"d001": true}}, nil},
		{"accepted", Facts{Msgs: msgs("d001.md@9000"), Replies: map[string]bool{"d001": true}}, map[string]bool{"d001": true}},
		{"working", Facts{Msgs: msgs("d001.md@9000"), Drivers: 1}, nil},
		{"settling", Facts{Msgs: msgs("d001.md@9950")}, nil},
		{"stopped, continue next", Facts{Msgs: msgs("d001.md@9500")}, nil},
		{"stopped, restart next", Facts{Msgs: msgs("d001.md@9500", "d001.001.md@9600", "d001.002.md@9700")}, nil},
		{"ladder spent", Facts{Msgs: msgs("d001.md@9500", "d001.001.md@9600", "d001.002.md@9700", "d001.003.restart.md@9850")}, nil},
		{"context too long", Facts{Msgs: msgs("d001.md@9500"), Agent: "idle", TurnEnds: []TurnEnd{{At: 9600, Exit: 5, Class: ClassContext}}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc := tc.acc
			if acc == nil {
				acc = map[string]bool{}
			}
			if tc.f.Replies == nil {
				tc.f.Replies = map[string]bool{}
			}
			without := Compute(tc.f, false, Blind{}, acc, pol, now)
			with := tc.f
			with.Context = reading
			got := Compute(with, false, Blind{}, acc, pol, now)
			if got.State != without.State || got.NextMove != without.NextMove || got.Dispatch != without.Dispatch {
				t.Fatalf("a context reading changed the computation: without %+v, with %+v", without, got)
			}
			if want := without.Detail + contextNote(reading, now); got.Detail != want {
				t.Fatalf("Detail = %q, want the same line plus the note: %q", got.Detail, want)
			}
		})
	}
	// A failed look learned nothing about the node, its context included.
	f := Facts{Context: reading}
	if o := Compute(f, true, Blind{Looks: 1}, map[string]bool{}, pol, now); o.Detail != Compute(Facts{}, true, Blind{Looks: 1}, map[string]bool{}, pol, now).Detail {
		t.Fatalf("a failed look reported a context reading: %q", o.Detail)
	}
}

// The note is read by an operator deciding whether a member is about to
// compact, so it reads in the same short units as the rest of the line.
func TestTheContextNoteReadsLikeAnOperator(t *testing.T) {
	now := int64(10000)
	for _, tc := range []struct {
		c    *ContextReading
		want string
	}{
		{nil, ""},
		{&ContextReading{Tokens: 17117, Window: 258400}, " · context 17k of 258k"},
		{&ContextReading{Tokens: 62221, Compactions: 2, Compacted: now - 240}, " · context 62k · compacted 2 times, last 4m ago"},
		{&ContextReading{Tokens: -1, Compactions: 1, Compacted: now - 30}, " · context not read since the last compaction · compacted once, last 30s ago"},
		{&ContextReading{Tokens: 840}, " · context 840"},
		{&ContextReading{Tokens: 1_234_567, Window: 2_000_000}, " · context 1.2M of 2.0M"},
	} {
		if got := contextNote(tc.c, now); got != tc.want {
			t.Errorf("contextNote(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
}

// The driver prints key=value words, and "absent" or "unknown" when it has no
// reading. A key this parser does not know is a newer driver's, and is skipped.
func TestTheProbeParsesTheContextLine(t *testing.T) {
	for _, tc := range []struct {
		line string
		want *ContextReading
	}{
		{"CONTEXT tokens=40312 window=- compactions=11 compacted=1792267591 model=claude-opus-5",
			&ContextReading{Tokens: 40312, Compactions: 11, Compacted: 1792267591, Model: "claude-opus-5"}},
		{"CONTEXT tokens=17117 window=258400 compactions=0 compacted=- model=gpt-5.6-sol",
			&ContextReading{Tokens: 17117, Window: 258400, Model: "gpt-5.6-sol"}},
		{"CONTEXT tokens=- window=- compactions=1 compacted=1792267196 model=-",
			&ContextReading{Tokens: -1, Compactions: 1, Compacted: 1792267196}},
		{"CONTEXT tokens=5 compactions=0 future=x", &ContextReading{Tokens: 5}},
		{"CONTEXT absent", nil},
		{"CONTEXT unknown", nil},
		{"CONTEXT ", nil},
		{"CONTEXT tokens=5", nil},
	} {
		f := ParseProbe("DRIVERS 0\n" + tc.line + "\n")
		switch {
		case tc.want == nil && f.Context != nil:
			t.Errorf("%q parsed as %+v, want not known", tc.line, *f.Context)
		case tc.want != nil && (f.Context == nil || *f.Context != *tc.want):
			t.Errorf("%q parsed as %+v, want %+v", tc.line, f.Context, *tc.want)
		}
	}
}
