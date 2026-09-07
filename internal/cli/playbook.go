package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/codesweep-ai/campaign"
)

// playbookCmd prints the operator's playbook carried inside the binary. The
// manual says what each command does; this says how to decide what to run and
// what to write. It is a separate verb rather than a manual section because the
// two are read at different moments: the manual mid-campaign to look something
// up, the playbook before one, while the team is still being designed.
func (a *app) playbookCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "playbook",
		Short: "Print the operator's playbook: how to design, brief, run and harvest a campaign",
		Args:  cobra.NoArgs,
		Run: func(c *cobra.Command, _ []string) {
			fmt.Fprint(c.OutOrStdout(), campaign.PlaybookMD)
		},
	}
}
