package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codesweep-ai/campaign/internal/model"
)

// TestContextVariantSplitsWhatATranscriptCanName: a declared model may name a
// context window the transcript never records, and only the part before the
// bracket can be compared with what the turn answered on.
func TestContextVariantSplitsWhatATranscriptCanName(t *testing.T) {
	for _, tc := range []struct{ name, declared, base, variant string }{
		{"a context variant", "claude-opus-5-5[1m]", "claude-opus-5-5", "[1m]"},
		{"no variant", "claude-opus-5-5", "claude-opus-5-5", ""},
		{"a provider-qualified model", "anthropic/claude-opus-5-5", "anthropic/claude-opus-5-5", ""},
		{"an unclosed bracket is part of the name", "weird[1m", "weird[1m", ""},
		{"a leading bracket is not a variant", "[1m]", "[1m]", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, variant := contextVariant(tc.declared)
			if base != tc.base || variant != tc.variant {
				t.Errorf("contextVariant(%q) = %q, %q; want %q, %q", tc.declared, base, variant, tc.base, tc.variant)
			}
		})
	}
}

// namingModels is a guest whose transcript names exactly these models, which is
// what `cs-campaign create` greps out of it to confirm a declaration.
func namingModels(t *testing.T, models ...string) sandboxCLI {
	t.Helper()
	var lines strings.Builder
	for _, m := range models {
		fmt.Fprintf(&lines, "echo 'model=%s'\n", m)
	}
	dir := installFakeTool(t, "fake-sandbox", "case \"$1\" in\n  exec)\n"+lines.String()+"    ;;\nesac\nexit 0")
	return sandboxCLI{Bin: filepath.Join(dir, "fake-sandbox")}
}

// TestAssertDeclaredTurnConfigAcceptsAContextVariant: the check greps the API
// model id out of the transcript, and a context variant never appears there.
// Comparing the whole declaration failed every seat that named one, on a
// campaign that was configured correctly.
func TestAssertDeclaredTurnConfigAcceptsAContextVariant(t *testing.T) {
	for _, tc := range []struct {
		name     string
		declared string
		observed []string
		want     string
	}{
		{"a variant is satisfied by its base id", "claude-opus-5-5[1m]", []string{"claude-opus-5-5"}, ""},
		{"a variant beside other models", "claude-opus-5-5[1m]", []string{"claude-haiku-4-5", "claude-opus-5-5"}, ""},
		{"the base still has to be there", "claude-opus-5-5[1m]", []string{"claude-sonnet-5"}, "did not take"},
		{"a plain model is unchanged", "claude-opus-5-5", []string{"claude-opus-5-5"}, ""},
		{"a plain model that did not take", "claude-opus-5-5", []string{"claude-sonnet-5"}, "did not take"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &app{sandbox: namingModels(t, tc.observed...)}
			member := model.Member{Name: "dev", Ref: "dev.g", CLI: "claude", Model: tc.declared}
			got := a.assertDeclaredTurnConfig(t.Context(), member)
			switch {
			case tc.want == "" && got != "":
				t.Errorf("declared %q against %v: %s", tc.declared, tc.observed, got)
			case tc.want != "" && !strings.Contains(got, tc.want):
				t.Errorf("declared %q against %v: got %q, want it to mention %q", tc.declared, tc.observed, got, tc.want)
			case tc.want != "" && !strings.Contains(got, tc.declared):
				// A failure is read by someone holding the profile, so it quotes
				// what they wrote rather than the part the comparison used.
				t.Errorf("failure does not name the declaration as written: %q", got)
			}
		})
	}
}

// TestDeclaredSuffixSaysAVariantIsUnconfirmed: the confirmation line is the
// operator's only evidence that a declaration took, so it must not claim to
// cover the half the transcript cannot show.
func TestDeclaredSuffixSaysAVariantIsUnconfirmed(t *testing.T) {
	variant := declaredSuffix(model.Member{CLI: "claude", Model: "claude-opus-5-5[1m]", Effort: "high"})
	if !strings.Contains(variant, "[1m]") || !strings.Contains(variant, "does not name") {
		t.Errorf("a declared variant is not reported as unconfirmed: %q", variant)
	}
	plain := declaredSuffix(model.Member{CLI: "claude", Model: "claude-opus-5-5", Effort: "high"})
	if plain != " on model claude-opus-5-5, effort high (confirmed by the answering turn)" {
		t.Errorf("a plain declaration changed wording: %q", plain)
	}
}
