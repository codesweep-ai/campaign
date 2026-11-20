package cli

// What an operator holding only the binary can find (SAC-115). The binary
// carries its help, the manual, the playbook, the orientation and the starter
// profile init writes, and nothing else. These tests hold those surfaces to the
// profile's own structs, and keep them free of anything that exists only in
// this repository.

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/codesweep-ai/campaign"
	"github.com/codesweep-ai/campaign/internal/model"
)

// manualKeyRows reads the rows of MANUAL.md's "Every profile key" table as
// "where key" pairs.
func manualKeyRows(t *testing.T) []string {
	t.Helper()
	_, section, ok := strings.Cut(campaign.ManualMD, "\n### Every profile key\n")
	if !ok {
		t.Fatal(`MANUAL.md has no "### Every profile key" section`)
	}
	if end := strings.Index(section, "\n#"); end >= 0 {
		section = section[:end]
	}
	row := regexp.MustCompile("(?m)^\\| `([A-Za-z]+)` \\| ([a-z\\[\\]]+)(?:, per seat)? \\|")
	var out []string
	for _, m := range row.FindAllStringSubmatch(section, -1) {
		out = append(out, m[2]+" "+m[1])
	}
	return out
}

func structKeyRows() []string {
	var out []string
	for _, l := range profileLevels {
		for _, k := range yamlKeys(l.t) {
			out = append(out, l.where+" "+k)
		}
	}
	return out
}

// MANUAL.md: "Every profile key ... lists each key the profile takes." The
// table is the only complete list an operator can reach, so a key added to a
// profile struct without a row would exist only for someone reading the source.
func TestTheManualTableListsEveryProfileKey(t *testing.T) {
	got, want := manualKeyRows(t), structKeyRows()
	for _, k := range want {
		if !slices.Contains(got, k) {
			t.Errorf("MANUAL.md's Every profile key table has no row for %q; add one", k)
		}
	}
	for _, k := range got {
		if !slices.Contains(want, k) {
			t.Errorf("MANUAL.md's Every profile key table has a row for %q, which no profile struct takes", k)
		}
	}
}

// repoOnly matches what exists only in this repository: its documents, its
// source paths, its make targets and its ledger ids. A Go install path such as
// github.com/codesweep-ai/campaign/cmd/cs-campaign-member is a command an
// operator can run, and passes because cmd/ follows a slash there.
var repoOnly = regexp.MustCompile("(SPEC|PROTOCOL|README|CONTRIBUTING|INSTALL)\\.md|(^|[^/\\w])(internal|cmd|scripts|ledger)/|test/cassettes|`make (build|install)`|\\bSAC-[0-9]")

// The binary must not send an operator to a file it does not carry. Every
// surface it prints is checked: the two documents, the orientation template,
// every command's help and flag text, and the message create gives when the
// binary carries no member binary.
func TestTheShippedTextNamesNoRepositoryFile(t *testing.T) {
	surfaces := map[string]string{
		"MANUAL.md":                  campaign.ManualMD,
		"PLAYBOOK.md":                campaign.PlaybookMD,
		"the orientation template":   orientationTemplateText,
		"the starter profile":        profileHeader + authHint + optionalHint,
		"the no-member-binary error": errNoGuestBinary("v0.0.0-20261120000000-000000000000").Error(),
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		text := c.Short + "\n" + c.Long
		text += "\n" + c.Flags().FlagUsages()
		surfaces["help for "+c.CommandPath()] = text
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk((&app{}).root())
	for name, text := range surfaces {
		for i, line := range strings.Split(text, "\n") {
			if m := repoOnly.FindString(line); m != "" {
				t.Errorf("%s line %d names %q, which exists only in the repository: %s", name, i+1, m, strings.TrimSpace(line))
			}
		}
	}
}

func decodeProfileText(t *testing.T, text string) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profile.yaml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := decodeProfile(path)
	return err
}

const seatProfile = "apiVersion: codesweep.ai/v1alpha1\nkind: CampaignProfile\norchestrator:\n  cli: claude\n  repos: [{path: /srv/p}]\nagents:\n  backend:\n    cli: claude\n    repos: [{path: /srv/p}]\n"

// A guessed key is how an operator holding only the binary probes the
// profile, so the refusal has to teach: the level, the nearest real key, every
// key that level takes, and where the full list is. A Go type name teaches
// nothing.
func TestAnUnknownProfileKeyNamesItsLevelAndItsKeys(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"fanot: native", `a seat has no key "fanot" (did you mean "fanout"?)`},
		{"Model: x", `(did you mean "model"?)`},
		{"handoffTokens: 1000", `("handoffTokens" goes in a policy block)`},
		{"subagents: true", `a seat has no key "subagents"; a seat takes cli, policy, model`},
	} {
		err := decodeProfileText(t, seatProfile+"    "+tc.key+"\n")
		if err == nil {
			t.Fatalf("%s: decoded", tc.key)
		}
		msg := err.Error()
		for _, want := range []string{tc.want, "fanout, fanoutMax", "`cs-campaign manual`, under Configuration"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: the refusal lacks %q:\n%s", tc.key, want, msg)
			}
		}
		if strings.Contains(msg, "model.") {
			t.Errorf("%s: the refusal names a Go type:\n%s", tc.key, msg)
		}
	}
	// A wrong value is not an unknown key, and keeps its own message.
	if err := decodeProfileText(t, seatProfile+"    fanoutMax: lots\n"); err == nil || strings.Contains(err.Error(), "has no key") {
		t.Errorf("a bad value read as an unknown key: %v", err)
	}
}

// init's starter profile is where most operators first see a profile. It has
// to show every key, and every key it shows has to be real and at the right
// depth: the optional block, uncommented, must decode.
func TestTheStarterProfileNamesEveryKey(t *testing.T) {
	dir := t.TempDir()
	profile, err := profileFromFlags("claude", nil, "opencode", 1, "/srv/p", "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := scaffoldCampaign(&out, dir, "demo", profile); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "profile.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, row := range structKeyRows() {
		_, key, _ := strings.Cut(row, " ")
		if !regexp.MustCompile(`\b` + key + `\b`).MatchString(text) {
			t.Errorf("the starter profile never names %q", key)
		}
	}
	if !strings.Contains(out.String(), "cs-campaign manual") {
		t.Errorf("init's closing lines do not name the manual:\n%s", out.String())
	}
	var b2 strings.Builder
	b2.WriteString("apiVersion: codesweep.ai/v1alpha1\nkind: CampaignProfile\norchestrator:\n  cli: claude\n")
	for line := range strings.SplitSeq(optionalHint, "\n") {
		if rest, ok := strings.CutPrefix(line, "#   "); ok {
			b2.WriteString(rest + "\n")
		}
	}
	uncommented := b2.String()
	var p model.Profile
	d := yaml.NewDecoder(strings.NewReader(uncommented))
	d.KnownFields(true)
	if err := d.Decode(&p); err != nil {
		t.Errorf("the optional block, uncommented, does not decode: %v\n%s", explainProfileError(err), uncommented)
	}
}

// The two flags an operator gets wrong first: no orchestrator, and a clone URL
// where a path goes. Each refusal names the flag and what it takes.
func TestInitNamesWhatItsFlagsTake(t *testing.T) {
	if _, err := profileFromFlags("", nil, "", 0, "", ""); err == nil || !strings.Contains(err.Error(), "--orchestrator is required") || !strings.Contains(err.Error(), "claude, codex or opencode") {
		t.Errorf("no orchestrator: %v", err)
	}
	for _, url := range []string{"https://example.com/r.git", "git@example.com:o/r.git"} {
		if _, err := profileFromFlags("claude", nil, "", 0, url, ""); err == nil || !strings.Contains(err.Error(), "--repo takes a local path") {
			t.Errorf("--repo %s: %v", url, err)
		}
	}
	if err := decodeProfileText(t, strings.Replace(seatProfile, "/srv/p", "https://example.com/r.git", 1)); err == nil || !strings.Contains(err.Error(), "is a URL") {
		t.Errorf("a URL as a profile's repository path: %v", err)
	}
}

// create's refusal for a binary without a member binary names the install
// at this binary's own version, and nothing a release user cannot run.
func TestTheNoMemberBinaryErrorSaysWhatToInstall(t *testing.T) {
	msg := errNoGuestBinary("v0.0.0-20261120000000-0123456789ab").Error()
	for _, want := range []string{"go install github.com/codesweep-ai/campaign/cmd/cs-campaign-member@v0.0.0-20261120000000-0123456789ab", "CS_CAMPAIGN_GUEST_BIN"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message lacks %q: %s", want, msg)
		}
	}
}
