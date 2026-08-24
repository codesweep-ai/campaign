package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/spf13/cobra"
)

func (a *app) lsCmd() *cobra.Command {
	return &cobra.Command{Use: "ls", Short: "List campaigns", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		campaigns, problems, err := a.store.List()
		if err != nil {
			return err
		}
		for _, problem := range problems {
			fmt.Fprintln(c.ErrOrStderr(), "warning: unreadable campaign state:", problem)
		}
		if a.json {
			return writeJSON(c.OutOrStdout(), campaigns)
		}
		table := tabwriter.NewWriter(c.OutOrStdout(), 0, 4, 2, ' ', 0)
		// GROUP rather than NETWORK: the group is the isolation boundary and
		// the handle every cs-sandbox command takes; its network is derived
		// from it and stays visible in `status`/`inspect` JSON.
		fmt.Fprintln(table, "NAME\tPROVISIONING\tGROUP\tMEMBERS\tGATEWAY\tAGE")
		for _, campaign := range campaigns {
			age := "-"
			if !campaign.CreatedAt.IsZero() {
				age = time.Since(campaign.CreatedAt).Round(time.Second).String()
			}
			gw := "-"
			if campaign.Gateway != 0 {
				gw = strconv.Itoa(campaign.Gateway)
			}
			prov := campaign.Provisioning
			if prov == "" {
				prov = "-"
			}
			fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%s\t%s\n", campaign.Name, prov, campaign.Group, len(campaign.Members), gw, age)
		}
		return table.Flush()
	}}
}

func (a *app) doctorCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "doctor [campaign]", Short: "Check cs-campaign dependencies, or a campaign's instantiation", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		if len(args) == 1 {
			campaign, err := a.store.Load(args[0])
			if err != nil {
				return err
			}
			// The readback is not re-run here: mid-campaign, anything sent to a
			// node with an open dispatch would be read as a continuation of it —
			// a message the orchestrator never sent. The readback happened once,
			// as dispatch d001, at create.
			err = a.campaignDoctor(c.Context(), c.OutOrStdout(), campaign)
			// Persist the refreshed per-member harness verdict either
			// way — the measurement is perishable.
			a.saveHarness(campaign)
			return err
		}
		reported, err := a.sandbox.version(context.Background())
		if err != nil {
			return fmt.Errorf("cs-sandbox version probe: %w", err)
		}
		// The same pattern the pin records with, rather than a second copy of
		// it: doctor and pin must agree on what a version even is, or a
		// surface reads as pinned here and unrecognized there.
		match := sandboxVersionPattern.FindString(reported)
		if match == "" {
			return fmt.Errorf("unrecognized cs-sandbox version output %q (need a group-aware build with ls --json support)", reported)
		}
		// The version cannot gate compatibility: an untagged build reports a
		// bare commit, and development builds never ordered against anything
		// anyway. The authoritative gates are the capability probes below.
		fmt.Fprintln(c.OutOrStdout(), "ok  cs-sandbox version", match)
		live, err := a.sandbox.list(context.Background())
		if err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), "ok  cs-sandbox supports ls --json")
		// Groups are the campaign isolation boundary, so a build without them
		// cannot host a campaign at all. `group ls` is the gate because it is
		// the one probe that answers on an empty host; a listing's ref/group
		// fields only prove anything when some sandbox already exists.
		if _, err = a.sandbox.groups(context.Background()); err != nil {
			return fmt.Errorf("cs-sandbox does not support sandbox groups (need a build with `group ls --json` and (group, name) identity): %w", err)
		}
		for _, sandbox := range live {
			if sandbox.Ref == "" || sandbox.Group == "" {
				return fmt.Errorf("cs-sandbox ls --json omits ref/group for %q; campaign addressing needs a group-aware build", sandbox.Name)
			}
		}
		fmt.Fprintln(c.OutOrStdout(), "ok  cs-sandbox supports sandbox groups")
		// `inspect` is how a member's branch is read rather than guessed, so a
		// build without it cannot run a campaign with repositories. Probed
		// against a real sandbox when the host has one: a synthetic call would
		// only be distinguishable from "unknown command" by matching the
		// wording of an error, which is the coupling this command removed.
		//
		// The probe target must be a sandbox `inspect` can still resolve. `ls`
		// keeps reporting a sandbox after it is gone (status "removed"), and
		// `inspect` correctly refuses that record — so probing the first listing
		// entry blindly turns a torn-down sandbox into "your cs-sandbox build
		// lacks inspect --json" and blocks every create on the host. Probe a
		// resolvable one, and when the listing holds none, skip exactly as on an
		// empty host: absence of a probe target is not evidence of absent support.
		if target, ok := inspectProbeTarget(live); ok {
			inst, err := a.sandbox.inspect(context.Background(), target.Ref)
			if err != nil {
				return fmt.Errorf("cs-sandbox inspect --json unavailable (campaign members read their branch from it): %w", err)
			}
			if inst.Ref == "" {
				return fmt.Errorf("cs-sandbox inspect --json returned no ref for %q; need a build that reports the resolved record", target.Ref)
			}
			fmt.Fprintln(c.OutOrStdout(), "ok  cs-sandbox supports inspect --json")
		}
		for _, cli := range []string{"claude", "codex", "opencode"} {
			for _, suffix := range []string{"-remote", "-remote-output", "-turn"} {
				tool := "cs-" + cli + suffix
				if _, err = exec.LookPath(tool); err != nil {
					return fmt.Errorf("required agent tool %s not found on PATH (run cs-sandbox install-agent-tools)", tool)
				}
			}
			fmt.Fprintf(c.OutOrStdout(), "ok  %s remote tool family\n", cli)
		}
		// Presence above, identity here — a passing doctor must mean
		// "the surface we validated", not "a surface that answers".
		if err = a.reportPin(context.Background(), func(line string) { fmt.Fprintln(c.OutOrStdout(), line) }); err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), "ok  state directory:", a.store.Dir)
		return nil
	}}
	return cmd
}

// inspectProbeTarget picks a sandbox the `inspect --json` capability probe can
// actually resolve. `cs-sandbox ls` continues to report a sandbox after teardown
// with status "removed", and inspect answers "no such sandbox" for those, so the
// probe must skip them rather than read the refusal as a missing capability.
// Statuses are matched case-insensitively and unknown ones are treated as
// probeable: the gate is meant to catch a build without `inspect --json`, not to
// enumerate every state a future cs-sandbox might report.
func inspectProbeTarget(sandboxes []model.Sandbox) (model.Sandbox, bool) {
	for _, sandbox := range sandboxes {
		if sandbox.Ref == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(sandbox.Status)) {
		case "removed", "removing", "deleted", "gone":
			continue
		}
		return sandbox, true
	}
	return model.Sandbox{}, false
}
