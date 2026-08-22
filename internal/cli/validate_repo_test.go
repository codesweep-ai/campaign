package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/covmap"
)

func writeValidateProfile(t *testing.T, dir, repo string) string {
	t.Helper()
	profile := filepath.Join(dir, "profile.yaml")
	content := fmt.Sprintf(`apiVersion: codesweep.ai/v1alpha1
kind: CampaignProfile
orchestrator:
  cli: codex
  repos:
    - path: %s
agents:
  worker:
    cli: codex
`, repo)
	if err := os.WriteFile(profile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	seedBriefsFor(t, profile, "worker")
	return profile
}

// SPEC.md § 9: validate must accept exactly what create accepts — an absent or
// empty repo path is initializable, while non-git content is rejected.
func TestValidateMatchesCreateOnRepoPaths(t *testing.T) {
	covmap.ProveCoreOnPass(t, "plan-determinism", covmap.TierUnit)
	dir := t.TempDir()
	var out strings.Builder
	validate := func(profile string) error {
		c := (&app{}).validateCmd()
		c.SetOut(&out)
		c.SetErr(&out)
		c.SetArgs([]string{profile})
		return c.Execute()
	}
	if err := validate(writeValidateProfile(t, dir, filepath.Join(dir, "does-not-exist-yet"))); err != nil {
		t.Fatalf("validate rejected an initializable (absent) repo path: %v", err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validate(writeValidateProfile(t, dir, empty)); err != nil {
		t.Fatalf("validate rejected an initializable (empty) repo path: %v", err)
	}
	full := filepath.Join(dir, "full")
	if err := os.MkdirAll(full, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "data.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validate(writeValidateProfile(t, dir, full)); err == nil {
		t.Fatal("validate accepted a non-empty non-git repo path that create rejects")
	}
}
