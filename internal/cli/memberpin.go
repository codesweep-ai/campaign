package cli

// The pin answers for the host, and the host is not where the work
// happens. `doctor` hashes the operator's ~/.local/bin and reports the surface
// verified; members receive their tools from the image cs-sandbox builds, and
// nothing spanned the boundary. On trial-01 the pin line named the very
// revision that fixed the driver while all three guests ran the one before it.
//
// This is the same comparison pointed at the plane that matters. Three
// mechanics, each verified against a live microVM (2026-08-11) rather than read
// out of the source, shape it:
//
//  1. A member's tools live at ~/.local/bin — the SAME literal path as the
//     host's, since the guest home is /home/<the host user>. The two planes are
//     indistinguishable by path; only which side of the exec you run on tells
//     them apart.
//  2. The image's home skeleton (/sandbox/home) is copied into a member ONCE,
//     on first boot, gated by ~/.cs-sandbox-initialized. A stale member stays
//     stale across every reboot, and rebuilding the image never repairs a
//     member that already exists.
//  3. cs-<cli>-remote deploys cs-<cli>-turn to whatever machine it drives
//     before it runs (an md5 compare + scp), so a HOST-driven dispatch silently
//     heals the driver — even one that fails on auth without running a turn —
//     and an orchestrator-driven dispatch pushes the ORCHESTRATOR's copy back
//     onto agents. The measurement is therefore perishable, and it must be
//     taken before anything dispatches. campaignDoctor runs it first, and
//     create reaches it before warmup (which is opt-in and host-driven, i.e.
//     the step that would make members look right just before an orchestrator
//     makes them wrong again).
//
// Detection only: the fix belongs at the image, not in the member. See
// harnessRemedy for why patching a guest is refused rather than offered.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
)

// memberPinnedToolNames is the pinned surface MINUS cs-sandbox: a member never
// drives sandboxes, so the binary is absent there by design and demanding it
// would report a deviation on every healthy member.
func memberPinnedToolNames() []string {
	var names []string
	for _, name := range pinnedToolNames() {
		if name != "cs-sandbox" {
			names = append(names, name)
		}
	}
	return names
}

// memberToolProbe hashes, inside the member, the file each pinned name would
// actually execute. It follows the move-aside: on the orchestrator the
// cs-*-remote family is replaced by symlinks to cs-campaign-member (the guard face) with the real
// tools under ~/.local/share/cs-campaign/real, so hashing ~/.local/bin blindly
// would report 15 deviations on every healthy campaign.
//
// A missing sha256sum is reported rather than skipped: "we asked and cannot
// tell" is precisely the state this check exists to remove.
func memberToolProbe() string {
	return `command -v sha256sum >/dev/null 2>&1 || { echo "PROBE-ERROR sha256sum not available in this member"; exit 0; }; ` +
		`for t in ` + strings.Join(memberPinnedToolNames(), " ") + `; do ` +
		`p="$HOME/` + guestBinDir + `/$t"; r="$HOME/` + guestRealToolsDir + `/$t"; ` +
		`if [ -L "$p" ]; then l=$(readlink "$p"); ` +
		`case "${l##*/}" in ` +
		`cs-campaign-member) if [ -f "$r" ]; then echo "$t guarded $(sha256sum "$r" | cut -d" " -f1)"; else echo "$t guarded-missing -"; fi ;; ` +
		`*) echo "$t symlink $l" ;; esac; ` +
		`elif [ -f "$p" ]; then echo "$t plain $(sha256sum "$p" | cut -d" " -f1)"; ` +
		`else echo "$t missing -"; fi; done`
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// memberHarness measures one member's upstream tool surface and compares it to
// the pin. pinned=false means no pin exists — the caller warns, exactly as the
// host-side check does, rather than inventing a verdict.
func (a *app) memberHarness(ctx context.Context, member model.Member) (check model.HarnessCheck, pinned bool, err error) {
	pin, pinned, err := loadPin()
	if err != nil || !pinned {
		return model.HarnessCheck{}, pinned, err
	}
	out, err := a.sandbox.memberOutput(ctx, member, memberToolProbe())
	if err != nil {
		return model.HarnessCheck{}, true, fmt.Errorf("harness probe failed: %w", err)
	}
	text := string(out)
	if strings.Contains(text, "PROBE-ERROR") {
		return model.HarnessCheck{}, true, fmt.Errorf("harness probe failed: %s", firstLineContaining(text, "PROBE-ERROR"))
	}
	observed := map[string][2]string{} // tool -> {state, value}
	for line := range strings.SplitSeq(text, "\n") {
		if fields := strings.Fields(line); len(fields) == 3 {
			observed[fields[0]] = [2]string{fields[1], fields[2]}
		}
	}
	check = model.HarnessCheck{CheckedAt: time.Now().UTC(), Pinned: true, Tools: map[string]string{}}
	for _, name := range memberPinnedToolNames() {
		want := pin.Tools[name]
		got, reported := observed[name]
		state, value := got[0], got[1]
		if sha256Pattern.MatchString(value) {
			check.Tools[name] = value
		}
		switch {
		case !reported:
			check.Deviations = append(check.Deviations, name+": the member did not answer for this tool")
		case state == "missing":
			check.Deviations = append(check.Deviations, fmt.Sprintf("%s: absent from the member's ~/%s (pinned %.12s…)", name, guestBinDir, want))
		case state == "guarded-missing":
			check.Deviations = append(check.Deviations, fmt.Sprintf("%s: guarded, but the real tool is absent from ~/%s (pinned %.12s…)", name, guestRealToolsDir, want))
		case state == "symlink":
			check.Deviations = append(check.Deviations, fmt.Sprintf("%s: is a symlink to %q, which is neither the pinned tool nor the guard", name, value))
		case value != want:
			check.Deviations = append(check.Deviations, fmt.Sprintf("%s: content differs (pinned %.12s…, member %.12s…)", name, want, value))
		}
	}
	sort.Strings(check.Deviations)
	return check, true, nil
}

// archiveMemberHarness re-measures every member's harness at archive time and
// diffs it against what doctor last recorded, then writes both into the
// evidence. Create-time alone would not have caught trial-01's actual sin: that
// fleet read its driver source, diagnosed the divergence itself, and then
// hand-patched the binary on all three guests mid-run — an unpinned harness
// modification no campaign record noted. A create-vs-archive delta is what
// makes that visible after the fact, and it must run BEFORE destroy, for the
// same reason verify-fleet does.
//
// Best-effort, like the host fingerprint: a probe failure is recorded inside
// the file rather than aborting evidence collection.
func (a *app) archiveMemberHarness(ctx context.Context, root string, campaign *model.Campaign) {
	type row struct {
		Member     string            `json:"member"`
		CLI        string            `json:"cli"`
		Recorded   map[string]string `json:"recordedByLastDoctor,omitempty"`
		RecordedAt string            `json:"recordedAt,omitempty"`
		AtArchive  map[string]string `json:"atArchive,omitempty"`
		Deviations []string          `json:"deviationsFromPin,omitempty"`
		Changed    []string          `json:"changedSinceLastDoctor,omitempty"`
		Error      string            `json:"error,omitempty"`
	}
	snapshot := struct {
		At      time.Time `json:"at"`
		Pinned  bool      `json:"pinned"`
		Members []row     `json:"members"`
	}{At: time.Now().UTC()}
	var anomalies []string
	for _, member := range campaign.Members {
		entry := row{Member: member.Name, CLI: member.CLI}
		if member.Harness != nil {
			entry.Recorded = member.Harness.Tools
			entry.RecordedAt = member.Harness.CheckedAt.Format(time.RFC3339)
		}
		check, pinned, err := a.memberHarness(ctx, member)
		snapshot.Pinned = pinned
		switch {
		case err != nil:
			entry.Error = err.Error()
			anomalies = append(anomalies, fmt.Sprintf("%s: harness could not be measured at archive time: %v", member.Name, err))
		case !pinned:
			entry.Error = "no pin on this host to compare against"
		default:
			entry.AtArchive, entry.Deviations = check.Tools, check.Deviations
			for _, name := range memberPinnedToolNames() {
				before, had := entry.Recorded[name]
				after, has := check.Tools[name]
				if had && has && before != after {
					entry.Changed = append(entry.Changed, fmt.Sprintf("%s: %.12s… -> %.12s…", name, before, after))
				}
			}
			if len(entry.Changed) > 0 {
				anomalies = append(anomalies, fmt.Sprintf("%s: %d harness tool(s) CHANGED during the run: %s",
					member.Name, len(entry.Changed), strings.Join(entry.Changed, ", ")))
			}
			if len(check.Deviations) > 0 {
				anomalies = append(anomalies, fmt.Sprintf("%s: harness deviates from the pin at archive time: %s",
					member.Name, strings.Join(check.Deviations, "; ")))
			}
		}
		snapshot.Members = append(snapshot.Members, entry)
	}
	if encoded, err := json.MarshalIndent(snapshot, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(root, "member-harness.json"), append(encoded, '\n'), 0o600)
	}
	if len(anomalies) > 0 {
		_ = os.WriteFile(filepath.Join(root, "HARNESS-ANOMALY.txt"), []byte(strings.Join(append([]string{
			"The harness these members ran is not the harness the pin certifies.",
			"A tool that CHANGED during the run was modified after create — either by a",
			"host-driven dispatch redeploying cs-*-turn, or by someone patching a guest.",
			"",
		}, anomalies...), "\n")+"\n"), 0o600)
	}
}

func firstLineContaining(text, needle string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if strings.Contains(line, needle) {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "PROBE-ERROR"))
		}
	}
	return strings.TrimSpace(text)
}

// harnessRemedy is the whole point of the row. Every symptom trial-01 produced
// — dispatches that failed seven minutes into a green pre-flight, members that
// saw a dispatch file arrive with no turn and no error, a fleet that diagnosed
// it by reading driver source and then hand-patched three guests — was missing
// exactly one sentence: "this member is running something else." Printing the
// diagnosis without the procedure would leave the next operator to improvise
// the same unrecorded repair, so the fix is spelled out in full, including the
// two plausible wrong turns.
func harnessRemedy(campaign string, pinVersion string) string {
	return strings.Join([]string{
		"",
		"HOW TO FIX — a member's harness is not the pinned harness.",
		"",
		"  What this means: members do NOT run the tools in your ~/.local/bin. They run the",
		"  copy baked into the sandbox image, and the image's home skeleton (/sandbox/home)",
		"  is copied into a member ONCE, on its first boot. So a member created from a stale",
		"  image keeps stale tools for its entire life, and no reboot repairs it. The host-side",
		"  pin line above (`upstream matches pin`) speaks only for the host.",
		"",
		"  Fix it at the image, in this order:",
		"    1. cd <your cs-sandbox checkout> && git log -1 --oneline",
		"         confirm the tree is the pinned revision " + pinVersion,
		"    2. cs-sandbox build",
		"         rebuilds the podman image AND the Firecracker rootfs members boot from;",
		"         a stale rootfs is what produced this on trial-01, and rebuilding only one",
		"         of the two leaves it in place",
		"    3. cs-campaign destroy " + campaign,
		"         these members cannot be repaired in place — see below",
		"    4. re-run your `cs-campaign create " + campaign + " …`",
		"         new members are seeded from the rebuilt image",
		"    5. cs-campaign doctor " + campaign,
		"         this check must print ok before you dispatch anything",
		"",
		"  Do NOT patch the tool inside the member. It is an unpinned harness modification",
		"  that no campaign record notes, it leaves the image stale so your next campaign",
		"  reproduces this exactly, and of the 21 tools only cs-*-turn would even survive.",
		"",
		"  Do NOT re-run a dispatch and read success as proof. `cs-<cli>-remote` deploys",
		"  cs-<cli>-turn onto whatever machine it drives before running (md5 compare + scp),",
		"  so ANY host-driven dispatch — including one that fails on auth without running a",
		"  turn — silently overwrites that one file with the host's copy. The symptom",
		"  disappears while the other 20 tools stay stale, and an orchestrator-driven",
		"  dispatch then pushes the orchestrator's stale driver straight back onto agents.",
		"",
		"  If the deviation is INTENDED (you moved upstream deliberately), do not work around",
		"  it here: re-validate the new surface, `cs-campaign pin --update --note '<why>'`,",
		"  commit the pin, rebuild the image, then recreate the campaign.",
		"",
	}, "\n")
}
