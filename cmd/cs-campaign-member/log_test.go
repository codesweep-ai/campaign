package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/protocol"
)

// writeLog puts a log on the orchestrator's machine as the given lines.
func writeLog(t *testing.T, home string, lines ...string) string {
	t.Helper()
	path := filepath.Join(home, protocol.LogFile)
	mustDo(t, os.MkdirAll(filepath.Dir(path), 0o700))
	mustDo(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return path
}

// TestLogPrintsTheEntriesAndCountsWhatTheToolDidNotWrite: `log` is how the
// orchestrator reads its log, so it must show every entry the tool wrote, a
// delivery's member and commit included. A line written some other way, such
// as run19's hand-written `{"at":"…06:10Z","event":…}`, is not an entry: it is
// counted, so the reader learns it is there without taking it for a record.
func TestLogPrintsTheEntriesAndCountsWhatTheToolDidNotWrite(t *testing.T) {
	home, orch := fakeHome(t, "orchestrator", protocol.MissionID+".md")
	writeLog(t, home,
		`{"at":"2026-11-03T06:12:48Z","kind":"plan","text":"modeller takes pre-register\nthen close"}`,
		`{"at":"2026-11-03T06:13:10Z","kind":"accepted","text":"surveyor-domain/d001"}`,
		`{"at":"2026-11-03T06:14:00Z","kind":"delivered","text":"delivered","member":"modeller","repo":"product","commit":"ff1d9e0"}`,
		`{"at":"2026-11-03T06:10Z","event":"accepted","what":"written by hand"}`,
	)
	out, err := captureStdout(t, func() error { return cmdLog(orch, nil) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"2026-11-03T06:12:48Z  plan       modeller takes pre-register\n" + strings.Repeat(" ", 33) + "then close\n",
		"2026-11-03T06:13:10Z  accepted   surveyor-domain/d001\n",
		"delivered  modeller product@ff1d9e0: delivered\n",
		"1 line(s) in the log were not written by cs-campaign-member and are ignored\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "written by hand") {
		t.Errorf("a line the tool did not write is shown as an entry:\n%s", out)
	}
}

func TestLogSaysAnEmptyLogIsEmpty(t *testing.T) {
	_, orch := fakeHome(t, "orchestrator", protocol.MissionID+".md")
	out, err := captureStdout(t, func() error { return cmdLog(orch, nil) })
	if err != nil || out != "your log is empty\n" {
		t.Fatalf("an absent log: %q, %v", out, err)
	}
}

// TestNoteRefusesTheLogAsItsBody: run19's orchestrator kept a copy of its log
// by hand, then noted the log itself, which made every line of it the text of
// one assessment. A note is written into the log, so the log is never its
// input, however it is named.
func TestNoteRefusesTheLogAsItsBody(t *testing.T) {
	home, orch := fakeHome(t, "orchestrator", protocol.MissionID+".md")
	path := writeLog(t, home, `{"at":"2026-11-03T06:13:10Z","kind":"accepted","text":"surveyor-domain/d001"}`)
	link := filepath.Join(t.TempDir(), "mylog.jsonl")
	mustDo(t, os.Symlink(path, link))
	for _, name := range []string{path, link} {
		err := cmdNote(orch, []string{"assessment", "--file", name})
		if err == nil || !strings.Contains(err.Error(), "names your log") {
			t.Errorf("note --file %s: %v, want a refusal that names the log", name, err)
		}
	}
	if b, _ := os.ReadFile(path); strings.Count(string(b), "\n") != 1 {
		t.Errorf("a refused note still wrote to the log:\n%s", b)
	}
	if _, err := captureStdout(t, func() error {
		return cmdNote(orch, []string{"assessment", "--file", writeTmp(t, "the modeller is on d003")})
	}); err != nil {
		t.Fatalf("a note from a file of its own must pass: %v", err)
	}
}
