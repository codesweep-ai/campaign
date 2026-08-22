package covmap

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TB is the slice of *testing.T that Prove needs; an interface so the
// failure paths are themselves testable.
type TB interface {
	Helper()
	Name() string
	Fatalf(format string, args ...any)
	Cleanup(func())
	Failed() bool
	Skipped() bool
}

// Prove records that the calling test just demonstrated a behavior for one
// adapter×role cell at the given tier. Call it immediately AFTER the
// assertion that proves the behavior: if the test fails before reaching the
// call, nothing is recorded and the cell decays to a gap on the next fold.
// Role "" marks a role-agnostic proof (host-side tooling) covering both
// roles. The record lands in the untracked buffer
// covmap/runs.local.jsonl; `make covmap` folds it into the tracked
// results.
func Prove(t TB, behavior, adapter, role string, tier Tier) {
	t.Helper()
	emit(t, Record{Behavior: behavior, Adapter: adapter, Role: role, Tier: tier})
}

// ProveCore records a proof for an adapter-agnostic (core) behavior.
func ProveCore(t TB, behavior string, tier Tier) {
	t.Helper()
	emit(t, Record{Behavior: behavior, Tier: tier})
}

// ProveOnPass registers a proof that is emitted only if the whole test
// passes (and was not skipped). One line at the top of a unit test whose
// entire body IS the proving assertion set; long multi-milestone tests
// should call Prove inline at each milestone instead.
func ProveOnPass(t TB, behavior, adapter, role string, tier Tier) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() && !t.Skipped() {
			emit(t, Record{Behavior: behavior, Adapter: adapter, Role: role, Tier: tier})
		}
	})
}

// ProveCoreOnPass is ProveOnPass for core behaviors.
func ProveCoreOnPass(t TB, behavior string, tier Tier) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() && !t.Skipped() {
			emit(t, Record{Behavior: behavior, Tier: tier})
		}
	})
}

func emit(t TB, rec Record) {
	t.Helper()
	// Explicit returns after every Fatalf: a TB implementation need not
	// abort, and an invalid record must never reach the buffer.
	root, err := FindRepoRoot(".")
	if err != nil {
		t.Fatalf("covmap: %v", err)
		return
	}
	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatalf("covmap: %v", err)
		return
	}
	rec.Test = topLevelTestName(t.Name())
	rec.Repo = "campaign"
	rec.Commit = repoCommit(root)
	rec.Time = time.Now().UTC().Format(time.RFC3339)
	if err := reg.ValidateRecord(rec); err != nil {
		t.Fatalf("covmap: %v", err)
		return
	}
	if err := AppendRecord(BufferPath(root), rec); err != nil {
		t.Fatalf("covmap: %v", err)
	}
}

// BufferPath is the untracked run buffer under root, overridable with
// COVMAP_BUFFER. The sandbox contract tests are aimed at this same file by
// `make covmap-scripts`, but through that repo's own CS_SANDBOX_COVERAGE_LOG
// — it names the sink itself and does not read this variable.
func BufferPath(root string) string {
	if v := os.Getenv("COVMAP_BUFFER"); v != "" {
		return v
	}
	return filepath.Join(root, "covmap", "runs.local.jsonl")
}

// AppendRecord appends one JSONL record; O_APPEND keeps concurrent test
// processes from interleaving within a line.
func AppendRecord(path string, rec Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// topLevelTestName strips subtest segments: proofs attach to the invocable
// test function.
func topLevelTestName(name string) string {
	if before, _, ok := strings.Cut(name, "/"); ok {
		return before
	}
	return name
}

var (
	commitOnce sync.Once
	commitVal  string
)

// repoCommit resolves the HEAD short hash once per process; "unknown" when
// git is unavailable.
func repoCommit(root string) string {
	commitOnce.Do(func() {
		out, err := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD").Output()
		if err != nil {
			commitVal = "unknown"
			return
		}
		commitVal = strings.TrimSpace(string(out))
	})
	return commitVal
}
