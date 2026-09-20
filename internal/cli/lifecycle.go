package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/spf13/cobra"
)

func (a *app) destroyCmd() *cobra.Command {
	var force, archive, dry bool
	var archiveDir string
	cmd := &cobra.Command{Use: "destroy <campaign>", Short: "Destroy all campaign members, then reclaim the group", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		if archiveDir != "" && !archive {
			return errors.New("--archive-output requires --archive")
		}
		if dry {
			// No lock: a preview changes nothing, and it has to answer while a
			// create or another destroy is holding the campaign.
			campaign, err := a.store.Load(args[0])
			if err != nil {
				return err
			}
			return a.previewDestroy(c.Context(), c.OutOrStdout(), campaign, force, archive, archiveDir)
		}
		unlock, err := a.store.Lock(args[0])
		if err != nil {
			return err
		}
		defer func() { _ = unlock() }()
		campaign, err := a.store.Load(args[0])
		if err != nil {
			return err
		}
		if archive {
			root := finalArchiveRoot(campaign, archiveDir)
			if _, err = a.archiveCampaign(c.Context(), campaign, root); err != nil {
				return fmt.Errorf("archive before destroy: %w", err)
			}
			incomplete, scanErr := archiveIncomplete(root)
			if scanErr != nil {
				return fmt.Errorf("verify archive before destroy: %w", scanErr)
			}
			if len(incomplete) > 0 {
				return fmt.Errorf("archive before destroy is incomplete (%s); fix collection and retry", strings.Join(incomplete, ", "))
			}
			fmt.Fprintln(c.OutOrStdout(), root)
		}
		live, err := a.sandbox.list(c.Context())
		if err != nil {
			return err
		}
		present := map[string]bool{}
		for _, sandbox := range live {
			present[sandbox.Ref] = true
		}
		for _, v := range slices.Backward(campaign.Members) {
			if !present[v.Ref] {
				continue
			}
			destroyArgs := []string{"destroy", v.Ref}
			if force {
				destroyArgs = append(destroyArgs, "--force")
			}
			if err = a.sandbox.run(c.Context(), destroyArgs...); err != nil {
				return err
			}
		}
		// A non-forced sandbox destroy refuses by printing advice and exiting 0,
		// so exit status cannot prove removal. Verify by evidence: while any
		// member sandbox is still present, the campaign record must survive.
		live, err = a.sandbox.list(c.Context())
		if err != nil {
			return err
		}
		stillPresent := map[string]bool{}
		for _, sandbox := range live {
			stillPresent[sandbox.Ref] = true
		}
		var remaining []string
		for i := range campaign.Members {
			if stillPresent[campaign.Members[i].Ref] {
				remaining = append(remaining, campaign.Members[i].Name)
			}
		}
		if len(remaining) > 0 {
			return fmt.Errorf("members still present (%s); campaign state preserved — re-run with --force to destroy", strings.Join(remaining, ", "))
		}
		// The members are gone, and with them every agent session. What the host
		// remembers of those sessions has to go too: a campaign made again from
		// the same profile gets the same session names, and would otherwise
		// resume sessions its new machines have never held.
		for i := range campaign.Members {
			a.sandbox.forgetSession(c.Context(), campaign.Members[i])
		}
		// Only now that no member survives: the group owns host-global
		// artifacts the members do not — its network, its SSH trust and its
		// gateway. Unforced on purpose: a second, independent check that the
		// group really is empty.
		if err = a.reclaimGroup(c.Context(), campaign); err != nil {
			return err
		}
		return a.store.Delete(campaign.Name)
	}}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "force member destruction")
	cmd.Flags().BoolVar(&archive, "archive", false, "archive all member evidence before destruction")
	cmd.Flags().StringVar(&archiveDir, "archive-output", "", "archive destination (requires --archive)")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "resolve only; destroy nothing")
	return cmd
}

// finalArchiveRoot is where `destroy --archive` writes: the operator's
// directory, else archives/<campaign>-final.
func finalArchiveRoot(campaign *model.Campaign, archiveDir string) string {
	if archiveDir != "" {
		return archiveDir
	}
	return filepath.Join("archives", campaign.Name+"-final")
}

// previewDestroy is `destroy --dry-run`: the member set destroy would act on,
// read from the same record and the same listing, with nothing removed.
//
// destroy is the one command here that cannot be undone, and a member's
// machine is the only copy of whatever was never fetched or archived. The
// question an operator has at that moment is "which machines, exactly, and
// nothing else?". On a host running several campaigns the only other way to
// answer it is to list the sandboxes and filter by group by hand, which is
// re-deriving what this command already holds.
//
// It names three sets, because destroy treats them differently. A member that
// is present is destroyed. A member with no live machine is skipped. A machine
// in the group that the record does not hold is left alone, and it then stops
// the group from being reclaimed, since that removal is unforced on purpose.
func (a *app) previewDestroy(ctx context.Context, out io.Writer, campaign *model.Campaign, force, archive bool, archiveDir string) error {
	live, err := a.sandbox.list(ctx)
	if err != nil {
		return err
	}
	status := map[string]string{}
	for _, sandbox := range live {
		status[sandbox.Ref] = sandbox.Status
	}
	mine := map[string]bool{}
	var present, absent []model.Member
	for _, member := range slices.Backward(campaign.Members) {
		mine[member.Ref] = true
		if _, ok := status[member.Ref]; ok {
			present = append(present, member)
		} else {
			absent = append(absent, member)
		}
	}
	var stray []string
	others := 0
	for _, sandbox := range live {
		switch {
		case mine[sandbox.Ref]:
		case campaign.Group != "" && sandbox.Group == campaign.Group:
			stray = append(stray, sandbox.Ref)
		default:
			others++
		}
	}
	width, refWidth := 0, 0
	for _, member := range campaign.Members {
		width = max(width, len(member.Name))
		refWidth = max(refWidth, len(member.Ref))
	}

	fmt.Fprintf(out, "destroy %s --dry-run: nothing is removed.\n\n", campaign.Name)
	fmt.Fprintf(out, "group    %s (network %s)\n", campaign.Group, campaign.Network)
	if len(present) == 0 {
		fmt.Fprintln(out, "members  none is present, so there is no machine to destroy")
	} else {
		fmt.Fprintln(out, "members  it would destroy these, last member first:")
		for _, member := range present {
			fmt.Fprintf(out, "  %-*s  %-*s  present, %s\n", width, member.Name, refWidth, member.Ref, status[member.Ref])
		}
	}
	if len(absent) > 0 {
		fmt.Fprintln(out, "absent   in the record with no live machine, so destroy skips these:")
		for _, member := range absent {
			fmt.Fprintf(out, "  %-*s  %s\n", width, member.Name, member.Ref)
		}
	}
	if len(stray) > 0 {
		fmt.Fprintln(out, "stray    in the group and not in the record. destroy leaves these, and the group")
		fmt.Fprintln(out, "         cannot be reclaimed while they stay:")
		for _, ref := range stray {
			fmt.Fprintf(out, "  %s\n", ref)
		}
	}
	if len(stray) > 0 {
		fmt.Fprintln(out, "then     it cannot reclaim the group while a stray machine stays, so the campaign record is kept")
	} else {
		fmt.Fprintln(out, "then     it reclaims the group's network, keys and gateway, and deletes the campaign record")
	}
	if archive {
		fmt.Fprintf(out, "archive  it would archive first, into %s. An incomplete archive stops the destroy.\n", finalArchiveRoot(campaign, archiveDir))
	} else {
		fmt.Fprintln(out, "archive  --archive is not set. Whatever was never fetched or archived goes with the machines.")
	}
	if force {
		fmt.Fprintln(out, "force    --force is set, so each member is destroyed without a question")
	} else {
		fmt.Fprintln(out, "force    --force is not set. cs-sandbox then only reports each member and removes nothing,")
		fmt.Fprintln(out, "         and the campaign record is kept.")
	}
	fmt.Fprintf(out, "keeps    %d other sandbox(es) on this host are outside the group, and destroy does not touch them\n", others)
	if n, dirs := hostSessionRecords(campaign); n > 0 {
		fmt.Fprintf(out, "         %d record(s) of the members' agent sessions on this host, under %s\n", n, strings.Join(dirs, " and "))
	}
	fmt.Fprintln(out, "         archives, and branches already fetched to this host")
	return nil
}

// hostSessionRecords counts what the remote tools keep on the host for this
// campaign's members, and names the directories. destroy leaves them, and a
// recreate of the same profile then meets them (hostSessionFresh).
func hostSessionRecords(campaign *model.Campaign) (int, []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, nil
	}
	n := 0
	var dirs []string
	for _, member := range campaign.Members {
		if member.Session.Name == "" {
			continue
		}
		dir := filepath.Join(home, ".cs-"+member.CLI+"-remote-sessions")
		found, _ := filepath.Glob(filepath.Join(dir, member.Session.Name+"*"))
		if len(found) == 0 {
			continue
		}
		n += len(found)
		if shown := "~/.cs-" + member.CLI + "-remote-sessions"; !slices.Contains(dirs, shown) {
			dirs = append(dirs, shown)
		}
	}
	return n, dirs
}

// reclaimGroup removes the campaign's cs-sandbox group once its members are
// gone. An absent group is success: teardown must be re-runnable.
func (a *app) reclaimGroup(ctx context.Context, campaign *model.Campaign) error {
	if campaign.Group == "" {
		return nil
	}
	if err := a.sandbox.removeGroup(ctx, campaign.Group); err != nil {
		return fmt.Errorf("reclaim campaign group %s: %w", campaign.Group, err)
	}
	return nil
}
