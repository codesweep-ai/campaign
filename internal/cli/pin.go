package cli

// The upstream surface — the cs-sandbox binary and the agent-tool
// families cs-campaign drives — is validated by hand (unit tests, doctor,
// check-live) and then FROZEN into a pin. doctor verifies identity against the
// pin, and create refuses to build a campaign on a deviating surface unless the
// operator explicitly accepts and the acceptance is recorded in the campaign
// record. While the sandbox branch is frozen, any deviation is unexpected by
// definition; after a validated upgrade, `cs-campaign pin --update` re-pins as
// a deliberate act whose repo copy is committed and reviewed.
//
// The pin covers what the campaign layer actually touches: the cs-sandbox
// binary and the 21 agent tools (3 CLIs x 7 verbs). The guest image id is NOT
// pinned yet: its tag is a per-build counter with no stable reference to
// resolve, so pinning it needs engine-specific plumbing, left for whoever does
// that migration.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// sandboxVersionPattern picks the version token out of what `cs-sandbox
// version` prints, which is one line of the shape `cs-sandbox <version>
// (<platform>, <toolchain>)`.
//
// Two shapes, because every tool in the family stamps `git describe --tags
// --always`: a tagged build reads as a semver, optionally v-prefixed and
// optionally carrying describe's commits-since suffix, and a repository with
// no tag yet reads as the bare short commit describe falls back to. Older
// builds said 0.0.1-snapshot-<rev>, which is the first shape; cs-sandbox has
// no tags today and says 6981299, which is the second. Both keep their
// -dirty when they have one — a pin that quietly dropped it would record a
// modified build under a clean revision's name.
//
// This is identity, not a compatibility gate. Development builds have never
// carried a version that ordered against anything, so nothing compares two of
// these: the pin records the string and reports when it moves. What actually
// gates a surface are the capability probes in inspect.go.
var sandboxVersionPattern = regexp.MustCompile(`\b(?:v?[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?|[0-9a-f]{7,40}(?:-[A-Za-z0-9.-]+)?)\b`)

type pinFile struct {
	PinnedAt       time.Time         `json:"pinnedAt"`
	SandboxVersion string            `json:"sandboxVersion"`
	Tools          map[string]string `json:"tools"` // basename -> sha256 hex of file contents
	// Fixtures is the REPLAY surface: the tools that make a recorded campaign
	// replayable, pinned separately from the upstream surface a campaign runs
	// on. Today that is cs-vcr alone.
	//
	// Separate because the two are needed in different places. cs-sandbox and
	// the agent tools must be present for `create` to build anything, so a
	// missing one is a refusal. cs-vcr is needed only to record or replay a
	// cassette: a host that runs campaigns for real does not have it and must
	// not be told its surface is broken because of that. So this is recorded
	// when cs-vcr is there, omitted when it is not, and never gates create.
	Fixtures map[string]string `json:"fixtures,omitempty"`
	// FixtureRuleset is cs-vcr's normalization ruleset, as `cs-vcr config`
	// reports it: v11, v12. It is the number that actually decides whether a
	// committed cassette can be replayed at all, and it moves independently of
	// the binary hash — a cs-vcr rebuilt with no rule change keeps it, and one
	// that changes a rule bumps it and invalidates every recording made before.
	// Worth recording beside the hash for the same reason the sandbox version
	// is recorded beside its hash: the hash says WHICH build, this says what
	// that build will accept.
	FixtureRuleset string `json:"fixtureRuleset,omitempty"`
	Note           string `json:"note,omitempty"`
}

// fixtureToolNames is the replay surface: present on a machine that records or
// replays cassettes, absent on one that only runs campaigns.
func fixtureToolNames() []string { return []string{"cs-vcr"} }

var vcrRulesetPattern = regexp.MustCompile(`ruleset\s+(v[0-9]+)`)

// fixtureSurface reads the replay surface, and reports nothing at all when it
// is not installed. Absence is the ordinary case on a campaign host, so it is
// not an error here and not a deviation later — only a difference between two
// hosts that both have it is worth a word.
func (a *app) fixtureSurface() (map[string]string, string) {
	var tools map[string]string
	for _, name := range fixtureToolNames() {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		sum, err := hashFile(path)
		if err != nil {
			continue
		}
		if tools == nil {
			tools = map[string]string{}
		}
		tools[name] = sum
	}
	if tools["cs-vcr"] == "" {
		return tools, ""
	}
	// `cs-vcr config` prints the resolved configuration, one field per line,
	// and the ruleset is the only place the version is stated. Best effort: a
	// build that cannot report it still pins by hash.
	out, err := exec.Command("cs-vcr", "config").Output()
	if err != nil {
		return tools, ""
	}
	if m := vcrRulesetPattern.FindSubmatch(out); m != nil {
		return tools, string(m[1])
	}
	return tools, ""
}

// pinnedToolNames is the fixed matrix of upstream artifacts the campaign layer
// depends on. "cs-sandbox" is hashed from the resolved sandbox binary; the rest
// resolve via PATH exactly as the campaign layer invokes them.
func pinnedToolNames() []string {
	names := []string{"cs-sandbox"}
	for _, cli := range []string{"claude", "codex", "opencode"} {
		for _, suffix := range []string{"", "-remote", "-remote-forget", "-remote-output", "-remote-sessions", "-remote-status", "-turn"} {
			names = append(names, "cs-"+cli+suffix)
		}
	}
	return names
}

func pinPath() string {
	if p := os.Getenv("CS_CAMPAIGN_PIN"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "pin.json"
	}
	return filepath.Join(home, ".config", "cs-campaign", "pin.json")
}

func hashFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(content)), nil
}

// resolveTool maps a pin entry to the file that would actually execute:
// "cs-sandbox" is the configured sandbox binary (which may itself be a bare
// name needing PATH resolution), everything else resolves via PATH.
func (a *app) resolveTool(name string) (string, error) {
	if name == "cs-sandbox" {
		if filepath.Base(a.sandbox.Bin) != a.sandbox.Bin {
			return a.sandbox.Bin, nil
		}
		return exec.LookPath(a.sandbox.Bin)
	}
	return exec.LookPath(name)
}

// currentFingerprint reads the live upstream surface. A missing tool is an
// error here (an incomplete surface must not be pinned); verifyPin reports
// missing tools as deviations instead.
func (a *app) currentFingerprint(ctx context.Context) (pinFile, error) {
	reported, err := a.sandbox.version(ctx)
	if err != nil {
		return pinFile{}, fmt.Errorf("cs-sandbox version probe: %w", err)
	}
	match := sandboxVersionPattern.FindString(reported)
	if match == "" {
		return pinFile{}, fmt.Errorf("unrecognized cs-sandbox version output %q", reported)
	}
	fingerprint := pinFile{PinnedAt: time.Now().UTC(), SandboxVersion: match, Tools: map[string]string{}}
	for _, name := range pinnedToolNames() {
		path, err := a.resolveTool(name)
		if err != nil {
			return pinFile{}, fmt.Errorf("cannot pin an incomplete surface: %s not found (%w)", name, err)
		}
		sum, err := hashFile(path)
		if err != nil {
			return pinFile{}, fmt.Errorf("hash %s: %w", name, err)
		}
		fingerprint.Tools[name] = sum
	}
	fingerprint.Fixtures, fingerprint.FixtureRuleset = a.fixtureSurface()
	return fingerprint, nil
}

// compareFixtures lists how the replay surface differs from the pin.
//
// Reported, never gating. A campaign host has no cs-vcr and is not broken for
// that, so an absent fixture surface says nothing; only a host that HAS one
// which disagrees with the pin is worth a line. What actually refuses a
// mismatched replay is the ruleset check the smoke tier runs against the
// cassettes themselves, which compares the recordings rather than the binary
// and is therefore the precise question.
func compareFixtures(pin, live pinFile) []string {
	if len(live.Fixtures) == 0 {
		return nil
	}
	var out []string
	if pin.FixtureRuleset != "" && live.FixtureRuleset != "" && pin.FixtureRuleset != live.FixtureRuleset {
		out = append(out, fmt.Sprintf("cs-vcr normalization ruleset %s, pinned %s — every committed cassette needs re-recording (`make fixtures`)",
			live.FixtureRuleset, pin.FixtureRuleset))
	}
	names := make([]string, 0, len(pin.Fixtures))
	for name := range pin.Fixtures {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		liveSum, ok := live.Fixtures[name]
		if !ok || liveSum == pin.Fixtures[name] {
			continue
		}
		out = append(out, fmt.Sprintf("%s content changed (%.12s… -> %.12s…)", name, pin.Fixtures[name], liveSum))
	}
	return out
}

func loadPin() (pinFile, bool, error) {
	content, err := os.ReadFile(pinPath())
	if os.IsNotExist(err) {
		return pinFile{}, false, nil
	}
	if err != nil {
		return pinFile{}, false, err
	}
	var pin pinFile
	if err := json.Unmarshal(content, &pin); err != nil {
		return pinFile{}, false, fmt.Errorf("unreadable pin %s: %w", pinPath(), err)
	}
	return pin, true, nil
}

// comparePin lists every way live deviates from pin, in stable order. Empty
// means PINNED-OK.
func comparePin(pin, live pinFile) []string {
	var deviations []string
	if pin.SandboxVersion != live.SandboxVersion {
		deviations = append(deviations, fmt.Sprintf("cs-sandbox version %s, pinned %s", live.SandboxVersion, pin.SandboxVersion))
	}
	names := make([]string, 0, len(pin.Tools))
	for name := range pin.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		liveSum, ok := live.Tools[name]
		switch {
		case !ok:
			deviations = append(deviations, fmt.Sprintf("%s missing from PATH (pinned %.12s…)", name, pin.Tools[name]))
		case liveSum != pin.Tools[name]:
			deviations = append(deviations, fmt.Sprintf("%s content changed (%.12s… -> %.12s…)", name, pin.Tools[name], liveSum))
		}
	}
	return deviations
}

// verifyPin compares the live surface against the pin. pinned=false means no
// pin exists — the caller decides how loudly to warn; deviations are only
// meaningful when pinned.
func (a *app) verifyPin(ctx context.Context) (deviations []string, pin pinFile, pinned bool, err error) {
	pin, pinned, err = loadPin()
	if err != nil || !pinned {
		return nil, pin, pinned, err
	}
	live := pinFile{SandboxVersion: "", Tools: map[string]string{}}
	if reported, versionErr := a.sandbox.version(ctx); versionErr != nil {
		deviations = append(deviations, fmt.Sprintf("cs-sandbox version probe failed: %v (pinned %s)", versionErr, pin.SandboxVersion))
	} else if match := sandboxVersionPattern.FindString(reported); match == "" {
		deviations = append(deviations, fmt.Sprintf("unrecognized cs-sandbox version output %q (pinned %s)", reported, pin.SandboxVersion))
	} else {
		live.SandboxVersion = match
	}
	if live.SandboxVersion == "" {
		live.SandboxVersion = "(unknown)"
	}
	for name := range pin.Tools {
		path, lookErr := a.resolveTool(name)
		if lookErr != nil {
			continue // reported as missing by comparePin
		}
		if sum, hashErr := hashFile(path); hashErr == nil {
			live.Tools[name] = sum
		}
	}
	// The version-probe deviations above are carried out with the comparison's
	// own. Dropping them left the operator with "cs-sandbox version (unknown)"
	// and no way to tell a failed probe from an unparseable one.
	return append(deviations, comparePin(pin, live)...), pin, true, nil
}

func (a *app) pinCmd() *cobra.Command {
	var update bool
	var note string
	cmd := &cobra.Command{Use: "pin", Short: "Record the validated upstream surface", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		live, err := a.currentFingerprint(c.Context())
		if err != nil {
			return err
		}
		live.Note = note
		if existing, pinned, err := loadPin(); err != nil {
			return err
		} else if pinned {
			if deviations := comparePin(existing, live); len(deviations) == 0 {
				// The surface has not moved. The note still can, and must.
				//
				// The pin's own sequence is two pins of one surface: a
				// provisional pin so the validating run can create at all, then
				// a re-pin carrying that run's evidence. The surface is
				// identical across the two by construction — that is the point
				// — so a compare that only looks at the surface reports nothing
				// to do and the second half never lands. What that leaves
				// behind is a validated surface whose pin still says it was
				// never validated, which is precisely the state the pin this
				// replaced had been sitting in.
				//
				// The replay surface is the same case again. It is not part of
				// comparePin, because a host without cs-vcr must not read as
				// deviating — so a cs-vcr that moved leaves the upstream
				// compare empty and would be dropped here with everything else.
				fixtures := compareFixtures(existing, live)
				sameNote := note == "" || note == existing.Note
				if sameNote && len(fixtures) == 0 && len(live.Fixtures) == len(existing.Fixtures) {
					fmt.Fprintln(c.OutOrStdout(), "pin unchanged:", existing.SandboxVersion)
					return nil
				}
				switch {
				case len(fixtures) > 0:
					fmt.Fprintln(c.OutOrStdout(), "upstream unchanged; recording the replay surface:\n  "+strings.Join(fixtures, "\n  "))
				case !sameNote:
					fmt.Fprintln(c.OutOrStdout(), "surface unchanged; recording the new note")
				default:
					fmt.Fprintln(c.OutOrStdout(), "upstream unchanged; recording the replay surface")
				}
			} else if !update {
				return fmt.Errorf("refusing to overwrite a pin the live surface deviates from (%d deviations; first: %s) — re-validate the upstream, then `pin --update`", len(deviations), deviations[0])
			}
		}
		path := pinPath()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(live, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(c.OutOrStdout(), "pinned %s + %d tools -> %s\n", live.SandboxVersion, len(live.Tools)-1, path)
		fmt.Fprintln(c.OutOrStdout(), "commit the repo copy of this pin so the acceptance is reviewed")
		return nil
	}}
	cmd.Flags().BoolVar(&update, "update", false, "replace a pin the live surface deviates from (after re-validating)")
	cmd.Flags().StringVar(&note, "note", "", "why this surface is trusted (e.g. the validation evidence)")
	return cmd
}

// archiveUpstreamFingerprint writes the end-of-campaign surface snapshot and
// pin verdict into the archive. Best-effort by design: probe failures are
// recorded inside the file (host-surface metadata must not block evidence
// collection the way a missing member channel does).
func (a *app) archiveUpstreamFingerprint(ctx context.Context, root string) {
	snapshot := struct {
		At         time.Time `json:"at"`
		Live       *pinFile  `json:"live,omitempty"`
		Pinned     bool      `json:"pinned"`
		Deviations []string  `json:"deviations,omitempty"`
		Error      string    `json:"error,omitempty"`
	}{At: time.Now().UTC()}
	if live, err := a.currentFingerprint(ctx); err != nil {
		snapshot.Error = err.Error()
	} else {
		snapshot.Live = &live
	}
	if deviations, _, pinned, err := a.verifyPin(ctx); err != nil {
		snapshot.Error = strings.TrimSpace(snapshot.Error + "; " + err.Error())
	} else {
		snapshot.Pinned, snapshot.Deviations = pinned, deviations
	}
	if encoded, err := json.MarshalIndent(snapshot, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(root, "upstream-fingerprint.json"), append(encoded, '\n'), 0o600)
	}
}

// reportPin prints the pin verdict for doctor: an error return means doctor
// must fail. An unpinned environment warns loudly but does not fail doctor —
// create records the same fact into the campaign it builds.
func (a *app) reportPin(ctx context.Context, out func(string)) error {
	deviations, pin, pinned, err := a.verifyPin(ctx)
	if err != nil {
		return err
	}
	if !pinned {
		out("WARN upstream surface UNPINNED — validate it, then run `cs-campaign pin`")
		return nil
	}
	if len(deviations) > 0 {
		return fmt.Errorf("upstream surface deviates from pin %s (pinned %s):\n  %s\nre-validate and re-pin deliberately; do not campaign on an unvalidated surface",
			pin.SandboxVersion, pin.PinnedAt.Format("2006-01-02"), strings.Join(deviations, "\n  "))
	}
	out(fmt.Sprintf("ok  upstream matches pin: %s + %d tools (pinned %s)", pin.SandboxVersion, len(pin.Tools)-1, pin.PinnedAt.Format("2006-01-02")))
	// The replay surface, when this host has one. A warning rather than a
	// failure: cs-vcr records and replays cassettes and has no part in running
	// a campaign, so a host without it is complete, and a host whose copy has
	// moved can still campaign — it just cannot be trusted to reproduce a
	// recording until the cassettes are re-made.
	live, err := a.currentFingerprint(ctx)
	if err != nil {
		return nil
	}
	drift := compareFixtures(pin, live)
	switch {
	case len(live.Fixtures) == 0:
		// Nothing installed, nothing to say.
	case len(drift) > 0:
		out("WARN replay surface deviates from pin:\n  " + strings.Join(drift, "\n  "))
	default:
		out("ok  replay surface matches pin: cs-vcr ruleset " + live.FixtureRuleset)
	}
	return nil
}
