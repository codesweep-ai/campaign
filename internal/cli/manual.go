package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/codesweep-ai/campaign"
)

// manualCmd prints the user-facing manual carried inside the binary. `--help` is
// the authority on flags and always current; this is the prose around them, and it
// travels with the executable so a host with only the binary is not left guessing.
func (a *app) manualCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "manual",
		Short: "Print the cs-campaign manual",
		Args:  cobra.NoArgs,
		Run: func(c *cobra.Command, _ []string) {
			fmt.Fprint(c.OutOrStdout(), campaign.ManualMD)
		},
	}
}
