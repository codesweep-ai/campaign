package cli

import (
	_ "embed"
	"fmt"
	"os"
)

// guestBinaryBytes is the embedded cs-campaign-member binary, produced by
// `make guestbin` and embedded so the two binaries ship in lockstep. The
// committed file is a placeholder that keeps plain `go build ./...` working;
// create refuses to install it.
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
		return nil, fmt.Errorf("the embedded guest binary is the committed placeholder (%d bytes) — build with `make build`, which compiles cmd/cs-campaign-member first", len(guestBinaryBytes))
	}
	return guestBinaryBytes, nil
}
