package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/model"
)

// TestModelConfigValidation: the field exists so model choice stops being an
// out-of-band manual step, which only holds if a declaration the adapter cannot
// honour is refused rather than written. Values also reach a shell, a TOML file
// and a JSON file, so anything outside the token class is rejected once here
// instead of escaped three ways.
func TestModelConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile model.MemberProfile
		wantErr string
	}{
		{"claude model and effort", model.MemberProfile{CLI: "claude", Model: "claude-opus-5", Effort: "high"}, ""},
		{"codex model and effort", model.MemberProfile{CLI: "codex", Model: "gpt-5.6-sol", Effort: "xhigh"}, ""},
		{"codex unknown effort tier", model.MemberProfile{CLI: "codex", Effort: "ultra"}, ""},
		{"opencode qualified slug", model.MemberProfile{CLI: "opencode", Model: "fireworks-ai/accounts/fireworks/models/kimi-k3"}, ""},
		{"neither declared", model.MemberProfile{CLI: "claude"}, ""},
		// opencode DOES honour effort for pairs that expose reasoning variants,
		// so the field is accepted; only the model it attaches to is required.
		{"opencode model and effort", model.MemberProfile{CLI: "opencode", Model: "zai/glm-5.2", Effort: "high"}, ""},
		{"opencode effort without model", model.MemberProfile{CLI: "opencode", Effort: "high"}, "effort requires model for opencode"},
		{"model with a space", model.MemberProfile{CLI: "claude", Model: "claude opus"}, "invalid model"},
		{"effort with a substitution", model.MemberProfile{CLI: "codex", Effort: "$(id)"}, "invalid effort"},
		{"model with a quote", model.MemberProfile{CLI: "codex", Model: `a"b`}, "invalid model"},
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

// runGuestCommand executes a generated provisioning command against a throwaway
// HOME. The commands are shell written for the guest, and the properties that
// matter — where a TOML key lands, whether a rewrite duplicates a line — are
// properties of running them, not of their text.
func runGuestCommand(t *testing.T, home, command string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("guest command failed: %v: %s", err, out)
	}
}

func capturedCommand(t *testing.T, capture string) string {
	t.Helper()
	b, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestApplyModelConfigCodexKeepsKeysOutOfTables: a bare key written after a
// [table] header belongs to that table, so appending model_reasoning_effort
// lands it inside whatever section codex wrote last — in practice
// [tui.model_availability_nux], whose values are integers, and the whole config
// then fails to load with "invalid type: string, expected u32". The member is
// dead on arrival, and the diagnosis points at the TUI rather than the write.
func TestApplyModelConfigCodexKeepsKeysOutOfTables(t *testing.T) {
	sandbox, capture := captureExecSandbox(t, `if [ "$1" = exec ]; then printf '%s' "$5" > "$CAPTURE"; fi`)
	home := t.TempDir()
	dir := filepath.Join(home, ".cs-codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A config in the state codex leaves it in once it has run: top-level keys
	// first, then tables it wrote itself.
	seeded := "approval_policy = \"on-request\"\nsandbox_mode = \"workspace-write\"\n\n[tui.model_availability_nux]\n\"gpt-5.6-sol\" = 1\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(seeded), 0o600); err != nil {
		t.Fatal(err)
	}
	member := model.Member{CLI: "codex", Ref: "box.group", Profile: model.MemberProfile{CLI: "codex", Model: "gpt-5.6-sol", Effort: "high"}}
	if err := sandbox.applyModelConfig(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	runGuestCommand(t, home, capturedCommand(t, capture))

	body, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	firstTable := strings.Index(got, "[tui.model_availability_nux]")
	if firstTable < 0 {
		t.Fatalf("the seeded table was lost: %s", got)
	}
	for _, key := range []string{`model = "gpt-5.6-sol"`, `model_reasoning_effort = "high"`} {
		at := strings.Index(got, key)
		if at < 0 {
			t.Fatalf("%s missing: %s", key, got)
		}
		if at > firstTable {
			t.Fatalf("%s landed inside a table, which codex cannot load: %s", key, got)
		}
	}
	if !strings.Contains(got, `"gpt-5.6-sol" = 1`) {
		t.Fatalf("the table's own contents were disturbed: %s", got)
	}
	// Create is resumable, so applying twice must converge rather than stack.
	runGuestCommand(t, home, capturedCommand(t, capture))
	body, err = os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(body), "model_reasoning_effort = "); n != 1 {
		t.Fatalf("re-applying duplicated the key %d times: %s", n, body)
	}
}

// TestApplyModelConfigClaudeRewritesEnv: cs-claude sources this file with
// `set -a`, so a duplicated assignment is not merely untidy — the last one wins
// and a resumed create could leave the member on a value nobody declared.
func TestApplyModelConfigClaudeRewritesEnv(t *testing.T) {
	sandbox, capture := captureExecSandbox(t, `if [ "$1" = exec ]; then printf '%s' "$5" > "$CAPTURE"; fi`)
	home := t.TempDir()
	dir := filepath.Join(home, ".cs-claude")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A pre-existing assignment, as a hand-injected pin or an earlier create
	// would have left it, plus an unrelated line that must survive.
	if err := os.WriteFile(filepath.Join(dir, "env"), []byte("ANTHROPIC_MODEL=claude-opus-4-8\nSOMETHING_ELSE=keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	member := model.Member{CLI: "claude", Ref: "box.group", Profile: model.MemberProfile{CLI: "claude", Model: "claude-opus-5", Effort: "low"}}
	if err := sandbox.applyModelConfig(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	runGuestCommand(t, home, capturedCommand(t, capture))

	body, err := os.ReadFile(filepath.Join(dir, "env"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if n := strings.Count(got, "ANTHROPIC_MODEL="); n != 1 {
		t.Fatalf("expected exactly one model assignment, got %d: %s", n, got)
	}
	if !strings.Contains(got, "ANTHROPIC_MODEL=claude-opus-5") {
		t.Fatalf("the stale assignment survived: %s", got)
	}
	if !strings.Contains(got, "CLAUDE_CODE_EFFORT_LEVEL=low") {
		t.Fatalf("effort not written: %s", got)
	}
	if !strings.Contains(got, "SOMETHING_ELSE=keep") {
		t.Fatalf("an unrelated assignment was dropped: %s", got)
	}
}

// TestApplyModelConfigOpenCodeKeepsProfile: opencode's model pin fails closed —
// without a resolvable model `run` errors rather than falling back — so the
// rest of the shipped profile (blanket permissions, disabled providers) has to
// survive the rewrite or the member comes up unusable in a different way.
func TestApplyModelConfigOpenCodeKeepsProfile(t *testing.T) {
	sandbox, capture := captureExecSandbox(t, `if [ "$1" = exec ]; then printf '%s' "$5" > "$CAPTURE"; fi`)
	home := t.TempDir()
	dir := filepath.Join(home, ".cs-opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	seeded := `{"$schema":"https://opencode.ai/config.json","model":"fireworks-ai/accounts/fireworks/models/kimi-k3","disabled_providers":["opencode"],"permission":{"*":"allow"}}`
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(seeded), 0o600); err != nil {
		t.Fatal(err)
	}
	member := model.Member{CLI: "opencode", Ref: "box.group", Profile: model.MemberProfile{CLI: "opencode", Model: "fireworks-ai/accounts/fireworks/models/other"}}
	if err := sandbox.applyModelConfig(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	runGuestCommand(t, home, capturedCommand(t, capture))

	body, err := os.ReadFile(filepath.Join(dir, "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err = json.Unmarshal(body, &got); err != nil {
		t.Fatalf("rewrote the profile into invalid JSON: %v: %s", err, body)
	}
	if got["model"] != "fireworks-ai/accounts/fireworks/models/other" {
		t.Fatalf("model not applied: %s", body)
	}
	if _, ok := got["permission"]; !ok {
		t.Fatalf("the shipped permission block was dropped: %s", body)
	}
	if _, ok := got["disabled_providers"]; !ok {
		t.Fatalf("disabled_providers was dropped, which re-enables the anonymous provider: %s", body)
	}
}

// TestApplyModelConfigOpenCodeAttachesEffortToItsModel: opencode hangs reasoning
// options off a named model rather than the session, so the effort has to land
// under provider.<provider>.models.<model>.options — anywhere else and the
// declaration is inert with nothing to say so.
func TestApplyModelConfigOpenCodeAttachesEffortToItsModel(t *testing.T) {
	sandbox, capture := captureExecSandbox(t, `if [ "$1" = exec ]; then printf '%s' "$5" > "$CAPTURE"; fi`)
	home := t.TempDir()
	dir := filepath.Join(home, ".cs-opencode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(`{"model":"old","permission":{"*":"allow"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	member := model.Member{CLI: "opencode", Ref: "box.group", Profile: model.MemberProfile{
		CLI: "opencode", Model: "fireworks-ai/accounts/fireworks/models/kimi-k3", Effort: "high",
	}}
	if err := sandbox.applyModelConfig(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	runGuestCommand(t, home, capturedCommand(t, capture))

	body, err := os.ReadFile(filepath.Join(dir, "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Model    string `json:"model"`
		Provider map[string]struct {
			Models map[string]struct {
				Options map[string]string `json:"options"`
			} `json:"models"`
		} `json:"provider"`
	}
	if err = json.Unmarshal(body, &got); err != nil {
		t.Fatalf("invalid JSON: %v: %s", err, body)
	}
	if got.Model != "fireworks-ai/accounts/fireworks/models/kimi-k3" {
		t.Fatalf("model not applied: %s", body)
	}
	// The slug splits on the FIRST separator: provider id, then the rest as the
	// model id, which is itself path-shaped.
	entry, ok := got.Provider["fireworks-ai"].Models["accounts/fireworks/models/kimi-k3"]
	if !ok {
		t.Fatalf("effort did not land under its model: %s", body)
	}
	if entry.Options["reasoningEffort"] != "high" {
		t.Fatalf("reasoningEffort = %q: %s", entry.Options["reasoningEffort"], body)
	}
}

// TestApplyModelConfigSkipsUndeclaredMembers: an undeclared member must be left
// exactly as the image shipped it, so adding the field changes nothing for the
// campaigns that do not use it.
func TestApplyModelConfigSkipsUndeclaredMembers(t *testing.T) {
	sandbox, capture := captureExecSandbox(t, `if [ "$1" = exec ]; then printf '%s' "$5" > "$CAPTURE"; fi`)
	for _, cli := range model.AdapterCLIs {
		member := model.Member{CLI: cli, Ref: "box.group", Profile: model.MemberProfile{CLI: cli}}
		if err := sandbox.applyModelConfig(context.Background(), member); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(capture); !os.IsNotExist(err) {
			t.Fatalf("%s: ran a guest command with nothing declared", cli)
		}
	}
}
