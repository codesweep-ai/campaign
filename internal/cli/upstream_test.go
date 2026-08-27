package cli

// The upstream check must be provable in both directions — a host running what
// this build names reports ok, and every class of deviation (a cs-sandbox at
// another version, one that will not say what it is) fails doctor and refuses
// create LOUDLY. A refusal nobody has seen fire is decoration.
//
// The reference is no longer a file anyone writes. It is the go.mod embedded in
// this binary, which is also the go.mod these tests are compiled from, so the
// version a fake must report to look healthy is READ from it rather than
// spelled out here. A literal would be a second copy of the pin, and keeping
// two in agreement by hand is the arrangement this replaced.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/store"
)

// fakeSandbox writes a cs-sandbox stand-in reporting the given version and
// answering the two probes doctor makes before it reaches the upstream report,
// plus `agent-tools --json`.
//
// Group-aware on purpose: doctor gates on `group ls --json` first, so a fake
// without it would fail for the wrong reason and prove nothing about upstream.
func fakeSandbox(version string, tools map[string]string) string {
	encoded, _ := json.Marshal(struct {
		Version string            `json:"version"`
		Tools   map[string]string `json:"tools"`
	}{version, tools})
	return `
case "$1" in
  version) echo 'cs-sandbox ` + version + ` (linux/amd64, go1.27.0)';;
  ls) echo '[]';;
  group) echo '[]';;
  agent-tools) echo '` + string(encoded) + `';;
esac
`
}

// installUpstream fakes a COMPLETE host surface and returns the app plus the
// path of the fake binary, so a test can move the surface underneath a running
// check.
//
// The agent tools are installed too, not just the sandbox. Doctor checks them
// before it reaches the upstream report, so a helper that left them out would
// pass on a developer's machine — where a real ~/.local/bin is on PATH — and
// fail in CI, which is exactly what it did.
func installUpstream(t *testing.T, sandboxVersion string) (*app, string) {
	t.Helper()
	for _, cli := range []string{"claude", "codex", "opencode"} {
		for _, suffix := range []string{"-remote", "-remote-output", "-turn"} {
			installFakeTool(t, "cs"+"-"+cli+suffix, `exit 0`)
		}
	}
	dir := installFakeTool(t, "fake-sandbox", fakeSandbox(sandboxVersion, map[string]string{"cs-claude": strings.Repeat("a", 64)}))
	bin := filepath.Join(dir, "fake-sandbox")
	return &app{store: store.Store{Dir: t.TempDir()}, sandbox: sandboxCLI{Bin: bin}}, bin
}

// pinnedSandbox is the version a host must report to be the one this build
// names.
func pinnedSandbox(t *testing.T) string {
	t.Helper()
	v := toolPins()[sandboxModule]
	if v == "" {
		t.Fatal("the embedded go.mod names no cs-sandbox version; the whole check has nothing to compare against")
	}
	return v
}

func runDoctor(t *testing.T, a *app) (string, error) {
	t.Helper()
	var out strings.Builder
	cmd := a.doctorCmd()
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	err := cmd.Execute()
	return out.String(), err
}

// The manifest travels inside the binary, so there is always something to
// compare against. This is the property the whole design rests on: without it
// the check silently degrades to "no reference, everything passes", which is
// what the file-based pin did on any host that had never run `pin`.
func TestTheEmbeddedManifestNamesTheUpstream(t *testing.T) {
	pins := toolPins()
	if pins[sandboxModule] == "" {
		t.Fatal("no cs-sandbox pin read out of the embedded go.mod")
	}
	for _, tool := range siblingTools {
		if pins[tool.module] == "" {
			t.Errorf("%s is checked against go.mod but go.mod pins no version for it", tool.bin)
		}
	}
	// Nothing outside the family: a require line for cobra is not an upstream
	// pin, and letting one in would have doctor hunt for a `cobra` on PATH.
	for module := range pins {
		if !strings.HasPrefix(module, "github.com/codesweep-ai/") {
			t.Errorf("toolPins picked up %q, which is not a codesweep module", module)
		}
	}
}

func TestDoctorReportsAMatchingUpstream(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, _ := installUpstream(t, pinnedSandbox(t))
	out, err := runDoctor(t, a)
	if err != nil {
		t.Fatalf("doctor on a matching surface: %v\n%s", err, out)
	}
	if !strings.Contains(out, "cs-sandbox on PATH is the one this build names: "+pinnedSandbox(t)) {
		t.Fatalf("doctor must name the version it matched, got:\n%s", out)
	}
}

func TestDoctorFailsLoudlyOnVersionDrift(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, _ := installUpstream(t, "v0.0.0-20990101000000-ffffffffffff")
	out, err := runDoctor(t, a)
	if err == nil {
		t.Fatalf("doctor must fail when cs-sandbox is not the pinned one, got:\n%s", out)
	}
	// Asserted on the report rather than the error: doctor prints its findings
	// and returns a terse sentinel, so the report is what an operator reads.
	for _, want := range []string{
		"this build was made against",
		"v0.0.0-20990101000000-ffffffffffff",
		pinnedSandbox(t),
		"go install " + sandboxModule + "/cmd/cs-sandbox@",
		"issue(s) to fix above",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("drift report missing %q:\n%s", want, out)
		}
	}
}

// A failed or unreadable version probe must reach the operator with its cause.
// "(unknown)" alone reads the same whether the probe failed, timed out, or
// printed something no reading matches.
func TestUpstreamReportsWhyTheVersionProbeFailed(t *testing.T) {
	a, bin := installUpstream(t, pinnedSandbox(t))
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	report := a.verifyUpstream(context.Background())
	if len(report.Deviations) == 0 {
		t.Fatal("a failed version probe must be a deviation")
	}
	if !strings.Contains(report.Deviations[0], "version probe failed") {
		t.Fatalf("the cause must be named, got: %v", report.Deviations)
	}

	// Answering, but with something unreadable, is its own case: the probe
	// worked and the answer is still no version.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'not a version line'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	report = a.verifyUpstream(context.Background())
	if len(report.Deviations) == 0 || !strings.Contains(report.Deviations[0], "unrecognized cs-sandbox version output") {
		t.Fatalf("an unreadable answer must be named as such, got: %v", report.Deviations)
	}
}

func TestCreateRefusesDeviatingUpstreamUnlessAccepted(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	a, _ := installUpstream(t, "v0.0.0-20990101000000-ffffffffffff")

	// Wiring: create must refuse BEFORE touching the sandbox. The fake would
	// fail any provisioning call differently, so reaching it would surface as
	// the wrong failure text here.
	cmd := a.createCmd(false)
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"gate-test", "--orchestrator", "claude", "--agent", "worker=codex"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "not the one this cs-campaign was built against") ||
		!strings.Contains(err.Error(), "--accept-upstream-change") {
		t.Fatalf("create must refuse a deviating surface with the remedy, got: %v", err)
	}

	// The acceptance path records the deviation on the campaign — auditable,
	// never silent.
	campaign := &model.Campaign{Name: "gate-test"}
	if err := a.gateUpstream(context.Background(), &out, campaign, true); err != nil {
		t.Fatalf("accepted deviation must proceed: %v", err)
	}
	if campaign.Upstream == nil || !campaign.Upstream.Accepted {
		t.Fatalf("campaign must record the accepted deviation: %+v", campaign.Upstream)
	}
	if len(campaign.Upstream.Deviations) == 0 || !strings.Contains(campaign.Upstream.Deviations[0], "cs-sandbox on PATH is") {
		t.Fatalf("recorded deviations = %v", campaign.Upstream.Deviations)
	}

	// A surface this build names records cleanly, with no acceptance flag.
	clean := &model.Campaign{Name: "clean"}
	good, _ := installUpstream(t, pinnedSandbox(t))
	if err := good.gateUpstream(context.Background(), &out, clean, false); err != nil {
		t.Fatalf("a matching surface must pass the gate: %v", err)
	}
	if clean.Upstream == nil || clean.Upstream.Accepted || len(clean.Upstream.Deviations) != 0 {
		t.Fatalf("clean record = %+v", clean.Upstream)
	}
	if clean.Upstream.SandboxVersion != pinnedSandbox(t) {
		t.Fatalf("the record must name the version it saw: %+v", clean.Upstream)
	}
}

// The sibling tools are reported, never gating. A host that runs real campaigns
// has no cs-vcr and none of the gates, and must not be told its surface is
// broken for that — which is the whole reason they are Notes and not
// Deviations.
func TestSiblingToolsAreReportedButNeverGate(t *testing.T) {
	a, _ := installUpstream(t, pinnedSandbox(t))
	// A PATH holding the fake sandbox and the agent tools doctor requires, and
	// nothing else: every sibling is absent, and the checks BEFORE this one
	// still pass so a failure here can only be about siblings.
	bare := t.TempDir()
	for _, cli := range []string{"claude", "codex", "opencode"} {
		for _, suffix := range []string{"-remote", "-remote-output", "-turn"} {
			if err := os.WriteFile(filepath.Join(bare, "cs"+"-"+cli+suffix), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("PATH", bare+string(os.PathListSeparator)+filepath.Dir(a.sandbox.Bin))

	report := a.verifyUpstream(context.Background())
	if len(report.Deviations) != 0 {
		t.Fatalf("absent siblings must not deviate: %v", report.Deviations)
	}
	joined := strings.Join(report.Notes, "\n")
	if !strings.Contains(joined, "not on PATH (fine") {
		t.Fatalf("absent siblings must be reported as fine: %v", report.Notes)
	}
	for _, tool := range siblingTools {
		if !strings.Contains(joined, tool.bin) {
			t.Errorf("%s is not accounted for: %v", tool.bin, report.Notes)
		}
	}

	// One of them present at the wrong version: still a note, still not a gate.
	dir := installFakeTool(t, "cs-vcr", `case "$1" in version) echo 'cs-vcr v0.0.0-WRONG (linux/amd64, go1.27.0)';; esac`)
	t.Setenv("PATH", strings.Join([]string{dir, bare, filepath.Dir(a.sandbox.Bin)}, string(os.PathListSeparator)))
	report = a.verifyUpstream(context.Background())
	if len(report.Deviations) != 0 {
		t.Fatalf("a mismatched sibling must not refuse a campaign: %v", report.Deviations)
	}
	joined = strings.Join(report.Warnings, "\n")
	if !strings.Contains(joined, "cs-vcr on PATH is v0.0.0-WRONG") {
		t.Fatalf("a mismatched sibling must be named: %v", report.Warnings)
	}
	if !strings.Contains(joined, "go install github.com/codesweep-ai/vcr/cmd/cs-vcr@") {
		t.Fatalf("the warning must carry the command that agrees with the pin: %v", report.Warnings)
	}
	// A finding, not good news: rendered as an ok line it would read as the
	// opposite of what it says.
	if len(report.Notes) > 0 && strings.Contains(strings.Join(report.Notes, "\n"), "v0.0.0-WRONG") {
		t.Errorf("a mismatch must not be filed as a note: %v", report.Notes)
	}
	// And doctor still passes on it, because none of this stops a campaign.
	if out, err := runDoctor(t, a); err != nil {
		t.Fatalf("doctor must not fail on a sibling warning: %v\n%s", err, out)
	}
}

// A sibling that matches gets a line of its own. Printing nothing on a match
// leaves "compared, and they agree" indistinguishable from "never compared",
// which is the whole doubt these checks exist to remove.
func TestAMatchingSiblingIsReportedByName(t *testing.T) {
	a, _ := installUpstream(t, pinnedSandbox(t))
	pinned := toolPins()["github.com/codesweep-ai/vcr"]
	dir := installFakeTool(t, "cs-vcr", `case "$1" in version) echo 'cs-vcr `+pinned+` (linux/amd64, go1.27.0)';; esac`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+filepath.Dir(a.sandbox.Bin))

	report := a.verifyUpstream(context.Background())
	if len(report.Warnings) != 0 {
		t.Fatalf("a matching sibling is not a finding: %v", report.Warnings)
	}
	if !strings.Contains(strings.Join(report.Notes, "\n"), "cs-vcr on PATH matches this build ("+pinned+")") {
		t.Fatalf("a matching sibling must be named with its version: %v", report.Notes)
	}
}

// Go stamps +dirty on a binary built from a modified tree. A module version
// cannot carry that, so such a build can never equal a pin — and it must not be
// trimmed into agreement, because it is not the revision it names. This is the
// build somebody installs mid-campaign without meaning to.
func TestToolVersionKeepsTheDirtyStamp(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"cs-sandbox v0.0.0-20260826171442-c36e1fe91606 (linux/amd64, go1.27.0)", "v0.0.0-20260826171442-c36e1fe91606"},
		{"cs-sandbox v0.0.0-20260826171442-c36e1fe91606+dirty (linux/amd64, go1.27.0)", "v0.0.0-20260826171442-c36e1fe91606+dirty"},
		{"cs-sandbox 0.0.1-snapshot-dd879be (linux/amd64, go1.25.0)", "0.0.1-snapshot-dd879be"},
		{"cs-sandbox 6981299 (linux/amd64, go1.25.0)", "6981299"},
		{"", ""},
		{"cs-sandbox", ""},
	} {
		if got := toolVersion(tc.line); got != tc.want {
			t.Errorf("toolVersion(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// The member harness compares against what cs-sandbox says it ships, so a
// cs-sandbox that cannot answer leaves the fleet unmeasurable. That has to
// surface as an error naming the cause, never as an empty expectation that
// every member trivially matches.
func TestAgentToolHashesFailsLoudlyRatherThanEmpty(t *testing.T) {
	a, bin := installUpstream(t, pinnedSandbox(t))
	tools, err := a.agentToolHashes(context.Background())
	if err != nil {
		t.Fatalf("a healthy sandbox must answer: %v", err)
	}
	if tools["cs-claude"] == "" {
		t.Fatalf("the answer must carry the tools: %v", tools)
	}

	for _, tc := range []struct{ name, body, want string }{
		{"refuses", "#!/bin/sh\nexit 4\n", "agent-tools --json"},
		{"unreadable", "#!/bin/sh\necho 'not json'\n", "unreadable JSON"},
		{"empty", "#!/bin/sh\necho '{\"tools\":{}}'\n", "named no tools at all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(bin, []byte(tc.body), 0o700); err != nil {
				t.Fatal(err)
			}
			tools, err := a.agentToolHashes(context.Background())
			if err == nil {
				t.Fatalf("must not report a usable expectation, got %v", tools)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error must name the cause %q: %v", tc.want, err)
			}
		})
	}
}

// upstreamCases is what every fake cs-sandbox must answer before a create or a
// doctor gets past the upstream gate: the version this build names, and what it
// ships. Shared rather than repeated, and READ from the embedded manifest
// rather than written out, so bumping the cs-sandbox pin in go.mod never fails
// a suite of tests that are not about the pin.
func upstreamCases() string {
	shipped, _ := json.Marshal(struct {
		Version string            `json:"version"`
		Tools   map[string]string `json:"tools"`
	}{toolPins()[sandboxModule], shippedTools()})
	return "  version) echo 'cs-sandbox " + toolPins()[sandboxModule] + " (linux/amd64, go1.27.0)';;\n" +
		"  agent-tools) echo '" + string(shipped) + "';;\n"
}
