package frames

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// write creates path with body and the given mtime — event times in these
// fixtures are mtimes, exactly as the archive extractor preserves them.
func write(t *testing.T, path, body string, at time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, clobbered bool) string {
	t.Helper()
	root := t.TempDir()
	base := time.Date(2026, 8, 18, 7, 0, 0, 0, time.UTC)
	at := func(min int) time.Time {
		if clobbered {
			return base.Add(2 * time.Hour) // one collection moment for every file
		}
		return base.Add(time.Duration(min) * time.Minute)
	}
	write(t, filepath.Join(root, "campaign.json"), `{
	  "name":"fix1","id":"fix1-1234","createdAt":"2026-08-18T07:00:00Z","updatedAt":"2026-08-18T08:00:00Z",
	  "policy":{"continueAttempts":2,"restarts":1},
	  "members":[
	    {"name":"dev","role":"agent","cli":"opencode"},
	    {"name":"orchestrator","role":"orchestrator","cli":"codex"}
	  ]}`, at(0))
	o := filepath.Join(root, "orchestrator")
	d := filepath.Join(root, "agents", "dev")
	write(t, filepath.Join(o, "input", "d001.md"), "# readback", at(1))
	write(t, filepath.Join(o, "input", "m1.md"), "# mission", at(2))
	write(t, filepath.Join(o, "output", "replies", "d001.json"),
		`{"dispatch":"d001","phase":"done","note":"rb","at":"2026-08-18T07:03:00Z"}`, at(3))
	// The log exercises the whole sweep: a bare d001 accept (ambiguous across
	// nodes, readback authority, and earlier than both d001 replies), a clean
	// accept, an unknown ID, a duplicate accept, and an own-channel accept.
	write(t, filepath.Join(o, "output", "log.jsonl"),
		`{"at":"2026-08-18T07:05:00Z","kind":"plan","text":"# plan"}
{"at":"2026-08-18T07:02:00Z","kind":"accepted","text":"d001"}
{"at":"2026-08-18T07:30:00Z","kind":"accepted","text":"dev/d002"}
{"at":"2026-08-18T07:31:00Z","kind":"accepted","text":"d999"}
{"at":"2026-08-18T07:32:00Z","kind":"accepted","text":"dev/d002"}
{"at":"2026-08-18T07:41:00Z","kind":"accepted","text":"orchestrator/m1"}
`, at(31))
	write(t, filepath.Join(o, "output", "replies", "m1.json"),
		`{"dispatch":"m1","phase":"done","outcome":"campaign-met","note":"met","at":"2026-08-18T07:40:00Z"}`, at(40))
	write(t, filepath.Join(d, "input", "d001.md"), "# readback", at(1))
	write(t, filepath.Join(d, "output", "replies", "d001.json"),
		`{"dispatch":"d001","phase":"done","note":"rb","at":"2026-08-18T07:04:00Z"}`, at(4))
	write(t, filepath.Join(d, "input", "d002.md"), "# work", at(10))
	// The continuation lands in the same second as d002's reply: an
	// undecidable order the sweep must report.
	write(t, filepath.Join(d, "input", "d002.001.md"), "continue", at(25))
	write(t, filepath.Join(d, "output", "replies", "d002.json"),
		`{"dispatch":"d002","phase":"done","note":"did it","at":"2026-08-18T07:25:00Z"}`, at(25))
	// Never answered, and its ladder overspent the policy — the counts must
	// be reported even though no reply gates them.
	write(t, filepath.Join(d, "input", "d003.md"), "# more work, never answered", at(35))
	write(t, filepath.Join(d, "input", "d003.001.md"), "c1", at(36))
	write(t, filepath.Join(d, "input", "d003.002.md"), "c2", at(37))
	write(t, filepath.Join(d, "input", "d003.003.md"), "c3", at(38))
	return root
}

// A failed create's shape: no mission, no log, a second readback minted as
// d002 by the resumed create. Nothing here owes the orchestrator an
// acceptance — the whole record is create-phase, host-issued and host-judged.
func TestCreatePhaseNoMission(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }
	write(t, filepath.Join(root, "campaign.json"), `{
	  "name":"cf1","id":"cf1-1","createdAt":"2026-08-18T09:00:00Z","updatedAt":"2026-08-18T09:30:00Z",
	  "members":[{"name":"orchestrator","role":"orchestrator","cli":"claude"},{"name":"dev","role":"agent","cli":"opencode"}]}`, at(0))
	write(t, filepath.Join(root, "orchestrator", "input", "d001.md"), "# readback", at(1))
	write(t, filepath.Join(root, "agents", "dev", "input", "d001.md"), "# readback", at(1))
	write(t, filepath.Join(root, "agents", "dev", "output", "replies", "d001.json"),
		`{"dispatch":"d001","phase":"done","note":"rb","at":"2026-08-18T09:03:00Z"}`, at(3))
	write(t, filepath.Join(root, "agents", "dev", "input", "d002.md"), "# readback again", at(10))
	write(t, filepath.Join(root, "agents", "dev", "output", "replies", "d002.json"),
		`{"dispatch":"d002","phase":"done","note":"rb2","at":"2026-08-18T09:12:00Z"}`, at(12))
	run, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]int{}
	for _, i := range run.Issues {
		codes[i.Code]++
	}
	if codes["reply-not-accepted"] != 0 {
		t.Errorf("create-phase readbacks owe no acceptance: %+v", run.Issues)
	}
	for _, want := range []string{"no-verdict", "log-missing", "no-reply"} {
		if codes[want] == 0 {
			t.Errorf("missing expected %q in %v", want, codes)
		}
	}
}

func TestLoadHealthyRun(t *testing.T) {
	run, err := Load(fixture(t, false))
	if err != nil {
		t.Fatal(err)
	}
	if !run.TimelineValid {
		t.Fatal("timeline should be valid for spread mtimes")
	}
	if run.Campaign.Outcome != "campaign-met" {
		t.Fatalf("outcome %q", run.Campaign.Outcome)
	}
	if run.Nodes[0].Role != "orchestrator" {
		t.Fatalf("orchestrator must lead the lanes, got %q", run.Nodes[0].Name)
	}
	codes := map[string]int{}
	for _, i := range run.Issues {
		codes[i.Code]++
	}
	for _, want := range []string{"accept-unknown", "no-reply", "accept-ambiguous",
		"accept-of-readback", "accept-of-own-channel", "accepted-twice",
		"accept-before-reply", "ambiguous-order", "continues-exceed-policy"} {
		if codes[want] == 0 {
			t.Errorf("missing expected issue %q in %v", want, codes)
		}
	}
	if codes["reply-not-accepted"] != 0 {
		t.Errorf("d002 is accepted; unexpected reply-not-accepted: %v", run.Issues)
	}
	var d002 *Span
	for i := range run.Spans {
		if run.Spans[i].ID == "d002" {
			d002 = &run.Spans[i]
		}
	}
	if d002 == nil || d002.Continues != 1 || d002.AcceptedAt == "" {
		t.Fatalf("d002 span wrong: %+v", d002)
	}
	// Events must arrive time-sorted for the shell's event-indexed columns.
	for i := 1; i < len(run.Events); i++ {
		if run.Events[i].At < run.Events[i-1].At {
			t.Fatalf("events not sorted at %d: %v", i, run.Events[i])
		}
	}
}

func TestReplyNameMismatch(t *testing.T) {
	root := fixture(t, false)
	// A reply whose filename and content disagree: the filename closes d003,
	// the content claims d002. The filename must win, with a finding.
	base := time.Date(2026, 8, 18, 7, 38, 0, 0, time.UTC)
	write(t, filepath.Join(root, "agents", "dev", "output", "replies", "d003.json"),
		`{"dispatch":"d002","phase":"done","note":"mislabeled","at":"2026-08-18T07:38:00Z"}`, base)
	// And an orphan: a reply for a dispatch no message ever opened.
	write(t, filepath.Join(root, "agents", "dev", "output", "replies", "d005.json"),
		`{"dispatch":"d005","phase":"done","note":"orphan","at":"2026-08-18T07:39:00Z"}`, base)
	run, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	found, orphan := false, false
	for _, i := range run.Issues {
		if i.Code == "reply-name-mismatch" && i.Dispatch == "d003" {
			found = true
		}
		if i.Code == "reply-without-dispatch" && i.Dispatch == "d005" {
			orphan = true
		}
		if i.Code == "no-reply" {
			t.Errorf("d003 is closed by filename; no-reply must not fire: %v", i)
		}
	}
	if !found {
		t.Fatalf("reply-name-mismatch finding missing: %+v", run.Issues)
	}
	if !orphan {
		t.Fatalf("reply-without-dispatch finding missing for orphan d005: %+v", run.Issues)
	}
	if run.Replies["dev"]["d003"] == nil {
		t.Fatal("reply must be keyed by its filename id")
	}
}

func TestLoadClobberedRun(t *testing.T) {
	run, err := Load(fixture(t, true))
	if err != nil {
		t.Fatal(err)
	}
	if run.TimelineValid {
		t.Fatal("uniform mtimes must invalidate the timeline")
	}
	found := false
	for _, i := range run.Issues {
		if i.Code == "mtimes-clobbered" {
			found = true
		}
		if i.Code == "reply-before-open" {
			t.Errorf("derived symptom reported alongside root cause: %v", i)
		}
	}
	if !found {
		t.Fatal("mtimes-clobbered finding missing")
	}
}

// resumedFixture is a create that took two attempts. dev's first readback,
// d001, could not be used, so the resumed create asked again in d002, and the
// mission then gave dev its work as d003. Nothing in a dispatch id says d002 is
// a readback: only the mission's opening time, and readback.json's record of
// the dispatch dev's answer closed, can.
func resumedFixture(t *testing.T, clobbered, mission, recorded bool, log string) string {
	t.Helper()
	root := t.TempDir()
	base := time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC)
	at := func(min int) time.Time {
		if clobbered {
			return base.Add(2 * time.Hour)
		}
		return base.Add(time.Duration(min) * time.Minute)
	}
	stamp := func(min int) string { return base.Add(time.Duration(min) * time.Minute).Format(time.RFC3339) }
	reply := func(node, id string, min int, extra string) {
		dir := filepath.Join(root, "agents", node)
		if node == "orchestrator" {
			dir = filepath.Join(root, "orchestrator")
		}
		write(t, filepath.Join(dir, "output", "replies", id+".json"),
			`{"dispatch":"`+id+`","phase":"done","note":"n","at":"`+stamp(min)+`"`+extra+`}`, at(min))
	}
	write(t, filepath.Join(root, "campaign.json"), `{
	  "name":"rs1","id":"rs1-1","createdAt":"2026-09-22T14:00:00Z","updatedAt":"2026-09-22T15:00:00Z",
	  "policy":{"continueAttempts":2,"restarts":1},
	  "members":[{"name":"orchestrator","role":"orchestrator","cli":"claude"},{"name":"dev","role":"agent","cli":"codex"}]}`, at(0))
	o := filepath.Join(root, "orchestrator")
	d := filepath.Join(root, "agents", "dev")
	write(t, filepath.Join(o, "input", "d001.md"), "# readback", at(1))
	reply("orchestrator", "d001", 2, "")
	write(t, filepath.Join(d, "input", "d001.md"), "# readback", at(1))
	reply("dev", "d001", 2, "")
	write(t, filepath.Join(d, "input", "d002.md"), "# readback, asked again by the resumed create", at(10))
	reply("dev", "d002", 11, "")
	if mission {
		write(t, filepath.Join(o, "input", "m1.md"), "# mission", at(20))
		write(t, filepath.Join(d, "input", "d003.md"), "# work", at(25))
		reply("dev", "d003", 30, "")
		reply("orchestrator", "m1", 40, `,"outcome":"campaign-met"`)
		write(t, filepath.Join(o, "output", "log.jsonl"), log, at(35))
	}
	if recorded {
		write(t, filepath.Join(root, "readback.json"), `{"members":[
		  {"member":"orchestrator","role":"orchestrator","cli":"claude","readback":{"member":"orchestrator","dispatch":"d001"}},
		  {"member":"dev","role":"agent","cli":"codex","readback":{"member":"dev","dispatch":"d002"}}]}`, at(41))
	}
	return root
}

func issuesFor(t *testing.T, root, code string) []Issue {
	t.Helper()
	run, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	var found []Issue
	for _, i := range run.Issues {
		if i.Code == code {
			found = append(found, i)
		}
	}
	return found
}

// An acceptance of a readback has no standing whatever its id. A resumed create
// puts dev's readback at d002, and the viewer only ever recognised d001.
func TestAnAcceptOfALaterReadbackIsReported(t *testing.T) {
	log := `{"at":"2026-09-22T14:32:00Z","kind":"accepted","text":"dev/d002"}
{"at":"2026-09-22T14:33:00Z","kind":"accepted","text":"dev/d003"}
`
	for _, clobbered := range []bool{false, true} {
		found := issuesFor(t, resumedFixture(t, clobbered, true, true, log), "accept-of-readback")
		if len(found) != 1 || found[0].Dispatch != "d002" || !strings.Contains(found[0].Message, "dev/d002") {
			t.Errorf("clobbered=%v: the accept of dev's d002 readback must be reported once, naming it; got %+v", clobbered, found)
		}
	}
}

// A readback owes the orchestrator no acceptance. With real message times the
// mission's opening says which dispatches came before it. With clobbered ones,
// readback.json's record of the dispatch each answer closed still says so.
func TestALaterReadbackOwesNoAcceptance(t *testing.T) {
	log := `{"at":"2026-09-22T14:33:00Z","kind":"accepted","text":"dev/d003"}
`
	for _, clobbered := range []bool{false, true} {
		if found := issuesFor(t, resumedFixture(t, clobbered, true, true, log), "reply-not-accepted"); len(found) != 0 {
			t.Errorf("clobbered=%v: dev's d002 is a readback and owes no acceptance; got %+v", clobbered, found)
		}
	}
	// A create that never opened the mission issued every dispatch in it, so no
	// message time and no readback record is needed to say so.
	if found := issuesFor(t, resumedFixture(t, true, false, false, ""), "reply-not-accepted"); len(found) != 0 {
		t.Errorf("a failed create's readbacks owe no acceptance, even with clobbered times; got %+v", found)
	}
}
