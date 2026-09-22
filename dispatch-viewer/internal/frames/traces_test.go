package frames

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAfterReadsADispatchID(t *testing.T) {
	cases := []struct {
		text, prefix, stop, want string
		ok                       bool
	}{
		{"Read ~/x and follow it. Dispatch ID: d002. When done", "Dispatch ID: ", ".", "d002", true},
		{"replied to d014 — the dispatch is closed.", "replied to ", " ", "d014", true},
		{"replied to nothing yet", "replied to ", " ", "", false},
		{"Dispatch ID: m1.", "Dispatch ID: ", ".", "m1", true},
		{"Dispatch ID: x9.", "Dispatch ID: ", ".", "", false},
		{"no prefix here", "Dispatch ID: ", ".", "", false},
	}
	for _, c := range cases {
		got, ok := after(c.text, c.prefix, c.stop)
		if got != c.want || ok != c.ok {
			t.Errorf("after(%q) = %q,%v; want %q,%v", c.text, got, ok, c.want, c.ok)
		}
	}
}

// joinAnchors reads only the harness's own text: the member's user turn
// carries the id by construction, and the three receipts are what send,
// reply and accept print.
func TestJoinAnchorsFindsTheFourAnchors(t *testing.T) {
	run := &Run{
		Nodes: []Node{{Name: "orch", Role: "orchestrator"}, {Name: "dev", Role: "agent"}},
		Spans: []Span{{ID: "d002", Node: "dev", OpenedAt: "t1", RepliedAt: "t2", AcceptedAt: "t3"}},
	}
	tool := func(i int, out string) tracerEvent {
		return tracerEvent{I: i, Kind: "tool_call",
			Tool: &tracerTool{Name: "Bash", Command: "cs-campaign-member x"}, Result: &tracerResult{Text: out}}
	}
	tr := &Traces{Sessions: []Session{
		{ID: "o1", Node: "orch", events: []tracerEvent{
			tool(5, "dev/d002 opened"),
			tool(9, "accepted d002 from dev — the agent is free"),
		}},
		{ID: "s1", Node: "dev", events: []tracerEvent{
			{I: 0, Kind: "user", Text: "Read ~/in/d002.md and follow it. Dispatch ID: d002. When done"},
			tool(7, "replied to d002 — the dispatch is closed."),
		}},
	}}
	joinAnchors(run, tr)
	want := map[string]Anchor{
		"sent":     {Node: "dev", Dispatch: "d002", Kind: "sent", Session: "o1", I: 5},
		"arrived":  {Node: "dev", Dispatch: "d002", Kind: "arrived", Session: "s1", I: 0},
		"replied":  {Node: "dev", Dispatch: "d002", Kind: "replied", Session: "s1", I: 7},
		"accepted": {Node: "dev", Dispatch: "d002", Kind: "accepted", Session: "o1", I: 9},
	}
	if len(tr.Anchors) != len(want) {
		t.Fatalf("got %d anchors, want %d: %+v", len(tr.Anchors), len(want), tr.Anchors)
	}
	for _, a := range tr.Anchors {
		if a != want[a.Kind] {
			t.Errorf("anchor %s = %+v, want %+v", a.Kind, a, want[a.Kind])
		}
	}
	for _, i := range run.Issues {
		if i.Code == "trace-unlinked" {
			t.Errorf("unexpected finding: %+v", i)
		}
	}
}

func TestJoinAnchorsReportsWhatItCannotFind(t *testing.T) {
	run := &Run{
		Nodes:  []Node{{Name: "orch", Role: "orchestrator"}, {Name: "dev", Role: "agent"}},
		Events: []Event{{Node: "orch", Type: "open", Dispatch: "m1", At: "t0"}},
		Spans: []Span{
			{ID: "d001", Node: "dev", OpenedAt: "t1", RepliedAt: "t2"},
			{ID: "d002", Node: "dev", OpenedAt: "t1", RepliedAt: "t2", AcceptedAt: "t3"},
		},
	}
	tr := &Traces{Sessions: []Session{{ID: "s1", Node: "dev", events: []tracerEvent{
		{I: 0, Kind: "user", Text: "Dispatch ID: d001."},
		{I: 3, Kind: "user", Text: "Dispatch ID: d002."},
	}}}}
	joinAnchors(run, tr)
	var missing []string
	for _, i := range run.Issues {
		if i.Code == "trace-unlinked" {
			missing = append(missing, i.Message)
		}
	}
	// d001 is a readback the host issued, so no send is owed; its reply
	// receipt is. d002 opened after the mission, and owes all three receipts.
	want := []string{
		"dev/d001: no replied anchor was found in the traces",
		"dev/d002: no sent anchor was found in the traces",
		"dev/d002: no replied anchor was found in the traces",
		"dev/d002: no accepted anchor was found in the traces",
	}
	if strings.Join(missing, "\n") != strings.Join(want, "\n") {
		t.Errorf("findings:\n%s\nwant:\n%s", strings.Join(missing, "\n"), strings.Join(want, "\n"))
	}
}

// A resumed create asks dev again at d002, which the host writes as it wrote
// d001, so neither owes a send. The work the orchestrator opened at d003 does.
func TestJoinAnchorsOwesNoSendForALaterReadback(t *testing.T) {
	run := &Run{
		Nodes:    []Node{{Name: "orch", Role: "orchestrator"}, {Name: "dev", Role: "agent"}},
		Events:   []Event{{Node: "orch", Type: "open", Dispatch: "m1", At: "t2"}},
		Readback: []byte(`{"members":[{"member":"dev","readback":{"dispatch":"d002"}}]}`),
		Spans: []Span{
			{ID: "d001", Node: "dev", OpenedAt: "t0"},
			{ID: "d002", Node: "dev", OpenedAt: "t1"},
			{ID: "d003", Node: "dev", OpenedAt: "t3"},
		},
	}
	tr := &Traces{Sessions: []Session{{ID: "s1", Node: "dev", events: []tracerEvent{
		{I: 0, Kind: "user", Text: "Dispatch ID: d001."},
		{I: 2, Kind: "user", Text: "Dispatch ID: d002."},
		{I: 4, Kind: "user", Text: "Dispatch ID: d003."},
	}}}}
	joinAnchors(run, tr)
	var missing []string
	for _, i := range run.Issues {
		if i.Code == "trace-unlinked" {
			missing = append(missing, i.Message)
		}
	}
	want := "dev/d003: no sent anchor was found in the traces"
	if strings.Join(missing, "\n") != want {
		t.Errorf("findings:\n%s\nwant:\n%s", strings.Join(missing, "\n"), want)
	}
}

func writeTgz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUntarRefusesAnEscape(t *testing.T) {
	dir := t.TempDir()
	tgz := filepath.Join(dir, "bad.tgz")
	writeTgz(t, tgz, map[string]string{"../outside": "x"})
	if err := untar(tgz, filepath.Join(dir, "store")); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("untar accepted an escaping entry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "outside")); err == nil {
		t.Fatal("the escaping entry was written")
	}
}

func TestUntarUnpacksAStore(t *testing.T) {
	dir := t.TempDir()
	tgz := filepath.Join(dir, "ok.tgz")
	writeTgz(t, tgz, map[string]string{".cs-claude/projects/p/s.jsonl": "{}\n"})
	if err := untar(tgz, filepath.Join(dir, "store")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "store", ".cs-claude", "projects", "p", "s.jsonl")); err != nil || string(b) != "{}\n" {
		t.Fatalf("unpacked file: %q, %v", b, err)
	}
}

// Without a tracer the page is still rendered, and says why it has no traces.
func TestAttachTracesWithoutATracer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "campaign.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := &Run{Nodes: []Node{{Name: "orch", Role: "orchestrator"}}}
	t.Setenv("PATH", dir) // nothing on it
	if err := AttachTraces(context.Background(), run, dir, TraceOptions{}); err != nil {
		t.Fatal(err)
	}
	if run.Traces != nil || len(run.Issues) != 1 || run.Issues[0].Code != "tracer-absent" {
		t.Fatalf("traces=%v issues=%+v", run.Traces, run.Issues)
	}
}

// A member whose archive holds no transcript is a finding, not a failure,
// and the members that have one are still read.
func TestAttachTracesReportsAMissingTranscript(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "campaign.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTgz(t, filepath.Join(dir, "orchestrator", "transcript", "cli-evidence.tgz"), map[string]string{".cs-turns/claude.log": "x\n"})
	// A tracer that writes an empty index for whatever it is given.
	fake := filepath.Join(dir, "cs-tracer")
	script := "#!/bin/sh\ncase \"$1\" in version) echo fake 1;; normalize) mkdir -p \"$4\" && echo '{\"trajectories\":[]}' > \"$4/index.json\";; esac\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	run := &Run{Nodes: []Node{{Name: "orch", Role: "orchestrator"}, {Name: "dev", Role: "agent"}}}
	if err := AttachTraces(context.Background(), run, dir, TraceOptions{Tracer: fake}); err != nil {
		t.Fatal(err)
	}
	if run.Traces == nil || run.Traces.Tool != "fake 1" {
		t.Fatalf("traces = %+v", run.Traces)
	}
	if len(run.Issues) != 1 || run.Issues[0].Code != "trace-missing" || run.Issues[0].Node != "dev" {
		t.Fatalf("issues = %+v", run.Issues)
	}
}

func TestIsWaitCallReadsTheVerb(t *testing.T) {
	call := func(cmd string) tracerEvent {
		return tracerEvent{Kind: "tool_call", Tool: &tracerTool{Name: "Bash", Command: cmd}}
	}
	if !isWaitCall(call("cd ~/workspace && cs-campaign-member wait 2>&1 | tail -20")) {
		t.Error("a wait call was not recognised")
	}
	if isWaitCall(call("cs-campaign-member send dev --file /tmp/d.md # then cs-campaign-member wait")) {
		t.Error("a send whose text mentions waiting was taken for a wait")
	}
	if isWaitCall(call("ls")) {
		t.Error("a plain command was taken for a wait")
	}
}
