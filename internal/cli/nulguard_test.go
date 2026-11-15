package cli

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/store"
)

// OpenCode 2 leaves a shell call whose command holds a NUL byte running for
// good, and the seat waits on it until something outside kills the turn. Every
// OpenCode member is given the guard, whatever its role, and on a resumed
// create as well as a fresh one: a member that is already running when create
// resumes was provisioned by an attempt that may have stopped before this step.
// A member on another CLI is given nothing.
func TestEveryOpenCodeMemberGetsTheNulGuard(t *testing.T) {
	if !strings.Contains(openCodeNulGuard, `cmd.includes("\u0000")`) || !strings.Contains(openCodeNulGuard, "throw new Error(") {
		t.Fatal("the embedded plugin no longer refuses a command that holds a NUL byte")
	}
	ensureGuestBinary(t)
	for _, cli := range []string{"opencode", "claude", "codex"} {
		for _, role := range []string{"orchestrator", "agent"} {
			for _, running := range []bool{false, true} {
				dir := t.TempDir()
				tool := filepath.Join(dir, "fake-sandbox")
				capture := filepath.Join(dir, "capture")
				body := `#!/bin/sh
case "$1" in
  inspect) printf '%s\n' '{"ref":"'"$2"'","ip":"10.89.0.9","repos":[]}' ;;
  exec)
    case "$5" in
      *"$GUARD_PATH"*) cat >> "$CAPTURE" ;;
      *) cat > /dev/null ;;
    esac ;;
esac
`
				if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("CAPTURE", capture)
				t.Setenv("GUARD_PATH", opencodeNulGuardPlugin)
				// A new machine makes create look for the host's record of an earlier
				// session; this keeps that look, and any forget it runs, off the real home.
				t.Setenv("HOME", filepath.Join(dir, "home"))
				a := &app{store: store.Store{Dir: filepath.Join(dir, "state")}, sandbox: sandboxCLI{Bin: tool}}
				campaign := &model.Campaign{Name: "camp", Group: "camp-grp", Network: "net", Members: []model.Member{
					{Name: "seat", Role: role, CLI: cli, Sandbox: "seat", Ref: "seat.camp-grp", Session: model.Session{Name: "camp-seat"}},
				}}
				var live []model.Sandbox
				if running {
					live = []model.Sandbox{{Ref: "seat.camp-grp", Group: "camp-grp", Status: "running"}}
				}
				if err := a.provisionMember(t.Context(), campaign, &campaign.Members[0], live, campaignInputs{}); err != nil {
					t.Fatalf("%s %s (running %v): %v", cli, role, running, err)
				}
				// The guest command reads the file's content, base64, from stdin.
				payload, err := os.ReadFile(capture)
				if installed := err == nil; installed != (cli == "opencode") {
					t.Fatalf("%s %s (running %v): guard installed = %v", cli, role, running, installed)
				}
				if cli != "opencode" {
					continue
				}
				got, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(payload)))
				if err != nil || string(got) != openCodeNulGuard {
					t.Fatalf("%s %s (running %v): the file installed is not the plugin, once: %v", cli, role, running, err)
				}
			}
		}
	}
}
