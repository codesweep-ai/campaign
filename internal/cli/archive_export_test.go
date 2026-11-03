package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
)

// An opencode session export that never returns must not cost the run its
// transcript tarball.
//
// The export and the tar share one collection bound and the export runs first,
// so an unbounded one leaves no cli-evidence.tgz at all — and verifyFleetArchive
// reads a missing or empty tarball as "declared opencode but produced no
// transcript; another CLI did the work". That is the audit's most serious
// finding, and a member that was only slow must not be able to earn it.
//
// The stub cs-opencode hangs, which is the shape a saturated guest produces.
// Running the real command through a real sh also checks the quoting: the
// export loop is nested inside `timeout ... sh -c '...'` and a mistake there
// would break every opencode archive rather than only a slow one.
func TestOpencodeExportCannotCostTheTarball(t *testing.T) {
	// The collection runs in a Linux guest. Where there is no GNU timeout,
	// as on macOS, the bounded loop never starts, so there is nothing to test.
	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("timeout is not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	// The evidence that must survive: the raw db the audit reads, holding one
	// session, so the export loop reaches the stub below.
	writeOpenCodeStore(t, home, []string{"ses_one"}, nil)

	// A cs-opencode whose export never returns, on PATH ahead of anything
	// real. It records that it was asked, because a loop that listed no
	// session would pass this test without bounding anything.
	stub := t.TempDir()
	asked := filepath.Join(t.TempDir(), "asked")
	if err := os.WriteFile(filepath.Join(stub, "cs-opencode"),
		[]byte("#!/bin/sh\necho \"$*\" >> "+asked+"\nsleep 600\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))

	// A sandbox that runs the collection command here, the shape memberOutput
	// calls it with: `exec <ref> sh -lc <command>`.
	tool := filepath.Join(t.TempDir(), "fake-sandbox")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n[ \"$1\" = exec ] || exit 1\nexec sh -c \"$5\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &app{sandbox: sandboxCLI{Bin: tool}, exportBound: 300 * time.Millisecond}

	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "transcript"), 0o700); err != nil {
		t.Fatal(err)
	}
	member := model.Member{Name: "dev", Role: "agent", CLI: "opencode", Sandbox: "box"}

	start := time.Now()
	if err := a.archiveTranscripts(t.Context(), base, member); err != nil {
		t.Fatalf("archiveTranscripts: %v", err)
	}
	elapsed := time.Since(start)

	marker := filepath.Join(base, "transcript", "INCOMPLETE.txt")
	if body, err := os.ReadFile(marker); err == nil {
		t.Fatalf("a hung export left the transcript INCOMPLETE rather than collecting the db: %s", body)
	}
	tgz := filepath.Join(base, "transcript", "cli-evidence.tgz")
	info, err := os.Stat(tgz)
	if err != nil {
		t.Fatalf("no cli-evidence.tgz: %v — audit reads that as the wrong CLI doing the work", err)
	}
	if info.Size() == 0 {
		t.Fatal("empty cli-evidence.tgz — audit reads that as the wrong CLI doing the work")
	}
	entries, err := tgzEntryCount(tgz)
	if err != nil || entries == 0 {
		t.Fatalf("tarball carries nothing (%d entries): %v", entries, err)
	}
	// The export is bounded well inside the collection bound; without the
	// bound this would have run for the stub's full 600s.
	if elapsed >= archiveCollectBound {
		t.Fatalf("collection took %s, past its own %s bound", elapsed, archiveCollectBound)
	}
	if got, _ := os.ReadFile(asked); strings.TrimSpace(string(got)) != "session export ses_one" {
		t.Fatalf("the loop did not ask for the one session's export: %q", got)
	}
}
