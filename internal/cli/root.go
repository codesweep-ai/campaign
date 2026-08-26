package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/codesweep-ai/campaign/internal/store"
	"github.com/spf13/cobra"
)

// devVersion marks a binary that carried no release stamp.
const devVersion = "dev"

var version = devVersion

// buildVersion reports the release stamp when there is one, and otherwise the
// module version the toolchain recorded. A binary installed straight from the
// module path carries no stamp, so without this it would answer "dev" and
// leave you guessing which revision ran a campaign.
func buildVersion() string {
	if version != devVersion {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return version
	}
	return info.Main.Version
}

type app struct {
	store   store.Store
	sandbox sandboxCLI
	json    bool
	// collectBound is how long one archive collection command may run inside a
	// member. Zero means archiveCollectBound; tests set it small.
	collectBound time.Duration
	// exportBound is how long the opencode session export may run inside a
	// member before the tar after it runs regardless. Zero means
	// archiveExportBound; tests set it small.
	exportBound time.Duration
}

func Execute() error {
	a := &app{store: store.Store{Dir: store.DefaultDir()}, sandbox: newSandbox()}
	return a.root().Execute()
}

func (a *app) root() *cobra.Command {
	root := &cobra.Command{Use: "cs-campaign", Short: "Run a team of AI coding agents in Firecracker microVMs on one mission", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().BoolVar(&a.json, "json", false, "print machine-readable JSON")
	root.AddCommand(
		// Planning and lifecycle.
		a.initCmd(), a.createCmd(false), a.createCmd(true), a.validateCmd(), a.destroyCmd(),
		// The protocol: one send verb, one observation surface, one operator
		// recovery instrument. Node state is computed, never stored.
		a.observeCmd(), a.sendCmd(), a.restartCmd(),
		// Member access.
		a.memberPass("ssh"), a.fetchCmd(), a.transcriptCmd(),
		// Evidence.
		a.archiveCmd(), a.auditCmd(),
		// Host and campaign health.
		a.lsCmd(), a.doctorCmd(), a.pinCmd(),
		a.versionCmd(), a.manualCmd(),
	)
	return root
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{Use: "version", Args: cobra.NoArgs, Run: func(c *cobra.Command, _ []string) {
		fmt.Fprintf(c.OutOrStdout(), "cs-campaign %s (%s/%s, %s)\n", buildVersion(), runtime.GOOS, runtime.GOARCH, runtime.Version())
	}}
}
