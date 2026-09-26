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
	"fmt"
	"io"
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
// The agent tools are installed too, not just the sandbox, and they are the
// bytes the fake says it ships: doctor compares the two. A helper that left
// them out would pass on a developer's machine — where a real ~/.local/bin is
// on PATH — and fail in CI, which is exactly what it did.
func installUpstream(t *testing.T, sandboxVersion string) (*app, string) {
	t.Helper()
	tools := t.TempDir()
	installShippedTools(t, tools)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := installFakeTool(t, "fake-sandbox", fakeSandbox(sandboxVersion, shippedTools()))
	bin := filepath.Join(dir, "fake-sandbox")
	return &app{store: store.Store{Dir: t.TempDir()}, sandbox: sandboxCLI{Bin: bin}}, bin
}

// pinnedSandbox is the version a host must report to be the one this build
// names.
// installShippedTools writes every agent tool the fake cs-sandbox ships into
// dir, as a faithful install would.
func installShippedTools(t *testing.T, dir string) {
	t.Helper()
	for _, name := range shippedToolNames() {
		if err := os.WriteFile(filepath.Join(dir, name), fakeToolBytes(), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

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
	// The other direction: a pin with no entry is a tool on PATH that doctor
	// never compares, which reads exactly like one that matched.
	listed := map[string]bool{sandboxModule: true}
	for _, tool := range siblingTools {
		listed[tool.module] = true
	}
	for module := range pins {
		if !listed[module] {
			t.Errorf("go.mod pins %s but siblingTools does not name it, so doctor never checks its pin", module)
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
	if !strings.Contains(out, "cs-sandbox on PATH matches the pin ("+pinnedSandbox(t)+")") {
		t.Fatalf("doctor must name the version it matched, got:\n%s", out)
	}
	if want := fmt.Sprintf("the %d on PATH match cs-sandbox %s", len(shippedToolNames()), pinnedSandbox(t)); !strings.Contains(out, want) {
		t.Fatalf("doctor must say the agent tools are the ones cs-sandbox ships (%q), got:\n%s", want, out)
	}
}

// The agent tools are required here: cs-campaign starts every turn through
// them, and a host-driven dispatch copies the host's cs-<cli>-turn into the
// member. So a missing one or a stale one fails doctor, naming which.
func TestDoctorFailsOnAgentToolsTheSandboxDoesNotShip(t *testing.T) {
	a, _ := installUpstream(t, pinnedSandbox(t))
	// Only these tools and the fake: a developer's own ~/.local/bin would
	// otherwise answer for the one removed below.
	tools := t.TempDir()
	installShippedTools(t, tools)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+filepath.Dir(a.sandbox.Bin))
	if err := os.WriteFile(filepath.Join(tools, "cs-claude-turn"), []byte("#!/bin/sh\n# an older driver\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(tools, "cs-codex-remote")); err != nil {
		t.Fatal(err)
	}
	out, err := runDoctor(t, a)
	if err == nil {
		t.Fatalf("doctor must fail on agent tools cs-sandbox does not ship:\n%s", out)
	}
	for _, want := range []string{
		"missing from PATH: cs-codex-remote",
		"on PATH but not the ones cs-sandbox " + pinnedSandbox(t) + " ships",
		"cs-claude-turn differs",
		"cs-sandbox install-agent-tools",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q:\n%s", want, out)
		}
	}
}

// The agent CLIs are optional in both doctors: nothing on the host runs them,
// and a host without any of them is complete.
func TestDoctorReportsAbsentAgentCLIsAsFine(t *testing.T) {
	a, _ := installUpstream(t, pinnedSandbox(t))
	tools := t.TempDir()
	installShippedTools(t, tools)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+filepath.Dir(a.sandbox.Bin))
	out, err := runDoctor(t, a)
	if err != nil {
		t.Fatalf("doctor must pass on a host without agent CLIs: %v\n%s", err, out)
	}
	if !strings.Contains(out, "not on PATH (fine — nothing here needs them): claude codex opencode") {
		t.Fatalf("absent agent CLIs must be reported as fine:\n%s", out)
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
		"this build pins",
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
	cmd.SetArgs([]string{"gate-test", "--orchestrator", "claude", "--agent", "worker=codex",
		"--repo", filepath.Join(t.TempDir(), "app")})
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
	installShippedTools(t, bare)
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
	if !strings.Contains(strings.Join(report.Notes, "\n"), "cs-vcr on PATH matches the pin ("+pinned+")") {
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

// imagingSandbox is a cs-sandbox stand-in at the pinned version that names its
// two images, and a podman holding the ones in held, each as "ref id revision".
func imagingSandbox(t *testing.T, held ...string) *app {
	t.Helper()
	pinned := pinnedSandbox(t)
	dir := installFakeTool(t, "fake-sandbox", `
case "$1 $2" in
  "version --images") printf 'image              ghcr.io/test/sandbox:`+pinned+`\nimage-local        localhost/test/sandbox:`+pinned+`\n';;
  version*) echo 'cs-sandbox `+pinned+` (linux/amd64, go1.27.0)';;
esac
`)
	var body strings.Builder
	body.WriteString("for ref; do :; done\ncase \"$ref\" in\n")
	for _, h := range held {
		f := strings.Fields(h)
		fmt.Fprintf(&body, "  %s) echo '%s %s'; exit 0;;\n", f[0], f[1], f[2])
	}
	body.WriteString("esac\nexit 125")
	installFakeTool(t, "podman", body.String())
	return &app{store: store.Store{Dir: t.TempDir()}, sandbox: sandboxCLI{Bin: filepath.Join(dir, "fake-sandbox")}}
}

// TestUpstreamRecordsTheImageMembersBoot: the version names what CI publishes,
// and a build of it made here carries a localhost/ name of its own, so the
// record names the image too: its reference, ID and revision. The published
// image wins where the host holds both, as it does for cs-sandbox create.
func TestUpstreamRecordsTheImageMembersBoot(t *testing.T) {
	pinned := pinnedSandbox(t)
	pub, loc := "ghcr.io/test/sandbox:"+pinned, "localhost/test/sandbox:"+pinned
	for _, c := range []struct {
		name string
		held []string
		want model.ImageIdentity
	}{
		{"the local build alone", []string{loc + " sha256:1111 abc123"}, model.ImageIdentity{Ref: loc, ID: "sha256:1111", Revision: "abc123"}},
		{"both", []string{pub + " sha256:2222 def456", loc + " sha256:1111 abc123"}, model.ImageIdentity{Ref: pub, ID: "sha256:2222", Revision: "def456"}},
		{"neither", nil, model.ImageIdentity{Ref: pub}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("CS_SANDBOX_IMAGE", "")
			a := imagingSandbox(t, c.held...)
			campaign := &model.Campaign{Name: "image"}
			if err := a.gateUpstream(context.Background(), io.Discard, campaign, false); err != nil {
				t.Fatal(err)
			}
			if got := campaign.Upstream.SandboxImage; got == nil || *got != c.want {
				t.Fatalf("recorded image = %+v, want %+v", got, c.want)
			}
			notes := strings.Join(campaign.Upstream.Notes, "\n")
			if !strings.Contains(notes, "members boot "+c.want.Ref) {
				t.Errorf("notes do not name the image: %v", campaign.Upstream.Notes)
			}

			root := t.TempDir()
			a.archiveUpstreamFingerprint(context.Background(), root)
			var fp struct {
				SandboxImage *model.ImageIdentity `json:"sandboxImage"`
			}
			data, err := os.ReadFile(filepath.Join(root, "upstream-fingerprint.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &fp); err != nil || fp.SandboxImage == nil || *fp.SandboxImage != c.want {
				t.Errorf("fingerprint image = %+v (%v), want %+v", fp.SandboxImage, err, c.want)
			}
		})
	}
}

// TestUpstreamRecordsTheImageCSSandboxImageNames: a name the operator chose is
// the image members boot, whatever cs-sandbox would name.
func TestUpstreamRecordsTheImageCSSandboxImageNames(t *testing.T) {
	t.Setenv("CS_SANDBOX_IMAGE", "localhost/pinned:7")
	a := imagingSandbox(t, "localhost/pinned:7 sha256:7777 fedcba")
	report := a.verifyUpstream(context.Background())
	if want := (model.ImageIdentity{Ref: "localhost/pinned:7", ID: "sha256:7777", Revision: "fedcba"}); report.SandboxImage == nil || *report.SandboxImage != want {
		t.Fatalf("image = %+v, want %+v", report.SandboxImage, want)
	}
}

// TestUpstreamNamesTheBinaryCSSandboxBinPicks: a deviation says which binary it
// asked, since CS_SANDBOX_BIN can put one other than PATH's in front of it.
func TestUpstreamNamesTheBinaryCSSandboxBinPicks(t *testing.T) {
	a, bin := installUpstream(t, "v0.0.0-20990101000000-ffffffffffff")
	t.Setenv("CS_SANDBOX_BIN", bin)
	report := a.verifyUpstream(context.Background())
	if len(report.Deviations) == 0 || !strings.Contains(report.Deviations[0], "cs-sandbox at "+bin+" (CS_SANDBOX_BIN) is") {
		t.Fatalf("deviations = %v", report.Deviations)
	}
}
