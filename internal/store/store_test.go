package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/covmap"

	"github.com/codesweep-ai/campaign/internal/model"
)

func TestRoundTripAndList(t *testing.T) {
	covmap.ProveCoreOnPass(t, "state-safety", covmap.TierUnit)
	s := Store{Dir: t.TempDir()}
	for _, n := range []string{"zeta", "alpha"} {
		if err := s.Save(&model.Campaign{Version: CurrentVersion, Name: n, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	x, problems, err := s.List()
	if err != nil || len(problems) != 0 {
		t.Fatalf("list: %v problems=%v", err, problems)
	}
	if len(x) != 2 || x[0].Name != "alpha" {
		t.Fatalf("list: %+v", x)
	}
	if err = os.WriteFile(filepath.Join(s.Dir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	x, problems, err = s.List()
	if err != nil || len(x) != 2 {
		t.Fatalf("list with corrupt entry: %v %+v", err, x)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "broken.json") {
		t.Fatalf("corrupt entry not diagnosed: %v", problems)
	}
	c, err := s.Load("zeta")
	if err != nil || c.Name != "zeta" {
		t.Fatalf("load: %+v %v", c, err)
	}
}

func TestStateVersionRejectionAndCorruptionErrors(t *testing.T) {
	covmap.ProveCoreOnPass(t, "state-safety", covmap.TierUnit)
	dir := t.TempDir()
	s := Store{Dir: dir}
	// Pre-group records are refused rather than migrated, and the error has to
	// say why: their members were created on a build that addressed sandboxes
	// by bare name, so a silently adopted record would load fine and then miss
	// on every command. Both version 0 (pre-versioning) and version 1 qualify.
	for _, old := range []string{`{"name":"legacy","version":0}`, `{"name":"legacy","version":1}`} {
		if err := os.WriteFile(filepath.Join(dir, "legacy.json"), []byte(old), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := s.Load("legacy")
		if err == nil || !strings.Contains(err.Error(), "predates sandbox groups") {
			t.Fatalf("pre-group record %s: error = %v", old, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "future.json"), []byte(`{"name":"future","version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("future"); err == nil || !strings.Contains(err.Error(), "unsupported state version") {
		t.Fatalf("future version error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "corrupt.json"), []byte(`{"name":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("corrupt"); err == nil || !strings.Contains(err.Error(), "decode campaign state") {
		t.Fatalf("corruption error = %v", err)
	}
}

func TestLockSerializesWriters(t *testing.T) {
	covmap.ProveCoreOnPass(t, "state-safety", covmap.TierUnit)
	s := Store{Dir: t.TempDir()}
	unlock, err := s.Lock("demo")
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func() error, 1)
	go func() {
		u, e := s.Lock("demo")
		if e == nil {
			acquired <- u
		}
	}()
	select {
	case <-acquired:
		t.Fatal("second lock acquired early")
	case <-time.After(50 * time.Millisecond):
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case u := <-acquired:
		_ = u()
	case <-time.After(time.Second):
		t.Fatal("second lock never acquired")
	}
}

// Teardown must reclaim everything a campaign created. The lock file is small
// and easy to overlook, and the failure mode is invisible: one zero-byte file
// per campaign, accruing forever, until a state directory is mostly litter.
func TestDeleteReclaimsTheLockFileToo(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	unlock, err := s.Lock("gone")
	if err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(&model.Campaign{Name: "gone"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("gone"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("destroy left %q behind in the state directory", e.Name())
	}
}
