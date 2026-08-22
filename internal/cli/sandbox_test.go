package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"

	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/covmap"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/store"
)

func channelArchive(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func installFakeTool(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestDestroyArchiveBlocksOnIncompleteAndRetries(t *testing.T) {
	covmap.ProveCoreOnPass(t, "archive-safety", covmap.TierUnit)
	covmap.ProveCoreOnPass(t, "group-reclaim", covmap.TierUnit)
	dir := t.TempDir()
	tool := filepath.Join(dir, "fake-sandbox")
	control := filepath.Join(dir, "fail-archive")
	calls := filepath.Join(dir, "calls")
	body := `#!/bin/sh
case "$1" in
  ls)
    # Reflect destroys: once destroy has been called, the sandbox is gone.
    if [ -f "$CALLS" ]; then printf '%s\n' '[]'; else printf '%s\n' '[{"ref":"worker.grp","name":"worker","group":"grp","status":"running"}]'; fi ;;
  exec)
    if [ -f "$CONTROL" ]; then exit 1; fi
    tar -czf - --files-from /dev/null ;;
  destroy) printf '%s\n' "$*" >> "$CALLS" ;;
  group)
    case "$2" in
      ls) echo '[{"name":"grp","network":"cs-sandbox-grp","members":0}]' ;;
      *) printf '%s\n' "$*" >> "$CALLS" ;;
    esac ;;
esac
`
	if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(control, []byte("fail\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTROL", control)
	t.Setenv("CALLS", calls)
	stateDir := filepath.Join(dir, "state")
	a := &app{store: store.Store{Dir: stateDir}, sandbox: sandboxCLI{Bin: tool}}
	campaign := &model.Campaign{Name: "test", Group: "grp", Members: []model.Member{{Name: "agent-01", Role: "agent", CLI: "codex", Sandbox: "worker", Ref: "worker.grp", Session: model.Session{Name: "s"}}}}
	if err := a.store.Save(campaign); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "archive")
	runDestroy := func() error {
		cmd := a.destroyCmd()
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{"--archive", "--archive-output", archive, "--force", "test"})
		return cmd.Execute()
	}
	if err := runDestroy(); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("first destroy error = %v", err)
	}
	if _, err := a.store.Load("test"); err != nil {
		t.Fatalf("campaign state removed after incomplete archive: %v", err)
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("destroy ran before complete archive: %v", err)
	}
	if err := os.Remove(control); err != nil {
		t.Fatal(err)
	}
	if err := runDestroy(); err != nil {
		t.Fatalf("retry destroy: %v", err)
	}
	if _, err := a.store.Load("test"); err == nil {
		t.Fatal("campaign state remains after successful archived destroy")
	}
	b, err := os.ReadFile(calls)
	if err != nil || !strings.Contains(string(b), "destroy worker.grp --force") {
		t.Fatalf("destroy call missing: %v: %s", err, b)
	}
	// The group is reclaimed too, and only after the members are gone: it owns
	// a network, a key pair, a gateway and its published port, none of which
	// any member destroy releases.
	if !strings.Contains(string(b), "group rm grp") {
		t.Fatalf("campaign group never reclaimed: %s", b)
	}
	if incomplete, err := archiveIncomplete(archive); err != nil || len(incomplete) != 0 {
		t.Fatalf("archive incomplete after retry: %v, %v", incomplete, err)
	}
}

func TestCreateResumesAfterInjectedMemberFailure(t *testing.T) {
	covmap.ProveCoreOnPass(t, "create-resume", covmap.TierUnit)
	dir := t.TempDir()
	tool := filepath.Join(dir, "fake-sandbox")
	resources := filepath.Join(dir, "resources")
	calls := filepath.Join(dir, "calls")
	failOnce := filepath.Join(dir, "fail-once")
	body := `#!/bin/sh
case "$1" in
  ls)
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
    printf 'create %s\n' "$name" >> "$CALLS"
    case "$name" in
      agent-01-*) if [ -f "$FAIL_ONCE" ]; then rm -f "$FAIL_ONCE"; exit 1; fi ;;
    esac
    printf '%s|%s\n' "$name" "$group" >> "$RESOURCES" ;;
  inspect) printf '%s\n' '{"ref":"'"$2"'","ip":"10.89.0.9","repos":[]}' ;;
  exec)
    # Mini-guest: run the command for real under a fake HOME so the installed
    # manifest, guard and doctor probes behave as they would in the VM
    # (the create-tail doctor needs a truthful guest, not exit 0).
    mkdir -p "$FAKE_HOME/.local/bin"
    HOME="$FAKE_HOME" PATH="$FAKE_HOME/.local/bin:/usr/bin:/bin" sh -c "$5" ;;
esac
`
	if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(failOnce, []byte("fail\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RESOURCES", resources)
	t.Setenv("CALLS", calls)
	t.Setenv("FAIL_ONCE", failOnce)
	ensureGuestBinary(t)
	seedMiniGuest(t, filepath.Join(dir, "guest-home"), "codex")
	installReplyingRemotes(t)
	a := &app{store: store.Store{Dir: filepath.Join(dir, "state")}, sandbox: sandboxCLI{Bin: tool}}
	runCreate := func() error {
		cmd := a.createCmd(false)
		cmd.SilenceUsage = true
		cmd.SetArgs([]string{"resume-test", "--orchestrator", "codex", "--agent-cli", "codex", "--agents", "1"})
		return cmd.Execute()
	}
	if err := runCreate(); err == nil {
		t.Fatal("injected create failure unexpectedly succeeded")
	}
	failed, err := a.store.Load("resume-test")
	if err != nil || failed.Provisioning != "create-failed" {
		t.Fatalf("failed checkpoint = %+v, %v", failed, err)
	}
	if err := runCreate(); err != nil {
		t.Fatalf("resume create: %v", err)
	}
	resumed, err := a.store.Load("resume-test")
	if err != nil || resumed.Provisioning != "" {
		t.Fatalf("resumed state = %+v, %v", resumed, err)
	}
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	// Member sandbox names are <member>-<campaign discriminator>; the
	// orchestrator is created once and the injected agent failure is retried.
	if strings.Count(string(b), "create orchestrator-") != 1 || strings.Count(string(b), "create agent-01-") != 2 {
		t.Fatalf("unexpected create attempts:\n%s", b)
	}
}

func TestDoctorRejectsUnversionedSandbox(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	dir := installFakeTool(t, "fake-sandbox", `
case "$1" in version) echo legacy;; ls) echo '[]';; esac
`)
	a := &app{store: store.Store{Dir: t.TempDir()}, sandbox: sandboxCLI{Bin: filepath.Join(dir, "fake-sandbox")}}
	cmd := a.doctorCmd()
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unrecognized cs-sandbox version") {
		t.Fatalf("doctor version error = %v", err)
	}
}

func TestDoctorChecksVersionJSONAndRemoteFamilies(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	dir := installFakeTool(t, "fake-sandbox", `
case "$1" in
  version) echo 'cs-sandbox 0.1.0-dev (linux/amd64, go1.25.0)';;
  ls) echo '[]';;
  group) echo '[]';;
esac
`)
	for _, cli := range []string{"claude", "codex", "opencode"} {
		for _, suffix := range []string{"-remote", "-remote-output", "-remote-status", "-turn"} {
			installFakeTool(t, "cs-"+cli+suffix, `exit 0`)
		}
	}
	a := &app{store: store.Store{Dir: t.TempDir()}, sandbox: sandboxCLI{Bin: filepath.Join(dir, "fake-sandbox")}}
	var out strings.Builder
	cmd := a.doctorCmd()
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cs-sandbox version 0.1.0-dev", "supports ls --json", "supports sandbox groups", "claude remote tool family", "codex remote tool family", "opencode remote tool family"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out.String())
		}
	}
}

// A cs-sandbox without groups cannot host a campaign at all: the campaign's
// isolation boundary IS a group. Doctor has to say that before a create tries
// to pass --group and fails somewhere less legible.
func TestDoctorRejectsSandboxWithoutGroups(t *testing.T) {
	covmap.ProveCoreOnPass(t, "doctor", covmap.TierUnit)
	covmap.ProveCoreOnPass(t, "member-addressing", covmap.TierUnit)
	dir := installFakeTool(t, "fake-sandbox", `
case "$1" in
  version) echo 'cs-sandbox 0.1.0-dev (linux/amd64, go1.25.0)';;
  ls) echo '[]';;
  group) echo 'unknown command "group"' >&2; exit 1;;
esac
`)
	a := &app{store: store.Store{Dir: t.TempDir()}, sandbox: sandboxCLI{Bin: filepath.Join(dir, "fake-sandbox")}}
	cmd := a.doctorCmd()
	cmd.SetOut(&strings.Builder{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "does not support sandbox groups") {
		t.Fatalf("doctor group error = %v", err)
	}
}

// The host plane and the guest plane use different addresses for the same
// member, and mixing them up fails silently rather than loudly: the manifest
// feeds `ssh <host>` inside the orchestrator, where the group's DNS serves
// bare names and the guest ssh config ("Host * !*.*") withholds the tier key
// from anything dotted. So the manifest must carry bare names even though the
// exec that installs it is addressed by qualified ref.
func TestOrchestratorManifestUsesInGroupNamesNotHostRefs(t *testing.T) {
	covmap.ProveCoreOnPass(t, "helper-scoping", covmap.TierUnit)
	covmap.ProveCoreOnPass(t, "member-addressing", covmap.TierUnit)
	dir := t.TempDir()
	tool := filepath.Join(dir, "fake-sandbox")
	capture := filepath.Join(dir, "exec-args")
	body := `#!/bin/sh
printf '%s\n' "$*" >> "$CAPTURE"
`
	if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE", capture)
	campaign := &model.Campaign{Name: "demo", Group: "demo-grp", Network: groupNetwork("demo-grp"), Members: []model.Member{
		{Name: "orchestrator", Role: "orchestrator", CLI: "codex",
			Sandbox: "demo-orchestrator", Ref: "demo-orchestrator.demo-grp",
			Session: model.Session{Name: "demo-orchestrator"}},
		{Name: "backend", Role: "agent", CLI: "claude", Sandbox: "demo-backend", Ref: "demo-backend.demo-grp",
			Branch: "cs-sandbox/demo-backend", Session: model.Session{Name: "demo-backend"},
			Profile: model.MemberProfile{Repos: []model.Repo{{Path: "/src/app", Name: "app"}}}},
	}}
	if err := (sandboxCLI{Bin: tool}).configureOrchestrator(context.Background(), campaign); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	call := string(b)
	// Installed against the orchestrator's HOST ref.
	if !strings.HasPrefix(call, "exec demo-orchestrator.demo-grp ") {
		t.Fatalf("manifest not installed via the host ref: %q", call)
	}
	// The payload is base64; decode it to read what the orchestrator will see.
	field := strings.Fields(call)
	var manifest map[string]any
	for _, f := range field {
		raw, decErr := base64.StdEncoding.DecodeString(f)
		if decErr != nil || !strings.HasPrefix(string(raw), "{") {
			continue
		}
		if json.Unmarshal(raw, &manifest) == nil {
			break
		}
	}
	if manifest == nil {
		t.Fatalf("no manifest payload in call: %q", call)
	}
	agents, _ := manifest["agents"].(map[string]any)
	backend, _ := agents["backend"].(map[string]any)
	if backend == nil {
		t.Fatalf("manifest has no backend agent: %v", manifest)
	}
	if got := backend["sandbox"]; got != "demo-backend" {
		t.Fatalf("manifest agent address = %v, want the bare in-group name demo-backend", got)
	}
}

func TestExtractMemberChannelsAllowsOnlyInputAndOutput(t *testing.T) {
	covmap.ProveCoreOnPass(t, "archive-safety", covmap.TierUnit)
	base := t.TempDir()
	valid := channelArchive(t, map[string]string{"input/prompt.md": "ask\n", "output/SUMMARY.md": "answer\n"})
	if err := extractMemberChannels(base, valid); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"input/prompt.md": "ask\n", "output/SUMMARY.md": "answer\n"} {
		b, err := os.ReadFile(filepath.Join(base, path))
		if err != nil || string(b) != want {
			t.Fatalf("%s = %q, %v", path, b, err)
		}
	}
	escapeRoot := t.TempDir()
	bad := channelArchive(t, map[string]string{"../escape": "bad"})
	if err := extractMemberChannels(filepath.Join(escapeRoot, "archive"), bad); err == nil {
		t.Fatal("path traversal archive accepted")
	}
	if _, err := os.Stat(filepath.Join(escapeRoot, "escape")); !os.IsNotExist(err) {
		t.Fatalf("archive escaped destination: %v", err)
	}
}

func TestRootExposesNoPushOrLegacyCtlCommand(t *testing.T) {
	covmap.ProveCoreOnPass(t, "no-push", covmap.TierUnit)
	a := &app{store: store.Store{Dir: t.TempDir()}}
	for _, command := range a.root().Commands() {
		if command.Name() == "push" || command.Name() == "ctl" {
			t.Fatalf("forbidden command exposed: %s", command.Name())
		}
	}
}

func TestShellCompletionSmoke(t *testing.T) {
	covmap.ProveCoreOnPass(t, "inspection", covmap.TierUnit)
	a := &app{store: store.Store{Dir: t.TempDir()}}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		var out bytes.Buffer
		root := a.root()
		root.SetOut(&out)
		root.SetArgs([]string{"completion", shell})
		if err := root.Execute(); err != nil {
			t.Fatalf("completion %s: %v", shell, err)
		}
		if out.Len() < 100 || !strings.Contains(out.String(), "cs-campaign") {
			t.Fatalf("completion %s output is incomplete", shell)
		}
	}
}

func TestCreateRefusesExistingGeneratedGroup(t *testing.T) {
	covmap.ProveCoreOnPass(t, "create-resume", covmap.TierUnit)
	p, err := profileFromFlags("codex", []string{"worker=codex"}, "", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	campaign := buildCampaign("collision", p, "", "", time.Time{})
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	tool := filepath.Join(dir, "fake-sandbox")
	body := `#!/bin/sh
case "$1" in
  ls) printf '[{"ref":"foreign.%s","name":"foreign","group":"%s","status":"running"}]\n' "$COLLISION_GROUP" "$COLLISION_GROUP" ;;
  *) printf '%s\n' "$*" >> "$CALLS" ;;
esac
`
	if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COLLISION_GROUP", campaign.Group)
	t.Setenv("CALLS", calls)
	a := &app{store: store.Store{Dir: filepath.Join(dir, "state")}, sandbox: sandboxCLI{Bin: tool}}
	cmd := a.createCmd(false)
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"collision", "--orchestrator", "codex", "--agent", "worker=codex"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "foreign sandbox") {
		t.Fatalf("collision error = %v", err)
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("create side effect occurred after collision: %v", err)
	}
}

func TestRepositoryArchiveNamesAndShellArgumentsAreBounded(t *testing.T) {
	covmap.ProveCoreOnPass(t, "archive-safety", covmap.TierUnit)
	if got := shellQuote("odd'name"); got != `'odd'"'"'name'` {
		t.Fatalf("shell quote = %q", got)
	}
	got := archiveComponent("../odd repo/name")
	if strings.Contains(got, "/") || strings.HasPrefix(got, ".") || !strings.Contains(got, "odd_repo_name") {
		t.Fatalf("unsafe archive component = %q", got)
	}
}

// The orchestrator's doctrine must NAME the fleet: ctv4 showed that a manifest the
// orchestrator never reads is not the same as knowledge it has.
func TestOrchestratorDoctrineNamesTeammateCLIs(t *testing.T) {
	ensureGuestBinary(t)
	s, capture := captureExecSandbox(t, `printf '%s' "$5" >> "$CAPTURE"`)
	campaign := &model.Campaign{Name: "c1", Network: "net", Members: []model.Member{
		{Name: "orchestrator", Role: "orchestrator", CLI: "claude", Sandbox: "box0", Ref: "box0.grp"},
		{Name: "fixer", Role: "agent", CLI: "codex", Sandbox: "box1", Ref: "box1.grp"},
	}}
	if err := s.configureChannels(context.Background(), campaign, campaign.Members[0], campaignInputs{Roles: map[string]seededFile{}}); err != nil {
		t.Fatal(err)
	}
	command, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	doctrine := string(decodeEmbeddedBase64(t, string(command), 2))
	// The doctrine names each teammate WITH its CLI (the ctv4 lesson) and the one
	// control path. It no longer teaches the raw cs-*-remote families at all —
	// send resolves the family from the manifest, and the guard's own refusal
	// text explains the rule at the exact moment someone breaks it.
	for _, want := range []string{"`fixer` (codex)", "cs-campaign-member"} {
		if !strings.Contains(doctrine, want) {
			t.Fatalf("orchestrator doctrine missing %q:\n%s", want, doctrine)
		}
	}
	if strings.Contains(doctrine, "`orchestrator` (claude)") {
		t.Fatalf("doctrine should list teammates, not the reader itself:\n%s", doctrine)
	}
}

// The branch is READ from cs-sandbox, not derived here. The fake deliberately
// reports a branch the planner would never produce: if create still recorded
// its own guess, this passes silently today and breaks the moment cs-sandbox
// changes how it spells a branch — which is exactly how it broke before.
func TestCreateAdoptsTheBranchSandboxActuallyMade(t *testing.T) {
	covmap.ProveCoreOnPass(t, "repo-adoption", covmap.TierUnit)
	dir := t.TempDir()
	tool := filepath.Join(dir, "fake-sandbox")
	body := `#!/bin/sh
case "$1" in
  ls) echo '[]' ;;
  inspect)
    printf '%s\n' '{"ref":"'"$2"'","repos":[{"dir":"app","source":"/src/app","branch":"refs/upstream/decides/this"}]}' ;;
  exec)
    mkdir -p "$FAKE_HOME/.local/bin"
    HOME="$FAKE_HOME" PATH="$FAKE_HOME/.local/bin:/usr/bin:/bin" sh -c "$5" ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "app-repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	ensureGuestBinary(t)
	seedMiniGuest(t, filepath.Join(dir, "guest-home"), "codex")
	installReplyingRemotes(t)
	a := &app{store: store.Store{Dir: filepath.Join(dir, "state")}, sandbox: sandboxCLI{Bin: tool}}
	cmd := a.createCmd(false)
	cmd.SilenceUsage = true
	cmd.SetOut(&strings.Builder{})
	cmd.SetArgs([]string{"adopt", "--orchestrator", "codex", "--agent", "worker=codex", "--repo", repo})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("create: %v", err)
	}
	saved, err := a.store.Load("adopt")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range saved.Members {
		if m.Branch != "refs/upstream/decides/this" {
			t.Errorf("member %s branch = %q; create must adopt what cs-sandbox reported, not its own guess",
				m.Name, m.Branch)
		}
	}
}

// A member with no repositories still has a record to read — its address, which
// the gateway needs — so inspect runs for every member; it simply finds no
// branch to adopt, and create must not fail on that.
func TestCreateReadsRecordEvenWithoutRepos(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "fake-sandbox")
	body := `#!/bin/sh
case "$1" in
  ls) echo '[]' ;;
  inspect) printf '%s\n' '{"ref":"'"$2"'","ip":"10.89.0.55","repos":[]}' ;;
  exec)
    mkdir -p "$FAKE_HOME/.local/bin"
    HOME="$FAKE_HOME" PATH="$FAKE_HOME/.local/bin:/usr/bin:/bin" sh -c "$5" ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(tool, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	ensureGuestBinary(t)
	seedMiniGuest(t, filepath.Join(dir, "guest-home"), "codex")
	installReplyingRemotes(t)
	a := &app{store: store.Store{Dir: filepath.Join(dir, "state")}, sandbox: sandboxCLI{Bin: tool}}
	cmd := a.createCmd(false)
	cmd.SilenceUsage = true
	cmd.SetOut(&strings.Builder{})
	cmd.SetArgs([]string{"norepo", "--orchestrator", "codex", "--agent", "worker=codex"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("create without repos: %v", err)
	}
	saved, err := a.store.Load("norepo")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range saved.Members {
		if m.IP != "10.89.0.55" {
			t.Errorf("member %s recorded ip %q; the gateway needs the address", m.Name, m.IP)
		}
	}
}
