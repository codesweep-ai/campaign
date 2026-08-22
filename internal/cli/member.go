package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/spf13/cobra"
)

// parseMember resolves "<campaign>[/member]" to a pointer at the named member,
// or at the orchestrator when no member is given.
func parseMember(a *app, address string) (*model.Member, error) {
	parts := strings.SplitN(address, "/", 2)
	campaign, err := a.store.Load(parts[0])
	if err != nil {
		return nil, err
	}
	memberName := "orchestrator"
	if len(parts) == 2 {
		memberName = parts[1]
	}
	for i := range campaign.Members {
		if campaign.Members[i].Name == memberName {
			return &campaign.Members[i], nil
		}
	}
	return nil, fmt.Errorf("member %q not found", memberName)
}

// fetchCmd harvests a member's committed work, then reports the one thing a
// ref line cannot: whether the tree actually differs from base. An empty
// branch presented as delivered work buys a wrong acceptance.
func (a *app) fetchCmd() *cobra.Command {
	return &cobra.Command{Use: "fetch <campaign[/member]>", Short: "Fetch a campaign member's branch; reports tree-vs-base, never commit count", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		member, err := parseMember(a, args[0])
		if err != nil {
			return err
		}
		if err = a.sandbox.run(c.Context(), "fetch", member.Ref); err != nil {
			// Diagnose by asking git what is on the branch, not by matching the
			// wording of the failure.
			if occupied := priorRunOnBranch(c.Context(), *member); occupied != "" {
				return fmt.Errorf("%w\n\n%s", err, occupied)
			}
			return err
		}
		for _, repo := range member.Profile.Repos {
			if repo.ResolvedCommit == "" || member.Branch == "" {
				continue
			}
			path := expandPath(repo.Path)
			baseTree, err1 := gitOutput(c.Context(), path, "rev-parse", repo.ResolvedCommit+"^{tree}")
			headTree, err2 := gitOutput(c.Context(), path, "rev-parse", member.Branch+"^{tree}")
			if err1 != nil || err2 != nil {
				continue
			}
			name := repoGuestName(repo)
			if strings.TrimSpace(baseTree) == strings.TrimSpace(headTree) {
				fmt.Fprintf(c.OutOrStdout(), "%s %s — WARNING: tree identical to base; whatever this branch claims, it delivers no change\n", name, member.Branch)
			} else {
				fmt.Fprintf(c.OutOrStdout(), "%s %s — tree differs from base (real changes present)\n", name, member.Branch)
			}
		}
		return nil
	}}
}

// priorRunOnBranch returns an explanation when the member's branch in a host
// source repository already holds a commit — a re-run with identical inputs
// lands on the same branch, and fetch is fast-forward-only.
func priorRunOnBranch(ctx context.Context, member model.Member) string {
	if member.Branch == "" {
		return ""
	}
	var notes []string
	for _, repo := range member.Profile.Repos {
		path := expandPath(repo.Path)
		out, err := gitOutput(ctx, path, "log", "-1", "--format=%h %ad %s", "--date=short", member.Branch)
		if err != nil || strings.TrimSpace(out) == "" {
			continue // no such branch here: not this problem
		}
		notes = append(notes, fmt.Sprintf(
			"%s already has %s at %s.\n"+
				"  A campaign's ID is derived from its name and profile, so re-running one with the\n"+
				"  same inputs lands on the same branch. This run's work is a sibling of that commit,\n"+
				"  not a descendant, and fetch is fast-forward-only — so nothing was overwritten.\n"+
				"  Keep the earlier run:    git -C %s branch -m %s %s-prev\n"+
				"  Or discard it:           git -C %s branch -D %s\n"+
				"  then fetch again.",
			path, member.Branch, strings.TrimSpace(out), path, member.Branch, member.Branch, path, member.Branch))
	}
	return strings.Join(notes, "\n\n")
}

// gitOutput runs a read-only git query against a host repository.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return string(out), err
}

// memberPass forwards a verb to cs-sandbox against one member, passing any
// trailing arguments through — the supported form of a non-interactive look
// inside a member.
func (a *app) memberPass(verb string) *cobra.Command {
	return &cobra.Command{Use: verb + " <campaign[/member]> [args...]", Short: titleWord(verb) + " a campaign member", Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		member, err := parseMember(a, args[0])
		if err != nil {
			return err
		}
		return a.sandbox.run(c.Context(), append([]string{verb, member.Ref}, args[1:]...)...)
	}}
}
