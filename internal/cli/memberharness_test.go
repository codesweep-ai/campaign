package cli

// Doctor must answer for the plane the work happens on. A member
// running a harness other than the pinned one has to fail LOUDLY and with the
// procedure that actually fixes it, because every trial-01 symptom was missing
// exactly that sentence. The refusal is proved in both directions here — a
// faithful member passes, each way a member can diverge fails — since a check
// that has only ever seen a matching machine is what let the original
// divergence survive a green pre-flight.

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/store"
)

// fakeToolBody is every fake tool's body, on both planes (identical bytes is
// what the pin checks). It doubles as the replying remote: an invocation
// carrying a dispatch trigger answers it by writing a well-formed reply into
// the fake guest's output channel; any other invocation exits 0.
const fakeToolBody = `ID=$(printf '%s\n' "$@" | grep -o 'Dispatch ID: [dm][0-9]*' | head -1 | awk '{print $3}')
[ -n "$ID" ] || exit 0
[ -n "$FAKE_HOME" ] || exit 0
mkdir -p "$FAKE_HOME/.local/share/cs-campaign/output/replies"
python3 - "$ID" > "$FAKE_HOME/.local/share/cs-campaign/output/replies/$ID.json" <<'PYEOF2'
import json,sys
note=json.dumps({"member":"","role":"","branch":"","missing":[],"goal":"stated goal","scope":"stated scope","obligations":"reply before stopping"})
print(json.dumps({"dispatch":sys.argv[1],"phase":"done","note":note,"at":"2026-01-01T00:00:00Z"}))
PYEOF2
exit 0`

func fakeToolBytes() []byte { return []byte("#!/bin/sh\n" + fakeToolBody + "\n") }

// shippedToolNames is what the fake cs-sandbox claims to ship. It is test data
// on purpose: the expectation now comes from cs-sandbox, so a test that wants a
// faithful member must state what a faithful member holds rather than reach for
// a list the product keeps.
func shippedToolNames() []string {
	var names []string
	for _, cli := range []string{"claude", "codex", "opencode"} {
		for _, suffix := range []string{"", "-remote", "-remote-forget", "-remote-output", "-remote-sessions", "-remote-status", "-turn"} {
			names = append(names, "cs-"+cli+suffix)
		}
	}
	return names
}

// shippedTools is that list with the hash a faithful copy has.
func shippedTools() map[string]string {
	sum := fmt.Sprintf("%x", sha256.Sum256(fakeToolBytes()))
	tools := map[string]string{}
	for _, name := range shippedToolNames() {
		tools[name] = sum
	}
	return tools
}

// seedMemberTools installs the shipped surface on BOTH planes with identical
// bytes — the host PATH the campaign drives through, and the mini-guest's
// ~/.local/bin that the harness check hashes. The two planes share the path
// ~/.local/bin in reality too (the guest home is the host user's name), which
// is exactly why nothing noticed they had diverged.
func seedMemberTools(t *testing.T, home string) string {
	t.Helper()
	guestBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(guestBin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range shippedToolNames() {
		installFakeTool(t, name, fakeToolBody)
		if err := os.WriteFile(filepath.Join(guestBin, name), fakeToolBytes(), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return guestBin
}

// A faithful member passes — and this is also the move-aside test. By the time
// doctor runs, configureOrchestrator has replaced 15 of the 21 tools with
// symlinks to cs-campaign-guard; a check that hashed ~/.local/bin blindly would
// report 15 deviations on this, the healthiest campaign there is.
func TestCampaignDoctorPassesAMemberOnTheShippedHarness(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, home := miniGuestApp(t)
	seedMemberTools(t, home)
	out, err := createMiniCampaign(t, a)
	if err != nil {
		t.Fatalf("create on a faithful harness: %v\n%s", err, out)
	}
	for _, want := range []string{
		"member orchestrator runs the tools cs-sandbox",
		"member fixer runs the tools cs-sandbox",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("create output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "HOW TO FIX") {
		t.Fatalf("a faithful campaign printed the remedy block:\n%s", out)
	}
	campaign, err := a.store.Load("mini")
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range campaign.Members {
		if member.Harness == nil || len(member.Harness.Tools) != 21 || len(member.Harness.Deviations) > 0 {
			t.Fatalf("member %s harness not recorded on the campaign: %+v", member.Name, member.Harness)
		}
	}
}

// The trial-01 shape: the image predates an upstream fix, so a member's turn
// driver is not the pinned one. create must refuse before the campaign can be
// mistaken for usable.
func TestCreateRefusesAMemberRunningAStaleDriver(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, home := miniGuestApp(t)
	guestBin := seedMemberTools(t, home)
	stale := []byte("#!/bin/sh\n# the pre-fix driver\nexit 0\n")
	if err := os.WriteFile(filepath.Join(guestBin, "cs-codex-turn"), stale, 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := createMiniCampaign(t, a)
	if err == nil {
		t.Fatalf("create accepted a member running an unpinned harness:\n%s", out)
	}
	for _, want := range []string{
		"runs a harness cs-sandbox did NOT ship",
		"cs-codex-turn: content differs (cs-sandbox ships ",
		"HOW TO FIX — a member's harness is not the one cs-sandbox ships.",
		"cs-sandbox build",
		"Do NOT patch the tool inside the member",
		"Do NOT re-run a dispatch and read success as proof",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("failure output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(err.Error(), "FAILED its doctor") {
		t.Fatalf("create error = %v", err)
	}
	campaign, loadErr := a.store.Load("mini")
	if loadErr != nil || campaign.Provisioning != "create-failed" {
		t.Fatalf("campaign after a refused create = %+v, %v", campaign, loadErr)
	}
}

// A tool the image never shipped is a different defect from a tool that
// changed, and the operator needs to be told which.
func TestCampaignDoctorNamesAMissingMemberTool(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, home := miniGuestApp(t)
	guestBin := seedMemberTools(t, home)
	if err := os.Remove(filepath.Join(guestBin, "cs-opencode-turn")); err != nil {
		t.Fatal(err)
	}
	out, _ := createMiniCampaign(t, a)
	if !strings.Contains(out, "cs-opencode-turn: absent from the member's ~/.local/bin (cs-sandbox ships ") {
		t.Fatalf("output does not name the missing tool:\n%s", out)
	}
}

// "We asked and cannot tell" is the state this row exists to remove, so an
// unmeasurable member fails rather than being skipped.
func TestMemberHarnessFailsClosedWhenItCannotHash(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	sandboxDir := installFakeTool(t, "fake-sandbox", `echo "PROBE-ERROR sha256sum not available in this member"`)
	a := &app{store: store.Store{Dir: t.TempDir()}, sandbox: sandboxCLI{Bin: filepath.Join(sandboxDir, "fake-sandbox")}}
	_, err := a.memberHarness(t.Context(), model.Member{Name: "worker", Ref: "worker.g"},
		map[string]string{"cs-codex-turn": "deadbeef"})
	if err == nil || !strings.Contains(err.Error(), "sha256sum not available") {
		t.Fatalf("an unhashable member did not fail closed: %v", err)
	}
}

// A cs-sandbox that will not say what it ships leaves the whole fleet
// unmeasurable, which must read as the same unknown a failed probe is. An empty
// expectation would instead have every member match it trivially, turning the
// one check that speaks for the guests into a line that always says ok.
func TestCampaignDoctorFailsWhenTheShippedSurfaceCannotBeRead(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	sandboxDir := installFakeTool(t, "fake-sandbox", `case "$1" in agent-tools) exit 7;; esac`)
	a := &app{store: store.Store{Dir: t.TempDir()}, sandbox: sandboxCLI{Bin: filepath.Join(sandboxDir, "fake-sandbox")}}
	campaign := &model.Campaign{Name: "mini", Members: []model.Member{{Name: "worker", CLI: "codex", Ref: "worker.g"}}}

	var lines []string
	remedy := a.reportMemberHarness(t.Context(), campaign, func(ok bool, format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "UNVERIFIABLE") {
		t.Fatalf("an unreadable shipped surface must not pass silently:\n%s", joined)
	}
	if remedy == "" || !strings.Contains(remedy, "HOW TO FIX") {
		t.Fatalf("the operator must still get the procedure:\n%s", remedy)
	}
}

// trial-01's fleet patched the driver on all three guests mid-run and no
// campaign record noted it. A create-time check alone cannot see that; the
// create-vs-archive delta can.
func TestArchiveFlagsAHarnessChangedDuringTheRun(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, home := miniGuestApp(t)
	guestBin := seedMemberTools(t, home)
	if out, err := createMiniCampaign(t, a); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	campaign, err := a.store.Load("mini")
	if err != nil {
		t.Fatal(err)
	}
	// The mid-run patch, after doctor recorded a clean member.
	if err := os.WriteFile(filepath.Join(guestBin, "cs-codex-turn"),
		[]byte("#!/bin/sh\n# hand-patched to unblock the mission\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	a.archiveMemberHarness(t.Context(), root, campaign)
	anomaly, err := os.ReadFile(filepath.Join(root, "HARNESS-ANOMALY.txt"))
	if err != nil {
		t.Fatalf("no HARNESS-ANOMALY.txt written for a mid-run harness change: %v", err)
	}
	for _, want := range []string{"CHANGED during the run", "cs-codex-turn"} {
		if !strings.Contains(string(anomaly), want) {
			t.Fatalf("anomaly file missing %q:\n%s", want, anomaly)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "member-harness.json")); err != nil {
		t.Fatalf("no member-harness.json written: %v", err)
	}
}
