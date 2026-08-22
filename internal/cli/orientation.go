package cli

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"

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
