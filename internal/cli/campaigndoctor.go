package cli

// The campaign-scoped doctor verifies the INSTANTIATION, not the
// host — closing the gap between "the environment was validated"
// and "this campaign is wired the way the profile declared". It runs
// automatically at the tail of create and on demand as `doctor <campaign>`.
// The guard check does not trust presence: it fires a deliberate wrong-family
// probe and expects the refusal, so the layer below is proven, not
// assumed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
)

// guestManifest mirrors what configureOrchestrator installs.
type guestManifest struct {
	Campaign string `json:"campaign"`
	Network  string `json:"network"`
	Agents   map[string]struct {
		CLI     string `json:"cli"`
		Sandbox string `json:"sandbox"`
		Session string `json:"session"`
	} `json:"agents"`
}

func (a *app) campaignDoctor(ctx context.Context, out io.Writer, campaign *model.Campaign) error {
	var orchestrator *model.Member
	var agents []model.Member
	for i := range campaign.Members {
		if campaign.Members[i].Role == "orchestrator" {
			orchestrator = &campaign.Members[i]
		} else {
			agents = append(agents, campaign.Members[i])
		}
	}
	if orchestrator == nil {
		return errors.New("campaign has no orchestrator")
	}
	var problems []string
	report := func(ok bool, format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		if ok {
			fmt.Fprintln(out, "ok  "+line)
		} else {
			fmt.Fprintln(out, "BAD "+line)
			problems = append(problems, line)
		}
	}

	// The harness the MEMBERS run. This goes first and touches every
	// member including the orchestrator, because the measurement is perishable:
	// cs-<cli>-remote redeploys a member's turn driver before every dispatch, so
	// any check that lets a dispatch happen first is reading a repair rather than
	// the state the campaign booted with. (Check 3's guard probe is deliberately
	// wrong-family, so the guard refuses before the real tool — and its deploy —
	// is reached; ordering this first keeps that from being load-bearing.)
	remedy := a.reportMemberHarness(ctx, campaign, report)

	// 1. Manifest fidelity: the roster the guard and helper route by must be
	// the roster the campaign record declares — name, CLI, sandbox, session.
	//
	// Two planes meet in this file. Every exec below addresses a member by its
	// HOST reference (<name>.<group>); the manifest comparison further down
	// stays on the bare in-group name, because that is what the orchestrator
	// and the guard route by — qualifying it there would report drift on every
	// well-formed campaign.
	raw, err := a.sandbox.refOutput(ctx, orchestrator.Ref, "cat ~/"+guestManifestJSON)
	if err != nil {
		report(false, "orchestrator manifest unreadable: %v", err)
	} else {
		var manifest guestManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			report(false, "orchestrator manifest unparseable: %v", err)
		} else {
			drift := manifestDrift(campaign, agents, manifest)
			report(len(drift) == 0, "manifest fidelity: %s", summarizeDrift(drift, len(agents)))
		}
	}

	// 2. Scoped controls present: the guest binary and the doctrine.
	if _, err := a.sandbox.refOutput(ctx, orchestrator.Ref,
		fmt.Sprintf("test -x ~/%s && test -r ~/%s && echo controls-ok", guestMemberHelper, guestOrientationFile)); err != nil {
		report(false, "orchestrator controls (guest binary, doctrine): %v", err)
	} else {
		report(true, "orchestrator controls installed (guest binary, doctrine)")
	}

	// 3. The guard FIRES: a deliberate wrong-family invocation against a real
	// member must produce the refusal (exit 78), read from the guard's
	// own mouth. Probing -status is side-effect-free — the guard refuses
	// before the real tool is reached.
	if len(agents) > 0 {
		target := agents[0]
		family := wrongFamilyFor(target.CLI)
		probe := fmt.Sprintf(
			`t="$HOME/.local/bin/cs-%s-remote-output"; if [ -L "$t" ]; then o=$("$t" %q 2>&1); echo "probe-exit:$?"; printf '%%s\n' "$o" | head -n1; else echo probe-exit:no-symlink; fi`,
			family, target.Session.Name)
		probeOut, err := a.sandbox.refOutput(ctx, orchestrator.Ref, probe)
		text := string(probeOut)
		switch {
		case err != nil:
			report(false, "guard probe could not run: %v", err)
		case strings.Contains(text, "probe-exit:no-symlink"):
			report(false, "guard not armed: cs-%s-remote-output is not guarded in the orchestrator VM", family)
		case !strings.Contains(text, "probe-exit:78") || !strings.Contains(text, "REFUSED"):
			report(false, "guard did NOT refuse a wrong-family probe (cs-%s-remote-output against %s/%s): %s", family, target.Name, target.CLI, strings.TrimSpace(text))
		default:
			report(true, "guard fires: cs-%s-remote-output against %s (%s) refused with the true diagnosis", family, target.Name, target.CLI)
		}
	}

	// 4. Each member is provisioned for its DECLARED CLI — the binary the
	// declaration promises actually exists in that member's VM.
	for _, member := range agents {
		check := fmt.Sprintf("command -v %q >/dev/null 2>&1 && echo cli-ok || echo cli-missing", member.CLI)
		cliOut, err := a.sandbox.refOutput(ctx, member.Ref, check)
		switch {
		case err != nil:
			report(false, "member %s (%s) unreachable: %v", member.Name, member.CLI, err)
		case strings.Contains(string(cliOut), "cli-ok"):
			report(true, "member %s answers with its declared CLI (%s) present", member.Name, member.CLI)
		default:
			report(false, "member %s: declared CLI %q not present in its VM", member.Name, member.CLI)
		}
	}

	if len(problems) > 0 {
		// The remedy block is printed to out rather than folded into the error:
		// it is a procedure, and it belongs after the ok/BAD list the operator
		// just read, not wrapped inside a one-line failure.
		fmt.Fprint(out, remedy)
		return fmt.Errorf("campaign %s FAILED its doctor (%d problems) — do not dispatch; fix the instantiation or destroy:\n  %s",
			campaign.Name, len(problems), strings.Join(problems, "\n  "))
	}
	return nil
}

// reportMemberHarness checks every member's upstream tool surface against the
// pin and returns the remedy block to print if any member deviates.
//
// A member that cannot be measured FAILS rather than being skipped: an
// unanswerable probe is the same "certified but unverified" state the row
// exists to remove. An unpinned host warns and moves on, matching reportPin —
// with no pin there is nothing to deviate from.
func (a *app) reportMemberHarness(ctx context.Context, campaign *model.Campaign, report func(bool, string, ...any)) string {
	var deviated bool
	var pinVersion string
	if pin, pinned, err := loadPin(); err == nil && pinned {
		pinVersion = pin.SandboxVersion
	}
	for i := range campaign.Members {
		member := &campaign.Members[i]
		check, pinned, err := a.memberHarness(ctx, *member)
		switch {
		case err != nil:
			member.Harness = &model.HarnessCheck{CheckedAt: time.Now().UTC(), Pinned: pinned, Error: err.Error()}
			deviated = true
			report(false, "member %s (%s) harness UNVERIFIABLE: %v — this member may be running any code at all", member.Name, member.CLI, err)
		case !pinned:
			report(true, "member %s harness not compared: no pin on this host (run `cs-campaign pin`)", member.Name)
		case len(check.Deviations) > 0:
			member.Harness = &check
			deviated = true
			report(false, "member %s (%s) runs a harness that is NOT the pinned one — %d of %d tools deviate:\n      %s",
				member.Name, member.CLI, len(check.Deviations), len(memberPinnedToolNames()), strings.Join(check.Deviations, "\n      "))
		default:
			member.Harness = &check
			report(true, "member %s runs the pinned harness (%d tools match %s)", member.Name, len(check.Tools), pinVersion)
		}
	}
	if !deviated {
		return ""
	}
	return harnessRemedy(campaign.Name, pinVersion)
}

func manifestDrift(campaign *model.Campaign, agents []model.Member, manifest guestManifest) []string {
	var drift []string
	if manifest.Campaign != campaign.Name {
		drift = append(drift, fmt.Sprintf("campaign %q != %q", manifest.Campaign, campaign.Name))
	}
	if manifest.Network != campaign.Network {
		drift = append(drift, fmt.Sprintf("network %q != %q", manifest.Network, campaign.Network))
	}
	seen := map[string]bool{}
	for _, member := range agents {
		seen[member.Name] = true
		entry, ok := manifest.Agents[member.Name]
		if !ok {
			drift = append(drift, fmt.Sprintf("member %s missing from manifest", member.Name))
			continue
		}
		if entry.CLI != member.CLI {
			drift = append(drift, fmt.Sprintf("member %s: manifest cli %q != declared %q", member.Name, entry.CLI, member.CLI))
		}
		if entry.Sandbox != member.Sandbox || entry.Session != member.Session.Name {
			drift = append(drift, fmt.Sprintf("member %s: manifest address %s/%s != declared %s/%s", member.Name, entry.Sandbox, entry.Session, member.Sandbox, member.Session.Name))
		}
	}
	for name := range manifest.Agents {
		if !seen[name] {
			drift = append(drift, fmt.Sprintf("manifest names %q, which the campaign never declared", name))
		}
	}
	return drift
}

func summarizeDrift(drift []string, agentCount int) string {
	if len(drift) == 0 {
		// "agents", not "members": the orchestrator's manifest lists the peers it
		// drives and never itself, so a 3-member fleet correctly checks 2. Saying
		// "members" made a correct line read as off by one, on the one output the
		// operator is told to gate dispatch on.
		return fmt.Sprintf("%d agents match the campaign record exactly", agentCount)
	}
	return strings.Join(drift, "; ")
}

// wrongFamilyFor picks a deliberately wrong family for the probe; with three
// families there is always one.
func wrongFamilyFor(cli string) string {
	for _, family := range []string{"claude", "codex", "opencode"} {
		if family != cli {
			return family
		}
	}
	return "claude"
}
