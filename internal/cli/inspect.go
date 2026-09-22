package cli

import (
	"context"
	"fmt"
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
		fmt.Fprintln(table, "NAME\tPROVISIONING\tGROUP\tMEMBERS\tAGE")
		for _, campaign := range campaigns {
			age := "-"
			if !campaign.CreatedAt.IsZero() {
				age = time.Since(campaign.CreatedAt).Round(time.Second).String()
			}
			prov := campaign.Provisioning
			if prov == "" {
				prov = "-"
			}
			fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%s\n", campaign.Name, prov, campaign.Group, len(campaign.Members), age)
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
		ctx := context.Background()
		p := newDoctorPrinter(c.OutOrStdout(), "cs-campaign doctor")

		// Every check is measured and reported, and only the summary decides the
		// exit status. Stopping at the first failure told an operator to install
		// one thing, and told them about the next only after they had.
		//
		// The groups are the ones `cs-sandbox doctor` prints for the same things,
		// in the same words, so the two reports read as one.
		upstream := a.verifyUpstream(ctx)
		p.section("cs-sandbox (required)")
		// One line for the binary, and it is the pin: a bare version line read
		// "ok" about the same cs-sandbox a later group failed as the wrong build.
		// The capability probes below still gate on what it can do.
		reportSandboxPin(p, upstream)
		var live []model.Sandbox
		if listed, err := a.sandbox.list(ctx); err != nil {
			p.bad("cs-sandbox ls --json unavailable: %v", err)
		} else {
			live = listed
			p.ok("cs-sandbox supports ls --json")
		}
		// Groups are the campaign isolation boundary, so a build without them
		// cannot host a campaign at all. `group ls` is the gate because it is
		// the one probe that answers on an empty host; a listing's ref/group
		// fields only prove anything when some sandbox already exists.
		if _, err := a.sandbox.groups(ctx); err != nil {
			p.bad("cs-sandbox does not support sandbox groups (need a build with `group ls --json` and (group, name) identity): %v", err)
		} else {
			malformed := ""
			for _, sandbox := range live {
				if sandbox.Ref == "" || sandbox.Group == "" {
					malformed = sandbox.Name
					break
				}
			}
			if malformed != "" {
				p.bad("cs-sandbox ls --json omits ref/group for %q; campaign addressing needs a group-aware build", malformed)
			} else {
				p.ok("cs-sandbox supports sandbox groups")
			}
		}
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
			switch inst, err := a.sandbox.inspect(ctx, target.Ref); {
			case err != nil:
				p.bad("cs-sandbox inspect --json unavailable (campaign members read their branch from it): %v", err)
			case inst.Ref == "":
				p.bad("cs-sandbox inspect --json returned no ref for %q; need a build that reports the resolved record", target.Ref)
			default:
				p.ok("cs-sandbox supports inspect --json")
			}
		}

		// Identity, not presence — a passing doctor must mean "the surface this
		// build names", not "a surface that answers".
		p.section("agent tools (required — cs-campaign drives its agents with them)")
		a.reportAgentTools(ctx, p, upstream.SandboxVersion)

		p.section("agent CLIs (optional — only to sign in on this host)")
		reportAgentCLIs(p)

		p.section("developer tools (optional — checked against this build's go.mod)")
		reportDeveloperTools(p, upstream)

		p.section("state")
		p.ok("state directory: %s", a.store.Dir)
		p.summary("try: cs-campaign init <name>")
		if p.issues > 0 {
			return errChecksFailed
		}
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
