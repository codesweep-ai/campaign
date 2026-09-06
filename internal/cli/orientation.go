package cli

import (
	_ "embed"
	"fmt"
	"io"
	"strings"
	"text/template"

	"github.com/spf13/cobra"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
)

// orientationTemplate is the product-authored standing context every member
// receives. It is a FILE rather than a Go string literal for the same reason
// MANUAL.md is: prose addressed to a model is reviewable as prose, diffable in
// a pull request, and cannot drift from a shipped copy that is the same bytes.
// The previous version was string concatenation, which is why nobody ever read
// it in review.
//
// What belongs here is exactly what is true of EVERY campaign and knowable only
// by the product: identity, channel paths, branches, the roster, the control
// verbs, the report vocabulary. What the member is here to DO comes from the
// operator's brief, seeded alongside. Neither restates the other.
//
//go:embed assets/orientation.md.tmpl
var orientationTemplateText string

var orientationTemplate = template.Must(template.New("orientation").Parse(orientationTemplateText))

type orientationRepo struct{ Name, Branch string }

type orientationPeer struct{ Name, CLI, BriefPath string }

type orientationData struct {
	Campaign       string
	Member         string
	Role           string
	IsOrchestrator bool
	InputDir       string
	OutputDir      string
	Repos          []orientationRepo
	Inputs         []string
	Teammates      []orientationPeer
}

// buildOrientation renders one member's orientation and its manifest from the
// same data, so the prose and the machine-readable copy cannot disagree.
func buildOrientation(campaign *model.Campaign, member model.Member, inputs campaignInputs) (string, protocol.Member, error) {
	data := orientationData{
		Campaign:       campaign.Name,
		Member:         member.Name,
		Role:           member.Role,
		IsOrchestrator: member.Role == "orchestrator",
		InputDir:       guestInputDir,
		OutputDir:      guestOutputDir,
		Inputs:         inputs.seededNames(member),
	}
	// The manifest lists EVERYTHING this member must read, orientation first;
	// the prose above lists only what the operator placed here. They differ on
	// purpose: a document does not sensibly announce itself as one of the files
	// placed there for you, but the readback walks this list and reports what is
	// absent, so leaving the orientation out of it made the product's own
	// doctrine the one file whose delivery nothing checked.
	doc := protocol.Member{
		Campaign: campaign.Name, Member: member.Name, Role: member.Role,
		Network: campaign.Network, Branch: member.Branch,
		Inputs:      append([]string{orientationFileName}, data.Inputs...),
		InputDir:    guestInputDir,
		OutputDir:   guestOutputDir,
		Orientation: guestOrientationFile,
		Policy:      campaign.Policy,
		Deadline:    campaign.Deadline,
	}
	for _, repo := range member.Profile.Repos {
		name := repoGuestName(repo)
		data.Repos = append(data.Repos, orientationRepo{Name: name, Branch: member.Branch})
		doc.Repos = append(doc.Repos, protocol.RepoRef{Name: name, Base: repo.ResolvedCommit})
	}
	if data.IsOrchestrator {
		for _, peer := range campaign.Members {
			if peer.Name == member.Name {
				continue
			}
			p := orientationPeer{Name: peer.Name, CLI: peer.CLI}
			if f, ok := inputs.Roles[peer.Name]; ok {
				p.BriefPath = rolesDirName + "/" + f.Name
			}
			data.Teammates = append(data.Teammates, p)
		}
	}
	var out strings.Builder
	if err := orientationTemplate.Execute(&out, data); err != nil {
		return "", doc, fmt.Errorf("render orientation for %s: %w", member.Name, err)
	}
	return out.String(), doc, nil
}

// orientationCmd prints what each member will be told, before anything exists.
//
// It closes the gap that made role briefs guesswork. The orientation and the
// operator's brief are two halves of one contract, and SPEC.md R35 forbids
// either half from restating the other — a rule the brief's author could not
// keep, because nothing rendered the half they were writing against. So they
// guessed, and a guess that lands in a brief reaches the member as fact,
// contradicting the orientation sitting beside it.
//
// It reads the same buildOrientation the create path renders from, so this is
// the text itself rather than a description of it. One field is a prediction:
// see the branch note in memberBranch. Guidance goes to stderr so a redirect
// captures the orientation alone.
func (a *app) orientationCmd() *cobra.Command {
	opts := new(createOpts)
	var member string
	cmd := &cobra.Command{
		Use:   "orientation <campaign>",
		Short: "Print the standing context every member is given, before writing briefs",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			campaign, profile, err := a.planCampaign(*opts, args[0], true)
			if err != nil {
				return err
			}
			profilePath := opts.profile
			if profilePath != "" {
				profilePath = expandPath(profilePath)
			}
			// Inspection, not creation: a brief that has not been written yet
			// is carried by name, because reading this is what an author does
			// before writing one.
			inputs, absent, err := plannedCampaignInputs(profilePath, profile)
			if err != nil {
				return err
			}
			return a.printOrientations(c.OutOrStdout(), c.ErrOrStderr(), campaign, inputs, absent, member)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.profile, "profile", "", "campaign profile YAML")
	flags.StringVar(&member, "member", "", "print one member's orientation instead of every member's")
	flags.StringVar(&opts.orchestrator, "orchestrator", "", "orchestrator CLI")
	flags.StringSliceVar(&opts.agents, "agent", nil, "agent name=cli (repeatable)")
	flags.StringVar(&opts.agentCLI, "agent-cli", "", "CLI for homogeneous agents")
	flags.IntVar(&opts.count, "agents", 0, "number of homogeneous agents")
	flags.StringVar(&opts.repo, "repo", "", "repository cloned into every member")
	flags.StringArrayVar(&opts.sets, "set", nil, "override a supported profile path (path=value, repeatable)")
	return cmd
}

func (a *app) printOrientations(out, errOut io.Writer, campaign *model.Campaign, inputs campaignInputs, absent []string, only string) error {
	selected := campaign.Members
	if only != "" {
		selected = nil
		for _, m := range campaign.Members {
			if m.Name == only {
				selected = append(selected, m)
			}
		}
		if len(selected) == 0 {
			return fmt.Errorf("no member named %q in campaign %s; declared members are %s",
				only, campaign.Name, strings.Join(memberNames(campaign), ", "))
		}
	}
	branched := false
	for i, m := range selected {
		text, _, err := buildOrientation(campaign, m, inputs)
		if err != nil {
			return err
		}
		if len(m.Profile.Repos) > 0 {
			branched = true
		}
		if len(selected) > 1 {
			if i > 0 {
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "===== %s (%s) — ~/%s =====\n\n", m.Name, m.Role, guestOrientationFile)
		}
		fmt.Fprint(out, text)
	}
	writeOrientationNotes(errOut, absent, branched)
	return nil
}

// writeOrientationNotes states the rule this command exists to serve, and the
// one field above that is a prediction. It goes to stderr so that redirecting
// stdout yields the orientation and nothing else.
func writeOrientationNotes(errOut io.Writer, absent []string, branched bool) {
	fmt.Fprint(errOut, "\n---\nThis is what the member is ALREADY told. Do not restate any of it in a\n"+
		"brief, and do not guess at it: a brief that contradicts this text reaches\n"+
		"the member as a second, conflicting instruction. Write only what is true\n"+
		"of THIS campaign — what the member owns, what it must not touch, and what\n"+
		"its work must prove.\n")
	if branched {
		fmt.Fprint(errOut, "\nThe branch above is predicted. cs-sandbox spells the real one, which create\n"+
			"reads back before it writes this file, so that line alone may differ.\n")
	}
	if len(absent) > 0 {
		fmt.Fprintf(errOut, "\nNot written yet, and listed above as the member will be told to expect them:\n  %s\n",
			strings.Join(absent, "\n  "))
	}
}

func memberNames(campaign *model.Campaign) []string {
	out := make([]string, 0, len(campaign.Members))
	for _, m := range campaign.Members {
		out = append(out, m.Name)
	}
	return out
}
