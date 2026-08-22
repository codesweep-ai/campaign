package cli

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/codesweep-ai/campaign/internal/model"
)

// The guest ABI: every path cs-campaign creates, writes or probes inside a
// member VM. These were previously string literals repeated across sandbox.go,
// campaigndoctor.go, archive.go, memberpin.go and both embedded shell assets,
// which made the member environment a contract with no definition — changing
// the input channel meant finding it in five files and two languages.
//
// Paths are guest-relative to $HOME unless the name says otherwise, because
// that is the form the remote command strings need ("~/"+X or "$HOME/"+X).
const (
	// guestConfigDir holds identity, not work: the member manifest cs-campaign
	// writes at create and the orchestrator's roster. Mode 0600 throughout.
	guestConfigDir    = ".config/cs-campaign"
	guestMemberJSON   = guestConfigDir + "/member.json"
	guestManifestJSON = guestConfigDir + "/manifest.json"

	// guestChannelsDir is the root of the four-channel model. archive tars
	// input and output from here, so anything added under it is collected.
	guestChannelsDir = ".local/share/cs-campaign"
	guestInputDir    = guestChannelsDir + "/input"
	guestOutputDir   = guestChannelsDir + "/output"
	guestRepliesDir  = guestOutputDir + "/replies"
	guestLogFile     = guestOutputDir + "/log.jsonl"
	guestSourceDir   = guestChannelsDir + "/source"

	// guestRealToolsDir is where the guard moves the genuine cs-*-remote
	// binaries aside so the guard can occupy their names on PATH. memberpin
	// hashes what would actually execute, which means following that move.
	guestRealToolsDir = guestChannelsDir + "/real"

	guestBinDir       = ".local/bin"
	guestMemberHelper = guestBinDir + "/cs-campaign-member"

	// guestOrientationFile is the product-authored standing context every member
	// receives: who it is, what it can see, what it owes the campaign. It lives
	// in the input channel because that channel is defined as carrying prompts,
	// task files and doctrine — and because being archived with the dispatches
	// is what makes a run auditable after the fact.
	orientationFileName  = "ORIENTATION.md"
	guestOrientationFile = guestInputDir + "/" + orientationFileName

	// guestRolesDir is where the orchestrator receives its agents' briefs, kept
	// in a subdirectory so an agent named "mission" cannot collide with the
	// mission file itself.
	guestRolesDir = guestInputDir + "/roles"

	// guestWorkDir is the working directory every turn runs in. It comes from
	// the sandbox layer rather than from cs-campaign, and it is NOT the member's
	// clone — the clone is at $HOME/<repo-name>. A relative path in a prompt
	// therefore resolves somewhere the operator did not intend.
	guestWorkDir = "/workspace"
)

// reservedInputNames are basenames the product owns inside a member's input
// channel. An operator-seeded file may not take one: silently overwriting the
// orientation would remove a member's only account of its own obligations, and
// the failure would look like a model that ignored its instructions.
var reservedInputNames = map[string]bool{
	"ORIENTATION.md": true,
	"WORKFLOW.md":    true, // the orientation's former name; still refused
	"member.json":    true,
	"manifest.json":  true,
}

// putGuestFile builds the shell fragment that materialises one file inside a
// member from base64. Base64 is not decoration: the payload is composed on the
// host and must arrive as a literal, so it never crosses as shell-visible text
// where quoting or a stray backtick could break it — or execute.
//
// Paths are $HOME-relative, matching the constants above.
func putGuestFile(path, content string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	return fmt.Sprintf("printf %%s %s | base64 -d > ~/%s && chmod 600 ~/%s", encoded, path, path)
}

// mkGuestDirs builds one mkdir -p covering every directory named.
func mkGuestDirs(paths ...string) string {
	quoted := make([]string, 0, len(paths))
	for _, p := range paths {
		quoted = append(quoted, "~/"+p)
	}
	return "mkdir -p " + strings.Join(quoted, " ")
}

// repoGuestName is the directory a repository lands in inside a member, at
// $HOME/<name>. Derived from the declared name, else the last segment of the
// host path. Extracted because the manifest, the orientation and the sandbox
// spec must all agree on it — deriving it separately on each side is how a
// fetch lands somewhere nothing looks.
func repoGuestName(repo model.Repo) string {
	if repo.Name != "" {
		return repo.Name
	}
	return filepath.Base(repo.Path)
}
