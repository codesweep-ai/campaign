package cli

// The pin must be provable in both directions — a matching surface
// reports PINNED-OK, and every class of deviation (version drift, tool
// mutation, tool removal) fails doctor and refuses create LOUDLY. A pin whose
// refusal has never been seen firing is decoration.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/store"
)

// TestMain isolates every test from the operator's real pin: unit tests run
// against fake tools, and comparing those against a real host pin would fail
// the suite on any pinned machine. Pin tests set CS_CAMPAIGN_PIN themselves
// via t.Setenv, which overrides this default.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cs-campaign-test-pin-")
	if err == nil {
		os.Setenv("CS_CAMPAIGN_PIN", filepath.Join(dir, "pin.json"))
	}
	code := m.Run()
	if err == nil {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}

// installPinnedSurface fakes the complete upstream surface (fake-sandbox
// reporting `version`, plus all 21 agent tools) and points CS_CAMPAIGN_PIN at
// a fresh path. Returns the app and the path of one tool for mutation.
func installPinnedSurface(t *testing.T, sandboxVersion string) (*app, string) {
	t.Helper()
	t.Setenv("CS_CAMPAIGN_PIN", filepath.Join(t.TempDir(), "pin.json"))
	// A group-aware surface: doctor gates on `group ls --json` before it reaches
	// the pin report, so a fake without it would fail for the wrong reason.
	sandboxDir := installFakeTool(t, "fake-sandbox", `
case "$1" in
  version) echo 'cs-sandbox `+sandboxVersion+` (linux/amd64, go1.25.0)';;
  ls) echo '[]';;
  group) echo '[]';;
esac
`)
	var mutable string
	for _, name := range pinnedToolNames() {
		if name == "cs-sandbox" {
			continue
		}
		dir := installFakeTool(t, name, `exit 0`)
		if name == "cs-codex-remote" {
			mutable = filepath.Join(dir, name)
		}
	}
	a := &app{store: store.Store{Dir: t.TempDir()}, sandbox: sandboxCLI{Bin: filepath.Join(sandboxDir, "fake-sandbox")}}
	return a, mutable
}

func runPin(t *testing.T, a *app, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	cmd := a.pinCmd()
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func runDoctor(t *testing.T, a *app) (string, error) {
	t.Helper()
	var out strings.Builder
	cmd := a.doctorCmd()
	cmd.SetOut(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestPinRoundTripAndDoctorPinnedOK(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, _ := installPinnedSurface(t, "0.0.1-snapshot-aaaa111")
	out, err := runPin(t, a)
	if err != nil {
		t.Fatalf("pin: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pinned 0.0.1-snapshot-aaaa111 + 21 tools") {
		t.Fatalf("pin output = %q", out)
	}
	out, err = runDoctor(t, a)
	if err != nil {
		t.Fatalf("doctor on a matching surface: %v\n%s", err, out)
	}
	if !strings.Contains(out, "upstream matches pin: 0.0.1-snapshot-aaaa111") {
		t.Fatalf("doctor must report PINNED-OK, got:\n%s", out)
	}
}

func TestDoctorWarnsLoudlyWhenUnpinned(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, _ := installPinnedSurface(t, "0.0.1-snapshot-aaaa111")
	out, err := runDoctor(t, a) // no pin written
	if err != nil {
		t.Fatalf("doctor unpinned must warn, not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "UNPINNED") || !strings.Contains(out, "cs-campaign pin") {
		t.Fatalf("doctor must warn loudly with the remedy, got:\n%s", out)
	}
}

func TestDoctorFailsLoudlyOnVersionDrift(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, _ := installPinnedSurface(t, "0.0.1-snapshot-aaaa111")
	if _, err := runPin(t, a); err != nil {
		t.Fatal(err)
	}
	// The upstream moves: same binary path, new reported version.
	if err := os.WriteFile(a.sandbox.Bin, []byte("#!/bin/sh\ncase \"$1\" in version) echo 'cs-sandbox 0.0.1-snapshot-bbbb222';; ls) echo '[]';; group) echo '[]';; esac\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := runDoctor(t, a)
	if err == nil {
		t.Fatalf("doctor must fail on version drift, got:\n%s", out)
	}
	for _, want := range []string{"deviates from pin", "0.0.1-snapshot-aaaa111", "0.0.1-snapshot-bbbb222", "re-validate"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("drift error missing %q: %v", want, err)
		}
	}
}

func TestDoctorFailsLoudlyOnToolMutation(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, mutable := installPinnedSurface(t, "0.0.1-snapshot-aaaa111")
	if _, err := runPin(t, a); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mutable, []byte("#!/bin/sh\nexit 0\n# upstream edited this\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := runDoctor(t, a)
	if err == nil || !strings.Contains(err.Error(), "cs-codex-remote content changed") {
		t.Fatalf("doctor must name the mutated tool, got: %v", err)
	}
	// Removal is a distinct deviation class with its own message. Deleting the
	// fake would just expose a real host tool further down PATH (correctly
	// reported as a hash mismatch), so the missing branch is proven on the
	// comparison itself.
	deviations := comparePin(pinFile{Tools: map[string]string{"cs-codex-remote": "abc"}}, pinFile{Tools: map[string]string{}})
	if len(deviations) != 1 || !strings.Contains(deviations[0], "cs-codex-remote missing from PATH") {
		t.Fatalf("missing-tool deviation = %v", deviations)
	}
}

func TestPinRefusesCasualOverwriteOfDeviatingSurface(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, mutable := installPinnedSurface(t, "0.0.1-snapshot-aaaa111")
	if _, err := runPin(t, a); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mutable, []byte("#!/bin/sh\nexit 0\n# drifted\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := runPin(t, a); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("pin must refuse to normalize drift without --update, got: %v", err)
	}
	if out, err := runPin(t, a, "--update"); err != nil {
		t.Fatalf("pin --update after re-validation: %v\n%s", err, out)
	}
	if _, err := runDoctor(t, a); err != nil {
		t.Fatalf("doctor after re-pin: %v", err)
	}
}

func TestCreateRefusesDeviatingUpstreamUnlessAccepted(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, mutable := installPinnedSurface(t, "0.0.1-snapshot-aaaa111")
	if _, err := runPin(t, a); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mutable, []byte("#!/bin/sh\nexit 0\n# drifted\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Wiring: create must refuse BEFORE touching the sandbox. The fake sandbox
	// would fail any provisioning call with a distinct error, so reaching it
	// would surface as the wrong failure text here.
	cmd := a.createCmd(false)
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"gate-test", "--orchestrator", "claude", "--agent", "worker=codex"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "deviates from pin") || !strings.Contains(err.Error(), "--accept-upstream-change") {
		t.Fatalf("create must refuse a deviating surface with the remedy, got: %v", err)
	}

	// The acceptance path records the deviation on the campaign — auditable,
	// never silent.
	campaign := &model.Campaign{Name: "gate-test"}
	if err := a.gateUpstream(context.Background(), &out, campaign, true); err != nil {
		t.Fatalf("accepted deviation must proceed: %v", err)
	}
	if campaign.Upstream == nil || !campaign.Upstream.Pinned || !campaign.Upstream.Accepted {
		t.Fatalf("campaign must record the accepted deviation: %+v", campaign.Upstream)
	}
	if len(campaign.Upstream.Deviations) == 0 || !strings.Contains(campaign.Upstream.Deviations[0], "cs-codex-remote") {
		t.Fatalf("recorded deviations = %v", campaign.Upstream.Deviations)
	}

	// A clean surface records PINNED-OK with no acceptance flag.
	clean := &model.Campaign{Name: "clean"}
	restored, _ := installPinnedSurface(t, "0.0.1-snapshot-cccc333")
	if _, err := runPin(t, restored); err != nil {
		t.Fatal(err)
	}
	if err := restored.gateUpstream(context.Background(), &out, clean, false); err != nil {
		t.Fatalf("clean surface must pass the gate: %v", err)
	}
	if clean.Upstream == nil || !clean.Upstream.Pinned || clean.Upstream.Accepted || len(clean.Upstream.Deviations) != 0 {
		t.Fatalf("clean record = %+v", clean.Upstream)
	}
}

// A failed or unreadable version probe must reach the operator with its cause.
// comparePin can only say "(unknown)", which reads the same whether the probe
// failed, timed out, or printed something no pattern matches.
func TestPinReportsWhyTheVersionProbeFailed(t *testing.T) {
	a, _ := installPinnedSurface(t, "0.0.1-snapshot-aaaa111")
	if out, err := runPin(t, a); err != nil {
		t.Fatalf("pin: %v\n%s", err, out)
	}
	// Same surface, but the sandbox now prints something no version pattern
	// matches.
	broken := installFakeTool(t, "fake-sandbox", `
case "$1" in
  version) echo 'not a version at all';;
  ls) echo '[]';;
  group) echo '[]';;
esac
`)
	a.sandbox = sandboxCLI{Bin: filepath.Join(broken, "fake-sandbox")}
	deviations, _, pinned, err := a.verifyPin(t.Context())
	if err != nil || !pinned {
		t.Fatalf("verifyPin: err=%v pinned=%v", err, pinned)
	}
	joined := strings.Join(deviations, "\n")
	if !strings.Contains(joined, "unrecognized cs-sandbox version output") {
		t.Fatalf("the cause of the probe failure must survive to the operator:\n%s", joined)
	}
}

// The version token the family actually prints, in every shape it takes.
//
// This is a regression with a cost already paid. Every tool in the family
// stamps `git describe --tags --always`, and cs-sandbox has no tags — so the
// day its build moved to that stamp it began reporting a bare short commit
// where it had reported 0.0.1-snapshot-<rev>. The pattern still demanded three
// dot-separated numbers, so `doctor` refused the surface outright and `create`
// counted the whole thing as a deviation. Nothing about the sandbox had
// changed except how it spelt its own name.
//
// The suffixes are not decoration. A -dirty that the pattern dropped would
// record a modified build under a clean revision's name, which is the one
// thing a pin exists to prevent.
func TestSandboxVersionPatternReadsEveryStampShape(t *testing.T) {
	for _, tc := range []struct {
		reported, want string
	}{
		{"cs-sandbox 6981299 (linux/amd64, go1.26.2-X:nodwarf5)", "6981299"},
		{"cs-sandbox 6981299-dirty (linux/amd64, go1.26.2)", "6981299-dirty"},
		{"cs-sandbox 0.0.1-snapshot-99ab02a (linux/amd64, go1.25.0)", "0.0.1-snapshot-99ab02a"},
		{"cs-sandbox 0.1.0-dev (linux/amd64, go1.25.0)", "0.1.0-dev"},
		{"cs-sandbox v1.4.0 (linux/amd64, go1.26.2)", "v1.4.0"},
		{"cs-sandbox v1.4.0-3-gabc1234 (linux/amd64, go1.26.2)", "v1.4.0-3-gabc1234"},
		{"not a version at all", ""},
		{"", ""},
	} {
		if got := sandboxVersionPattern.FindString(tc.reported); got != tc.want {
			t.Errorf("FindString(%q) = %q, want %q", tc.reported, got, tc.want)
		}
	}
}

// And the same thing through the commands, because a pattern that matches in
// isolation is worth nothing if doctor and pin disagree about it.
func TestAnUntaggedSandboxBuildPinsAndReportsOK(t *testing.T) {
	a, _ := installPinnedSurface(t, "6981299")
	out, err := runPin(t, a)
	if err != nil {
		t.Fatalf("pin an untagged build: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pinned 6981299 + 21 tools") {
		t.Fatalf("pin output = %q", out)
	}
	out, err = runDoctor(t, a)
	if err != nil {
		t.Fatalf("doctor on a matching untagged surface: %v\n%s", err, out)
	}
	if !strings.Contains(out, "upstream matches pin: 6981299") {
		t.Fatalf("a bare-commit stamp must pin and verify by that name:\n%s", out)
	}
}

// The pin records one surface twice: provisionally, so the validating run can
// create at all, and again with that run's evidence. The surface is identical
// across the two by construction, so a `pin` that compared only the surface
// reported "unchanged" and dropped the evidence on the floor — leaving a
// validated pin that still claimed it had never been validated. That is not
// hypothetical: it is the state the repository's own pin was found in.
func TestRepinRecordsNewEvidenceOnAnUnchangedSurface(t *testing.T) {
	a, _ := installPinnedSurface(t, "0.0.1-snapshot-aaaa111")
	if out, err := runPin(t, a, "--note", "provisional: not yet validated"); err != nil {
		t.Fatalf("first pin: %v\n%s", err, out)
	}

	out, err := runPin(t, a, "--note", "validated: TestLiveMatrix PASS on firecracker")
	if err != nil {
		t.Fatalf("re-pin with evidence: %v\n%s", err, out)
	}
	if !strings.Contains(out, "surface unchanged; recording the new note") {
		t.Fatalf("the re-pin must say it recorded the note, got:\n%s", out)
	}
	pin, pinned, err := loadPin()
	if err != nil || !pinned {
		t.Fatalf("loadPin: err=%v pinned=%v", err, pinned)
	}
	if pin.Note != "validated: TestLiveMatrix PASS on firecracker" {
		t.Fatalf("note = %q, want the evidence", pin.Note)
	}
}

// The same surface and the same note is genuinely nothing to do, and must stay
// a no-op: `doctor` and CI call this on every run, and a pin whose timestamp
// moved on every invocation would report a change that never happened.
func TestRepinWithNoNewNoteStaysANoOp(t *testing.T) {
	a, _ := installPinnedSurface(t, "0.0.1-snapshot-aaaa111")
	if out, err := runPin(t, a, "--note", "same note"); err != nil {
		t.Fatalf("first pin: %v\n%s", err, out)
	}
	before, _, _ := loadPin()
	out, err := runPin(t, a, "--note", "same note")
	if err != nil {
		t.Fatalf("re-pin: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pin unchanged") {
		t.Fatalf("want a no-op, got:\n%s", out)
	}
	after, _, _ := loadPin()
	if !after.PinnedAt.Equal(before.PinnedAt) {
		t.Fatalf("an unchanged pin must not restamp: %s -> %s", before.PinnedAt, after.PinnedAt)
	}
}

// The replay surface is pinned, and it is pinned SEPARATELY.
//
// cs-vcr records and replays cassettes; it takes no part in running a
// campaign. A host that campaigns for real does not have it, and must not be
// told its surface is incomplete for that — which is why it cannot join
// pinnedToolNames(), where a missing tool refuses `create` outright.
func TestTheReplaySurfaceIsPinnedWithoutGatingCreate(t *testing.T) {
	a, _ := installPinnedSurface(t, "0.0.1-snapshot-aaaa111")
	vcrDir := installFakeTool(t, "cs-vcr", `case "$1" in config) echo 'normalize ruleset  v11 (7 strip, 1 query, 10 replace)';; esac`)
	t.Setenv("PATH", vcrDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if out, err := runPin(t, a); err != nil {
		t.Fatalf("pin: %v\n%s", err, out)
	}
	pin, pinned, err := loadPin()
	if err != nil || !pinned {
		t.Fatalf("loadPin: err=%v pinned=%v", err, pinned)
	}
	if pin.Fixtures["cs-vcr"] == "" {
		t.Fatalf("cs-vcr was not recorded on the pin: %+v", pin.Fixtures)
	}
	if pin.FixtureRuleset != "v11" {
		t.Fatalf("fixtureRuleset = %q, want v11", pin.FixtureRuleset)
	}
	// It must NOT have landed in the gating surface.
	if _, ok := pin.Tools["cs-vcr"]; ok {
		t.Fatal("cs-vcr is in the gating surface; a campaign host without it would be refused")
	}
	if out, err := runDoctor(t, a); err != nil || !strings.Contains(out, "replay surface matches pin") {
		t.Fatalf("doctor must report the replay surface: err=%v\n%s", err, out)
	}
}

// A ruleset that moved is the one difference that actually invalidates every
// committed cassette, so it has to be said in those words — and still only as
// a warning, because a campaign runs perfectly well on it.
func TestARulesetThatMovedIsReportedButDoesNotFailDoctor(t *testing.T) {
	a, _ := installPinnedSurface(t, "0.0.1-snapshot-aaaa111")
	old := installFakeTool(t, "cs-vcr", `case "$1" in config) echo 'normalize ruleset  v11';; esac`)
	t.Setenv("PATH", old+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := runPin(t, a); err != nil {
		t.Fatalf("pin: %v\n%s", err, out)
	}

	moved := installFakeTool(t, "cs-vcr", `case "$1" in config) echo 'normalize ruleset  v12';; esac`)
	t.Setenv("PATH", moved+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := runDoctor(t, a)
	if err != nil {
		t.Fatalf("a moved ruleset must not fail doctor: %v\n%s", err, out)
	}
	for _, want := range []string{"replay surface deviates", "ruleset v12", "pinned v11", "make fixtures"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor must name %q:\n%s", want, out)
		}
	}
}
