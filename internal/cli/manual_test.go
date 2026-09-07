package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign"
	"github.com/codesweep-ai/campaign/internal/covmap"
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

// TestManualNamesEveryOverridePath is the same drift gate for --set. The
// override list is a closed allowlist an operator cannot discover by reading a
// profile, so the manual is the only place it exists. Adding a path without
// documenting it leaves the operator guessing at a switch statement.
func TestManualNamesEveryOverridePath(t *testing.T) {
	var missing []string
	for _, p := range setPaths {
		if !strings.Contains(campaign.ManualMD, p.path) {
			missing = append(missing, p.path)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("MANUAL.md does not name these --set paths: %v\n"+
			"document them, or an operator learns the allowlist by having a path refused", missing)
	}
}

// TestSpecNamesEveryCommand is TestManualNamesEveryCommand for the other
// document that prints the surface. SPEC 3.1 lists the verbs as pipe-separated
// alternatives inside a fenced block, which the surface linter reads as a shell
// pipeline rather than as a list of commands. Nothing else held it, and it drifted
// twice: it kept naming `pin` for six months after the verb was removed, and it
// never gained `orientation`.
func TestSpecNamesEveryCommand(t *testing.T) {
	covmap.ProveCoreOnPass(t, "profile-validation", covmap.TierUnit)
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Skip("no repository root, so SPEC.md is not readable from here")
	}
	spec, err := os.ReadFile(filepath.Join(root, "SPEC.md"))
	if err != nil {
		t.Skip("SPEC.md is not in this tree")
	}
	named := specSurfaceVerbs(string(spec))
	if len(named) == 0 {
		t.Fatal("SPEC.md 3.1 has no readable command block; this test cannot hold anything")
	}
	generated := map[string]bool{"help": true, "completion": true}
	have := map[string]bool{}
	for _, c := range new(app).root().Commands() {
		if generated[c.Name()] {
			continue
		}
		have[c.Name()] = true
		if !named[c.Name()] {
			t.Errorf("SPEC.md 3.1 does not name %q; the section claims to be the host command surface", c.Name())
		}
	}
	for verb := range named {
		if !have[verb] {
			t.Errorf("SPEC.md 3.1 names %q and the binary has no such verb", verb)
		}
	}
}

// specSurfaceVerbs reads the verbs out of the fenced block under 3.1. Each line
// is `cs-campaign a|b|c  # what they are for`, so the verbs are the second field
// split on the pipe; everything after it is a placeholder for an argument.
func specSurfaceVerbs(spec string) map[string]bool {
	const heading = "### 3.1 The host command surface"
	start := strings.Index(spec, heading)
	if start < 0 {
		return nil
	}
	block := spec[start:]
	open := strings.Index(block, "```sh")
	if open < 0 {
		return nil
	}
	block = block[open+len("```sh"):]
	if end := strings.Index(block, "```"); end >= 0 {
		block = block[:end]
	}
	verbs := map[string]bool{}
	for line := range strings.SplitSeq(block, "\n") {
		if cut := strings.Index(line, "#"); cut >= 0 {
			line = line[:cut]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "cs-campaign" {
			continue
		}
		for verb := range strings.SplitSeq(fields[1], "|") {
			if verb != "" {
				verbs[verb] = true
			}
		}
	}
	return verbs
}
