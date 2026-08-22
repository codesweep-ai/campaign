package cli

import (
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign"
)

// TestManualIsTheEmbeddedFile needs no byte comparison: //go:embed reads MANUAL.md
// itself, so the shipped copy and the reviewable one are the same bytes. What is
// worth asserting is that it arrived at all — an empty embed would ship a binary
// whose `manual` verb prints nothing, and nothing else would notice.
func TestManualIsTheEmbeddedFile(t *testing.T) {
	if len(campaign.ManualMD) < 1000 {
		t.Fatalf("embedded manual is %d bytes; MANUAL.md did not make it into the binary", len(campaign.ManualMD))
	}
	if !strings.HasPrefix(campaign.ManualMD, "# The cs-campaign manual") {
		t.Errorf("embedded manual does not start with the manual's title")
	}
}

// TestManualNamesEveryCommand is the drift gate. Prose describing a CLI rots the
// moment nobody is looking, and a manual carried inside the binary is exactly the
// place a reader trusts it. Asserting the link is cheaper than remembering to
// update it: add a command without documenting it and this fails.
//
// `help` and `completion` are exempt because Cobra generates them; they are not
// this tool's surface.
func TestManualNamesEveryCommand(t *testing.T) {
	generated := map[string]bool{"help": true, "completion": true}

	a := &app{}
	var missing []string
	for _, c := range a.root().Commands() {
		name := c.Name()
		if generated[name] {
			continue
		}
		if !strings.Contains(campaign.ManualMD, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("MANUAL.md does not name these commands: %v\n"+
			"document them, or the manual is lying by omission to a reader who has only the binary", missing)
	}
}
