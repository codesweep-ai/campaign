package cli

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
)

// guestBinaryBytes is the embedded cs-campaign-member binary, produced by
// `make guestbin` and embedded so the two binaries ship in lockstep. The
// committed file is a placeholder that keeps plain `go build ./...` working;
// create refuses to install it. `make build` passes -overlay, which has the
// compiler read the real guest from bin/guest/ in its place, so the file on
// disk never changes.
//
//go:embed assets/cs-campaign-member.bin
var guestBinaryBytes []byte

// guestBinaryPlaceholderMax is well below any real Go binary and well above
// the placeholder text.
const guestBinaryPlaceholderMax = 100_000

func guestBinary() ([]byte, error) {
	// CS_CAMPAIGN_GUEST_BIN overrides the embed — the seam that lets the unit
	// suite build and install the real guest binary without going through
	// make, and a developer run a locally built one.
	if path := os.Getenv("CS_CAMPAIGN_GUEST_BIN"); path != "" {
		return os.ReadFile(path)
	}
	if len(guestBinaryBytes) < guestBinaryPlaceholderMax {
		return nil, errNoGuestBinary(buildVersion())
	}
	return guestBinaryBytes, nil
}

// errNoGuestBinary says what to do about a cs-campaign that carries no member
// binary, which is every one installed with `go install`. It names the
// install and the variable, because the operator holds the binary and nothing
// else: a build step from this repository would mean nothing to them.
func errNoGuestBinary(v string) error {
	at := v
	if !strings.HasPrefix(v, "v") {
		at = "<the version of this cs-campaign>"
	}
	return fmt.Errorf("this cs-campaign carries no member binary, so create cannot install one; "+
		"install it at the same version with `go install github.com/codesweep-ai/campaign/cmd/cs-campaign-member@%s` "+
		"and name it with CS_CAMPAIGN_GUEST_BIN=<path to it>", at)
}
