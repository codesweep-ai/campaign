package cli

import (
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

// opencode keeps its sessions in SQLite, so there is no transcript to read. Its
// log names the provider and the model of every stream, which is the evidence a
// declaration is checked against, and this reads it out of there.
//
// The lines below are copied from a member's own log, including the title
// subagent that answers on a smaller model. Both are reported: the declaration
// has to be PRESENT among what answered, not alone, or a campaign whose summariser
// uses a different model would fail a readback it should pass.
func TestTurnConfigReadsTheOpencodeLog(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "share", "opencode", "log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		`timestamp=2026-09-17T01:29:24.144Z level=INFO run=9fdcbe7c message=stream providerID=fireworks-ai modelID=accounts/fireworks/models/kimi-k2p7-code session.id=ses_a small=true agent=title mode=primary`,
		`timestamp=2026-09-17T01:29:25.381Z level=INFO run=9fdcbe7c message=stream providerID=fireworks-ai modelID=accounts/fireworks/models/glm-5p2 session.id=ses_a small=false agent=build mode=primary`,
		`timestamp=2026-09-17T01:29:26.002Z level=INFO run=9fdcbe7c message=loop session.id=ses_a step=3`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "opencode.log"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

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
	// A line naming neither is not a model.
	if strings.Contains(got, "model=/") {
		t.Errorf("a line with no provider or model was reported as one:\n%s", got)
	}
}

// An opencode member with no log is not a member answering on no model.
func TestTurnConfigOnAnOpencodeHomeWithNoLog(t *testing.T) {
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

// opencode records a reasoning count rather than an effort label, so its effort
// cannot be confirmed. Checking a declared effort against nothing would fail a
// campaign that is correctly configured.
func TestOpencodeEffortIsNotClaimedReadable(t *testing.T) {
	if !turnConfigReadable("opencode") {
		t.Error("opencode names its model in its log, so the model is readable")
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
