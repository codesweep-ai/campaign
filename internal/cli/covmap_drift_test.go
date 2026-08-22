package cli

// Rubric drift guard: every member-facing command must map to a registered
// coverage behavior, and every command in the tree must be classified — so
// adding a command without deciding its place in the coverage universe fails
// here, and a behavior id referenced below cannot silently leave the rubric.

import (
	"testing"

	"github.com/codesweep-ai/campaign/internal/covmap"
)

// memberFacingBehaviors maps command name → rubric behavior id.
var memberFacingBehaviors = map[string]string{
	"send":       "prompt-await",     // the dispatch-and-reply rule, one verb
	"restart":    "restart-resume",   // rung two of the ladder, operator-invoked
	"transcript": "output-retrieval", // raw session transcript, forensics only
	"archive":    "archive-evidence",
}

// exemptCommands are infrastructure/introspection surfaces with no
// per-adapter behavior row of their own.
var exemptCommands = map[string]bool{
	"init": true, "create": true, "plan": true, "validate": true, "destroy": true,
	"ls": true, "ssh": true, "fetch": true, "observe": true, "audit": true,
	"doctor": true, "pin": true, "version": true, "manual": true,
	"completion": true, "help": true,
}

func TestEveryCommandIsClassifiedInCoverageRubric(t *testing.T) {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := covmap.LoadRegistry(root)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{}
	seen := map[string]bool{}
	for _, cmd := range a.root().Commands() {
		name := cmd.Name()
		seen[name] = true
		if exemptCommands[name] {
			continue
		}
		behavior, ok := memberFacingBehaviors[name]
		if !ok {
			t.Errorf("command %q is unclassified: map it to a rubric behavior or exempt it explicitly", name)
			continue
		}
		if _, ok := reg.Lookup(behavior); !ok {
			t.Errorf("command %q maps to behavior %q, which is not in covmap/behaviors.json", name, behavior)
		}
	}
	for name := range memberFacingBehaviors {
		if !seen[name] {
			t.Errorf("mapped command %q no longer exists in the command tree", name)
		}
	}
}
