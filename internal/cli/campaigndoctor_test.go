package cli

// The campaign doctor must PASS a faithful instantiation and FAIL
// loudly on each class of wiring defect — manifest drift, a disarmed guard, a
// member whose declared CLI is absent — and create must refuse to declare a
// campaign usable when its instantiation is broken. The fake sandbox is a
// "mini-guest": exec commands run for real under a fake HOME, so the installed
// manifest, guard symlinks and probes behave as they would in the VM.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/store"
)

// seedMiniGuest pre-populates the fake guest's ~/.local/bin the way the image
// would: the member CLI binaries, and the real remote tool the guard probe
// targets (cs-<wrong-family>-remote-status), which the install loop then moves
// aside and guards. Member CLI "opencode" is deliberate: the campaigndoctor
// tests need a CLI name that cannot leak in from the host through the
// /usr/bin:/bin PATH clamp (host codex lives in /usr/bin; host opencode does
// not), so absence tests stay deterministic.
// installReplyingRemotes puts fake cs-<cli>-remote tools on PATH that answer
// any dispatch by writing a well-formed reply into the fake guest's output
// channel — the unit-test stand-in for a member that reads its trigger and
// replies. The note carries a minimal readback restatement, member-agnostic
// (verifyReadback skips identity/branch checks on empty fields).
func installReplyingRemotes(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	body := `#!/bin/sh
ID=$(printf '%s\n' "$@" | grep -o 'Dispatch ID: [dm][0-9]*' | head -1 | awk '{print $3}')
[ -n "$ID" ] || exit 0
mkdir -p "$FAKE_HOME/.local/share/cs-campaign/output/replies"
python3 - "$ID" > "$FAKE_HOME/.local/share/cs-campaign/output/replies/$ID.json" <<'PYEOF2'
import json,sys
note=json.dumps({"member":"","role":"","branch":"","missing":[],"goal":"stated goal","scope":"stated scope","obligations":"reply before stopping"})
print(json.dumps({"dispatch":sys.argv[1],"phase":"done","note":note,"at":"2026-01-01T00:00:00Z"}))
PYEOF2
`
	for _, name := range []string{"cs-claude-remote", "cs-codex-remote", "cs-opencode-remote"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func seedMiniGuest(t *testing.T, home, memberCLI string) string {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed; the guard requires it")
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{memberCLI, "cs-claude-remote-output"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FAKE_HOME", home)
	return home
}

// miniGuestApp builds an app whose fake sandbox provisions instantly and runs
// exec commands inside the mini-guest.
func miniGuestApp(t *testing.T) (*app, string) {
	t.Helper()
	dir := t.TempDir()
	tool := filepath.Join(dir, "fake-sandbox")
	body := `#!/bin/sh
case "$1" in
  # the pin comparison reads the version before it hashes anything.
  version) echo 'cs-sandbox 0.0.1-snapshot-mini (linux/amd64, go1.25.0)' ;;
  ls) echo '[]' ;;
  create) : ;;
  # create reads each member's record back for its branch and address.
  inspect) printf '%s\n' '{"ref":"'"$2"'","ip":"10.89.0.42","repos":[]}' ;;
  exec)
    mkdir -p "$FAKE_HOME/.local/bin"
    HOME="$FAKE_HOME" PATH="$FAKE_HOME/.local/bin:/usr/bin:/bin" sh -c "$5" ;;
esac
`
	if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	ensureGuestBinary(t)
	home := seedMiniGuest(t, filepath.Join(dir, "guest-home"), "opencode")
	installReplyingRemotes(t)
	return &app{store: store.Store{Dir: filepath.Join(dir, "state")}, sandbox: sandboxCLI{Bin: tool}}, home
}

func createMiniCampaign(t *testing.T, a *app) (string, error) {
	t.Helper()
	cmd := a.createCmd(false)
	cmd.SilenceUsage = true
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"mini", "--orchestrator", "claude", "--agent", "fixer=opencode"})
	err := cmd.Execute()
	return out.String(), err
}

func runCampaignDoctor(t *testing.T, a *app, name string) error {
	t.Helper()
	cmd := a.doctorCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{name})
	return cmd.Execute()
}

func TestCreateRunsCampaignDoctorOnFaithfulInstantiation(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, _ := miniGuestApp(t)
	out, err := createMiniCampaign(t, a)
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	for _, want := range []string{
		"manifest fidelity: 1 agents match",
		"orchestrator controls installed",
		"guard fires: cs-claude-remote-output against fixer (opencode) refused with the true diagnosis",
		"member fixer answers with its declared CLI (opencode) present",
		"campaign mini created",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("create output missing %q:\n%s", want, out)
		}
	}
	campaign, err := a.store.Load("mini")
	if err != nil || campaign.Provisioning != "" {
		t.Fatalf("campaign after doctored create = %+v, %v", campaign, err)
	}
}

func TestCampaignDoctorCatchesManifestDrift(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, home := miniGuestApp(t)
	if out, err := createMiniCampaign(t, a); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	manifest := filepath.Join(home, ".config", "cs-campaign", "manifest.json")
	content, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	drifted := strings.Replace(string(content), `"cli": "opencode"`, `"cli": "claude"`, 1)
	if drifted == string(content) {
		t.Fatalf("test setup: cli field not found in manifest:\n%s", content)
	}
	if err = os.WriteFile(manifest, []byte(drifted), 0o600); err != nil {
		t.Fatal(err)
	}
	err = runCampaignDoctor(t, a, "mini")
	if err == nil || !strings.Contains(err.Error(), `manifest cli "claude" != declared "opencode"`) {
		t.Fatalf("doctor must name the drifted member CLI, got: %v", err)
	}
}

func TestCampaignDoctorCatchesDisarmedGuard(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, home := miniGuestApp(t)
	if out, err := createMiniCampaign(t, a); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	// Disarm: the guarded name reverts to a plain executable (as if the real
	// tool were reinstalled over the symlink).
	guarded := filepath.Join(home, ".local", "bin", "cs-claude-remote-output")
	if err := os.Remove(guarded); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guarded, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := runCampaignDoctor(t, a, "mini")
	if err == nil || !strings.Contains(err.Error(), "guard not armed") {
		t.Fatalf("doctor must catch a disarmed guard, got: %v", err)
	}
}

func TestCampaignDoctorCatchesMissingMemberCLI(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, home := miniGuestApp(t)
	if out, err := createMiniCampaign(t, a); err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	if err := os.Remove(filepath.Join(home, ".local", "bin", "opencode")); err != nil {
		t.Fatal(err)
	}
	err := runCampaignDoctor(t, a, "mini")
	if err == nil || !strings.Contains(err.Error(), `declared CLI "opencode" not present`) {
		t.Fatalf("doctor must catch the absent member CLI, got: %v", err)
	}
}

func TestCreateFailsLoudlyWhenInstantiationIsBroken(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, home := miniGuestApp(t)
	// Sabotage before create: the member CLI never existed in the guest.
	if err := os.Remove(filepath.Join(home, ".local", "bin", "opencode")); err != nil {
		t.Fatal(err)
	}
	out, err := createMiniCampaign(t, a)
	if err == nil || !strings.Contains(err.Error(), "FAILED its doctor") || !strings.Contains(err.Error(), "do not dispatch") {
		t.Fatalf("create must fail loudly on a broken instantiation, got: %v\n%s", err, out)
	}
	campaign, loadErr := a.store.Load("mini")
	if loadErr != nil || campaign.Provisioning != "create-failed" {
		t.Fatalf("campaign provisioning after failed doctor = %+v, %v", campaign, loadErr)
	}
}

// campaign-doctor straddles two addressing planes and must not confuse them:
// it EXECS against a member's host reference (<name>.<group>), but compares the
// manifest against the bare in-group name, because that is what the orchestrator
// and the guard route by. Qualifying the manifest side would report drift on
// every well-formed campaign; leaving the exec side bare would reach the default
// group, or nothing at all.
func TestCampaignDoctorKeepsTheTwoAddressingPlanesApart(t *testing.T) {
	covmap.ProveCoreOnPass(t, "member-addressing", covmap.TierUnit)
	src, err := os.ReadFile("campaigndoctor.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	// Every exec goes through the qualified reference. Matched on refOutput,
	// the helper that owns the exec argv now — asserting on a literal `"exec",`
	// would read as a pass the moment the last direct call went away.
	if n := strings.Count(body, `refOutput(ctx, orchestrator.Sandbox`) + strings.Count(body, `refOutput(ctx, member.Sandbox`); n != 0 {
		t.Errorf("%d exec call(s) still address a member by bare name; they must use .Ref", n)
	}
	if strings.Count(body, `refOutput(ctx, orchestrator.Ref`)+strings.Count(body, `refOutput(ctx, member.Ref`) == 0 {
		t.Error("no exec call addresses a member by its host reference")
	}
	// And the manifest comparison stays bare.
	if !strings.Contains(body, "entry.Sandbox != member.Sandbox") {
		t.Error("the manifest comparison must stay on the bare in-group name")
	}
}

// A torn-down sandbox lingers in `cs-sandbox ls` as status "removed", and
// `inspect` answers "no such sandbox" for it. Probing that record made doctor
// report a perfectly good cs-sandbox as lacking `inspect --json`, which fails
// doctor and blocks every create on the host — observed 2026-08-06 with a single
// removed sandbox in the listing.
func TestInspectProbeSkipsUnresolvableSandboxes(t *testing.T) {
	removed := model.Sandbox{Ref: "gone.default", Name: "gone", Group: "default", Status: "removed"}
	running := model.Sandbox{Ref: "live.grp", Name: "live", Group: "grp", Status: "running"}

	if _, ok := inspectProbeTarget([]model.Sandbox{removed}); ok {
		t.Fatal("a removed sandbox was offered as an inspect probe target")
	}
	got, ok := inspectProbeTarget([]model.Sandbox{removed, running})
	if !ok || got.Ref != running.Ref {
		t.Fatalf("probe target = %+v (ok=%v), want %q", got, ok, running.Ref)
	}
	if _, ok := inspectProbeTarget(nil); ok {
		t.Fatal("an empty listing offered a probe target")
	}
	// Status casing and padding come from whatever cs-sandbox prints; matching
	// must not depend on it.
	if _, ok := inspectProbeTarget([]model.Sandbox{{Ref: "x.y", Status: " Removed "}}); ok {
		t.Fatal("status matching is case/space sensitive")
	}
	// An unknown future status stays probeable: the gate exists to catch a build
	// without `inspect --json`, not to enumerate sandbox states.
	if _, ok := inspectProbeTarget([]model.Sandbox{{Ref: "x.y", Status: "hibernating"}}); !ok {
		t.Fatal("an unknown status was treated as unprobeable")
	}
}
