//go:build !integration && !smoke

package cli

// What the unit tier needs from its host, decided once for the whole package.

import (
	"os"
	"testing"
)

// TestMain pins the socket-path budget to a short instances root.
//
// Planning refuses a campaign whose member sockets would not fit in sun_path
// (memberPathBudget), and it computes that from the host's real state root.
// That is right on a real host and wrong for this tier: every test here drives
// a FAKE sandbox that opens no socket at all, so the verdict would be a
// property of whichever home directory the suite happens to run in.
//
// It IS a property of that, and the difference is one operating system wide. A
// Linux root is ~/.local/share/cs-sandbox/instances and leaves about twelve
// bytes of headroom; the macOS root is ~/Library/Application Support/… , thirty
// bytes longer, and seventeen tests then fail on a limit none of them is about.
//
// A short literal rather than a temporary directory, because nothing here reads
// or writes it — only its length is used — and t.TempDir() on macOS is under
// /var/folders/…, long enough to keep failing. The two tests that ARE about the
// budget name their own root with t.Setenv, which wins over this; so does an
// operator who exports one.
func TestMain(m *testing.M) {
	if _, set := os.LookupEnv("CS_SANDBOX_INSTANCES_DIR"); !set {
		os.Setenv("CS_SANDBOX_INSTANCES_DIR", "/tmp/cs-campaign-unit/instances")
	}
	os.Exit(m.Run())
}
