package cli

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/codesweep-ai/campaign/internal/model"
)

// packetShell is the review page: a React app in dispatch-viewer/app, built on
// @codesweep-ai/ui into one self-contained file, as the viewer's page is. The
// command writes it with the packet's data in place of packetMarker.
//
//go:embed assets/packet.html
var packetShell string

const (
	packetMarker = "<!--PACKET-DATA-->"
	packetOpen   = `<script type="application/json" id="packet-data">`
	// packetSchema changes when a field the page reads changes meaning, so a
	// page can refuse data it would misdraw, and --since can refuse an
	// earlier packet it cannot compare.
	packetSchema = 1
)

// packetData is everything the page draws. It carries the text itself rather
// than a summary of it, because a reviewer reads the brief, not a description
// of the brief. Everything in it comes from the same resolution create runs:
// the plan, the files the convention finds beside the profile, and the
// orientation buildOrientation renders.
type packetData struct {
	Schema      int             `json:"schema"`
	Tool        string          `json:"tool"`
	GeneratedAt time.Time       `json:"generatedAt"`
	Deadline    string          `json:"deadline,omitempty"`
	Campaign    *model.Campaign `json:"campaign"`
	// OrientationGuest is where every member finds its orientation.
	OrientationGuest string     `json:"orientationGuest"`
	Profile          packetFile `json:"profile"`
	// Files is the mission and every brief, as the author wrote them and
	// keyed by their path beside the profile. A member's seeds point here.
	Files    []packetFile    `json:"files"`
	Members  []packetMember  `json:"members"`
	Findings []packetFinding `json:"findings"`
	// Since is an earlier packet of the same campaign, for the page to show
	// what changed between the two reviews.
	Since *packetData `json:"since,omitempty"`
}

type packetFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Digest  string `json:"digest,omitempty"`
	Absent  bool   `json:"absent,omitempty"`
}

type packetMember struct {
	Name        string       `json:"name"`
	Orientation packetFile   `json:"orientation"`
	Seeds       []packetSeed `json:"seeds"`
	Trees       []packetTree `json:"trees"`
}

// packetSeed is one file in a member's input channel: where it lands under
// the member's home, and which of Files it is.
type packetSeed struct {
	Guest  string `json:"guest"`
	Source string `json:"source"`
}

// packetTree is one tree in a member's home: a repository it clones and
// commits to, or a snapshot it can only read.
type packetTree struct {
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Ref      string   `json:"ref,omitempty"`
	Commit   string   `json:"commit,omitempty"`
	Created  bool     `json:"created,omitempty"`
	Withhold []string `json:"withhold,omitempty"`
}

// packetFinding is something the operator should see before create: a
// refusal create would make that plan does not, or a warning plan prints.
// Text is the tool's own first line, and Detail the explanation after it.
type packetFinding struct {
	Level  string `json:"level"`
	Text   string `json:"text"`
	Detail string `json:"detail,omitempty"`
}

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// buildPacket resolves the packet for a planned campaign. It reads the inputs
// twice on purpose: once as create will, for the warnings and refusals, and
// once as orientation does, so a brief nobody has written yet is still listed
// where the member will look for it.
func buildPacket(campaign *model.Campaign, profile model.Profile, profilePath string, now time.Time) (*packetData, error) {
	raw, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, err
	}
	planned, absent, err := plannedCampaignInputs(profilePath, profile)
	if err != nil {
		return nil, err
	}
	full, _, err := resolveCampaignInputs(profilePath, profile)
	if err != nil {
		return nil, err
	}
	data := &packetData{
		Schema:           packetSchema,
		Tool:             buildVersion(),
		GeneratedAt:      now.UTC(),
		Deadline:         profile.Defaults.Deadline,
		Campaign:         campaign,
		OrientationGuest: guestOrientationFile,
		Profile:          packetFile{Path: filepath.Base(profilePath), Content: string(raw), Digest: campaign.ProfileDigest},
	}

	authored := func(rel string, f seededFile) packetFile {
		if slices.Contains(absent, rel) {
			return packetFile{Path: rel, Absent: true}
		}
		return packetFile{Path: rel, Content: f.Content, Digest: f.Digest}
	}
	data.Files = append(data.Files, authored(missionFileName, planned.Mission))
	for _, m := range campaign.Members {
		if f, ok := planned.Roles[m.Name]; ok {
			data.Files = append(data.Files, authored(rolesDirName+"/"+f.Name, f))
		}
	}

	for _, m := range campaign.Members {
		text, _, err := buildOrientation(campaign, m, planned)
		if err != nil {
			return nil, err
		}
		pm := packetMember{
			Name:        m.Name,
			Orientation: packetFile{Path: orientationFileName, Content: text, Digest: digestOf(text)},
			Seeds:       []packetSeed{},
			Trees:       []packetTree{},
		}
		for _, e := range planned.seedSet(m) {
			pm.Seeds = append(pm.Seeds, packetSeed{Guest: e.Path, Source: e.Source})
		}
		for _, r := range m.Profile.Repos {
			pm.Trees = append(pm.Trees, packetTree{Kind: "repo", Name: repoGuestName(r), Path: r.Path, Ref: r.Ref,
				Commit: r.ResolvedCommit, Created: r.Initialize, Withhold: r.Withhold})
		}
		for _, s := range m.Profile.Snapshots {
			pm.Trees = append(pm.Trees, packetTree{Kind: "snapshot", Name: snapshotGuestName(s), Path: s.Path, Withhold: s.Withhold})
		}
		data.Members = append(data.Members, pm)
	}

	// The refusals create makes after plan, in the order it makes them, then
	// the warnings plan prints. Each is the tool's own text, so the page and
	// the terminal never disagree about what is wrong.
	if _, err := loadCampaignInputs(profilePath, profile); err != nil {
		data.Findings = append(data.Findings, finding("refusal", err.Error()))
	}
	if err := requireKeyEnv(campaign.Members); err != nil {
		data.Findings = append(data.Findings, finding("refusal", err.Error()))
	}
	var warned bytes.Buffer
	warnLargeSeeds(&warned, campaign.Members, full)
	warnInventedOutcomes(&warned, full)
	warnUnsatisfiedKeyEnv(&warned, campaign.Members)
	warnCopiedCredentials(&warned, campaign.Members)
	for line := range strings.Lines(warned.String()) {
		if text, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "warning: "); ok {
			data.Findings = append(data.Findings, packetFinding{Level: "warning", Text: text})
		}
	}
	if data.Findings == nil {
		data.Findings = []packetFinding{}
	}
	return data, nil
}

func finding(level, message string) packetFinding {
	text, detail, _ := strings.Cut(message, "\n")
	return packetFinding{Level: level, Text: text, Detail: strings.TrimSpace(detail)}
}

// renderPacket puts the data into the page. encoding/json escapes <, > and &
// inside strings, so no brief can close the script element it travels in.
func renderPacket(data *packetData) ([]byte, error) {
	if !strings.Contains(packetShell, packetMarker) {
		return nil, fmt.Errorf("the packet page has no %s marker", packetMarker)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return []byte(strings.Replace(packetShell, packetMarker, packetOpen+string(encoded)+"</script>", 1)), nil
}

// readPacket recovers the data from a page renderPacket wrote.
func readPacket(path string) (*packetData, error) {
	page, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	_, rest, ok := strings.Cut(string(page), packetOpen)
	if !ok {
		return nil, fmt.Errorf("%s is not a page written by cs-campaign packet", path)
	}
	encoded, _, ok := strings.Cut(rest, "</script>")
	if !ok {
		return nil, fmt.Errorf("%s is cut short: its packet data never ends", path)
	}
	var data packetData
	if err := json.Unmarshal([]byte(encoded), &data); err != nil {
		return nil, fmt.Errorf("%s holds packet data this build cannot read: %w", path, err)
	}
	if data.Schema != packetSchema {
		return nil, fmt.Errorf("%s is a schema %d packet, and this build compares schema %d", path, data.Schema, packetSchema)
	}
	return &data, nil
}

// packetCmd writes the review packet: one page holding everything to read
// before create, which PLAYBOOK.md's two review passes are run against.
func (a *app) packetCmd() *cobra.Command {
	opts := new(createOpts)
	var file, since string
	cmd := &cobra.Command{
		Use:   "packet <campaign>",
		Short: "Write one HTML page holding everything to review before create",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			// The packet shows the mission and briefs, which live beside a
			// profile. A fleet from flags has none, so there is nothing to show.
			if opts.profile == "" {
				return errors.New("packet needs --profile: the mission and briefs it shows live beside the profile")
			}
			campaign, profile, err := a.planCampaign(*opts, args[0], true)
			if err != nil {
				return err
			}
			data, err := buildPacket(campaign, profile, expandPath(opts.profile), time.Now())
			if err != nil {
				return err
			}
			if since != "" {
				earlier, err := readPacket(since)
				if err != nil {
					return err
				}
				if earlier.Campaign == nil || earlier.Campaign.Name != campaign.Name {
					return fmt.Errorf("%s is a packet of another campaign, so there is nothing to compare", since)
				}
				earlier.Since = nil
				data.Since = earlier
			}
			if a.json {
				return writeJSON(c.OutOrStdout(), data)
			}
			page, err := renderPacket(data)
			if err != nil {
				return err
			}
			if file == "" {
				file = "packet.html"
			}
			if err := os.WriteFile(file, page, 0o644); err != nil {
				return err
			}
			return describePacket(c.OutOrStdout(), file, data)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.profile, "profile", "", "campaign profile YAML")
	flags.StringVar(&opts.credentials, "credentials", "", "the campaign's credential verb: lend or inherit (default: the profile's, else lend)")
	flags.StringArrayVar(&opts.sets, "set", nil, "override a supported profile path (path=value, repeatable)")
	flags.StringVar(&file, "file", "", "page to write (default: packet.html)")
	flags.StringVar(&since, "since", "", "an earlier packet of this campaign, to show what changed")
	return cmd
}

func describePacket(w io.Writer, file string, data *packetData) error {
	var refusals, warnings int
	for _, f := range data.Findings {
		if f.Level == "refusal" {
			refusals++
		} else {
			warnings++
		}
	}
	_, err := fmt.Fprintf(w, "wrote %s: %s, %s, %s\n", file, counted(len(data.Members), "member"),
		counted(refusals, "refusal"), counted(warnings, "warning"))
	return err
}

func counted(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
