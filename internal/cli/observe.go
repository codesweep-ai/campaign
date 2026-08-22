package cli

// observe is the host's whole observation surface: every node's state,
// computed now from each node's own machine, printed beside what the
// orchestrator recorded — two panes, never one column. One is derived fact,
// the other is the orchestrator's claim, and "orchestrator says qa is
// working" beside "qa is unreachable" is the highest-value line an operator
// can see.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
	"github.com/spf13/cobra"
)

type nodeView struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	State    string `json:"state"`
	Dispatch string `json:"dispatch,omitempty"`
	Detail   string `json:"detail"`
}

type observation struct {
	Derived []nodeView       `json:"derived"`
	Claimed []protocol.Entry `json:"claimed"`
	Mission *protocol.Reply  `json:"mission,omitempty"`
	// MissionErr is set when the orchestrator is node-replied on the mission
	// but its reply could not be read back. Reported rather than swallowed:
	// an unreadable verdict and an unfinished campaign look identical from
	// the outside, and only one of them is the operator's to wait out.
	MissionErr string `json:"missionError,omitempty"`
	Gateway    int    `json:"gateway,omitempty"`
}

func (a *app) observeCmd() *cobra.Command {
	return &cobra.Command{Use: "observe <campaign>", Short: "Every node's state, computed now, beside the orchestrator's log", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		campaign, err := a.store.Load(args[0])
		if err != nil {
			return err
		}
		obs, err := a.observeCampaign(c.Context(), campaign)
		if err != nil {
			return err
		}
		if a.json {
			return writeJSON(c.OutOrStdout(), obs)
		}
		printObservation(c.OutOrStdout(), obs)
		return nil
	}}
}

// observeCampaign computes the two panes. The orchestrator's log is read
// first because agents' acceptance state comes from it — and mirrored beside
// the campaign record so campaign evidence survives a lost orchestrator
// machine (a backup of a claim, not derived state).
func (a *app) observeCampaign(ctx context.Context, campaign *model.Campaign) (observation, error) {
	obs := observation{Gateway: campaign.Gateway}
	var orchestrator *model.Member
	for i := range campaign.Members {
		if campaign.Members[i].Role == "orchestrator" {
			orchestrator = &campaign.Members[i]
		}
	}
	if orchestrator == nil {
		return obs, errors.New("campaign has no orchestrator")
	}
	logBytes, logErr := a.sandbox.memberOutput(ctx, *orchestrator, "cat ~/"+guestLogFile+" 2>/dev/null || true")
	entries := protocol.ParseLog(logBytes)
	obs.Claimed = entries
	if logErr == nil && len(logBytes) > 0 {
		a.mirrorLog(campaign.Name, logBytes)
	}

	now := time.Now().Unix()
	pol := campaign.Policy
	for _, member := range campaign.Members {
		facts, failed, blindRun := a.probeWithBurst(ctx, member, pol)
		// Acceptance is node-qualified — dispatch IDs are per-node sequences.
		// The orchestrator's own set stays empty: the host never accepts, and
		// the mission ends at node-replied.
		acc := map[string]bool{}
		if member.Role != "orchestrator" {
			acc = protocol.AcceptedFor(entries, member.Name)
		}
		o := protocol.Compute(facts, failed, blindRun, acc, pol, now)
		obs.Derived = append(obs.Derived, nodeView{
			Name: member.Name, Role: member.Role,
			State: string(o.State), Dispatch: o.Dispatch, Detail: o.Detail,
		})
		if member.Role == "orchestrator" && o.State == protocol.StateReplied && o.Dispatch == protocol.MissionID {
			r, err := a.readReply(ctx, member, protocol.MissionID)
			if err != nil {
				obs.MissionErr = err.Error()
			} else {
				obs.Mission = &r
			}
		}
	}
	sort.Slice(obs.Derived, func(i, j int) bool {
		if obs.Derived[i].Role != obs.Derived[j].Role {
			return obs.Derived[i].Role == "orchestrator"
		}
		return obs.Derived[i].Name < obs.Derived[j].Name
	})
	return obs, nil
}

// observeBurstDelay spaces the re-probes of a failing node. A variable so
// tests can run the burst without the wait.
var observeBurstDelay = time.Second

// probeWithBurst is the host's one look at a node — plus, when that look
// fails, a scoped burst of re-probes of THAT node only, up to BlindProbes
// within this observe invocation. Without it observe's single look could
// never accumulate the consecutive-failure run that concludes "machine gone"
// (§2.3), and a truly gone machine rendered forever as the overlay. Nothing
// is stored — the whole run happens inside one look, which is what keeps the
// "node state is computed, never stored" rule intact (persisting a blind
// count between calls would feed a computation from stored state). Healthy
// nodes pay nothing; a gone machine costs ~BlindProbes seconds, acceptable
// for an operator about to act on "gone". Note the burst's probes are spaced
// seconds apart, not a poll interval — a conclusion reached in ~10s, for a
// human who can simply look again.
func (a *app) probeWithBurst(ctx context.Context, member model.Member, pol protocol.Policy) (protocol.Facts, bool, int) {
	facts, failed := a.sandbox.probeMember(ctx, member)
	blindRun := 1
	for failed && blindRun < pol.Resolve().BlindProbes {
		select {
		case <-ctx.Done():
			return facts, failed, blindRun
		case <-time.After(observeBurstDelay):
		}
		if f, stillFailed := a.sandbox.probeMember(ctx, member); !stillFailed {
			return f, false, 1
		}
		blindRun++
	}
	return facts, failed, blindRun
}

// mirrorLog keeps a host-side copy of the orchestrator's log beside the
// campaign record. Best-effort: observation must never fail on its backup.
func (a *app) mirrorLog(campaign string, b []byte) {
	_ = os.MkdirAll(a.store.Dir, 0o700)
	_ = os.WriteFile(filepath.Join(a.store.Dir, campaign+".log-mirror.jsonl"), b, 0o600)
}

func printObservation(w io.Writer, obs observation) {
	fmt.Fprintf(w, "DERIVED — computed now, from each node's own machine\n")
	fmt.Fprintf(w, "  %-14s %-14s %-16s %-8s %s\n", "node", "role", "state", "dispatch", "detail")
	for _, n := range obs.Derived {
		fmt.Fprintf(w, "  %-14s %-14s %-16s %-8s %s\n", n.Name, n.Role, n.State, n.Dispatch, n.Detail)
	}
	fmt.Fprintf(w, "\nCLAIMED — the orchestrator's own record (a claim, shown beside the facts, never merged)\n")
	if len(obs.Claimed) == 0 {
		fmt.Fprintf(w, "  (the log is empty)\n")
	}
	for _, e := range obs.Claimed {
		fmt.Fprintf(w, "  %s  %-11s %s\n", e.At.Format("15:04:05"), e.Kind, oneLine(e.Text))
	}
	if obs.Mission != nil {
		fmt.Fprintf(w, "\nMISSION REPLY — the campaign's verdict\n")
		fmt.Fprintf(w, "  outcome: %s\n", obs.Mission.Outcome)
		for _, u := range obs.Mission.Unmet {
			fmt.Fprintf(w, "  unmet:   %s\n", u)
		}
		fmt.Fprintf(w, "  note:    %s\n", oneLine(obs.Mission.Note))
	}
	if obs.MissionErr != "" {
		fmt.Fprintf(w, "\nMISSION REPLY — present, but unreadable\n  %s\n", obs.MissionErr)
	}
	if obs.Gateway != 0 {
		fmt.Fprintf(w, "\ngateway: port %d — one entrance for this campaign's services\n", obs.Gateway)
	}
}
