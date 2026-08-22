package frames

import (
	"regexp"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign"
)

// findingsHeading opens the one section of the manual this test reads. The
// manual is full of other tables whose first column is a backticked token, so
// the section is sliced out before the rows are matched rather than matching
// the whole file.
const findingsHeading = "### Findings reference"

// The findings registry lives twice on purpose — issueDefs feeds the page's
// tooltips, the manual's reference table serves readers — and this test is
// what keeps the two from drifting: every defined code must be documented,
// and every documented code must be defined.
func TestFindingCodesMatchManual(t *testing.T) {
	start := strings.Index(campaign.ManualMD, findingsHeading)
	if start < 0 {
		t.Fatalf("MANUAL.md has no %q section", findingsHeading)
	}
	section := campaign.ManualMD[start+len(findingsHeading):]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}
	for code := range issueDefs {
		if !strings.Contains(section, "`"+code+"`") {
			t.Errorf("finding %q is defined in issueDefs but missing from the manual's reference table", code)
		}
	}
	// Backticked kebab-case tokens opening a row of that table.
	row := regexp.MustCompile("(?m)^\\| `([a-z][a-z0-9-]*)`(?: / `([a-z][a-z0-9-]*)`)? \\|")
	found := 0
	for _, m := range row.FindAllStringSubmatch(section, -1) {
		for _, code := range m[1:] {
			if code == "" {
				continue
			}
			found++
			if _, ok := issueDefs[code]; !ok {
				t.Errorf("the manual documents finding %q which issueDefs does not define", code)
			}
		}
	}
	if found < len(issueDefs) {
		t.Errorf("the manual names %d codes; issueDefs defines %d — table rows may not be parsing", found, len(issueDefs))
	}
}
