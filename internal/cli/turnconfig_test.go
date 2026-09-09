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
	for _, cli := range []string{"claude", "codex"} {
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
