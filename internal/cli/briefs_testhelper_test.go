package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// seedBriefsFor writes the campaign inputs a profile-based create now requires:
// a mission beside the profile and one role brief per declared member.
//
// It exists so unit tests state the thing they are actually testing. Every
// command-level test that builds a profile needs these files, and hand-writing
// them in each one would bury the assertion under fixture noise — while making
// the requirement itself easy to weaken by accident later.
func seedBriefsFor(t *testing.T, profilePath string, members ...string) {
	t.Helper()
	dir := filepath.Dir(profilePath)
	if err := os.WriteFile(filepath.Join(dir, missionFileName), []byte("# Mission\n\nA test campaign.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	roles := filepath.Join(dir, rolesDirName)
	if err := os.MkdirAll(roles, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, m := range append([]string{"orchestrator"}, members...) {
		body := "# Role: " + m + "\n\nA test brief.\n"
		if err := os.WriteFile(filepath.Join(roles, m+".md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
