package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/model"
)

// TestFanoutValidation: the key is a closed set, and a cap beside `none` is a
// contradiction the operator has to resolve, never a value to pick for them.
func TestFanoutValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile model.MemberProfile
		wantErr string
	}{
		{"native", model.MemberProfile{CLI: "claude", Fanout: "native"}, ""},
		{"none", model.MemberProfile{CLI: "codex", Fanout: "none"}, ""},
		{"native with a cap", model.MemberProfile{CLI: "opencode", Fanout: "native", FanoutMax: 4}, ""},
		{"a cap alone", model.MemberProfile{CLI: "claude", FanoutMax: 2}, ""},
		{"neither", model.MemberProfile{CLI: "claude"}, ""},
		{"unknown value", model.MemberProfile{CLI: "claude", Fanout: "on"}, `fanout: must be native or none, got "on"`},
		{"a cap beside none", model.MemberProfile{CLI: "claude", Fanout: "none", FanoutMax: 2}, "fanoutMax caps the sub-agents a seat runs at once, and fanout: none allows it none"},
		{"negative cap", model.MemberProfile{CLI: "claude", FanoutMax: -1}, "fanoutMax: must be positive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateModelConfig(tc.profile)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("expected an error containing %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// applyFanout runs the composed guest command twice against a throwaway HOME,
// as a resumed create would, and returns that HOME. Every assertion below is
// on the second pass: a write that stacks shows there and nowhere else.
func applyFanout(t *testing.T, cli string, profile model.MemberProfile, seed map[string]string) string {
	t.Helper()
	sandbox, capture := captureExecSandbox(t, `if [ "$1" = exec ]; then printf '%s' "$5" > "$CAPTURE"; fi`)
	home := t.TempDir()
	for rel, body := range seed {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	profile.CLI = cli
	member := model.Member{CLI: cli, Ref: "box.group", Profile: profile}
	if err := sandbox.applyFanoutConfig(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	command := capturedCommand(t, capture)
	runGuestCommand(t, home, command)
	runGuestCommand(t, home, command)
	return home
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, body)
	}
	return got
}

// TestApplyFanoutConfigClaudeMergesItsSettings: the image seeds the permissions
// and hooks a Claude Code seat runs under, so the switch is merged into that
// file and the rest of it is kept. `none` adds its two deny rules once.
func TestApplyFanoutConfigClaudeMergesItsSettings(t *testing.T) {
	seeded := map[string]string{".cs-claude/settings.json": `{"permissions":{"allow":["Read"],"deny":[]},"hooks":{"SessionStart":[]}}`}

	home := applyFanout(t, "claude", model.MemberProfile{Fanout: "native", FanoutMax: 3}, seeded)
	got := readJSON(t, filepath.Join(home, ".cs-claude", "settings.json"))
	if got["ultracode"] != true {
		t.Fatalf("ultracode not set: %v", got)
	}
	if _, ok := got["hooks"]; !ok {
		t.Fatalf("the seeded hooks were dropped: %v", got)
	}
	if deny := got["permissions"].(map[string]any)["deny"].([]any); len(deny) != 0 {
		t.Fatalf("native wrote a deny rule: %v", deny)
	}
	env, err := os.ReadFile(filepath.Join(home, ".cs-claude", "env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS=3\n", "CLAUDE_CODE_WORKFLOW_MAX_CONCURRENT_AGENTS=3\n"} {
		if n := strings.Count(string(env), line); n != 1 {
			t.Fatalf("%q appears %d times after two applies: %s", line, n, env)
		}
	}

	home = applyFanout(t, "claude", model.MemberProfile{Fanout: "none"}, seeded)
	got = readJSON(t, filepath.Join(home, ".cs-claude", "settings.json"))
	if _, ok := got["ultracode"]; ok {
		t.Fatalf("none set ultracode: %v", got)
	}
	deny := got["permissions"].(map[string]any)["deny"].([]any)
	if len(deny) != 2 || deny[0] != "Agent" || deny[1] != "Workflow" {
		t.Fatalf("deny = %v, want Agent and Workflow once each", deny)
	}
	if allow := got["permissions"].(map[string]any)["allow"].([]any); len(allow) != 1 {
		t.Fatalf("the seeded allow list was disturbed: %v", allow)
	}
	if _, err := os.Stat(filepath.Join(home, ".cs-claude", "env")); !os.IsNotExist(err) {
		t.Fatal("a seat without a cap had its env written")
	}
}

// TestApplyFanoutConfigCodexWritesWhereCodexReads: the instruction goes into
// the AGENTS.md every session reads, between markers so a resumed create
// replaces it, and the keys go above the first [table] for the reason
// applyModelConfig gives.
func TestApplyFanoutConfigCodexWritesWhereCodexReads(t *testing.T) {
	seeded := map[string]string{
		".cs-codex/config.toml": "approval_policy = \"on-request\"\n\n[tui.model_availability_nux]\n\"gpt-5.6-sol\" = 1\n",
		".cs-codex/AGENTS.md":   "# House rules\n\nCommit often.\n",
	}

	home := applyFanout(t, "codex", model.MemberProfile{Fanout: "native", FanoutMax: 4}, seeded)
	agents, err := os.ReadFile(filepath.Join(home, ".cs-codex", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(agents), "# House rules\n\nCommit often.\n") {
		t.Fatalf("the seeded instructions were disturbed: %s", agents)
	}
	if n := strings.Count(string(agents), fanoutBlockOpen); n != 1 {
		t.Fatalf("the block appears %d times after two applies: %s", n, agents)
	}
	for _, want := range []string{"## Parallel work", "one sub-agent per part at the same time", "Run at most 4 sub-agents at once."} {
		if !strings.Contains(string(agents), want) {
			t.Fatalf("AGENTS.md does not say %q: %s", want, agents)
		}
	}
	config, err := os.ReadFile(filepath.Join(home, ".cs-codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cap, table := strings.Index(string(config), "agents.max_concurrent_threads_per_session = 4"), strings.Index(string(config), "[tui.")
	if cap < 0 || table < 0 || cap > table {
		t.Fatalf("the cap is missing or inside a table: %s", config)
	}
	if strings.Contains(string(config), "agents.enabled") {
		t.Fatalf("native turned the feature off: %s", config)
	}
	if n := strings.Count(string(config), "agents.max_concurrent_threads_per_session"); n != 1 {
		t.Fatalf("the cap appears %d times after two applies: %s", n, config)
	}

	home = applyFanout(t, "codex", model.MemberProfile{Fanout: "none"}, seeded)
	config, err = os.ReadFile(filepath.Join(home, ".cs-codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"agents.enabled = false", "features.multi_agent_v2.enabled = false"} {
		at := strings.Index(string(config), key)
		if at < 0 || at > strings.Index(string(config), "[tui.") {
			t.Fatalf("%s is missing or inside a table: %s", key, config)
		}
	}
	if !strings.Contains(string(config), `approval_policy = "on-request"`) || !strings.Contains(string(config), `"gpt-5.6-sol" = 1`) {
		t.Fatalf("the seeded config was disturbed: %s", config)
	}
	if agents, _ := os.ReadFile(filepath.Join(home, ".cs-codex", "AGENTS.md")); string(agents) != seeded[".cs-codex/AGENTS.md"] {
		t.Fatalf("none wrote the instruction: %s", agents)
	}
}

// TestApplyFanoutConfigOpenCodeStaysInTheSeededDialect: the seeded opencode.json
// spells permissions in the first dialect, `permission` singular, and OpenCode 2
// drops those keys once a key from its own dialect appears beside them. So the
// write adds an `instructions` file and a `permission.task` rule, and nothing
// spelled the other way.
func TestApplyFanoutConfigOpenCodeStaysInTheSeededDialect(t *testing.T) {
	seeded := map[string]string{".cs-opencode/opencode.json": `{"model":"fireworks-ai/kimi","permission":{"*":"allow"},"disabled_providers":["opencode"]}`}

	home := applyFanout(t, "opencode", model.MemberProfile{Fanout: "native", FanoutMax: 2}, seeded)
	got := readJSON(t, filepath.Join(home, ".cs-opencode", "opencode.json"))
	for _, key := range []string{"agents", "permissions", "agent"} {
		if _, ok := got[key]; ok {
			t.Fatalf("wrote %q, which is the other dialect: %v", key, got)
		}
	}
	if got["permission"].(map[string]any)["*"] != "allow" || got["disabled_providers"] == nil {
		t.Fatalf("the seeded keys were disturbed: %v", got)
	}
	if _, ok := got["permission"].(map[string]any)["task"]; ok {
		t.Fatalf("native denied the tool: %v", got)
	}
	file := filepath.Join(home, ".cs-opencode", "fanout.md")
	instructions := got["instructions"].([]any)
	if len(instructions) != 1 || instructions[0] != file {
		t.Fatalf("instructions = %v after two applies, want %q once", instructions, file)
	}
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Parallel work", "one foreground batch", "Run at most 2 sub-agents at once."} {
		if !strings.Contains(string(text), want) {
			t.Fatalf("the instruction file does not say %q: %s", want, text)
		}
	}

	home = applyFanout(t, "opencode", model.MemberProfile{Fanout: "none"}, seeded)
	got = readJSON(t, filepath.Join(home, ".cs-opencode", "opencode.json"))
	if got["permission"].(map[string]any)["task"] != "deny" {
		t.Fatalf("none did not deny the tool: %v", got)
	}
	if _, ok := got["instructions"]; ok {
		t.Fatalf("none wrote an instruction: %v", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".cs-opencode", "fanout.md")); !os.IsNotExist(err) {
		t.Fatal("none wrote the instruction file")
	}

	// A cap alone asks for the cap and nothing else: OpenCode has no setting
	// that bounds how many sub-agents run at once, so the sentence is the cap.
	home = applyFanout(t, "opencode", model.MemberProfile{FanoutMax: 3}, seeded)
	text, err = os.ReadFile(filepath.Join(home, ".cs-opencode", "fanout.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(text) != "## Parallel work\n\nRun at most 3 sub-agents at once.\n" {
		t.Fatalf("a cap alone wrote %q", text)
	}
}

// TestApplyFanoutConfigSkipsUndeclaredMembers: a seat without the keys is left
// exactly as the image shipped it, so adding them changes nothing for the
// campaigns that do not use them.
func TestApplyFanoutConfigSkipsUndeclaredMembers(t *testing.T) {
	sandbox, capture := captureExecSandbox(t, `if [ "$1" = exec ]; then printf '%s' "$5" > "$CAPTURE"; fi`)
	for _, cli := range model.AdapterCLIs {
		member := model.Member{CLI: cli, Ref: "box.group", Profile: model.MemberProfile{CLI: cli}}
		if err := sandbox.applyFanoutConfig(context.Background(), member); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(capture); !os.IsNotExist(err) {
			t.Fatalf("%s: ran a guest command with nothing declared", cli)
		}
	}
}

// A seat set to native reads how to run its sub-agents, as one foreground
// batch, and a seat set to none or left alone reads what it always read. A
// seat with a hand-off line and native reads the rule once, in the section
// the line's own section points at.
func TestTheOrientationExplainsParallelWorkOnlyToANativeSeat(t *testing.T) {
	const heading = "## Parallel work"
	for _, who := range []string{"orchestrator", "dev"} {
		native := renderSeat(t, who, func(m *model.Member) { m.Profile.Fanout = model.FanoutNative })
		for _, want := range []string{heading, "run one sub-agent per part at the same time",
			"Run the sub-agents as one foreground batch, and wait for the whole batch inside your turn.",
			"Never run one in the background, and never end your turn while one runs."} {
			if !strings.Contains(fold(native), want) {
				t.Errorf("%s is set to native and is not told %q:\n%s", who, want, native)
			}
		}
		for _, set := range []func(*model.Member){func(*model.Member) {}, func(m *model.Member) { m.Profile.Fanout = model.FanoutNone }, func(m *model.Member) { m.Profile.FanoutMax = 2 }} {
			if without := renderSeat(t, who, set); strings.Contains(without, heading) || strings.Contains(without, "sub-agent") {
				t.Errorf("%s is not set to native and its orientation describes sub-agents:\n%s", who, without)
			}
		}
	}
	both := renderSeat(t, "dev", func(m *model.Member) { m.Profile.Fanout = model.FanoutNative; m.HandoffTokens = 60000 })
	if n := strings.Count(fold(both), "Never run one in the background"); n != 1 {
		t.Errorf("a seat with a line and native reads the background rule %d times:\n%s", n, both)
	}
	if !strings.Contains(both, "Run sub-agents as the Parallel work section says") {
		t.Errorf("the Long work section does not point at Parallel work:\n%s", both)
	}
	if at, long := strings.Index(both, heading), strings.Index(both, "## Long work"); at < 0 || long < 0 || at > long {
		t.Errorf("Parallel work comes before Long work: %d, %d\n%s", at, long, both)
	}
}
