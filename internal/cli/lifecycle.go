package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/spf13/cobra"
)

func (a *app) destroyCmd() *cobra.Command {
	var force, archive bool
	var archiveDir string
	cmd := &cobra.Command{Use: "destroy <campaign>", Short: "Destroy all campaign members, then reclaim the group", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		unlock, err := a.store.Lock(args[0])
		if err != nil {
			return err
		}
		defer func() { _ = unlock() }()
		campaign, err := a.store.Load(args[0])
		if err != nil {
			return err
		}
		if archiveDir != "" && !archive {
			return errors.New("--archive-output requires --archive")
		}
		if archive {
			root := archiveDir
			if root == "" {
				root = filepath.Join("archives", campaign.Name+"-final")
			}
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
		// Only now that no member survives: the group owns host-global
		// artifacts the members do not — network, SSH trust, gateway and its
		// port. Unforced on purpose: a second, independent check that the
		// group really is empty.
		if err = a.reclaimGroup(c.Context(), campaign); err != nil {
			return err
		}
		return a.store.Delete(campaign.Name)
	}}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "force member destruction")
	cmd.Flags().BoolVar(&archive, "archive", false, "archive all member evidence before destruction")
	cmd.Flags().StringVar(&archiveDir, "archive-output", "", "archive destination (requires --archive)")
	return cmd
}

// adoptGatewayPort records the campaign group's SSH jump-host port.
// Best-effort: a campaign is usable without it.
func (a *app) adoptGatewayPort(ctx context.Context, campaign *model.Campaign) {
	if campaign.Group == "" {
		return
	}
	groups, err := a.sandbox.groups(ctx)
	if err != nil {
		return
	}
	for _, g := range groups {
		if g.Name == campaign.Group {
			campaign.Gateway = g.Gateway
			return
		}
	}
	campaign.Gateway = 0 // the group is gone; do not advertise a dead entrance
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
