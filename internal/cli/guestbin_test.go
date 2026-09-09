package cli

// The unit suite installs the REAL guest binary into its fake guests: built
// once per test process, injected through the CS_CAMPAIGN_GUEST_BIN seam. The
// doctor's guard probe then exercises the actual guard face — the coverage
// the deleted shell-guard tests used to provide, now against the shipped code.

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

var guestBinOnce sync.Once
var guestBinPath string
var guestBinErr error

func ensureGuestBinary(t *testing.T) {
	t.Helper()
	guestBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "guestbin")
		if err != nil {
			guestBinErr = err
			return
		}
		guestBinPath = filepath.Join(dir, "cs-campaign-member")
		cmd := exec.Command("go", "build", "-o", guestBinPath, "../../cmd/cs-campaign-member")
		if out, err := cmd.CombinedOutput(); err != nil {
			guestBinErr = err
			t.Logf("build guest binary: %s", out)
		}
	})
	if guestBinErr != nil {
		t.Fatalf("cannot build the guest binary for tests: %v", guestBinErr)
	}
	// The value is the same for every caller, so a test whose parent already
	// planted it has nothing to do — and must do nothing: t.Setenv refuses to
	// run on a test that has called t.Parallel, which is every scenario in the
	// smoke tier. The parent calls this once before releasing them.
	if os.Getenv("CS_CAMPAIGN_GUEST_BIN") == guestBinPath {
		return
	}
	t.Setenv("CS_CAMPAIGN_GUEST_BIN", guestBinPath)
}
