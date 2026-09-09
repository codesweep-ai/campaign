package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
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
	// Planned marks a set resolved for INSPECTION rather than for seeding. A
	// file that does not exist yet is then carried by name alone, because the
	// name is fixed by the convention above and a member's input list is
	// therefore knowable before anybody has written a brief. Only `orientation`
	// sets it, and seedCommand refuses to seed from it.
	Planned bool
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

// missingInput is one file the convention requires that is not on disk. Desc
// says what the file is for, so the error can name a role rather than a path.
type missingInput struct {
	Rel    string // path relative to the profile, as the operator would type it
	Desc   string // what it holds, for the error listing
	Member string // the member it briefs; empty for the mission
}

// resolveCampaignInputs reads the mission and every member's brief relative to
// the profile, reporting what is absent rather than deciding what that means.
// Its two callers differ on exactly that: creation refuses an unbriefed fleet,
// and inspection does not.
func resolveCampaignInputs(profilePath string, p model.Profile) (campaignInputs, []missingInput, error) {
	if profilePath == "" {
		return campaignInputs{Roles: map[string]seededFile{}}, nil, nil
	}
	root := filepath.Dir(profilePath)
	in := campaignInputs{Roles: map[string]seededFile{}, RootDir: root, Declared: true}

	var missing []missingInput
	if f, err := newSeededFile(filepath.Join(root, missionFileName)); err == nil {
		in.Mission = f
	} else {
		missing = append(missing, missingInput{Rel: missionFileName, Desc: "the campaign's goal"})
	}

	for _, name := range append([]string{"orchestrator"}, sortedNames(p.Agents)...) {
		role := "agent " + name
		if name == "orchestrator" {
			role = "the orchestrator"
		}
		rel := filepath.Join(rolesDirName, name+".md")
		f, err := newSeededFile(filepath.Join(root, rel))
		if err != nil {
			missing = append(missing, missingInput{Rel: rel, Desc: role, Member: name})
			continue
		}
		if reservedInputNames[f.Name] {
			return in, missing, fmt.Errorf("brief %s uses a reserved name; the product owns that file inside a member", filepath.Join(root, rel))
		}
		in.Roles[name] = f
	}
	return in, missing, nil
}

// loadCampaignInputs resolves the mission and every member's brief relative to
// the profile. It reads and hashes but allocates nothing, so validate and plan
// can call it and fail before a VM exists.
//
// profilePath is empty for flag-path creates: those are a harness smoke test
// rather than a briefed campaign, so they resolve to no inputs rather than an
// error. A campaign that carries a mission is one that declared a profile.
func loadCampaignInputs(profilePath string, p model.Profile) (campaignInputs, error) {
	in, missing, err := resolveCampaignInputs(profilePath, p)
	if err != nil {
		return in, err
	}
	if len(missing) > 0 {
		lines := make([]string, 0, len(missing))
		for _, m := range missing {
			lines = append(lines, fmt.Sprintf("  %-28s %s", m.Rel, m.Desc))
		}
		return in, fmt.Errorf("campaign inputs are missing beside %s:\n%s\n\n"+
			"Every declared member needs a written purpose, and the campaign needs a mission —\n"+
			"they are seeded into each member at create and are what the fleet is verified against.\n"+
			"Write the files above. Nothing scaffolds them, because a blank one would\n"+
			"pass this check and brief a member with nothing. `cs-campaign playbook` says\n"+
			"what goes in each, and `cs-campaign orientation` shows what a member is\n"+
			"already told",
			profilePath, strings.Join(lines, "\n"))
	}
	return in, nil
}

// plannedCampaignInputs resolves the same files for INSPECTION, carrying a file
// that does not exist yet by its name alone. It returns the relative paths that
// are still absent, so a caller can say which parts of the answer describe a
// file nobody has written.
//
// This is what lets `orientation` answer for a member whose brief is still to
// be authored — the ordinary case, since reading the orientation is what an
// author does BEFORE writing one, and `init` refuses to scaffold a stub for a
// member added to a profile later.
func plannedCampaignInputs(profilePath string, p model.Profile) (campaignInputs, []string, error) {
	in, missing, err := resolveCampaignInputs(profilePath, p)
	if err != nil {
		return in, nil, err
	}
	in.Planned = true
	absent := make([]string, 0, len(missing))
	for _, m := range missing {
		if m.Member == "" {
			in.Mission = seededFile{Name: missionFileName}
		} else {
			in.Roles[m.Member] = seededFile{Name: m.Member + ".md"}
		}
		absent = append(absent, m.Rel)
	}
	return in, absent, nil
}

// seedCommand builds the shell that materialises one member's seeded inputs.
// The orchestrator additionally receives EVERY agent's brief under roles/: it
// allocates the work, and it cannot do that without knowing what each teammate
// owns. The alternative is an operator hand-copying a team table into the
// orchestrator's own brief, which drifts the moment one file is edited.
func (in campaignInputs) seedFiles(member model.Member) []guestFile {
	if !in.Declared || in.Planned {
		return nil
	}
	var files []guestFile
	if own, ok := in.Roles[member.Name]; ok {
		files = append(files, guestFile{guestInputDir + "/" + own.Name, own.Content})
	}
	if member.Role == "orchestrator" {
		if in.Mission.Content != "" {
			files = append(files, guestFile{guestInputDir + "/" + in.Mission.Name, in.Mission.Content})
		}
		for _, name := range sortedRoleNames(in.Roles) {
			if name == "orchestrator" {
				continue
			}
			f := in.Roles[name]
			files = append(files, guestFile{guestRolesDir + "/" + f.Name, f.Content})
		}
	}
	return files
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
		// A planned set carries a file that is not on disk by name alone, so
		// presence is the test there; seeding still requires the bytes.
		if in.Mission.Name != "" && (in.Planned || in.Mission.Content != "") {
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

// seedWarnBytes is when a member's seeded set is worth a second look. It is not
// a delivery limit: content rides on stdin and has no ceiling. It is about the
// reader. The playbook puts a member's total seeded reading in the thousands of
// words rather than the tens of thousands, and 64 KiB is about ten thousand.
const seedWarnBytes = 64 << 10

// seededBytes is how much content a member is given, and in how many files.
func (in campaignInputs) seededBytes(member model.Member) (files, bytes int) {
	for _, f := range in.seedFiles(member) {
		files++
		bytes += len(f.Content)
	}
	return files, bytes
}

// warnLargeSeeds names any member handed an unusually large set, before
// anything is provisioned. The orchestrator meets it first, because it is the
// one member given the mission and every role's brief.
//
// A warning rather than a refusal: how much a member should read is the
// operator's call, and the number that used to make this fatal is gone.
func warnLargeSeeds(w io.Writer, members []model.Member, inputs campaignInputs) {
	for _, m := range members {
		if files, bytes := inputs.seededBytes(m); bytes > seedWarnBytes {
			fmt.Fprintf(w, "warning: %s is seeded %d files, %d KiB — large enough that a reader will skim it; see cs-campaign playbook\n",
				m.Name, files, bytes>>10)
		}
	}
}

// profileMembers is the fleet as the profile declares it, for the checks that
// run before a campaign record exists. Only the name and role are set, which is
// all a seeded set depends on.
func profileMembers(p model.Profile) []model.Member {
	members := []model.Member{{Name: "orchestrator", Role: "orchestrator"}}
	for _, name := range sortedNames(p.Agents) {
		members = append(members, model.Member{Name: name, Role: "agent"})
	}
	return members
}

// outcomeToken matches an outcome value where an author is naming it as a value
// rather than using the word in prose: inside backticks, or after --outcome.
// Bare text is left alone deliberately, because "campaign-free" and
// "campaign-specific" are ordinary English and this document set uses both.
var outcomeToken = regexp.MustCompile("`(campaign-[a-z]+)`|--outcome[= ]+(campaign-[a-z]+)")

// warnInventedOutcomes names a seeded document that declares an outcome the
// product does not have.
//
// The four values are a closed set the product owns, so this checks a
// vocabulary rather than grading prose, which R50 forbids. A mission that
// declares its own vocabulary is seeded at create and worked against for the
// whole campaign, and the refusal arrives when the orchestrator finally tries
// to report with a word the reply verb rejects.
//
// A warning rather than a refusal: a document may name an outcome for a reason
// this cannot see, and the operator is the one who knows.
func warnInventedOutcomes(w io.Writer, in campaignInputs) {
	if !in.Declared || in.Planned {
		return
	}
	files := map[string]string{}
	if in.Mission.Content != "" {
		files[in.Mission.Name] = in.Mission.Content
	}
	for _, name := range sortedRoleNames(in.Roles) {
		files[in.Roles[name].Name] = in.Roles[name].Content
	}
	for _, name := range sortedStrings(keysOf(files)) {
		seen := map[string]bool{}
		for _, m := range outcomeToken.FindAllStringSubmatch(files[name], -1) {
			token := m[1]
			if token == "" {
				token = m[2]
			}
			if token == "" || seen[token] || slices.Contains(protocol.Outcomes, token) {
				continue
			}
			seen[token] = true
			fmt.Fprintf(w, "warning: %s names the outcome %q, which does not exist — the four are %s\n",
				name, token, strings.Join(protocol.Outcomes, ", "))
		}
	}
}
