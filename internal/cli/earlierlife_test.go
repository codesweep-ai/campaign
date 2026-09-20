package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/model"
)

// A campaign made again from the same profile gets the same session names. The
// host's markers from the first one used to survive destroy, so the second
// campaign resumed sessions that its brand new machines had never held.
func TestANewMemberForgetsTheSessionOfAnEarlierLife(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sessions := filepath.Join(home, ".cs-codex-remote-sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	member := model.Member{Name: "dev", CLI: "codex"}
	member.Session.Name = "demo-1a2b-dev"
	for _, f := range []string{member.Session.Name, member.Session.Name + ".token"} {
		if err := os.WriteFile(filepath.Join(sessions, f), []byte("stale\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if hostSessionFresh(member) {
		t.Fatal("the stale markers should read as a session to resume: that is the defect's precondition")
	}

	bin := t.TempDir()
	// Only this directory and the system's: the real forget tool may be installed.
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	a := &app{}

	// With no forget tool the markers cannot be dropped, and create must say so
	// by name rather than go on to resume a session that is not there.
	err := a.forgetEarlierLife(context.Background(), member)
	if err == nil || !strings.Contains(err.Error(), member.Session.Name) || !strings.Contains(err.Error(), "cs-codex-remote-forget") {
		t.Fatalf("an unforgettable stale session must fail create naming it and the remedy; got: %v", err)
	}

	forget := "#!/bin/sh\nrm -f \"$HOME/.cs-codex-remote-sessions/$1\" \"$HOME/.cs-codex-remote-sessions/$1.token\"\n"
	if err := os.WriteFile(filepath.Join(bin, "cs-codex-remote-forget"), []byte(forget), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := a.forgetEarlierLife(context.Background(), member); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if !hostSessionFresh(member) {
		t.Fatal("the new member would still resume the earlier campaign's session")
	}
}
