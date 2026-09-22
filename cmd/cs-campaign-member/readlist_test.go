package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

// memberHome runs every command sent to the agent in a real shell whose HOME is
// a temporary directory, so the listing script is exercised as a member would
// run it, find and all.
func memberHome(t *testing.T, files map[string]string) *envState {
	t.Helper()
	home := t.TempDir()
	for path, body := range files {
		full := filepath.Join(home, protocol.OutputDir, path)
		mustDo(t, os.MkdirAll(filepath.Dir(full), 0o700))
		mustDo(t, os.WriteFile(full, []byte(body), 0o600))
	}
	orig := sshOut
	t.Cleanup(func() { sshOut = orig })
	sshOut = func(host, command, payload string) ([]byte, error) {
		cmd := exec.Command("sh", "-c", command)
		cmd.Env = append(os.Environ(), "HOME="+home)
		return cmd.Output()
	}
	return &envState{
		Home:   t.TempDir(),
		Member: protocol.Member{Role: "orchestrator"},
		Manifest: &protocol.Manifest{Agents: map[string]protocol.AgentRecord{
			"ux": {CLI: "codex", Sandbox: "ux-box", Session: "sess-ux"},
		}},
	}
}

// The orchestrator is the only route between seats, and it could read a file
// in a seat's output channel only by a name it already knew. Seen live: a
// 99-line pack specification reached the seat that needed it only because the
// orchestrator pasted it into a dispatch, and in an earlier campaign five
// guessed names all missed. A listing names every file, as a path read takes.
func TestReadListNamesEveryFileAsAPathReadTakes(t *testing.T) {
	env := memberHome(t, map[string]string{
		"pack-spec.md":      strings.Repeat("a line of the specification\n", 99),
		"replies/d001.json": `{"dispatch":"d001"}`,
		"notes/risks.md":    "none yet\n",
		"odd name.txt":      "x",
	})

	out, err := captureStdout(t, func() error { return cmdRead(env, []string{"ux", "--list"}) })
	if err != nil {
		t.Fatalf("read --list: %v\n%s", err, out)
	}
	for _, want := range []string{
		"ux: 4 files in ~/" + protocol.OutputDir,
		"2772  pack-spec.md",
		"replies/d001.json",
		"notes/risks.md",
		"odd name.txt  (read cannot fetch this name)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing must carry %q:\n%s", want, out)
		}
	}

	got, err := captureStdout(t, func() error { return cmdRead(env, []string{"ux", "pack-spec.md"}) })
	if err != nil || !strings.HasPrefix(got, "a line of the specification\n") {
		t.Fatalf("a listed path must be one read fetches: %v\n%s", err, got)
	}

	sub, err := captureStdout(t, func() error { return cmdRead(env, []string{"ux", "--list", "notes/"}) })
	if err != nil || !strings.Contains(sub, "ux: 1 file in ~/"+protocol.OutputDir+"/notes") || !strings.Contains(sub, "  notes/risks.md") {
		t.Fatalf("a directory listing must print paths from the channel's root: %v\n%s", err, sub)
	}
}

// A listing says what it looked at, so an absent directory, what a directory
// holds and a refused name each read differently.
func TestReadListSaysWhatItFound(t *testing.T) {
	env := memberHome(t, nil)
	if _, err := captureStdout(t, func() error { return cmdRead(env, []string{"ux", "--list"}) }); err == nil ||
		!strings.Contains(err.Error(), "ux has no directory ~/"+protocol.OutputDir) {
		t.Errorf("a missing output channel must be named: %v", err)
	}

	env = memberHome(t, map[string]string{"notes/.keep": ""})
	out, err := captureStdout(t, func() error { return cmdRead(env, []string{"ux", "--list", "notes"}) })
	if err != nil || !strings.Contains(out, "ux: 1 file in") {
		t.Errorf("a listing must count what it found: %v\n%s", err, out)
	}
	if _, err := captureStdout(t, func() error { return cmdRead(env, []string{"ux", "--list", "../input"}) }); err == nil ||
		!strings.Contains(err.Error(), "must stay inside the output channel") {
		t.Errorf("a directory outside the channel must be refused: %v", err)
	}
}
