package cli

// The one documentation-claim regression that survives the dispatch-protocol
// redesign: state safety. The rest of this file used to pin the await/journal
// machinery, which the protocol deleted — completion is a reply artifact now,
// and no dispatch state is stored to be journaled or clobbered.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/store"
)

func TestCorruptStateIsNotSilentlyOmittedFromLs(t *testing.T) {
	covmap.ProveCoreOnPass(t, "state-safety", covmap.TierUnit)
	stateDir := t.TempDir()
	a := &app{store: store.Store{Dir: stateDir}}
	good := &model.Campaign{Name: "good"}
	if err := a.store.Save(good); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	cmd := a.lsCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ls: %v", err)
	}
	if !strings.Contains(out.String(), "broken.json") || !strings.Contains(out.String(), "good") {
		t.Fatalf("ls output must list good campaigns and warn about corrupt ones:\n%s", out.String())
	}
}
