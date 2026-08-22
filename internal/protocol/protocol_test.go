package protocol

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseMsgName(t *testing.T) {
	cases := []struct {
		name    string
		ok      bool
		id      string
		seq     int
		restart bool
	}{
		{"d001.md", true, "d001", 0, false},
		{"d001.001.md", true, "d001", 1, false},
		{"d001.002.restart.md", true, "d001", 2, true},
		{"m1.md", true, "m1", 0, false},
		{"m1.003.md", true, "m1", 3, false},
		{"ORIENTATION.md", false, "", 0, false},
		{"mission.md", false, "", 0, false},
		{"d1.md", false, "", 0, false},     // unpadded is not a dispatch
		{"d0001.md", false, "", 0, false},  // four digits never minted
		{"d001.1.md", false, "", 0, false}, // unpadded continuation
		{"d001.001.md.bak", false, "", 0, false},
	}
	for _, c := range cases {
		m, ok := ParseMsgName(c.name, 7)
		if ok != c.ok {
			t.Fatalf("%s: ok=%v want %v", c.name, ok, c.ok)
		}
		if ok && (m.ID != c.id || m.Seq != c.seq || m.Restart != c.restart) {
			t.Fatalf("%s: got %+v", c.name, m)
		}
	}
}

func msgs(specs ...string) []Msg {
	// spec: "name@mtime"
	var out []Msg
	for _, s := range specs {
		parts := strings.Split(s, "@")
		var mt int64
		for _, ch := range parts[1] {
			mt = mt*10 + int64(ch-'0')
		}
		m, ok := ParseMsgName(parts[0], mt)
		if !ok {
			panic("bad test msg " + s)
		}
		out = append(out, m)
	}
	return out
}

func TestCurrentDispatch(t *testing.T) {
	if Current(nil) != nil {
		t.Fatal("no messages must mean no dispatch")
	}
	d := Current(msgs("d001.md@100", "d001.001.md@200", "d001.002.restart.md@300", "d002.md@400"))
	if d.ID != "d002" || d.OpenedAt != 400 {
		t.Fatalf("newest dispatch wins: got %+v", d)
	}
	d = Current(msgs("d001.md@100", "d001.001.md@200", "d001.002.restart.md@300"))
	if d.ID != "d001" || d.Continues != 1 || d.Restarts != 1 || d.OpenedAt != 100 || d.NewestMsg != 300 {
		t.Fatalf("counts derive from the listing: got %+v", d)
	}
}

func TestMinting(t *testing.T) {
	id, err := NextDispatchID(nil)
	if err != nil || id != "d001" {
		t.Fatalf("first mint: %v %v", id, err)
	}
	id, err = NextDispatchID(msgs("d001.md@1", "d007.md@2", "m1.md@3"))
	if err != nil || id != "d008" {
		t.Fatalf("mint after d007: %v %v", id, err)
	}
	if _, err = NextDispatchID(msgs("d999.md@1")); err == nil {
		t.Fatal("mint past d999 must refuse")
	}
	if n := NextMsgName(nil, "d001", false); n != "d001.md" {
		t.Fatalf("opener: %s", n)
	}
	ms := msgs("d001.md@1", "d001.001.md@2")
	if n := NextMsgName(ms, "d001", false); n != "d001.002.md" {
		t.Fatalf("continuation: %s", n)
	}
	if n := NextMsgName(ms, "d001", true); n != "d001.002.restart.md" {
		t.Fatalf("restart marker: %s", n)
	}
}

func TestProbeScriptAndParse(t *testing.T) {
	s := ProbeScript("codex")
	if !strings.Contains(s, "cs-[c]odex-turn") {
		t.Fatalf("probe must not match its own command line: %s", s)
	}
	f := ParseProbe("garbage\nMSG 100 d001.md\nMSG 200 d001.001.md\nMSG 5 ORIENTATION.md\nREPLY d001\nDRIVERS 1\n")
	if len(f.Msgs) != 2 || !f.Replies["d001"] || f.Drivers != 1 {
		t.Fatalf("parse: %+v", f)
	}
	f = ParseProbe("NOCHANNELS\n")
	if len(f.Msgs) != 0 || f.Drivers != 0 {
		t.Fatalf("nochannels: %+v", f)
	}
}

// TestComputeTable walks every row of the state computation, in the order the
// spec fixes: reachability, then reply, then liveness, then the ladder.
func TestComputeTable(t *testing.T) {
	pol := Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 1000, BlindProbes: 3, SettlingSeconds: 100}
	acc := map[string]bool{}
	now := int64(10000)
	open := msgs("d001.md@9000") // opened 1000s ago... within elapsed (just)

	// probe failures
	if o := Compute(Facts{}, true, 1, acc, pol, now); o.State != StateUnreachable {
		t.Fatalf("one failed probe is an overlay: %+v", o)
	} else if !strings.Contains(o.Detail, "3 consecutive required to conclude") {
		t.Fatalf("below the threshold the detail must never read as a verdict: %q", o.Detail)
	}
	if o := Compute(Facts{}, true, 3, acc, pol, now); o.State != StateStuck {
		t.Fatalf("a run of failed probes is a conclusion: %+v", o)
	}
	// no dispatch
	if o := Compute(Facts{}, false, 0, acc, pol, now); o.State != StateFree {
		t.Fatalf("free: %+v", o)
	}
	// reply precedes liveness: replied-then-exited is replied, not stopped
	f := Facts{Msgs: open, Replies: map[string]bool{"d001": true}}
	if o := Compute(f, false, 0, acc, pol, now); o.State != StateReplied {
		t.Fatalf("reply check precedes liveness: %+v", o)
	}
	// accepted -> free (the one off-node input)
	if o := Compute(f, false, 0, map[string]bool{"d001": true}, pol, now); o.State != StateFree {
		t.Fatalf("accepted reads free: %+v", o)
	}
	// active
	f = Facts{Msgs: open, Replies: map[string]bool{}, Drivers: 1}
	if o := Compute(f, false, 0, acc, pol, now); o.State != StateWorking {
		t.Fatalf("working: %+v", o)
	}
	// settling: newest message young, no driver yet -> still working
	young := msgs("d001.md@9950")
	f = Facts{Msgs: young, Replies: map[string]bool{}}
	if o := Compute(f, false, 0, acc, pol, now); o.State != StateWorking {
		t.Fatalf("settling window reads working: %+v", o)
	}
	// settling re-arms on every send: old-but-in-bounds dispatch, young continuation
	rearmed := msgs("d001.md@9200", "d001.001.md@9950")
	f = Facts{Msgs: rearmed, Replies: map[string]bool{}}
	if o := Compute(f, false, 0, acc, pol, now); o.State != StateWorking {
		t.Fatalf("settling must key on newest message, not dispatch age: %+v", o)
	}
	// stopped, continue next
	stale := msgs("d001.md@9500")
	f = Facts{Msgs: stale, Replies: map[string]bool{}}
	if o := Compute(f, false, 0, acc, pol, now); o.State != StateStopped || o.NextMove != "continue" {
		t.Fatalf("stopped/continue: %+v", o)
	}
	// stopped, restart next once continues are spent
	spent := msgs("d001.md@9500", "d001.001.md@9600", "d001.002.md@9700")
	f = Facts{Msgs: spent, Replies: map[string]bool{}}
	if o := Compute(f, false, 0, acc, pol, now); o.State != StateStopped || o.NextMove != "restart" {
		t.Fatalf("stopped/restart: %+v", o)
	}
	// A fresh restart re-anchor is a SEND: inside the settling window the node
	// reads node-working even though the ladder counts are spent — otherwise
	// the restart rung is dead on arrival, stuck before its turn can boot
	// (adversarial review, finding 1; the old assertion here blessed the bug).
	restarted := msgs("d001.md@9500", "d001.001.md@9600", "d001.002.md@9700", "d001.003.restart.md@9990")
	f = Facts{Msgs: restarted, Replies: map[string]bool{}}
	if o := Compute(f, false, 0, acc, pol, now); o.State != StateWorking {
		t.Fatalf("a restart inside its settling window must read working: %+v", o)
	}
	// Only once the re-anchor has aged past settling with no driver and no
	// reply is the ladder genuinely exhausted.
	dead := msgs("d001.md@9500", "d001.001.md@9600", "d001.002.md@9700", "d001.003.restart.md@9850")
	f = Facts{Msgs: dead, Replies: map[string]bool{}}
	if o := Compute(f, false, 0, acc, pol, now); o.State != StateStuck {
		t.Fatalf("exhausted ladder past settling is stuck: %+v", o)
	}
	// elapsed bound from dispatch open, not reset by continuation
	ancient := msgs("d001.md@8000", "d001.001.md@9950")
	pol2 := pol
	pol2.ElapsedSeconds = 1500
	f = Facts{Msgs: ancient, Replies: map[string]bool{}}
	if o := Compute(f, false, 0, acc, pol2, now); o.State != StateStuck {
		t.Fatalf("elapsed runs from dispatch open: %+v", o)
	}
}

func TestValidateMissionReply(t *testing.T) {
	if err := ValidateMissionReply(Reply{Outcome: "met"}); err == nil {
		t.Fatal("bare 'met' is not in the vocabulary")
	}
	if err := ValidateMissionReply(Reply{Outcome: "campaign-met"}); err != nil {
		t.Fatalf("met with nothing unmet: %v", err)
	}
	if err := ValidateMissionReply(Reply{Outcome: "campaign-met", Unmet: []string{"auth"}}); err == nil {
		t.Fatal("met naming something unmet must refuse")
	}
	if err := ValidateMissionReply(Reply{Outcome: "campaign-blocked"}); err == nil {
		t.Fatal("blocked with an empty unmet list must refuse: the list is the payload")
	}
	if err := ValidateMissionReply(Reply{Outcome: "campaign-blocked", Unmet: []string{"upstream unreachable"}}); err != nil {
		t.Fatalf("blocked with unmet: %v", err)
	}
}

func TestReplyAndLogRoundTrip(t *testing.T) {
	home := t.TempDir()
	r := Reply{Dispatch: "d003", Phase: "done", Note: "built it", At: time.Unix(1000, 0).UTC(),
		Repos: []RepoState{{Repo: "task-tracker", Head: "abc", TreeDiffersFromBase: true}}}
	if err := WriteReplyLocal(home, r); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ReplyPath("d003")))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseReply(b)
	if err != nil || got.Dispatch != "d003" || !got.Repos[0].TreeDiffersFromBase {
		t.Fatalf("round trip: %+v %v", got, err)
	}

	if err := AppendLogLocal(home, Entry{At: time.Now(), Kind: "plan", Text: "auth first"}); err != nil {
		t.Fatal(err)
	}
	if err := AppendLogLocal(home, Entry{At: time.Now(), Kind: "accepted", Text: AcceptanceText("node-a", "d003")}); err != nil {
		t.Fatal(err)
	}
	if err := AppendLogLocal(home, Entry{At: time.Now(), Kind: "status", Text: "nope"}); err == nil {
		t.Fatal("unknown log kind must refuse")
	}
	lb, _ := os.ReadFile(filepath.Join(home, LogFile))
	entries := ParseLog(append(lb, []byte("{torn")...))
	if len(entries) != 2 || !AcceptedFor(entries, "node-a")["d003"] || AcceptedFor(entries, "node-b")["d003"] {
		t.Fatalf("log parse: %+v", entries)
	}
}

func TestPutFileScriptIsBase64(t *testing.T) {
	for _, s := range []string{
		PutFileScript(InputDir+"/d001.md", "run `rm -rf` $(boom)\n"),
		PutMsgScript(InputDir+"/d001.md", "run `rm -rf` $(boom)\n"),
	} {
		if strings.Contains(s, "rm -rf") || strings.Contains(s, "$(boom)") {
			t.Fatal("payload must never cross as shell-visible text")
		}
		if !strings.Contains(s, "base64 -d") {
			t.Fatalf("expected base64 delivery: %s", s)
		}
	}
}

// TestPutMsgScriptRefusesClobber runs the message-delivery script under a
// real shell: the first write claims the name, the second — a racing send
// that lost the ID-mint TOCTOU — must fail with the collision marker and
// leave the winner's bytes untouched.
func TestPutMsgScriptRefusesClobber(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, InputDir, "d002.md")
	run := func(content string) ([]byte, error) {
		cmd := exec.Command("sh", "-c", PutMsgScript(InputDir+"/d002.md", content))
		cmd.Env = append(os.Environ(), "HOME="+home)
		return cmd.CombinedOutput()
	}
	out, err := run("the winner's message\n")
	if err != nil {
		t.Fatalf("first delivery must succeed: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(target); string(b) != "the winner's message\n" {
		t.Fatalf("delivered content: %q", b)
	}
	out, err = run("the loser's message\n")
	if err == nil {
		t.Fatal("a second delivery to the same name must fail")
	}
	if !IsDeliveryCollision(out) {
		t.Fatalf("the failure must carry the collision marker:\n%s", out)
	}
	if b, _ := os.ReadFile(target); string(b) != "the winner's message\n" {
		t.Fatalf("the winner's message must survive intact: %q", b)
	}
	if IsDeliveryCollision([]byte("ssh: connect to host dev-box: no route")) {
		t.Fatal("an ordinary transport failure must not read as a collision")
	}
}

// TestAcceptanceIsPerNode pins the collision the adversarial review found:
// dispatch IDs are per-node sequences, so accepting backend/d002 must not
// free a different node's unjudged d002.
func TestAcceptanceIsPerNode(t *testing.T) {
	entries := []Entry{
		{Kind: "accepted", Text: "backend/d002"},
		{Kind: "accepted", Text: "d001"}, // bare legacy text matches no node
	}
	if !AcceptedFor(entries, "backend")["d002"] {
		t.Fatal("backend's own acceptance must count")
	}
	front := AcceptedFor(entries, "frontend")
	if front["d002"] || front["d001"] {
		t.Fatalf("another node's acceptance leaked: %v", front)
	}
	pol := Policy{ContinueAttempts: 2, Restarts: 1, ElapsedSeconds: 1000, BlindProbes: 3, SettlingSeconds: 100}
	f := Facts{Msgs: msgs("d002.md@9000"), Replies: map[string]bool{"d002": true}}
	if o := Compute(f, false, 0, front, pol, 10000); o.State != StateReplied {
		t.Fatalf("frontend's reply must surface for judgment, not read pre-accepted: %+v", o)
	}
	if o := Compute(f, false, 0, AcceptedFor(entries, "backend"), pol, 10000); o.State != StateFree {
		t.Fatalf("backend's accepted reply reads free: %+v", o)
	}
	if got := AcceptanceText("backend", "d002"); got != "backend/d002" {
		t.Fatalf("acceptance text: %s", got)
	}
}
