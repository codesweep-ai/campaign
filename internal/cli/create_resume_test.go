package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/store"
)

// A create that fails after every member confirmed its briefing is resumed
// through the real command, so what the resume changes is carried by
// executeCreate itself and not by a helper called on its own. The members are
// not asked again, the deadline moves to the resuming attempt, the mission
// says it moved, and ls and observe name the attempt that set it.
func TestAResumedCreateKeepsReadbacksAndMovesTheDeadline(t *testing.T) {
	covmap.ProveCoreOnPass(t, "create-resume", covmap.TierUnit)
	dir := t.TempDir()
	tool := filepath.Join(dir, "fake-sandbox")
	resources := filepath.Join(dir, "resources")
	failMission := filepath.Join(dir, "fail-mission-once")
	body := `#!/bin/sh
case "$1" in
` + upstreamCases() + `  ls)
    printf '['
    first=1
    if [ -f "$RESOURCES" ]; then
      while IFS='|' read -r name group; do
        [ -n "$name" ] || continue
        [ "$first" = 1 ] || printf ','
        first=0
        printf '{"ref":"%s.%s","name":"%s","group":"%s","status":"running","network":"cs-sandbox-%s"}' \
          "$name" "$group" "$name" "$group" "$group"
      done < "$RESOURCES"
    fi
    printf ']\n' ;;
  create)
    name="$2"; group=""
    shift 2
    while [ "$#" -gt 0 ]; do
      if [ "$1" = --group ]; then group="$2"; shift 2; else shift; fi
    done
    printf '%s|%s\n' "$name" "$group" >> "$RESOURCES" ;;
  inspect) printf '%s\n' '{"ref":"'"$2"'","ip":"10.89.0.9","repos":[]}' ;;
  exec)
    # The first attempt dies delivering the mission, after the readback.
    case "$5" in
      *input/m1.md*) if [ -f "$FAIL_MISSION" ]; then rm -f "$FAIL_MISSION"; exit 1; fi ;;
    esac
    mkdir -p "$FAKE_HOME/.local/bin"
    HOME="$FAKE_HOME" PATH="$FAKE_HOME/.local/bin:/usr/bin:/bin" sh -c "$5" ;;
esac
`
	if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(failMission, []byte("fail\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(dir, "profile.yaml")
	repo := filepath.Join(dir, "app")
	if err := os.WriteFile(profile, []byte(`apiVersion: codesweep.ai/v1alpha1
kind: CampaignProfile
defaults:
  deadline: 30m
orchestrator:
  cli: codex
  repos:
    - path: `+repo+`
agents:
  dev:
    cli: codex
    repos:
      - path: `+repo+`
`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"mission.md":            "Build the thing.",
		"roles/orchestrator.md": "Run the campaign.",
		"roles/dev.md":          "Write the code.",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("RESOURCES", resources)
	t.Setenv("FAIL_MISSION", failMission)
	ensureGuestBinary(t)
	home := seedMiniGuest(t, filepath.Join(dir, "guest-home"), "codex")
	installReplyingRemotes(t)
	a := &app{store: store.Store{Dir: filepath.Join(dir, "state")}, sandbox: sandboxCLI{Bin: tool}}
	runCreate := func() (string, error) {
		var out strings.Builder
		cmd := a.createCmd(false)
		cmd.SilenceUsage = true
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"resumed", "--profile", profile})
		err := cmd.Execute()
		return out.String(), err
	}

	out, err := runCreate()
	if err == nil {
		t.Fatalf("the injected mission failure did not fail create:\n%s", out)
	}
	failed, err := a.store.Load("resumed")
	if err != nil || failed.Provisioning != "create-failed" {
		t.Fatalf("failed checkpoint = %+v, %v\n%s", failed, err, out)
	}
	if len(failed.Attempts) != 1 {
		t.Fatalf("the first attempt must be recorded; attempts = %+v", failed.Attempts)
	}
	first := failed.Attempts[0]
	if !first.StartedAt.Equal(failed.CreatedAt) || !first.Deadline.Equal(first.StartedAt.Add(30*time.Minute)) {
		t.Fatalf("attempt 1 = %+v, createdAt %s", first, failed.CreatedAt)
	}

	out, err = runCreate()
	if err != nil {
		t.Fatalf("resume create: %v\n%s", err, out)
	}
	resumed, err := a.store.Load("resumed")
	if err != nil || resumed.Provisioning != "" {
		t.Fatalf("resumed state = %+v, %v", resumed, err)
	}

	// The deadline is the second attempt's, and the first is kept as it was.
	if len(resumed.Attempts) != 2 || resumed.Attempts[0] != first {
		t.Fatalf("attempts = %+v, want the first kept and a second added", resumed.Attempts)
	}
	second := resumed.Attempts[1]
	if !second.StartedAt.After(first.StartedAt) || !second.Deadline.Equal(second.StartedAt.Add(30*time.Minute)) {
		t.Fatalf("attempt 2 = %+v", second)
	}
	if !resumed.Deadline.Equal(second.Deadline) || !resumed.CreatedAt.Equal(first.StartedAt) {
		t.Fatalf("deadline %s, createdAt %s: the deadline must be attempt 2's and createdAt attempt 1's", resumed.Deadline, resumed.CreatedAt)
	}

	// Both members confirmed their briefing in d001, and are not asked again.
	if n := strings.Count(out, "confirmed its briefing in d001 on an earlier create"); n != 2 {
		t.Errorf("both members must be kept, %d were:\n%s", n, out)
	}
	if _, err := os.Stat(filepath.Join(home, guestInputDir, "d002.md")); err == nil {
		t.Error("a member whose readback passed was asked again")
	}

	// The mission states the moved deadline.
	mission, err := os.ReadFile(filepath.Join(home, guestInputDir, "m1.md"))
	if err != nil {
		t.Fatalf("the mission was not opened: %v", err)
	}
	for _, want := range []string{
		"The campaign's deadline is " + second.Deadline.UTC().Format(time.RFC3339) + ".",
		"the deadline moved to this attempt",
	} {
		if !strings.Contains(string(mission), want) {
			t.Errorf("the mission must say %q:\n%s", want, mission)
		}
	}

	// ls names the attempt that set the deadline.
	var ls strings.Builder
	lsCmd := a.lsCmd()
	lsCmd.SetOut(&ls)
	if err := lsCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := second.Deadline.UTC().Format(time.RFC3339) + " (attempt 2)"; !strings.Contains(ls.String(), want) {
		t.Errorf("ls must show %q:\n%s", want, ls.String())
	}

	// observe says the deadline moved, and which one it replaced.
	obs, err := a.observeCampaign(context.Background(), resumed)
	if err != nil {
		t.Fatal(err)
	}
	var shown strings.Builder
	printObservation(&shown, obs)
	for _, want := range []string{
		"DEADLINE — " + second.Deadline.UTC().Format(time.RFC3339),
		"moved by create attempt 2, which started at " + second.StartedAt.UTC().Format(time.RFC3339),
		"replaces " + first.Deadline.UTC().Format(time.RFC3339),
	} {
		if !strings.Contains(shown.String(), want) {
			t.Errorf("observe must show %q:\n%s", want, shown.String())
		}
	}
}
