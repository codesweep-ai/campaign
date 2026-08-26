package cli

import (
	"os"
	"path/filepath"
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
	home := t.TempDir()
	t.Setenv("HOME", home)

	// The evidence that must survive: the raw db the audit reads.
	ocDir := filepath.Join(home, ".cs-opencode")
	if err := os.MkdirAll(ocDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ocDir, "opencode.db"), []byte("sqlite-ish"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A cs-opencode that never returns, on PATH ahead of anything real.
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "cs-opencode"),
		[]byte("#!/bin/sh\nif [ \"$1\" = db ]; then echo ses_one; exit 0; fi\nsleep 600\n"), 0o700); err != nil {
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
}
