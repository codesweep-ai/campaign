package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/store"
	"go.yaml.in/yaml/v3"
)

func memberNamed(name string) model.Member {
	role := "agent"
	if name == "orchestrator" {
		role = "orchestrator"
	}
	return model.Member{Name: name, Role: role}
}

// writeProfile puts a profile on disk without any briefs beside it, which is
// the state an author is in when they want to read the orientation.
func writeProfile(t *testing.T, dir string) string {
	t.Helper()
	encoded, err := yaml.Marshal(testProfile())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "profile.yaml")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runOrientation(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	a := &app{store: store.Store{Dir: t.TempDir()}}
	var out, errOut bytes.Buffer
	cmd := a.orientationCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// The command's whole value is that it prints the TEXT rather than a
// description of it. A summary maintained beside the template is a second copy
// that drifts, and a brief author who reads the drifted copy is back to
// guessing — which is the defect this command exists to close.
func TestOrientationPrintsTheTextTheMemberWillReceive(t *testing.T) {
	dir := t.TempDir()
	profile := writeProfile(t, dir)
	seedBriefsFor(t, profile, "backend", "qa")

	out, _, err := runOrientation(t, "acme", "--profile", profile, "--member", "backend")
	if err != nil {
		t.Fatalf("orientation: %v", err)
	}

	campaign, prof, err := (&app{}).planCampaign(createOpts{profile: profile}, "acme", true)
	if err != nil {
		t.Fatal(err)
	}
	inputs, _, err := plannedCampaignInputs(profile, prof)
	if err != nil {
		t.Fatal(err)
	}
	var backend = campaign.Members[0]
	for _, m := range campaign.Members {
		if m.Name == "backend" {
			backend = m
		}
	}
	want, _, err := buildOrientation(campaign, backend, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if out != want {
		t.Errorf("printed text is not what create renders:\n--- printed ---\n%s\n--- create ---\n%s", out, want)
	}
}

// Reading the orientation is what an author does BEFORE writing a brief, and
// `init` refuses to scaffold a stub for a member added to a profile later. A
// command that required the brief to exist would therefore refuse in exactly
// the case that produced the guesswork.
func TestOrientationAnswersForAMemberWithNoBriefYet(t *testing.T) {
	dir := t.TempDir()
	profile := writeProfile(t, dir)

	out, errOut, err := runOrientation(t, "acme", "--profile", profile, "--member", "qa")
	if err != nil {
		t.Fatalf("orientation refused a fleet whose briefs are unwritten: %v", err)
	}
	if !strings.Contains(out, "You are `qa`") {
		t.Errorf("did not render the member's orientation:\n%s", out)
	}
	// The member will be told to expect this file, so the answer names it and
	// says it is not there yet rather than quietly omitting it.
	if !strings.Contains(out, "qa.md") {
		t.Errorf("input list omits the brief the member will be given:\n%s", out)
	}
	if !strings.Contains(errOut, "roles/qa.md") {
		t.Errorf("stderr does not name the brief that is still unwritten:\n%s", errOut)
	}
}

// create must keep refusing what inspection tolerates. R29 says a fleet is not
// creatable until every member has a written purpose, and the tolerant resolver
// added for `orientation` must not have loosened it.
func TestUnbriefedFleetIsStillRefusedByValidate(t *testing.T) {
	dir := t.TempDir()
	profile := writeProfile(t, dir)
	if _, err := loadCampaignInputs(profile, testProfile()); err == nil {
		t.Fatal("loadCampaignInputs accepted a fleet with no briefs")
	}
	in, absent, err := plannedCampaignInputs(profile, testProfile())
	if err != nil {
		t.Fatal(err)
	}
	if len(absent) != 4 {
		t.Errorf("expected the mission and three briefs to be reported absent, got %v", absent)
	}
	// Nothing may be seeded from a set that describes files which do not exist.
	for _, m := range []string{"orchestrator", "backend"} {
		if cmd := in.seedCommand(memberNamed(m)); cmd != "" {
			t.Errorf("a planned input set produced a seed command for %s: %q", m, cmd)
		}
	}
}

// The rendered text goes to stdout and the guidance to stderr, so redirecting
// stdout yields the orientation and nothing else. An author who pipes this into
// a file must not find a lecture appended to the member's own doctrine.
func TestOrientationSeparatesTextFromGuidance(t *testing.T) {
	dir := t.TempDir()
	profile := writeProfile(t, dir)
	seedBriefsFor(t, profile, "backend", "qa")

	out, errOut, err := runOrientation(t, "acme", "--profile", profile)
	if err != nil {
		t.Fatalf("orientation: %v", err)
	}
	if strings.Contains(out, "Do not restate") {
		t.Error("guidance leaked into the rendered orientation on stdout")
	}
	if !strings.Contains(errOut, "Do not restate") {
		t.Errorf("stderr does not carry the rule the command exists to serve:\n%s", errOut)
	}
	// Without --member every member is answered for, each named.
	for _, want := range []string{"orchestrator", "backend", "qa"} {
		if !strings.Contains(out, "===== "+want+" (") {
			t.Errorf("no section for %s:\n%s", want, out)
		}
	}
}

func TestOrientationNamesTheMembersItHasWhenAskedForOneItDoesNot(t *testing.T) {
	dir := t.TempDir()
	profile := writeProfile(t, dir)
	_, _, err := runOrientation(t, "acme", "--profile", profile, "--member", "nope")
	if err == nil {
		t.Fatal("expected an unknown member to fail")
	}
	for _, want := range []string{"nope", "orchestrator", "backend", "qa"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
}
