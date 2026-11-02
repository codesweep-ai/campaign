package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The reader must look at every transcript a member holds, not the newest one.
//
// This failed a correctly configured campaign in CI. The member had two session
// files three seconds apart; the check read the newer one, which had not named
// its model yet, and reported a member answering on no model at all. The turn
// it was confirming sat complete in the older file.
func TestTurnConfigReadsEveryTranscript(t *testing.T) {
	for _, cli := range []string{"claude", "codex", "opencode"} {
		if got := turnConfigCommand(cli); strings.Contains(got, "head -1") {
			t.Errorf("%s: the reader takes only the newest transcript:\n%s", cli, got)
		}
	}

	// And it finds a model that only the OLDER file names, which is the case
	// that failed. Run for real, because what broke was the shell rather than
	// anything Go can see.
	home := t.TempDir()
	dir := filepath.Join(home, ".cs-claude", "projects", "-home-runner")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("older.jsonl", `{"message":{"model":"claude-sonnet-5"},"effort":"high"}`+"\n")
	write("newer.jsonl", `{"message":{"role":"user"}}`+"\n") // a session that has not named its model

	cmd := exec.Command("sh", "-c", turnConfigCommand("claude"))
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run the reader: %v", err)
	}
	if !strings.Contains(string(out), "model=claude-sonnet-5") {
		t.Errorf("the model named only in the older transcript was not found:\n%s", out)
	}
}

// An absent transcript is not a member answering on no model: the reader must
// come back empty rather than fail, and say nothing it cannot support.
func TestTurnConfigOnAHomeWithNoTranscripts(t *testing.T) {
	cmd := exec.Command("sh", "-c", turnConfigCommand("claude"))
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("an empty home must not fail the reader: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("an empty home named something: %q", out)
	}
}

// writeOpenCodeStore writes the session store an OpenCode 2 profile keeps, at
// ~/.cs-opencode/opencode.db, holding the named sessions and one message per
// entry of messages, a type and the message's data, all in the first session.
// The columns are OpenCode 2.2.5's own, cut to the ones cs-campaign reads.
func writeOpenCodeStore(t *testing.T, home string, sessions []string, messages [][2]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".cs-opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	rows, err := json.Marshal(map[string]any{"sessions": sessions, "messages": messages})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "-c", `
import json, sqlite3, sys
rows = json.loads(sys.argv[2])
db = sqlite3.connect(sys.argv[1])
db.execute("CREATE TABLE session_v2 (id TEXT PRIMARY KEY, parent_id TEXT)")
db.execute("CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT, type TEXT, seq INTEGER, data TEXT)")
db.executemany("INSERT INTO session_v2 (id) VALUES (?)", [(s,) for s in rows["sessions"]])
db.executemany("INSERT INTO session_message VALUES (?, ?, ?, ?, ?)",
    [("msg_%d" % i, rows["sessions"][0], m[0], i, m[1]) for i, m in enumerate(rows["messages"] or [])])
db.commit()
`, filepath.Join(home, opencodeDB), string(rows))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write the opencode store: %v\n%s", err, out)
	}
}

// opencode keeps its sessions in SQLite, so there is no transcript to read.
// Each assistant message there names the provider and the model it answered
// on, which is the evidence a declaration is checked against, and this reads it
// out of there.
//
// The data below has OpenCode 2.2.5's shape, including a sub-agent answering on
// a smaller model. Both are reported: the declaration has to be PRESENT among
// what answered, not alone, or a campaign whose sub-agents use a different model
// would fail a readback it should pass.
func TestTurnConfigReadsTheOpencodeStore(t *testing.T) {
	home := t.TempDir()
	writeOpenCodeStore(t, home, []string{"ses_a"}, [][2]string{
		{"user", `{"text":"read the briefing"}`},
		{"assistant", `{"agent":"explore","model":{"id":"accounts/fireworks/models/kimi-k2p7-code","providerID":"fireworks-ai"},"content":[]}`},
		{"assistant", `{"agent":"build","model":{"id":"accounts/fireworks/models/glm-5p2","providerID":"fireworks-ai"},"content":[]}`},
		{"assistant", `{"agent":"build","content":[]}`},
		{"idle", `{"outcome":"completed"}`},
	})

	cmd := exec.Command("sh", "-c", turnConfigCommand("opencode"))
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run the reader: %v", err)
	}
	got := string(out)
	for _, want := range []string{
		"model=fireworks-ai/accounts/fireworks/models/kimi-k2p7-code",
		"model=fireworks-ai/accounts/fireworks/models/glm-5p2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the reader did not report %q:\n%s", want, got)
		}
	}
	// A message naming no model is not a model.
	if strings.Contains(got, "model=/") {
		t.Errorf("a message with no provider or model was reported as one:\n%s", got)
	}
}

// An opencode member with no store is not a member answering on no model.
func TestTurnConfigOnAnOpencodeHomeWithNoStore(t *testing.T) {
	cmd := exec.Command("sh", "-c", turnConfigCommand("opencode"))
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("an empty home must not fail the reader: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("an empty home named something: %q", out)
	}
}

// opencode records the model of each message and no effort, so its effort
// cannot be confirmed. Checking a declared effort against nothing would fail a
// campaign that is correctly configured.
func TestOpencodeEffortIsNotClaimedReadable(t *testing.T) {
	if !turnConfigReadable("opencode") {
		t.Error("opencode names its model in its session store, so the model is readable")
	}
	if turnEffortReadable("opencode") {
		t.Error("opencode names no effort label, so effort must not be claimed readable")
	}
	for _, cli := range []string{"claude", "codex"} {
		if !turnEffortReadable(cli) {
			t.Errorf("%s names its effort and must stay readable", cli)
		}
	}
}
