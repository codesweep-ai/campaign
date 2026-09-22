package cli

// The host's one no-rung instrument for the orchestrator (SAC-054). The
// orchestrator resumes a refused agent from its wait loop, and nothing above
// the orchestrator did the same for it: observe derived "resume next" for
// hours while nobody performed it. resume performs the move the state
// computation derives, for the orchestrator alone, and refuses every other
// move by name. A tending loop may call it whenever observe says so, because
// the pacing, the one-per-look rule and the bound come from the same
// computation: a call made early sends nothing.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
	"github.com/spf13/cobra"
)

func (a *app) resumeCmd() *cobra.Command {
	return &cobra.Command{Use: "resume <campaign>", Short: "Carry the orchestrator's open dispatch on after a provider refusal or a turn that never ran; spends no rung", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		member, err := parseMember(a, args[0])
		if err != nil {
			return err
		}
		if member.Role != "orchestrator" {
			return fmt.Errorf("the host resumes the orchestrator alone; %s's refusal is the orchestrator's ladder, run inside its own wait", member.Name)
		}
		name, _, _ := strings.Cut(args[0], "/")
		campaign, err := a.store.Load(name)
		if err != nil {
			return err
		}
		said, err := a.hostResumeOrchestrator(c.Context(), *member, campaign.Policy)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), said)
		return nil
	}}
}

// hostResumeOrchestrator takes one look at the orchestrator, computes its
// state as observe does, and performs the resume when that is the derived
// move. Every other state is refused with the state, its detail, and the
// instrument that applies, and nothing is sent.
func (a *app) hostResumeOrchestrator(ctx context.Context, member model.Member, pol protocol.Policy) (string, error) {
	facts, failed, blindRun := a.probeWithBurst(ctx, member, pol)
	if failed {
		return "", fmt.Errorf("cannot reach %s; no recovery instrument reaches a machine that cannot be reached at all", member.Name)
	}
	// The host never accepts, so the orchestrator's acceptance set is empty.
	o := protocol.Compute(facts, false, protocol.Blind{Looks: blindRun}, map[string]bool{}, pol, time.Now().Unix())
	where := fmt.Sprintf("%s is %s (%s)", member.Name, o.State, o.Detail)
	switch {
	case o.State == protocol.StateRefused && o.NextMove == "resume":
		if err := a.hostResume(ctx, member, facts, o.Dispatch, true); err != nil {
			return "", err
		}
		return o.Dispatch + " resumed after its provider's refusal, no rung spent", nil
	case o.State == protocol.StateStopped && o.NextMove == "resume":
		if err := a.hostResume(ctx, member, facts, o.Dispatch, false); err != nil {
			return "", err
		}
		return o.Dispatch + " resumed: its last turn never ran, no rung spent", nil
	case o.State == protocol.StateRefused:
		return "", fmt.Errorf("%s; the wait its provider asked for is still running, and nothing was sent", where)
	case o.State == protocol.StateStopped:
		instrument := "cs-campaign send"
		if o.NextMove == "restart" {
			instrument = "cs-campaign restart"
		}
		return "", fmt.Errorf("%s; a %s spends a rung, and that decision is yours: %s", where, o.NextMove, instrument)
	case o.State == protocol.StateStuck:
		return "", fmt.Errorf("%s; a resume reaches nothing here, and nothing was sent", where)
	default:
		return "", fmt.Errorf("%s; there is nothing to resume", where)
	}
}
