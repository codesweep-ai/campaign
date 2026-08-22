package covmap

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeTB records Fatalf instead of aborting, for exercising failure paths.
type fakeTB struct {
	name    string
	fatal   string
	failed  bool
	skipped bool
	cleanup []func()
}

func (f *fakeTB) Helper()       {}
func (f *fakeTB) Name() string  { return f.name }
func (f *fakeTB) Failed() bool  { return f.failed }
func (f *fakeTB) Skipped() bool { return f.skipped }
func (f *fakeTB) Cleanup(fn func()) {
	f.cleanup = append(f.cleanup, fn)
}
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.fatal = fmt.Sprintf(format, args...)
}
func (f *fakeTB) runCleanups() {
	for _, v := range slices.Backward(f.cleanup) {
		v()
	}
}

func TestRegistryLoadsStrictAndComplete(t *testing.T) {
	root, err := FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Rows("adapter")) == 0 || len(reg.Rows("core")) == 0 {
		t.Fatal("rubric has an empty scope section")
	}
	for _, b := range reg.Behaviors {
		if !strings.HasPrefix(b.DesignRef, "SPEC.md") {
			t.Errorf("behavior %q design_ref %q does not reference SPEC.md", b.ID, b.DesignRef)
		}
	}
}

func TestValidateRecordRejectsOffRubricProofs(t *testing.T) {
	root, err := FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	base := Record{Tier: TierUnit, Test: "TestX", Repo: "campaign", Commit: "abc", Time: "2026-07-31T00:00:00Z"}
	cases := []struct {
		name string
		mut  func(*Record)
	}{
		{"unknown behavior", func(r *Record) { r.Behavior = "made-up" }},
		{"core with adapter", func(r *Record) { r.Behavior = "no-push"; r.Adapter = "codex" }},
		{"adapter row without adapter", func(r *Record) { r.Behavior = "interrupt" }},
		{"unknown adapter", func(r *Record) { r.Behavior = "interrupt"; r.Adapter = "gemini" }},
		{"unknown role", func(r *Record) { r.Behavior = "interrupt"; r.Adapter = "codex"; r.Role = "manager" }},
		{"unknown tier", func(r *Record) { r.Behavior = "no-push"; r.Tier = "vibes" }},
		{"unknown repo", func(r *Record) { r.Behavior = "no-push"; r.Repo = "elsewhere" }},
	}
	for _, tc := range cases {
		rec := base
		tc.mut(&rec)
		if err := reg.ValidateRecord(rec); err == nil {
			t.Errorf("%s: record accepted: %+v", tc.name, rec)
		}
	}
	good := base
	good.Behavior = "interrupt"
	good.Adapter = "codex"
	if err := reg.ValidateRecord(good); err != nil {
		t.Errorf("valid record rejected: %v", err)
	}
}

// Prove is execution-gated end to end: a valid call appends a record to the
// buffer; an off-rubric call fails the test and writes nothing.
func TestProveEmitsValidatedRecords(t *testing.T) {
	buffer := filepath.Join(t.TempDir(), "runs.jsonl")
	t.Setenv("COVMAP_BUFFER", buffer)

	ok := &fakeTB{name: "TestSomething/sub"}
	Prove(ok, "interrupt", "codex", "", TierUnit)
	if ok.fatal != "" {
		t.Fatalf("valid Prove failed: %s", ok.fatal)
	}
	records, err := ReadBuffer(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Test != "TestSomething" || records[0].Behavior != "interrupt" ||
		records[0].Repo != "campaign" || records[0].Commit == "" || records[0].Time == "" {
		t.Fatalf("unexpected record: %+v", records)
	}

	bad := &fakeTB{name: "TestSomething"}
	Prove(bad, "not-a-behavior", "codex", "", TierUnit)
	if !strings.Contains(bad.fatal, "unknown behavior") {
		t.Fatalf("off-rubric Prove did not fail: %q", bad.fatal)
	}
	records, _ = ReadBuffer(buffer)
	if len(records) != 1 {
		t.Fatalf("off-rubric Prove wrote a record: %+v", records)
	}
}

// ProveCore records against a core row, which carries no adapter and no role.
// The distinction is load-bearing: an adapter-scoped behavior proved without an
// adapter would fill a cell that names one.
func TestProveCoreEmitsAnAdapterlessRecord(t *testing.T) {
	buffer := filepath.Join(t.TempDir(), "runs.jsonl")
	t.Setenv("COVMAP_BUFFER", buffer)

	ok := &fakeTB{name: "TestSomething"}
	ProveCore(ok, "state-safety", TierSmoke)
	if ok.fatal != "" {
		t.Fatalf("valid ProveCore failed: %s", ok.fatal)
	}
	records, err := ReadBuffer(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one record, got %+v", records)
	}
	if got := records[0]; got.Behavior != "state-safety" || got.Tier != TierSmoke ||
		got.Adapter != "" || got.Role != "" {
		t.Fatalf("core proof must carry no adapter or role: %+v", got)
	}

	// An adapter row refuses a core proof, which is what keeps the two kinds
	// of row from filling each other in.
	bad := &fakeTB{name: "TestSomething"}
	ProveCore(bad, "interrupt", TierSmoke)
	if bad.fatal == "" {
		t.Fatal("ProveCore against an adapter-scoped row should fail")
	}
}

// ProveOnPass emits only for a passed, non-skipped test.
func TestProveOnPassGatesOnOutcome(t *testing.T) {
	buffer := filepath.Join(t.TempDir(), "runs.jsonl")
	t.Setenv("COVMAP_BUFFER", buffer)
	for _, tc := range []struct {
		name            string
		failed, skipped bool
		want            int
	}{
		{"passed", false, false, 1},
		{"failed", true, false, 0},
		{"skipped", false, true, 0},
	} {
		os.Remove(buffer)
		tb := &fakeTB{name: "TestGate", failed: tc.failed, skipped: tc.skipped}
		ProveOnPass(tb, "interrupt", "codex", "", TierUnit)
		tb.runCleanups()
		records, err := ReadBuffer(buffer)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) != tc.want {
			t.Errorf("%s: %d records, want %d", tc.name, len(records), tc.want)
		}
	}
}

// Fold keeps the newest record per proof, prunes records whose test is gone
// or whose behavior left the rubric, and sorts deterministically.
func TestFoldMergesPrunesAndSorts(t *testing.T) {
	root, err := FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	old := Record{Behavior: "interrupt", Adapter: "codex", Tier: TierUnit,
		Test: "TestKept", Repo: "campaign", Commit: "old", Time: "2026-07-01T00:00:00Z"}
	newer := old
	newer.Commit, newer.Time = "new", "2026-07-31T00:00:00Z"
	gone := old
	gone.Test = "TestDeleted"
	offRubric := Record{Behavior: "retired-behavior", Tier: TierUnit, Test: "TestKept",
		Repo: "campaign", Commit: "x", Time: "2026-07-01T00:00:00Z"}
	unverifiable := Record{Behavior: "interrupt", Adapter: "claude", Tier: TierScripts,
		Test: "TestSandboxThing", Repo: "sandbox", Commit: "y", Time: "2026-07-01T00:00:00Z"}

	exists := map[string]map[string]bool{"campaign": {"TestKept": true}} // no sandbox set: keep
	folded, err := Fold(reg, &Results{Records: []Record{old, gone, offRubric}}, []Record{newer, unverifiable}, exists)
	if err != nil {
		t.Fatal(err)
	}
	if len(folded.Records) != 2 {
		t.Fatalf("folded records: %+v", folded.Records)
	}
	if folded.Records[0].Adapter != "claude" || folded.Records[1].Commit != "new" {
		t.Fatalf("fold order or newest-wins broken: %+v", folded.Records)
	}
}

// The committed artifacts must be internally consistent: coverage.html is a
// pure function of the rubric and tracked results.
func TestCoverageHTMLCurrent(t *testing.T) {
	root, err := FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := LoadResults(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "covmap", "covmap.html"))
	if err != nil {
		t.Fatalf("covmap/covmap.html missing — run `make covmap`: %v", err)
	}
	if string(got) != Render(reg, res) {
		t.Fatal("covmap/covmap.html is stale — run `make covmap` and commit the result")
	}
}

// Tracked results must survive their own validation (every record cites an
// existing test where verifiable, on-rubric behaviors only).
func TestTrackedResultsValid(t *testing.T) {
	root, err := FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := LoadResults(root)
	if err != nil {
		t.Fatal(err)
	}
	exists, err := ExistsSets(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Records {
		if err := reg.ValidateRecord(r); err != nil {
			t.Errorf("tracked record invalid: %v", err)
		}
		if set, ok := exists[r.Repo]; ok && !set[r.Test] {
			t.Errorf("tracked record cites missing %s test %s", r.Repo, r.Test)
		}
	}
}
