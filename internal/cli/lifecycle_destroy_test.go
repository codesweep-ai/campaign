package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/covmap"
	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/store"
)

// The sandbox CLI refuses a non-forced destroy by printing advice and exiting
// zero, so exit status cannot prove removal. A destroy that leaves member
// sandboxes alive must preserve the campaign record — deleting it orphans
// running VMs with no campaign to address them through (observed live).
func TestDestroyPreservesStateWhileMembersSurvive(t *testing.T) {
	covmap.ProveCoreOnPass(t, "group-reclaim", covmap.TierUnit)
	sandboxDir := installFakeTool(t, "fake-sandbox", `
case "$1" in
  ls) cat "$LS_FILE" ;;
  destroy)
    case "$*" in
      *--force*) echo "[]" > "$LS_FILE"; echo "destroyed $2" ;;
      *) echo "destroying \"$2\" and all its data. Re-run with -f to confirm." ;;
    esac ;;
  group)
    case "$2" in
      ls) cat "$GROUPS_FILE" ;;
      *) printf '%s\n' "$*" >> "$GROUP_CALLS" ;;
    esac ;;
  *) : ;;
esac
exit 0`)
	lsFile := filepath.Join(sandboxDir, "ls.json")
	if err := os.WriteFile(lsFile, []byte(`[{"ref":"boxA.gone-grp","name":"boxA","group":"gone-grp","status":"running"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	groupCalls := filepath.Join(sandboxDir, "group-calls")
	groupsFile := filepath.Join(sandboxDir, "groups.json")
	if err := os.WriteFile(groupsFile, []byte(`[{"name":"gone-grp","network":"cs-sandbox-gone-grp","members":1}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LS_FILE", lsFile)
	t.Setenv("GROUP_CALLS", groupCalls)
	t.Setenv("GROUPS_FILE", groupsFile)
	stateDir := t.TempDir()
	a := &app{store: store.Store{Dir: stateDir}, sandbox: sandboxCLI{Bin: filepath.Join(sandboxDir, "fake-sandbox")}}
	campaign := &model.Campaign{Name: "gone", Group: "gone-grp", Members: []model.Member{
		{Name: "agent-01", Role: "agent", CLI: "codex", Sandbox: "boxA", Ref: "boxA.gone-grp", Session: model.Session{Name: "s1"}},
	}}
	if err := a.store.Save(campaign); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		cmd := a.destroyCmd()
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		return cmd.Execute()
	}
	// Non-forced: the fake refuses (exit 0); state must survive with an error.
	err := run("gone")
	if err == nil || !strings.Contains(err.Error(), "still present") {
		t.Fatalf("non-forced destroy: want members-still-present error, got %v", err)
	}
	if _, err := a.store.Load("gone"); err != nil {
		t.Fatalf("campaign state was deleted while members survive: %v", err)
	}
	// The group must not be reclaimed either: removing its network, keys and
	// gateway out from under a running member would strand it — reachable by
	// nothing, and no longer destroyable through its own campaign.
	if _, err := os.Stat(groupCalls); !os.IsNotExist(err) {
		t.Fatalf("group reclaimed while a member survives: %v", err)
	}
	// Forced: the fake removes the sandbox; state deletion is now legitimate.
	if err := run("gone", "-f"); err != nil {
		t.Fatalf("forced destroy: %v", err)
	}
	if _, err := a.store.Load("gone"); err == nil {
		t.Fatal("campaign state should be deleted after all members are gone")
	}
	b, err := os.ReadFile(groupCalls)
	if err != nil || !strings.Contains(string(b), "group rm gone-grp") {
		t.Fatalf("group not reclaimed after the last member went: %v: %s", err, b)
	}
}

// Teardown has to be re-runnable. A campaign whose group is already gone (a
// destroy interrupted after the group rm but before the state delete) must
// still reach store.Delete rather than stalling forever on the one step it
// already completed.
func TestDestroyToleratesAlreadyReclaimedGroup(t *testing.T) {
	covmap.ProveCoreOnPass(t, "group-reclaim", covmap.TierUnit)
	// The group is absent from the inventory — the state an interrupted destroy
	// leaves behind. Establishing that from the listing, rather than from the
	// wording of an error, is the point.
	sandboxDir := installFakeTool(t, "fake-sandbox", `
case "$1" in
  ls) echo "[]" ;;
  group) case "$2" in ls) echo "[]" ;; *) echo "group rm must not run" >&2; exit 1 ;; esac ;;
  *) : ;;
esac
exit 0`)
	stateDir := t.TempDir()
	a := &app{store: store.Store{Dir: stateDir}, sandbox: sandboxCLI{Bin: filepath.Join(sandboxDir, "fake-sandbox")}}
	campaign := &model.Campaign{Name: "gone", Group: "gone-grp", Members: []model.Member{
		{Name: "agent-01", Role: "agent", CLI: "codex", Sandbox: "boxA", Ref: "boxA.gone-grp", Session: model.Session{Name: "s1"}},
	}}
	if err := a.store.Save(campaign); err != nil {
		t.Fatal(err)
	}
	cmd := a.destroyCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"gone", "-f"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("destroy with an already-absent group: %v", err)
	}
	if _, err := a.store.Load("gone"); err == nil {
		t.Fatal("campaign state should be deleted once members and group are gone")
	}
}

// `destroy --dry-run` answers the question an operator has before the one
// command that cannot be undone: which machines, exactly, and nothing else?
// It must name the three sets destroy treats differently, and it must remove
// nothing, archive nothing and keep the record, whatever other flags it is
// given. A preview that acted would be worse than none.
func TestDestroyDryRunNamesWhatItWouldRemoveAndRemovesNothing(t *testing.T) {
	sandboxDir := installFakeTool(t, "fake-sandbox", `
printf '%s\n' "$*" >> "$CALLS"
case "$1" in
  ls) cat "$LS_FILE" ;;
esac
exit 0`)
	lsFile := filepath.Join(sandboxDir, "ls.json")
	if err := os.WriteFile(lsFile, []byte(`[
{"ref":"orch.acme-grp","name":"orch","group":"acme-grp","status":"running"},
{"ref":"dev.acme-grp","name":"dev","group":"acme-grp","status":"stopped"},
{"ref":"left.acme-grp","name":"left","group":"acme-grp","status":"running"},
{"ref":"theirs.other-grp","name":"theirs","group":"other-grp","status":"running"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(sandboxDir, "calls")
	t.Setenv("LS_FILE", lsFile)
	t.Setenv("CALLS", calls)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// What the remote tools keep on the host for a member, which destroy leaves.
	sessions := filepath.Join(home, ".cs-codex-remote-sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"acme-1-qa", "acme-1-qa.token", "other-9-qa"} {
		if err := os.WriteFile(filepath.Join(sessions, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stateDir := t.TempDir()
	a := &app{store: store.Store{Dir: stateDir}, sandbox: sandboxCLI{Bin: filepath.Join(sandboxDir, "fake-sandbox")}}
	campaign := &model.Campaign{Name: "acme", Group: "acme-grp", Network: "cs-sandbox-acme-grp", Members: []model.Member{
		{Name: "orchestrator", Role: "orchestrator", CLI: "codex", Sandbox: "orch", Ref: "orch.acme-grp", Session: model.Session{Name: "acme-1-orchestrator"}},
		{Name: "developer", Role: "agent", CLI: "codex", Sandbox: "dev", Ref: "dev.acme-grp", Session: model.Session{Name: "acme-1-developer"}},
		{Name: "qa", Role: "agent", CLI: "codex", Sandbox: "qa", Ref: "qa.acme-grp", Session: model.Session{Name: "acme-1-qa"}},
	}}
	if err := a.store.Save(campaign); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		cmd := a.destroyCmd()
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}

	archiveRoot := filepath.Join(t.TempDir(), "final")
	out, err := run("acme", "--dry-run", "--force", "--archive", "--archive-output", archiveRoot)
	if err != nil {
		t.Fatalf("a preview must succeed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"nothing is removed",
		"acme-grp",
		"orch.acme-grp  present, running",
		"dev.acme-grp   present, stopped",
		"absent",
		"qa.acme-grp",
		"stray",
		"left.acme-grp",
		"cannot reclaim the group while a stray machine stays",
		"into " + archiveRoot,
		"1 other sandbox(es)",
		"2 record(s) of the members' agent sessions on this host, under ~/.cs-codex-remote-sessions",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the preview does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "theirs.other-grp") {
		t.Errorf("another group's sandbox is not this campaign's to list by name:\n%s", out)
	}
	// Last member first, as destroy takes them.
	if d, o := strings.Index(out, "dev.acme-grp"), strings.Index(out, "orch.acme-grp"); d < 0 || o < 0 || d > o {
		t.Errorf("members must be listed in the order destroy takes them, last first:\n%s", out)
	}

	made, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for call := range strings.SplitSeq(strings.TrimSpace(string(made)), "\n") {
		if !strings.HasPrefix(call, "ls") {
			t.Errorf("a preview may only list, and it ran `cs-sandbox %s`", call)
		}
	}
	if _, err := os.Stat(archiveRoot); !os.IsNotExist(err) {
		t.Errorf("a preview must not archive, and %s exists", archiveRoot)
	}
	if _, err := a.store.Load("acme"); err != nil {
		t.Errorf("a preview must keep the campaign record: %v", err)
	}

	// The flag rules are destroy's own, so a preview refuses what destroy refuses.
	if _, err := run("acme", "--dry-run", "--archive-output", archiveRoot); err == nil {
		t.Error("--archive-output without --archive must be refused in a preview too")
	}
	// Without --force and --archive the preview says what that means.
	out, err = run("acme", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--force is not set", "--archive is not set"} {
		if !strings.Contains(out, want) {
			t.Errorf("the preview does not say %q:\n%s", want, out)
		}
	}
}
