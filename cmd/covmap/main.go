// Command covmap folds the untracked run buffer into the tracked
// covmap/results.json (pruning records whose proving test no longer
// exists) and re-renders covmap/covmap.html. Run from anywhere in the
// repo (`make covmap`).
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/codesweep-ai/campaign/internal/covmap"
)

func main() {
	root, err := covmap.FindRepoRoot(".")
	if err != nil {
		fatal(err)
	}
	reg, err := covmap.LoadRegistry(root)
	if err != nil {
		fatal(err)
	}
	existing, err := covmap.LoadResults(root)
	if err != nil {
		fatal(err)
	}
	buffer := covmap.BufferPath(root)
	buffered, err := covmap.ReadBuffer(buffer)
	if err != nil {
		fatal(err)
	}
	exists, err := covmap.ExistsSets(root)
	if err != nil {
		fatal(err)
	}
	folded, err := covmap.Fold(reg, existing, buffered, exists)
	if err != nil {
		fatal(err)
	}
	if err := covmap.SaveResults(root, folded); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "covmap", "covmap.html"),
		[]byte(covmap.Render(reg, folded)), 0o644); err != nil {
		fatal(err)
	}
	_ = os.Remove(buffer) // consumed
	fmt.Printf("coverage: %d records (%d newly folded), %d adapter behaviors, %d core behaviors\n",
		len(folded.Records), len(buffered), len(reg.Rows("adapter")), len(reg.Rows("core")))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "covmap:", err)
	os.Exit(1)
}
