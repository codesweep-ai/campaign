package cli

// Unit coverage for previously untested surfaces: the orchestrator manifest
// content (the scoping mechanism), prepareAuth's key-name-only contract, and
// the archive's host-side session-log collection.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/covmap"

	"github.com/codesweep-ai/campaign/internal/model"
)

func captureExecSandbox(t *testing.T, script string) (sandboxCLI, string) {
	t.Helper()
	dir := t.TempDir()
	tool := filepath.Join(dir, "fake-sandbox")
	capture := filepath.Join(dir, "capture")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE", capture)
	return sandboxCLI{Bin: tool}, capture
}

// capturePayloads is the fake sandbox fragment that records what each exec was
// given: the command, and the base64 the guest command reads from stdin. One
// line per exec, empty for an exec that carries no payload.
const capturePayloads = `if [ "$1" = exec ]; then printf '%s' "$5" >> "$CAPTURE"; cat >> "$CAPTURE.stdin"; echo >> "$CAPTURE.stdin"; fi`

// capturedPayload decodes the payload the nth exec delivered on stdin. File
// content travels there rather than in argv, so this is where a test reads what
// a member was actually given.
func capturedPayload(t *testing.T, capture string, n int) []byte {
	t.Helper()
	raw, err := os.ReadFile(capture + ".stdin")
	if err != nil {
		t.Fatalf("no stdin capture: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) <= n {
		t.Fatalf("capture has %d execs, want > %d: %q", len(lines), n, raw)
	}
	decoded, err := base64.StdEncoding.DecodeString(lines[n])
	if err != nil {
		t.Fatalf("decode payload %d (%q): %v", n, lines[n], err)
	}
	return decoded
}

// The manifest is the orchestrator's scoping mechanism: it must list every
// agent with its CLI, sandbox, session, and branch mapping, and must not
// contain the orchestrator itself.
func TestConfigureOrchestratorManifestScopesAgents(t *testing.T) {
	covmap.ProveCoreOnPass(t, "helper-scoping", covmap.TierUnit)
	sandbox, capture := captureExecSandbox(t, capturePayloads)
	campaign := &model.Campaign{Name: "camp", Network: "net", Members: []model.Member{
		{Name: "orchestrator", Role: "orchestrator", CLI: "codex", Sandbox: "orch", Session: model.Session{Name: "orch"}},
		{Name: "worker", Role: "agent", CLI: "claude", Sandbox: "wbox", Branch: "cs-sandbox/wbox",
			Session: model.Session{Name: "wsess"},
			Profile: model.MemberProfile{CLI: "claude", Repos: []model.Repo{{Path: "/src/app"}}}},
	}}
	if err := sandbox.configureOrchestrator(context.Background(), campaign); err != nil {
		t.Fatal(err)
	}
	command, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Campaign string `json:"campaign"`
		Network  string `json:"network"`
		Agents   map[string]struct {
			CLI     string            `json:"cli"`
			Sandbox string            `json:"sandbox"`
			Session string            `json:"session"`
			Repos   map[string]string `json:"repos"`
		} `json:"agents"`
	}
	if err = json.Unmarshal(capturedPayload(t, capture, 0), &manifest); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if manifest.Campaign != "camp" || manifest.Network != "net" {
		t.Fatalf("manifest identity = %+v", manifest)
	}
	if _, present := manifest.Agents["orchestrator"]; present {
		t.Fatal("manifest must not list the orchestrator as an addressable agent")
	}
	worker, present := manifest.Agents["worker"]
	if !present || worker.CLI != "claude" || worker.Sandbox != "wbox" || worker.Session != "wsess" {
		t.Fatalf("worker record = %+v", worker)
	}
	if worker.Repos["app"] != "cs-sandbox/wbox" {
		t.Fatalf("worker repo mapping = %+v", worker.Repos)
	}
	// The guard loop must symlink the moved-aside tools to the guest binary.
	if !strings.Contains(string(command), "ln -sf cs-campaign-member") {
		t.Fatalf("guard loop must arm the guest binary: %q", command)
	}
}

func TestPrepareAuthPassesKeyNameNeverValue(t *testing.T) {
	covmap.ProveOnPass(t, "auth-provisioning", "codex", "", covmap.TierUnit)
	covmap.ProveOnPass(t, "auth-provisioning", "opencode", "", covmap.TierUnit)
	sandbox, capture := captureExecSandbox(t, `if [ "$1" = exec ]; then printf '%s' "$5" > "$CAPTURE"; fi`)
	t.Setenv("CS_TEST_API_KEY", "secret-value-123")
	codex := model.Member{CLI: "codex", Sandbox: "box", Profile: model.MemberProfile{Auth: model.Auth{APIKeyFromEnv: []string{"CS_TEST_API_KEY"}}}}
	if err := sandbox.prepareAuth(context.Background(), codex); err != nil {
		t.Fatal(err)
	}
	command, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(command), `${CS_TEST_API_KEY}`) {
		t.Fatalf("auth command must expand the key by name in the guest: %q", command)
	}
	if strings.Contains(string(command), "secret-value-123") {
		t.Fatalf("API-key value leaked into host argv: %q", command)
	}
	// opencode members write the profile env file with the same guest-side
	// expansion; the value never enters host argv either.
	if err = os.Remove(capture); err != nil {
		t.Fatal(err)
	}
	opencode := model.Member{CLI: "opencode", Sandbox: "box", Profile: codex.Profile}
	if err = sandbox.prepareAuth(context.Background(), opencode); err != nil {
		t.Fatal(err)
	}
	command, err = os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(command), `${CS_TEST_API_KEY}`) || !strings.Contains(string(command), ".cs-opencode/env") {
		t.Fatalf("opencode auth command must write the profile env file with guest-side expansion: %q", command)
	}
	if strings.Contains(string(command), "secret-value-123") {
		t.Fatalf("API-key value leaked into host argv: %q", command)
	}
	// Claude members and members without an available key perform no login.
	if err = os.Remove(capture); err != nil {
		t.Fatal(err)
	}
	claude := model.Member{CLI: "claude", Sandbox: "box", Profile: codex.Profile}
	if err = sandbox.prepareAuth(context.Background(), claude); err != nil {
		t.Fatal(err)
	}
	noKey := model.Member{CLI: "codex", Sandbox: "box", Profile: model.MemberProfile{Auth: model.Auth{APIKeyFromEnv: []string{"CS_TEST_ABSENT_KEY"}}}}
	if err = sandbox.prepareAuth(context.Background(), noKey); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(capture); !os.IsNotExist(err) {
		t.Fatalf("prepareAuth ran a login it should have skipped: %v", err)
	}
}

// The opencode evidence tar allowlists exactly the profile SQLite trio plus the
// per-session JSON export layer, and the guest command exports every session
// before packing; the profile env file and auth.json must never be allowlisted.
func TestArchiveTranscriptsOpenCodeAllowlistAndExport(t *testing.T) {
	covmap.ProveOnPass(t, "archive-evidence", "opencode", "", covmap.TierUnit)
	sandbox, capture := captureExecSandbox(t, `if [ "$1" = exec ]; then printf '%s' "$5" > "$CAPTURE"; tar -czf - --files-from /dev/null; fi`)
	a := &app{sandbox: sandbox}
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "transcript"), 0o700); err != nil {
		t.Fatal(err)
	}
	member := model.Member{Name: "oc", CLI: "opencode", Sandbox: "box"}
	if err := a.archiveTranscripts(context.Background(), base, member); err != nil {
		t.Fatal(err)
	}
	command, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		".cs-opencode/opencode.db", ".cs-opencode/opencode.db-wal", ".cs-opencode/opencode.db-shm",
		".cs-opencode/export", "cs-opencode export",
	} {
		if !strings.Contains(string(command), want) {
			t.Fatalf("opencode evidence command missing %q: %q", want, command)
		}
	}
	for _, banned := range []string{".cs-opencode/env", "auth.json"} {
		if strings.Contains(string(command), banned) {
			t.Fatalf("opencode evidence command must not touch %q: %q", banned, command)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "transcript", "cli-evidence.tgz")); err != nil {
		t.Fatalf("evidence tar not written: %v", err)
	}
}

// TestLargeSeededSetIsDelivered is SAC-019: a member's whole input set used to
// be base64-encoded into one shell script and passed as a single argv entry.
// Linux caps one entry at MAX_ARG_STRLEN, 32 pages, which is far below the
// total argument space, so an eight-member team's orchestrator failed `create`
// with "argument list too long" after its machine was already provisioned.
//
// The payload now rides on stdin. The property that keeps it fixed is asserted
// directly: no exec's argv may grow with the content, and every byte must still
// arrive.
func TestLargeSeededSetIsDelivered(t *testing.T) {
	covmap.ProveCoreOnPass(t, "helper-scoping", covmap.TierUnit)
	ensureGuestBinary(t)
	s, capture := captureExecSandbox(t, capturePayloads)
	// Well past the ceiling: seven briefs plus a mission, 40 KB each.
	const each = 40 << 10
	roles := map[string]seededFile{}
	for _, name := range []string{"orchestrator", "backend", "frontend", "qa", "docs", "infra", "translator"} {
		roles[name] = seededFile{Name: name + ".md", Content: strings.Repeat("b", each)}
	}
	inputs := campaignInputs{
		Declared: true,
		Mission:  seededFile{Name: missionFileName, Content: strings.Repeat("m", each)},
		Roles:    roles,
	}
	campaign := &model.Campaign{Name: "big", Network: "net", Members: []model.Member{
		{Name: "orchestrator", Role: "orchestrator", CLI: "codex", Sandbox: "box0", Ref: "box0.grp",
			Session: model.Session{Name: "box0"}},
	}}
	if err := s.configureChannels(context.Background(), campaign, campaign.Members[0], inputs); err != nil {
		t.Fatalf("a large seeded set must deliver: %v", err)
	}
	// MAX_ARG_STRLEN is 32 pages. Every command must sit far below it, and must
	// not scale with the payload at all.
	const maxArgStrLen = 32 * 4096
	commands, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	for cmd := range strings.SplitSeq(string(commands), "mkdir -p") {
		if len(cmd) > maxArgStrLen/8 {
			t.Fatalf("a delivery command scales with its payload: %d bytes", len(cmd))
		}
	}
	// Every byte arrived. The mission is the third exec: directories, then
	// member.json, then the orientation, then the seeded files.
	raw, err := os.ReadFile(capture + ".stdin")
	if err != nil {
		t.Fatal(err)
	}
	// The guest binary is streamed raw on the same channel, so only the base64
	// lines are seeded content.
	var delivered int
	for line := range strings.SplitSeq(strings.TrimRight(string(raw), "\n"), "\n") {
		if decoded, decErr := base64.StdEncoding.DecodeString(line); decErr == nil {
			delivered += len(decoded)
		}
	}
	if want := 8 * each; delivered < want {
		t.Fatalf("delivered %d bytes of seeded content, want at least %d", delivered, want)
	}
}
