package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/codesweep-ai/campaign/internal/model"
)

// Campaign inputs are discovered by CONVENTION, beside the profile, rather than
// declared with new profile keys:
//
//	campaign/
//	├── profile.yaml
//	├── mission.md          what this campaign must achieve
//	└── roles/<member>.md   one per declared member, orchestrator included
//
// There is deliberately no catch-all fallback. A shared roles/_all.md would let
// a member silently inherit a generic brief when the operator meant to write a
// specific one — and the readback would then PASS, because the member restates
// the boilerplate faithfully. A check that its own detector cannot see through
// is worse than no check, so a missing brief fails loudly and names itself.
const (
	missionFileName = "mission.md"
	rolesDirName    = "roles"
)

// campaignInputs is what the operator authored, resolved to bytes and digests
// before anything is allocated. Digests are recorded on the campaign so an
// archive can prove WHAT each member was handed, not merely that it was handed
// something — the same reason ProfileDigest exists.
type campaignInputs struct {
	Mission  seededFile            // the campaign's goal; orchestrator by default
	Roles    map[string]seededFile // member name -> that member's brief
	RootDir  string                // directory the profile lives in
	Declared bool                  // false for flag-path creates, which have no profile
}

type seededFile struct {
	Name    string // basename as it lands in the guest
	Path    string // host path it came from
	Content string
	Digest  string // sha256, recorded on the campaign
}

func newSeededFile(path string) (seededFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return seededFile{}, err
	}
	sum := sha256.Sum256(b)
	return seededFile{Name: filepath.Base(path), Path: path, Content: string(b), Digest: hex.EncodeToString(sum[:])}, nil
}

// loadCampaignInputs resolves the mission and every member's brief relative to
// the profile. It reads and hashes but allocates nothing, so validate and plan
// can call it and fail before a VM exists.
//
// profilePath is empty for flag-path creates: those are a harness smoke test
// rather than a briefed campaign, so they resolve to no inputs rather than an
// error. A campaign that carries a mission is one that declared a profile.
func loadCampaignInputs(profilePath string, p model.Profile) (campaignInputs, error) {
	if profilePath == "" {
		return campaignInputs{Roles: map[string]seededFile{}}, nil
	}
	root := filepath.Dir(profilePath)
	in := campaignInputs{Roles: map[string]seededFile{}, RootDir: root, Declared: true}

	var missing []string
	mission := filepath.Join(root, missionFileName)
	if f, err := newSeededFile(mission); err == nil {
		in.Mission = f
	} else {
		missing = append(missing, fmt.Sprintf("  %-28s the campaign's goal", missionFileName))
	}

	for _, name := range append([]string{"orchestrator"}, sortedNames(p.Agents)...) {
		role := "agent " + name
		if name == "orchestrator" {
			role = "the orchestrator"
		}
		path := filepath.Join(root, rolesDirName, name+".md")
		f, err := newSeededFile(path)
		if err != nil {
			missing = append(missing, fmt.Sprintf("  %-28s %s", filepath.Join(rolesDirName, name+".md"), role))
			continue
		}
		if reservedInputNames[f.Name] {
			return in, fmt.Errorf("brief %s uses a reserved name; the product owns that file inside a member", path)
		}
		in.Roles[name] = f
	}

	if len(missing) > 0 {
		return in, fmt.Errorf("campaign inputs are missing beside %s:\n%s\n\n"+
			"Every declared member needs a written purpose, and the campaign needs a mission —\n"+
			"they are seeded into each member at create and are what the fleet is verified against.\n"+
			"Create the files above, or run `cs-campaign init` to scaffold them",
			profilePath, strings.Join(missing, "\n"))
	}
	return in, nil
}

// seedCommand builds the shell that materialises one member's seeded inputs.
// The orchestrator additionally receives EVERY agent's brief under roles/: it
// allocates the work, and it cannot do that without knowing what each teammate
// owns. The alternative is an operator hand-copying a team table into the
// orchestrator's own brief, which drifts the moment one file is edited.
func (in campaignInputs) seedCommand(member model.Member) string {
	if !in.Declared {
		return ""
	}
	var parts []string
	dirs := []string{guestInputDir}
	if member.Role == "orchestrator" {
		dirs = append(dirs, guestRolesDir)
	}
	parts = append(parts, mkGuestDirs(dirs...))

	if own, ok := in.Roles[member.Name]; ok {
		parts = append(parts, putGuestFile(guestInputDir+"/"+own.Name, own.Content))
	}
	if member.Role == "orchestrator" {
		if in.Mission.Content != "" {
			parts = append(parts, putGuestFile(guestInputDir+"/"+in.Mission.Name, in.Mission.Content))
		}
		for _, name := range sortedRoleNames(in.Roles) {
			if name == "orchestrator" {
				continue
			}
			f := in.Roles[name]
			parts = append(parts, putGuestFile(guestRolesDir+"/"+f.Name, f.Content))
		}
	}
	return strings.Join(parts, " && ")
}

// seededNames lists what a member will find in its input channel, in the order
// the orientation should present them. This is the list member.json carries and
// the readback checks against.
func (in campaignInputs) seededNames(member model.Member) []string {
	if !in.Declared {
		return nil
	}
	var names []string
	if own, ok := in.Roles[member.Name]; ok {
		names = append(names, own.Name)
	}
	if member.Role == "orchestrator" {
		if in.Mission.Content != "" {
			names = append(names, in.Mission.Name)
		}
		for _, n := range sortedRoleNames(in.Roles) {
			if n == "orchestrator" {
				continue
			}
			names = append(names, rolesDirName+"/"+in.Roles[n].Name)
		}
	}
	return names
}

// digests returns the recorded provenance for every file seeded into a member.
func (in campaignInputs) digests(member model.Member) map[string]string {
	out := map[string]string{}
	if !in.Declared {
		return out
	}
	if own, ok := in.Roles[member.Name]; ok {
		out[own.Name] = own.Digest
	}
	if member.Role == "orchestrator" {
		if in.Mission.Content != "" {
			out[in.Mission.Name] = in.Mission.Digest
		}
		for _, n := range sortedRoleNames(in.Roles) {
			if n == "orchestrator" {
				continue
			}
			out[rolesDirName+"/"+in.Roles[n].Name] = in.Roles[n].Digest
		}
	}
	return out
}

func sortedRoleNames(m map[string]seededFile) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
