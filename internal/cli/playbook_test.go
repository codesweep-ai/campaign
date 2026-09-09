package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign"
	"github.com/codesweep-ai/campaign/internal/covmap"
)

// TestPlaybookIsTheEmbeddedFile is the manual's own embed assertion, for the
// second document the binary carries. //go:embed reads PLAYBOOK.md itself, so
// the shipped copy and the reviewable one are the same bytes; what is worth
// asserting is that it arrived. An empty embed ships a `playbook` verb that
// prints nothing, and nothing else notices.
func TestPlaybookIsTheEmbeddedFile(t *testing.T) {
	if len(campaign.PlaybookMD) < 1000 {
		t.Fatalf("embedded playbook is %d bytes; PLAYBOOK.md did not make it into the binary", len(campaign.PlaybookMD))
	}
	if !strings.HasPrefix(campaign.PlaybookMD, "# The cs-campaign playbook") {
		t.Errorf("embedded playbook does not start with the playbook's title")
	}
	var out bytes.Buffer
	cmd := new(app).playbookCmd()
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != campaign.PlaybookMD {
		t.Error("the playbook verb prints something other than the embedded document")
	}
}

// TestPlaybookStatesTheJudgementTheManualOmits holds the split the two documents
// are written to. The manual keeps every imperative and gains a pointer; the
// playbook carries the reasoning and the decisions nothing enforces. Both halves
// are asserted, because either one drifting alone recreates the gap: a rule that
// leaves the manual is a rule an agent reading `manual` mid-campaign never sees,
// and a playbook that stops covering a lifecycle stage is a stage with no
// judgement anywhere.
func TestPlaybookStatesTheJudgementTheManualOmits(t *testing.T) {
	covmap.ProveCoreOnPass(t, "profile-validation", covmap.TierUnit)
	for _, rule := range []string{
		"**Never type into a live session.**",
		"**Never answer a stalled orchestrator.**",
	} {
		if !strings.Contains(campaign.ManualMD, rule) {
			t.Errorf("MANUAL.md no longer states %q; the rule belongs on the page an agent reads mid-campaign", rule)
		}
		if !strings.Contains(campaign.PlaybookMD, rule) {
			t.Errorf("PLAYBOOK.md no longer states %q; the manual states it as a bare rule and this document owns the reasoning", rule)
		}
	}
	if !strings.Contains(campaign.ManualMD, "cs-campaign playbook") {
		t.Error("MANUAL.md no longer sends a reader to the playbook, so the judgement half is unreachable from the manual")
	}
	// The lifecycle this document is scoped to. A stage that leaves it is a
	// stage where an operator is back to inferring the answer from SPEC.
	for _, stage := range []string{
		"## Designing the team",
		"## Writing the mission",
		"## Writing the briefs",
		"## While it runs",
		"## Harvesting and closing out",
	} {
		if !strings.Contains(campaign.PlaybookMD, stage) {
			t.Errorf("PLAYBOOK.md no longer covers %q", stage)
		}
	}
}

// TestTheUnscaffoldedDocumentsAreTheOnesAMemberSees is SAC-022. `init` used to
// write a mission and a brief per member, and the check that refuses an
// unbriefed campaign asks only whether the file exists — so a scaffolded blank
// passed it and briefed a member with nothing, which is the substitution R30
// forbids. It also put authoring guidance in files that are seeded verbatim.
//
// Now only the profile is written, and the guidance lives in one place. Both
// halves are asserted, because either drifting alone reopens the defect: the
// scaffolder must not write a seeded document, and the playbook must still say
// what belongs under every heading those documents carry.
func TestTheUnscaffoldedDocumentsAreTheOnesAMemberSees(t *testing.T) {
	covmap.ProveCoreOnPass(t, "profile-validation", covmap.TierUnit)
	dir := t.TempDir()
	profile, err := profileFromFlags("codex", []string{"backend=claude"}, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := scaffoldCampaign(io.Discard, dir, "acme", profile); err != nil {
		t.Fatal(err)
	}
	for _, seeded := range []string{missionFileName, "roles/backend.md", "roles/orchestrator.md"} {
		if _, err := os.Stat(filepath.Join(dir, seeded)); !os.IsNotExist(err) {
			t.Errorf("init wrote %s; a blank one passes the check meant to refuse an unbriefed fleet", seeded)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "profile.yaml")); err != nil {
		t.Errorf("init no longer writes the one file it should: %v", err)
	}
	// The guidance the stubs used to carry has one home now. It is anchored to the
	// readback's three questions rather than to headings: nothing parses a brief,
	// so heading conformance is not a thing to hold, and the fields a member is
	// actually asked are.
	for _, anchor := range []string{
		"`goal`", "`scope`", "`obligations`",
		"only suggestions",
		"## Definition of done", "## Out of scope",
	} {
		if !strings.Contains(campaign.PlaybookMD, anchor) {
			t.Errorf("PLAYBOOK.md no longer anchors its brief guidance on %q", anchor)
		}
	}
	if !strings.Contains(campaign.PlaybookMD, "manufacture\ncoverage") &&
		!strings.Contains(campaign.PlaybookMD, "manufacture coverage") {
		t.Error("PLAYBOOK.md no longer warns that a countable gate invites a member to manufacture what is counted")
	}
}
