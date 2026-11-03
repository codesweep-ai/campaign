package store

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

func mirror(t *testing.T, s Store) string {
	t.Helper()
	b, err := os.ReadFile(s.LogMirrorPath("acme"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestTheLogMirrorKeepsWhatTheLogLost: the copy is a backup of a claim, and
// run19's orchestrator rewrote its log, dropping five acceptances. A copy
// replaced on every look dropped them too. This one keeps every line it has
// copied, adds what is new after it, and keeps both forms of a changed line.
func TestTheLogMirrorKeepsWhatTheLogLost(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	mustMerge(t, s, "a\nb\nc\n")
	mustMerge(t, s, "x\nb2\n") // the log as rewritten: a, b and c are gone, and b changed
	mustMerge(t, s, "x\nb2\ny\n")
	if got, want := mirror(t, s), "a\nb\nc\nx\nb2\ny\n"; got != want {
		t.Fatalf("mirror = %q, want %q", got, want)
	}
}

// TestTheLogMirrorWaitsForALineToFinish: the orchestrator may be appending as
// the log is read. A half-written line copied now would sit beside its
// finished self forever.
func TestTheLogMirrorWaitsForALineToFinish(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	mustMerge(t, s, "a\n{\"at\":\"2026-")
	if got := mirror(t, s); got != "a\n" {
		t.Fatalf("a line still being written was copied: %q", got)
	}
	mustMerge(t, s, "a\n{\"at\":\"2026-11-03\"}\n")
	if got, want := mirror(t, s), "a\n{\"at\":\"2026-11-03\"}\n"; got != want {
		t.Fatalf("mirror = %q, want %q", got, want)
	}
}

// TestTheLogMirrorTakesConcurrentObservesOnce: several observes run at once,
// a campaign's driver and an operator's among them. Each line lands once.
func TestTheLogMirrorTakesConcurrentObservesOnce(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	var b strings.Builder
	for i := range 50 {
		fmt.Fprintf(&b, "entry %d\n", i)
	}
	log := b.String()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := s.MergeLogMirror("acme", []byte(log)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := mirror(t, s); got != log {
		t.Fatalf("concurrent merges did not land each line once:\n%s", got)
	}
}

// TestTheLogMirrorCompletesAnOldCopysLastLine: a copy written by an earlier
// build is the log as it stood, and may lack a final newline. A new line must
// not be glued onto it.
func TestTheLogMirrorCompletesAnOldCopysLastLine(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if err := os.WriteFile(s.LogMirrorPath("acme"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustMerge(t, s, "a\nb\n")
	if got, want := mirror(t, s), "a\nb\n"; got != want {
		t.Fatalf("mirror = %q, want %q", got, want)
	}
}

// TestResetLogMirrorStartsANewCampaignEmpty: the copy only grows, so a new
// campaign under an old name must not inherit the old one's claims.
func TestResetLogMirrorStartsANewCampaignEmpty(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	mustMerge(t, s, "old campaign's line\n")
	if err := s.ResetLogMirror("acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.LogMirrorPath("acme")); !os.IsNotExist(err) {
		t.Fatalf("the copy survived a reset: %v", err)
	}
	if err := s.ResetLogMirror("acme"); err != nil {
		t.Fatalf("resetting a copy that is not there must pass: %v", err)
	}
}

func mustMerge(t *testing.T, s Store, log string) {
	t.Helper()
	if err := s.MergeLogMirror("acme", []byte(log)); err != nil {
		t.Fatal(err)
	}
}
